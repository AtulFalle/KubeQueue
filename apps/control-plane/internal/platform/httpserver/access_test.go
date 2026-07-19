package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/gin-gonic/gin"
)

func TestAccessRoutePermissionMatrixCoversProtectedSurface(t *testing.T) {
	if len(accessRoutePermissionMatrix) != 34 {
		t.Fatalf("access matrix has %d entries, want 34", len(accessRoutePermissionMatrix))
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerAccessAPI(router, nil, nil, "", nil, "")
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for route, permission := range accessRoutePermissionMatrix {
		if routePermissionMatrix[route] != permission {
			t.Errorf("%s is not registered in the shared matrix", route)
		}
		if !registered[route] {
			t.Errorf("%s is in the matrix but not registered", route)
		}
		if !permission.Valid() {
			t.Errorf("%s uses permission outside catalog: %q", route, permission)
		}
	}
	if accessRoutePermissionMatrix["GET /api/v1/access/me"] != domain.PermissionAuthenticated {
		t.Error("current-access route must require authentication without an administrative grant")
	}
}

func TestAccessHandlersExposeBoundedResourcesAndRoleRevisions(t *testing.T) {
	router, _ := newAccessTestRouter(t)

	created := accessRequest(t, router, http.MethodPost, "/api/v1/projects",
		`{"id":"project_a","name":"Project A"}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status = %d: %s", created.Code, created.Body)
	}

	page := accessRequest(t, router, http.MethodGet, "/api/v1/projects?limit=2", "", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"project_a"`) {
		t.Fatalf("project page status = %d: %s", page.Code, page.Body)
	}

	role := accessRequest(t, router, http.MethodPost, "/api/v1/role-definitions",
		`{"id":"job_reader","name":"Job reader","scopeType":"INSTALLATION","permissions":["jobs.read"]}`,
		"")
	if role.Code != http.StatusCreated || role.Header().Get("ETag") != `"1"` {
		t.Fatalf("create role status = %d, etag = %q: %s",
			role.Code, role.Header().Get("ETag"), role.Body)
	}

	stale := accessRequest(t, router, http.MethodPatch, "/api/v1/role-definitions/job_reader",
		`{"name":"Job viewer","scopeType":"INSTALLATION","permissions":["jobs.read"]}`, `"9"`)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale role update status = %d: %s", stale.Code, stale.Body)
	}

	updated := accessRequest(t, router, http.MethodPatch, "/api/v1/role-definitions/job_reader",
		`{"name":"Job viewer","scopeType":"INSTALLATION","permissions":["jobs.read"]}`, `"1"`)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("role update status = %d, etag = %q: %s",
			updated.Code, updated.Header().Get("ETag"), updated.Body)
	}

	current := accessRequest(t, router, http.MethodGet, "/api/v1/access/me", "", "")
	if current.Code != http.StatusOK ||
		!strings.Contains(current.Body.String(), `"installationOwner":true`) {
		t.Fatalf("current access status = %d: %s", current.Code, current.Body)
	}
}

func TestCredentialHandlersReturnPlaintextOnlyFromIssueAndRotate(t *testing.T) {
	router, _ := newAccessTestRouter(t)
	account := accessRequest(t, router, http.MethodPost, "/api/v1/service-accounts",
		`{"principalId":"build_bot","displayName":"Build bot"}`, "")
	if account.Code != http.StatusCreated {
		t.Fatalf("create service account status = %d: %s", account.Code, account.Body)
	}

	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	issued := accessRequest(t, router, http.MethodPost,
		"/api/v1/service-accounts/build_bot/credentials",
		fmt.Sprintf(`{"permissions":["jobs.read"],"expiresAt":%q}`, expiresAt), "")
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status = %d: %s", issued.Code, issued.Body)
	}
	var issueBody struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issueBody); err != nil {
		t.Fatal(err)
	}
	if issueBody.Credential.ID == "" || !strings.HasPrefix(issueBody.Token, "kqsa.") {
		t.Fatalf("issue response omitted one-time material: %s", issued.Body)
	}

	metadata := accessRequest(t, router, http.MethodGet,
		"/api/v1/service-accounts/build_bot/credentials/"+issueBody.Credential.ID, "", "")
	if metadata.Code != http.StatusOK || strings.Contains(metadata.Body.String(), issueBody.Token) ||
		strings.Contains(metadata.Body.String(), `"token"`) {
		t.Fatalf("metadata leaked plaintext: status = %d: %s", metadata.Code, metadata.Body)
	}

	guessedAccount := accessRequest(t, router, http.MethodGet,
		"/api/v1/service-accounts/other_bot/credentials/"+issueBody.Credential.ID, "", "")
	if guessedAccount.Code != http.StatusNotFound ||
		!strings.Contains(guessedAccount.Body.String(), `"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("cross-account credential read status = %d: %s",
			guessedAccount.Code, guessedAccount.Body)
	}

	rotated := accessRequest(t, router, http.MethodPost,
		"/api/v1/service-accounts/build_bot/credentials/"+issueBody.Credential.ID+"/rotate",
		fmt.Sprintf(
			`{"permissions":["jobs.read"],"expiresAt":%q,"overlapSeconds":60}`,
			expiresAt,
		), "")
	if rotated.Code != http.StatusCreated {
		t.Fatalf("rotate status = %d: %s", rotated.Code, rotated.Body)
	}
	var rotationBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotationBody); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rotationBody.Token, "kqsa.") || rotationBody.Token == issueBody.Token {
		t.Fatalf("rotation did not return a fresh one-time token: %s", rotated.Body)
	}
}

func newAccessTestRouter(t *testing.T) (*gin.Engine, *persistence.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := persistence.Open(
		t.Context(),
		fmt.Sprintf("file:test-http-access-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-")),
	)
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
	if err := store.BackfillCompatibility(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	access, err := application.NewAccessManagement(store, store)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := serviceaccountcredential.NewLifecycle(
		bytes.Repeat([]byte{1}, serviceaccountcredential.MinimumDigestKeyBytes),
		serviceaccountcredential.DefaultPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceAccounts, err := application.NewServiceAccounts(
		store, store, lifecycle, defaultServiceAccountDelegablePermissions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	const token = "access-test-token"
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request.Header.Set("Authorization", "Bearer "+token)
		c.Next()
	})
	registerAccessAPI(router, access, serviceAccounts, token, nil, "")
	return router, store
}

func accessRequest(
	t *testing.T,
	router http.Handler,
	method string,
	path string,
	body string,
	ifMatch string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
