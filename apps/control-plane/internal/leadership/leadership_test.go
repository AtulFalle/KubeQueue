package leadership

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestAcquireAndRenew(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Second

	tests := []struct {
		name    string
		current Lease
		holder  string
		want    Lease
		wantErr error
	}{
		{
			name:   "first acquisition",
			holder: "worker-a",
			want:   Lease{Holder: "worker-a", Generation: 1, ExpiresAt: now.Add(ttl)},
		},
		{
			name:    "active holder renews without advancing generation",
			current: Lease{Holder: "worker-a", Generation: 7, ExpiresAt: now.Add(time.Second)},
			holder:  "worker-a",
			want:    Lease{Holder: "worker-a", Generation: 7, ExpiresAt: now.Add(ttl)},
		},
		{
			name:    "active competing holder is rejected",
			current: Lease{Holder: "worker-a", Generation: 7, ExpiresAt: now.Add(time.Second)},
			holder:  "worker-b",
			wantErr: ErrLeaseHeld,
		},
		{
			name:    "expired lease advances generation",
			current: Lease{Holder: "worker-a", Generation: 7, ExpiresAt: now},
			holder:  "worker-b",
			want:    Lease{Holder: "worker-b", Generation: 8, ExpiresAt: now.Add(ttl)},
		},
		{
			name:    "same holder reacquisition after expiry advances generation",
			current: Lease{Holder: "worker-a", Generation: 7, ExpiresAt: now.Add(-time.Second)},
			holder:  "worker-a",
			want:    Lease{Holder: "worker-a", Generation: 8, ExpiresAt: now.Add(ttl)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Acquire(tt.current, tt.holder, now, ttl)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Acquire() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Acquire() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRenewRejectsExpirationAndStaleGeneration(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Second

	tests := []struct {
		name       string
		lease      Lease
		holder     string
		generation uint64
		wantErr    error
	}{
		{
			name:       "renew active lease",
			lease:      Lease{Holder: "worker-a", Generation: 4, ExpiresAt: now.Add(time.Second)},
			holder:     "worker-a",
			generation: 4,
		},
		{
			name:       "reject expired lease",
			lease:      Lease{Holder: "worker-a", Generation: 4, ExpiresAt: now},
			holder:     "worker-a",
			generation: 4,
			wantErr:    ErrLeaseExpired,
		},
		{
			name:       "reject stale generation deterministically",
			lease:      Lease{Holder: "worker-a", Generation: 5, ExpiresAt: now.Add(time.Second)},
			holder:     "worker-a",
			generation: 4,
			wantErr:    ErrStaleGeneration,
		},
		{
			name:       "reject different holder",
			lease:      Lease{Holder: "worker-a", Generation: 4, ExpiresAt: now.Add(time.Second)},
			holder:     "worker-b",
			generation: 4,
			wantErr:    ErrNotLeaseHolder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Renew(tt.lease, tt.holder, tt.generation, now, ttl)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Renew() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil {
				want := Lease{Holder: tt.holder, Generation: tt.generation, ExpiresAt: now.Add(ttl)}
				if got != want {
					t.Fatalf("Renew() = %#v, want %#v", got, want)
				}
			}
		})
	}
}

func TestGenerationMonotonicity(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	lease := Lease{}
	var err error

	for wantGeneration := uint64(1); wantGeneration <= 4; wantGeneration++ {
		lease, err = Acquire(lease, "worker-a", now, time.Second)
		if err != nil {
			t.Fatalf("Acquire() error = %v", err)
		}
		if lease.Generation != wantGeneration {
			t.Fatalf("generation = %d, want %d", lease.Generation, wantGeneration)
		}
		now = lease.ExpiresAt
	}

	_, err = Acquire(
		Lease{Holder: "worker-a", Generation: math.MaxUint64, ExpiresAt: now},
		"worker-b",
		now,
		time.Second,
	)
	if !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("Acquire() error = %v, want %v", err, ErrGenerationExhausted)
	}
}

func TestStaleLeaderPauseResumeAndLossSignal(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	lease := Lease{Holder: "worker-a", Generation: 9, ExpiresAt: now.Add(time.Second)}
	session, err := NewSession(lease, now)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	initialDone := session.Done()

	tests := []struct {
		name       string
		lease      Lease
		at         time.Time
		wantStatus Status
		wantDone   bool
	}{
		{
			name:       "local observation passes expiry and pauses",
			lease:      lease,
			at:         lease.ExpiresAt,
			wantStatus: StatusPaused,
			wantDone:   true,
		},
		{
			name: "authoritative renewal observation resumes",
			lease: Lease{
				Holder:     lease.Holder,
				Generation: lease.Generation,
				ExpiresAt:  now.Add(5 * time.Second),
			},
			at:         now.Add(2 * time.Second),
			wantStatus: StatusActive,
		},
		{
			name: "new generation causes definitive loss",
			lease: Lease{
				Holder:     "worker-b",
				Generation: lease.Generation + 1,
				ExpiresAt:  now.Add(10 * time.Second),
			},
			at:         now.Add(2 * time.Second),
			wantStatus: StatusLost,
			wantDone:   true,
		},
		{
			name:       "loss is terminal and signal remains closed",
			lease:      lease,
			at:         now,
			wantStatus: StatusLost,
			wantDone:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := session.Sync(tt.lease, tt.at); got != tt.wantStatus {
				t.Fatalf("Sync() = %v, want %v", got, tt.wantStatus)
			}
			select {
			case <-session.Done():
				if !tt.wantDone {
					t.Fatal("current Done() signal is closed")
				}
			default:
				if tt.wantDone {
					t.Fatal("current Done() signal remained open")
				}
			}
		})
	}

	select {
	case <-initialDone:
	default:
		t.Fatal("expiry did not cancel work holding the initial signal")
	}
}

func TestValidateBeforeMutationRejectsStaleAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	lease := Lease{Holder: "worker-a", Generation: 3, ExpiresAt: now.Add(time.Minute)}
	session, err := NewSession(lease, now)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	authority, err := session.Validate(lease, now)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name    string
		lease   Lease
		at      time.Time
		wantErr error
	}{
		{name: "current authority", lease: lease, at: now},
		{
			name:    "stale generation",
			lease:   Lease{Holder: "worker-b", Generation: 4, ExpiresAt: now.Add(time.Minute)},
			at:      now,
			wantErr: ErrStaleGeneration,
		},
		{
			name:    "expired authority token",
			lease:   lease,
			at:      lease.ExpiresAt,
			wantErr: ErrLeaseExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Authorize(authority, tt.lease, tt.at); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Authorize() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeterministicInputErrors(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	active := Lease{Holder: "worker-a", Generation: 1, ExpiresAt: now.Add(time.Second)}

	tests := []struct {
		name    string
		run     func() error
		wantErr error
	}{
		{
			name: "acquire requires holder",
			run: func() error {
				_, err := Acquire(Lease{}, "", now, time.Second)
				return err
			},
			wantErr: ErrInvalidHolder,
		},
		{
			name: "acquire requires positive ttl",
			run: func() error {
				_, err := Acquire(Lease{}, "worker-a", now, 0)
				return err
			},
			wantErr: ErrInvalidTTL,
		},
		{
			name: "invalid authority",
			run: func() error {
				return Authorize(Authority{}, active, now)
			},
			wantErr: ErrInvalidAuthority,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := tt.run()
			second := tt.run()
			if !errors.Is(first, tt.wantErr) || !errors.Is(second, tt.wantErr) {
				t.Fatalf("errors = (%v, %v), want %v", first, second, tt.wantErr)
			}
			if first.Error() != second.Error() {
				t.Fatalf("error text changed: %q != %q", first, second)
			}
		})
	}
}
