// Package leadership defines pure generation-fenced leadership rules.
//
// It deliberately does not acquire distributed locks or perform I/O. Adapters
// must persist Lease transitions atomically and condition mutations on the
// generation carried by an Authority token.
package leadership

import (
	"errors"
	"math"
	"time"
)

var (
	ErrInvalidHolder       = errors.New("leadership: holder is required")
	ErrInvalidTTL          = errors.New("leadership: ttl must be positive")
	ErrLeaseHeld           = errors.New("leadership: lease is held")
	ErrLeaseExpired        = errors.New("leadership: lease is expired")
	ErrNotLeaseHolder      = errors.New("leadership: not lease holder")
	ErrStaleGeneration     = errors.New("leadership: stale generation")
	ErrGenerationExhausted = errors.New("leadership: generation exhausted")
	ErrLeadershipLost      = errors.New("leadership: leadership lost")
	ErrLeadershipPaused    = errors.New("leadership: leadership paused")
	ErrInvalidAuthority    = errors.New("leadership: invalid mutation authority")
)

// Lease is the durable coordination value an adapter must compare and swap.
// A zero Lease means that leadership has never been acquired.
type Lease struct {
	Holder     string
	Generation uint64
	ExpiresAt  time.Time
}

// Active reports whether the lease authorizes its holder at now.
func (l Lease) Active(now time.Time) bool {
	return l.Holder != "" && l.Generation != 0 && now.Before(l.ExpiresAt)
}

// Acquire returns the next value to persist. A holder may renew its active
// lease without changing generation. Every other successful acquisition
// advances generation, including acquisition after expiration by the same
// holder.
func Acquire(current Lease, holder string, now time.Time, ttl time.Duration) (Lease, error) {
	if holder == "" {
		return Lease{}, ErrInvalidHolder
	}
	if ttl <= 0 {
		return Lease{}, ErrInvalidTTL
	}
	if current.Active(now) {
		if current.Holder != holder {
			return Lease{}, ErrLeaseHeld
		}
		return Lease{
			Holder:     holder,
			Generation: current.Generation,
			ExpiresAt:  now.Add(ttl),
		}, nil
	}
	if current.Generation == math.MaxUint64 {
		return Lease{}, ErrGenerationExhausted
	}
	return Lease{
		Holder:     holder,
		Generation: current.Generation + 1,
		ExpiresAt:  now.Add(ttl),
	}, nil
}

// Renew extends an active lease without changing its generation.
func Renew(current Lease, holder string, generation uint64, now time.Time, ttl time.Duration) (Lease, error) {
	if holder == "" {
		return Lease{}, ErrInvalidHolder
	}
	if ttl <= 0 {
		return Lease{}, ErrInvalidTTL
	}
	if generation != current.Generation {
		return Lease{}, ErrStaleGeneration
	}
	if current.Holder != holder {
		return Lease{}, ErrNotLeaseHolder
	}
	if !current.Active(now) {
		return Lease{}, ErrLeaseExpired
	}
	return Lease{
		Holder:     holder,
		Generation: generation,
		ExpiresAt:  now.Add(ttl),
	}, nil
}

// Session is local leadership state. Sync is called by the owner of the
// lease-observation loop; it never starts a goroutine. Done closes
// synchronously whenever current mutation authority is paused or lost.
type Session struct {
	holder     string
	generation uint64
	status     Status
	done       chan struct{}
	doneClosed bool
}

type Status uint8

const (
	StatusActive Status = iota + 1
	StatusPaused
	StatusLost
)

func NewSession(lease Lease, now time.Time) (*Session, error) {
	if lease.Holder == "" {
		return nil, ErrInvalidHolder
	}
	if !lease.Active(now) {
		return nil, ErrLeaseExpired
	}
	return &Session{
		holder:     lease.Holder,
		generation: lease.Generation,
		status:     StatusActive,
		done:       make(chan struct{}),
	}, nil
}

func (s *Session) Holder() string        { return s.holder }
func (s *Session) Generation() uint64    { return s.generation }
func (s *Session) Status() Status        { return s.status }
func (s *Session) Done() <-chan struct{} { return s.done }

// Sync applies an authoritative lease observation. Expiry pauses mutations
// and closes the current Done signal. A later observation of an already
// renewed lease resumes with a fresh signal. A different holder or generation
// is definitive loss.
func (s *Session) Sync(lease Lease, now time.Time) Status {
	if s.status == StatusLost {
		return StatusLost
	}
	if lease.Generation != s.generation || lease.Holder != s.holder {
		s.status = StatusLost
		s.cancelCurrent()
		return s.status
	}
	if !lease.Active(now) {
		s.status = StatusPaused
		s.cancelCurrent()
		return s.status
	}
	if s.status == StatusPaused {
		s.done = make(chan struct{})
		s.doneClosed = false
	}
	s.status = StatusActive
	return s.status
}

func (s *Session) cancelCurrent() {
	if s.doneClosed {
		return
	}
	close(s.done)
	s.doneClosed = true
}

// Authority is a short-lived validate-before-mutation token. Callers must
// validate again at the durable or Kubernetes mutation boundary; construction
// alone is not mutation permission.
type Authority struct {
	Holder     string
	Generation uint64
	ValidUntil time.Time
}

// Validate creates authority from the latest observed lease.
func (s *Session) Validate(lease Lease, now time.Time) (Authority, error) {
	switch s.Sync(lease, now) {
	case StatusLost:
		return Authority{}, ErrLeadershipLost
	case StatusPaused:
		return Authority{}, ErrLeadershipPaused
	case StatusActive:
		return Authority{
			Holder:     s.holder,
			Generation: s.generation,
			ValidUntil: lease.ExpiresAt,
		}, nil
	default:
		return Authority{}, ErrLeadershipLost
	}
}

// Authorize verifies a token immediately before a mutation. The generation is
// the fencing value that an integration must include in its conditional write.
func Authorize(authority Authority, current Lease, now time.Time) error {
	if authority.Holder == "" || authority.Generation == 0 || authority.ValidUntil.IsZero() {
		return ErrInvalidAuthority
	}
	if authority.Generation != current.Generation {
		return ErrStaleGeneration
	}
	if authority.Holder != current.Holder {
		return ErrNotLeaseHolder
	}
	if !current.Active(now) || !now.Before(authority.ValidUntil) {
		return ErrLeaseExpired
	}
	return nil
}
