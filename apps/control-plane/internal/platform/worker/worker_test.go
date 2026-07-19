package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
