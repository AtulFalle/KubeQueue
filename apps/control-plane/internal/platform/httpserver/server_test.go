package httpserver

import (
	"bytes"
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
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	registerAPI(router, application.NewJobs(store, scope), store, "")

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

func TestCreateJobEndpointRejectsNamespaceOutsideEffectiveScope(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-create-scope?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"batch"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerAPI(router, application.NewJobs(store, scope), store, "")
	body := []byte(`{
		"name":"report",
		"namespace":"other",
		"template":{"spec":{"template":{"spec":{
			"restartPolicy":"Never",
			"containers":[{"name":"job","image":"busybox"}]
		}}}}
	}`)
	request := httptest.NewRequestWithContext(
		ctx, http.MethodPost, "/api/v1/jobs", bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	var responseBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatal(err)
	}
	if responseBody.Error.Code != "NAMESPACE_OUT_OF_SCOPE" {
		t.Fatalf("error code = %q", responseBody.Error.Code)
	}
}

func TestInventoryFacetsAndCompleteQueueEndpoints(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-inventory?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, input := range []domain.CreateJob{
		{
			Name: "batch-job", Namespace: "batch", Team: "data", Priority: 1,
			Template: json.RawMessage(`{
				"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
			}`),
		},
		{
			Name: "default-job", Namespace: "default", Team: "platform", Priority: 10,
			Template: json.RawMessage(`{
				"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
			}`),
		},
	} {
		if _, err := store.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"batch", "default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerAPI(router, application.NewJobs(store, scope), store, "")

	filteredRequest := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/jobs?namespace=batch", nil,
	)
	filteredResponse := httptest.NewRecorder()
	router.ServeHTTP(filteredResponse, filteredRequest)
	if filteredResponse.Code != http.StatusOK {
		t.Fatalf("filtered inventory status = %d: %s",
			filteredResponse.Code, filteredResponse.Body)
	}
	var filtered struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(filteredResponse.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Count != 1 {
		t.Fatalf("filtered inventory count = %d, want 1", filtered.Count)
	}

	facetsRequest := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/jobs/facets", nil,
	)
	facetsResponse := httptest.NewRecorder()
	router.ServeHTTP(facetsResponse, facetsRequest)
	if facetsResponse.Code != http.StatusOK {
		t.Fatalf("facets status = %d: %s", facetsResponse.Code, facetsResponse.Body)
	}
	var facets domain.JobFacets
	if err := json.Unmarshal(facetsResponse.Body.Bytes(), &facets); err != nil {
		t.Fatal(err)
	}
	if facets.Total != 2 || len(facets.Namespaces) != 2 ||
		facets.ObservedStateCounts[string(domain.StateCreated)] != 2 {
		t.Fatalf("facets after filtered inventory = %#v", facets)
	}

	queueRequest := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/queue", nil)
	queueResponse := httptest.NewRecorder()
	router.ServeHTTP(queueResponse, queueRequest)
	if queueResponse.Code != http.StatusOK {
		t.Fatalf("queue status = %d: %s", queueResponse.Code, queueResponse.Body)
	}
	var queue struct {
		Items        []domain.Job `json:"items"`
		Count        int          `json:"count"`
		QueueVersion int64        `json:"queueVersion"`
	}
	if err := json.Unmarshal(queueResponse.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	wantVersion, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queue.Count != 2 || len(queue.Items) != 2 ||
		queue.Items[0].Name != "default-job" || queue.QueueVersion != wantVersion {
		t.Fatalf("queue response = %#v, want version %d", queue, wantVersion)
	}
}

func TestSystemStatusEndpoint(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-system-status?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.UpdateWorkerStatus(ctx, domain.WorkerStatus{
		State: domain.WorkerStateReady, HeartbeatAt: &now,
		LastSuccessfulReconciliationAt: &now,
		WatchMode:                      domain.WatchModeSelected, EffectiveNamespaces: []string{"default"},
		Namespaces: []domain.NamespaceAuthorityStatus{{
			Namespace: "default", InformerSynced: true, Authorized: true, ObservedAt: &now,
		}},
		GlobalConcurrency: 10, NamespaceConcurrency: 5, ReleaseVersion: "test",
	}); err != nil {
		t.Fatal(err)
	}
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerAPI(router, application.NewJobs(store, scope), store, "")
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/system/status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	var status domain.SystemStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Worker.State != domain.WorkerStateReady ||
		status.Watch.Mode != domain.WatchModeSelected ||
		len(status.Watch.Namespaces) != 1 {
		t.Fatalf("system status = %#v", status)
	}
}
