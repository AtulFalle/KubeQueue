package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
)

func TestLeadershipLeaseGenerationIsDurableAndMonotonic(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, "file:test-leadership-generation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.AcquireLeadership(ctx, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.AcquireLeadership(ctx, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || renewed.Generation != first.Generation {
		t.Fatalf("generations = %d then %d", first.Generation, renewed.Generation)
	}

	if _, err := store.db.ExecContext(
		ctx, `UPDATE leadership_leases SET expires_at=? WHERE name=?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), "reconciler",
	); err != nil {
		t.Fatal(err)
	}
	successor, err := store.AcquireLeadership(ctx, "reconciler", "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if successor.Generation != first.Generation+1 || successor.Holder != "worker-b" {
		t.Fatalf("successor lease = %#v", successor)
	}

	persisted, err := store.LeadershipLease(ctx, "reconciler")
	if err != nil {
		t.Fatal(err)
	}
	if persisted != successor {
		t.Fatalf("persisted lease = %#v, want %#v", persisted, successor)
	}
}

func TestLeadershipLeaseRejectsCompetingActiveHolder(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, "file:test-leadership-contention?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	current, err := store.AcquireLeadership(ctx, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := store.AcquireLeadership(ctx, "reconciler", "worker-b", time.Minute)
	if !errors.Is(err, leadership.ErrLeaseHeld) {
		t.Fatalf("AcquireLeadership() error = %v", err)
	}
	if observed != current {
		t.Fatalf("contended lease = %#v, want %#v", observed, current)
	}
}
