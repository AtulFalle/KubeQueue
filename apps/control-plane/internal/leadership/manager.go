package leadership

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// LeaseStore is the durable coordination surface required by Manager.
type LeaseStore interface {
	AcquireLeadership(context.Context, string, string, time.Duration) (Lease, error)
	LeadershipLease(context.Context, string) (Lease, error)
}

// Role is a bounded description of this process's mutation authority.
type Role string

const (
	RoleLeader   Role = "leader"
	RoleFollower Role = "follower"
	RolePaused   Role = "paused"
)

// Snapshot is safe to expose through readiness and operational status.
type Snapshot struct {
	Role       Role
	Generation uint64
	ExpiresAt  time.Time
}

// Manager campaigns for a durable generation-bearing lease and revalidates
// authority at each mutation boundary.
type Manager struct {
	store        LeaseStore
	leaseName    string
	holder       string
	ttl          time.Duration
	pollInterval time.Duration

	mu       sync.RWMutex
	session  *Session
	snapshot Snapshot
}

func NewManager(store LeaseStore, leaseName, holder string, ttl time.Duration) (*Manager, error) {
	if store == nil {
		return nil, errors.New("leadership: lease store is required")
	}
	if leaseName == "" {
		return nil, errors.New("leadership: lease name is required")
	}
	if holder == "" {
		return nil, ErrInvalidHolder
	}
	if ttl <= 0 {
		return nil, ErrInvalidTTL
	}
	return &Manager{
		store:        store,
		leaseName:    leaseName,
		holder:       holder,
		ttl:          ttl,
		pollInterval: ttl / 3,
		snapshot:     Snapshot{Role: RolePaused},
	}, nil
}

func (m *Manager) Holder() string { return m.holder }

// Run keeps campaigning while the process is alive. A follower remains
// available to its informer owner; only mutation authority is withheld.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.TryAcquire(ctx); err != nil && !errors.Is(err, ErrLeaseHeld) {
		m.pause()
	}
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.pause()
			return nil
		case <-ticker.C:
			if err := m.TryAcquire(ctx); err != nil && !errors.Is(err, ErrLeaseHeld) {
				m.pause()
			}
		}
	}
}

// TryAcquire performs one atomic campaign or renewal.
func (m *Manager) TryAcquire(ctx context.Context) error {
	lease, err := m.store.AcquireLeadership(ctx, m.leaseName, m.holder, m.ttl)
	if err != nil {
		if errors.Is(err, ErrLeaseHeld) {
			m.observeFollower(lease)
		} else {
			m.pause()
		}
		return err
	}

	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil || m.session.Generation() != lease.Generation {
		if m.session != nil {
			m.session.Sync(lease, now)
		}
		session, sessionErr := NewSession(lease, now)
		if sessionErr != nil {
			m.snapshot = Snapshot{Role: RolePaused}
			return sessionErr
		}
		m.session = session
	} else {
		m.session.Sync(lease, now)
	}
	m.snapshot = Snapshot{
		Role: RoleLeader, Generation: lease.Generation, ExpiresAt: lease.ExpiresAt,
	}
	return nil
}

// Authority reads the durable lease before issuing a short-lived token.
func (m *Manager) Authority(ctx context.Context) (Authority, error) {
	lease, err := m.store.LeadershipLease(ctx, m.leaseName)
	if err != nil {
		m.pause()
		return Authority{}, fmt.Errorf("read leadership lease: %w", err)
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return Authority{}, ErrLeadershipPaused
	}
	authority, err := m.session.Validate(lease, now)
	if err != nil {
		m.snapshot = Snapshot{
			Role: RolePaused, Generation: lease.Generation, ExpiresAt: lease.ExpiresAt,
		}
		return Authority{}, err
	}
	m.snapshot = Snapshot{
		Role: RoleLeader, Generation: lease.Generation, ExpiresAt: lease.ExpiresAt,
	}
	return authority, nil
}

// Revalidate performs the mandatory durable generation check immediately
// before an external mutation.
func (m *Manager) Revalidate(ctx context.Context, authority Authority) error {
	lease, err := m.store.LeadershipLease(ctx, m.leaseName)
	if err != nil {
		m.pause()
		return fmt.Errorf("revalidate leadership lease: %w", err)
	}
	if err := Authorize(authority, lease, time.Now().UTC()); err != nil {
		m.observeFollower(lease)
		return err
	}
	return nil
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

func (m *Manager) pause() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		m.session.Sync(Lease{
			Holder: m.session.Holder(), Generation: m.session.Generation(),
		}, time.Now().UTC())
	}
	m.snapshot.Role = RolePaused
}

func (m *Manager) observeFollower(lease Lease) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		m.session.Sync(lease, time.Now().UTC())
	}
	m.snapshot = Snapshot{
		Role: RoleFollower, Generation: lease.Generation, ExpiresAt: lease.ExpiresAt,
	}
}
