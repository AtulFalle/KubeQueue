package leadership

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestManagerRevalidatesGenerationBeforeMutation(t *testing.T) {
	store := &memoryLeaseStore{}
	manager, err := NewManager(store, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.TryAcquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	authority, err := manager.Authority(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.lease = Lease{
		Holder: "worker-b", Generation: authority.Generation + 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	store.mu.Unlock()

	if err := manager.Revalidate(t.Context(), authority); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("Revalidate() error = %v, want %v", err, ErrStaleGeneration)
	}
	if manager.Snapshot().Role == RoleLeader {
		t.Fatal("manager remained leader after observing a successor generation")
	}
	if _, err := manager.Authority(t.Context()); !errors.Is(err, ErrLeadershipLost) {
		t.Fatalf("Authority() after loss error = %v, want %v", err, ErrLeadershipLost)
	}
}

type memoryLeaseStore struct {
	mu    sync.Mutex
	lease Lease
}

func (s *memoryLeaseStore) AcquireLeadership(
	_ context.Context, _ string, holder string, ttl time.Duration,
) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	next, err := Acquire(s.lease, holder, now, ttl)
	if err != nil {
		return s.lease, err
	}
	s.lease = next
	return next, nil
}

func (s *memoryLeaseStore) LeadershipLease(context.Context, string) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lease, nil
}
