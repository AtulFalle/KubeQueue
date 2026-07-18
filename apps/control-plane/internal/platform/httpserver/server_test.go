package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		origin     string
		method     string
		wantStatus int
		wantOrigin string
	}{
		{
			name:       "allows configured origin",
			origin:     "http://localhost:8081",
			method:     http.MethodOptions,
			wantStatus: http.StatusNoContent,
			wantOrigin: "http://localhost:8081",
		},
		{
			name:       "rejects unknown preflight origin",
			origin:     "https://example.com",
			method:     http.MethodOptions,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "does not add headers without an origin",
			method:     http.MethodGet,
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := gin.New()
			router.Use(corsMiddleware("http://localhost:8081"))
			router.Any("/test", func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequestWithContext(t.Context(), tt.method, "/test", nil)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if origin := response.Header().Get("Access-Control-Allow-Origin"); origin != tt.wantOrigin {
				t.Fatalf("allowed origin = %q, want %q", origin, tt.wantOrigin)
			}
		})
	}
}

func TestArchiveJobEndpoint(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-archive?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "default", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "uid", ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMissing(
		ctx, job.ID, job.KubernetesUID, job.ResourceVersion, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerAPI(router, application.NewJobs(store), store, "")

	for range 2 {
		request := httptest.NewRequestWithContext(
			ctx, http.MethodDelete, "/api/v1/jobs/"+job.ID, nil,
		)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("archive status = %d, want %d", response.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/jobs/"+job.ID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("archived Job GET status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
