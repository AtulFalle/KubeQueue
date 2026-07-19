package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
)

func TestHealthHandlerSeparatesLivenessAndReadiness(t *testing.T) {
	t.Parallel()
	ready := false
	handler := healthHandler(func() bool { return ready })

	liveness := httptest.NewRecorder()
	handler.ServeHTTP(
		liveness, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil),
	)
	if liveness.Code != http.StatusNoContent {
		t.Fatalf("liveness status = %d", liveness.Code)
	}
	notReady := httptest.NewRecorder()
	handler.ServeHTTP(
		notReady, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil),
	)
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d", notReady.Code)
	}

	ready = true
	available := httptest.NewRecorder()
	handler.ServeHTTP(
		available, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil),
	)
	if available.Code != http.StatusNoContent {
		t.Fatalf("ready status = %d", available.Code)
	}
}

func TestWorkerLeadershipManagerAcquiresGenerationBearingAuthority(t *testing.T) {
	store := &workerLeaseStore{}
	manager, err := newLeadershipManager(store, "worker-a")
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
	if authority.Holder != "worker-a" || authority.Generation != 1 {
		t.Fatalf("authority = %#v", authority)
	}
}

type workerLeaseStore struct {
	lease leadership.Lease
}

func (s *workerLeaseStore) AcquireLeadership(
	_ context.Context, _ string, holder string, ttl time.Duration,
) (leadership.Lease, error) {
	next, err := leadership.Acquire(s.lease, holder, time.Now().UTC(), ttl)
	if err == nil {
		s.lease = next
	}
	return next, err
}

func (s *workerLeaseStore) LeadershipLease(context.Context, string) (leadership.Lease, error) {
	return s.lease, nil
}
