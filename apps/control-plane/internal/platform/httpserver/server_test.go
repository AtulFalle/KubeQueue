package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestListJobsEndpointUsesBoundedCursorPagination(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-list-page?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	template := json.RawMessage(`{
		"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
	}`)
	for index := range 201 {
		if _, err := store.Create(ctx, domain.CreateJob{
			Name: fmt.Sprintf("job-%03d", index), Namespace: "default",
			Priority: index % 3, Template: template,
		}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := store.Create(ctx, domain.CreateJob{
		Name: "target-job", Namespace: "default", Priority: 10, Template: template,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerLegacyTestAPI(t, router, store, scope)

	type listResponse struct {
		Items        []domain.Job `json:"items"`
		Count        int          `json:"count"`
		QueueVersion int64        `json:"queueVersion"`
		NextCursor   *string      `json:"nextCursor"`
	}
	getPage := func(path string) listResponse {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, response.Code, response.Body)
		}
		var body listResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	defaultPage := getPage("/api/v1/jobs")
	if defaultPage.Count != 50 || len(defaultPage.Items) != 50 || defaultPage.NextCursor == nil {
		t.Fatalf("default page count = %d, items = %d, next = %#v",
			defaultPage.Count, len(defaultPage.Items), defaultPage.NextCursor)
	}
	maxPage := getPage("/api/v1/jobs?limit=200")
	if maxPage.Count != 200 || maxPage.NextCursor == nil {
		t.Fatalf("maximum page count = %d, next = %#v", maxPage.Count, maxPage.NextCursor)
	}

	seen := make(map[string]struct{}, 202)
	cursor := ""
	for {
		path := "/api/v1/jobs?limit=37"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := getPage(path)
		for _, job := range page.Items {
			if _, duplicate := seen[job.ID]; duplicate {
				t.Fatalf("job %q was returned more than once", job.ID)
			}
			seen[job.ID] = struct{}{}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 202 {
		t.Fatalf("paginated inventory contained %d jobs, want 202", len(seen))
	}

	searchPage := getPage("/api/v1/jobs?search=%20%20target-job%20%20")
	if searchPage.Count != 1 || searchPage.Items[0].ID != target.ID {
		t.Fatalf("normalized search page = %#v", searchPage)
	}
}

func TestListJobsEndpointRejectsMalformedPaginationInput(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-list-invalid?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerLegacyTestAPI(t, router, store, scope)

	tests := []struct {
		path string
		code string
	}{
		{path: "/api/v1/jobs?cursor=not-a-cursor", code: "INVALID_CURSOR"},
		{path: "/api/v1/jobs?sort=priority", code: "INVALID_SORT"},
		{path: "/api/v1/jobs?limit=201", code: "INVALID_LIMIT"},
		{path: "/api/v1/jobs?synchronization=unknown", code: "INVALID_SYNCHRONIZATION"},
		{path: "/api/v1/jobs?createdAfter=not-a-time", code: "INVALID_TIME"},
		{
			path: "/api/v1/jobs?createdAfter=2026-07-20T00:00:00Z&createdBefore=2026-07-19T00:00:00Z",
			code: "INVALID_TIME_RANGE",
		},
		{
			path: "/api/v1/jobs?search=" + strings.Repeat("x", 201),
			code: "INVALID_SEARCH",
		},
	}
	for _, test := range tests {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", test.path, response.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != test.code {
			t.Fatalf("%s code = %q, want %q", test.path, body.Error.Code, test.code)
		}
	}
}

func TestJobMetadataAndManifestEndpointsSeparateSensitiveData(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-manifest?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "manifest-job", Namespace: "default", Template: json.RawMessage(`{
			"apiVersion":"batch/v1",
			"kind":"Job",
			"metadata":{
				"name":"manifest-job",
				"resourceVersion":"secret-version",
				"uid":"secret-uid",
				"annotations":{"example.com/password":"annotation-secret","example.com/purpose":"batch"}
			},
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
				"name":"job",
				"image":"busybox",
				"env":[
					{"name":"API_TOKEN","value":"inline-token"},
					{"name":"FROM_SECRET","valueFrom":{"secretKeyRef":{"name":"job-credentials","key":"token"}}}
				],
				"args":["--password=argument-secret","--mode=batch"]
			}]}}},
			"status":{"active":1}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerLegacyTestAPI(t, router, store, scope)

	metadataRequest := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/jobs/"+job.ID, nil,
	)
	metadataResponse := httptest.NewRecorder()
	router.ServeHTTP(metadataResponse, metadataRequest)
	if metadataResponse.Code != http.StatusOK {
		t.Fatalf("metadata status = %d: %s", metadataResponse.Code, metadataResponse.Body)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataResponse.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if got := metadata["projectId"]; got != "default" {
		t.Errorf("metadata projectId = %q, want %q", got, "default")
	}
	for _, field := range []string{
		"template", "namespaceBindingId", "creatorPrincipalId", "submissionSource",
		"resourceVersion", "pendingAction", "reconcileRetries",
	} {
		if _, exposed := metadata[field]; exposed {
			t.Errorf("metadata response exposed %q", field)
		}
	}

	manifestRequest := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/jobs/"+job.ID+"/manifest", nil,
	)
	manifestResponse := httptest.NewRecorder()
	router.ServeHTTP(manifestResponse, manifestRequest)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest status = %d: %s", manifestResponse.Code, manifestResponse.Body)
	}
	var body struct {
		JobID    string         `json:"jobId"`
		Manifest map[string]any `json:"manifest"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.JobID != job.ID {
		t.Fatalf("manifest jobId = %q, want %q", body.JobID, job.ID)
	}
	if _, exposed := body.Manifest["status"]; exposed {
		t.Fatal("manifest response exposed Kubernetes status")
	}
	manifestMetadata, _ := body.Manifest["metadata"].(map[string]any)
	if _, exposed := manifestMetadata["resourceVersion"]; exposed {
		t.Fatal("manifest response exposed Kubernetes resourceVersion")
	}
	if _, exposed := manifestMetadata["uid"]; exposed {
		t.Fatal("manifest response exposed Kubernetes UID")
	}
	annotations, _ := manifestMetadata["annotations"].(map[string]any)
	if got := annotations["example.com/password"]; got != "[REDACTED]" {
		t.Fatalf("sensitive annotation = %q, want redacted", got)
	}
	if got := annotations["example.com/purpose"]; got != "batch" {
		t.Fatalf("non-sensitive annotation = %q, want preserved", got)
	}
	spec, _ := body.Manifest["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	container, _ := containers[0].(map[string]any)
	environment, _ := container["env"].([]any)
	literalCredential, _ := environment[0].(map[string]any)
	if got := literalCredential["value"]; got != "[REDACTED]" {
		t.Fatalf("literal credential = %q, want redacted", got)
	}
	secretReference, _ := environment[1].(map[string]any)
	if _, preserved := secretReference["valueFrom"]; !preserved {
		t.Fatal("manifest response removed a Kubernetes Secret reference")
	}
	arguments, _ := container["args"].([]any)
	if got := arguments[0]; got != "--password=[REDACTED]" {
		t.Fatalf("credential argument = %q, want redacted", got)
	}
	if got := arguments[1]; got != "--mode=batch" {
		t.Fatalf("non-sensitive argument = %q, want preserved", got)
	}
	for _, secret := range []string{"annotation-secret", "inline-token", "argument-secret"} {
		if strings.Contains(manifestResponse.Body.String(), secret) {
			t.Fatalf("manifest response reflected secret %q", secret)
		}
	}
}

func TestJobEventsEndpointUsesBoundedCursorPagination(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-http-event-pages?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "event-job", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 25 {
		job, err = store.UpdateQueue(ctx, job.ID, index, job.Position, job.Version, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerLegacyTestAPI(t, router, store, scope)

	type eventPage struct {
		Items      []domain.Event `json:"items"`
		NextCursor *string        `json:"nextCursor"`
	}
	getPage := func(path string) eventPage {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, response.Code, response.Body)
		}
		var page eventPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := getPage("/api/v1/jobs/" + job.ID + "/events?limit=10")
	if len(first.Items) != 10 || first.NextCursor == nil {
		t.Fatalf("first event page = %#v", first)
	}
	second := getPage("/api/v1/jobs/" + job.ID + "/events?limit=10&cursor=" +
		url.QueryEscape(*first.NextCursor))
	if len(second.Items) != 10 || second.Items[0].ID >= first.Items[len(first.Items)-1].ID {
		t.Fatalf("second event page = %#v", second)
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
	registerLegacyTestAPI(t, router, store, scope)

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
	registerLegacyTestAPI(t, router, store, scope)
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
	registerLegacyTestAPI(t, router, store, scope)

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
	registerLegacyTestAPI(t, router, store, scope)
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

func TestRoutePermissionMatrixCoversCurrentAPI(t *testing.T) {
	expected := map[string]domain.Permission{
		"GET /api/v1/system/status":               domain.PermissionSystemStatusRead,
		"GET /api/v1/jobs":                        domain.PermissionJobsList,
		"POST /api/v1/jobs":                       domain.PermissionJobsSubmit,
		"GET /api/v1/jobs/facets":                 domain.PermissionJobsList,
		"GET /api/v1/jobs/:id":                    domain.PermissionJobsRead,
		"GET /api/v1/jobs/:id/manifest":           domain.PermissionJobsManifestRead,
		"DELETE /api/v1/jobs/:id":                 domain.PermissionJobsArchive,
		"GET /api/v1/jobs/:id/events":             domain.PermissionJobEventsRead,
		"POST /api/v1/jobs/:id/actions/start":     domain.PermissionJobsResume,
		"POST /api/v1/jobs/:id/actions/resume":    domain.PermissionJobsResume,
		"POST /api/v1/jobs/:id/actions/pause":     domain.PermissionJobsPause,
		"POST /api/v1/jobs/:id/actions/terminate": domain.PermissionJobsTerminate,
		"POST /api/v1/jobs/:id/actions/retry":     domain.PermissionJobsRetry,
		"PATCH /api/v1/jobs/:id/queue":            domain.PermissionQueueEntryUpdate,
		"GET /api/v1/queue":                       domain.PermissionQueueRead,
		"PUT /api/v1/queue/order":                 domain.PermissionQueueGlobalReorder,
		"GET /api/v1/events":                      domain.PermissionEventStreamRead,
	}
	for route, permission := range expected {
		if got := routePermissionMatrix[route]; got != permission {
			t.Errorf("%s permission = %q, want %q", route, got, permission)
		}
		if !permission.Valid() {
			t.Errorf("%s uses permission outside catalog: %q", route, permission)
		}
	}
}

func TestLegacyAdminTokenAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		configured string
		provided   string
		want       int
	}{
		{name: "empty token never authenticates", want: http.StatusUnauthorized},
		{name: "missing bearer is rejected", configured: "secret", want: http.StatusUnauthorized},
		{name: "wrong bearer is rejected", configured: "secret", provided: "wrong", want: http.StatusUnauthorized},
		{name: "valid bearer authenticates legacy admin", configured: "secret", provided: "secret", want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(authenticationMiddleware(test.configured))
			router.GET("/api/v1/check", func(c *gin.Context) {
				actor, err := application.ActorFromContext(c.Request.Context())
				if err != nil || actor.PrincipalID != "legacy_admin" {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/api/v1/check", nil,
			)
			if test.provided != "" {
				request.Header.Set("Authorization", "Bearer "+test.provided)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func registerLegacyTestAPI(
	t *testing.T,
	router *gin.Engine,
	store *persistence.Store,
	scope domain.NamespaceScope,
) {
	t.Helper()
	if err := store.BackfillCompatibility(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	const token = "test-legacy-token"
	router.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			c.Request.Header.Set("Authorization", "Bearer "+token)
		}
		c.Next()
	})
	registerAPI(router, application.NewJobs(store, scope, store), nil, store, store, token, nil, "")
}
