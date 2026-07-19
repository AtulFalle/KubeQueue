package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
)

func TestAuditRoutePermissionMatrixCoversRegisteredSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerAuditAPI(router, &auditHTTPApplication{}, "", nil, "")
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for route, permission := range auditRoutePermissionMatrix {
		if routePermissionMatrix[route] != permission {
			t.Errorf("%s is missing from the shared permission matrix", route)
		}
		if !registered[route] {
			t.Errorf("%s is not registered", route)
		}
		if !permission.Valid() {
			t.Errorf("%s uses invalid permission %q", route, permission)
		}
	}
}

func TestAuditSearchParsesBoundedFiltersAndCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	event := newHTTPAuditTestEvent(t, "event-001")
	next := &ports.AuditCursor{OccurredAt: event.OccurredAt(), EventID: event.ID()}
	service := &auditHTTPApplication{
		searchPage: application.AuditSearchPage{
			Events: []audit.Event{event},
			Next:   next,
		},
	}
	router := gin.New()
	registerAuditAPI(router, service, "audit-test-token", nil, "")
	from := event.OccurredAt().Add(-time.Hour)
	to := event.OccurredAt().Add(time.Hour)
	query := url.Values{
		"installationId": {"default"},
		"projectId":      {"project-one"},
		"action":         {"jobs.read"},
		"decision":       {"ALLOW"},
		"result":         {"SUCCESS"},
		"occurredFrom":   {from.Format(time.RFC3339Nano)},
		"occurredTo":     {to.Format(time.RFC3339Nano)},
	}
	response := auditHTTPRequest(
		t, router, "/api/v1/audit/events?"+query.Encode(), "audit-test-token",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.searchRequest.Limit != 50 ||
		service.searchRequest.InstallationID.String() != "default" ||
		len(service.searchRequest.Filter.ProjectIDs) != 1 ||
		service.searchRequest.Filter.ProjectIDs[0].String() != "project-one" ||
		service.searchRequest.Filter.Action.String() != "jobs.read" ||
		service.searchRequest.Filter.Decision != audit.DecisionAllow ||
		service.searchRequest.Filter.Result != audit.ResultSuccess ||
		!service.searchRequest.Filter.OccurredFrom.Equal(from) ||
		!service.searchRequest.Filter.OccurredTo.Equal(to) {
		t.Fatalf("search request = %#v", service.searchRequest)
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0]["id"] != "event-001" || body.NextCursor == "" {
		t.Fatalf("response = %#v", body)
	}
	decoded, err := decodeAuditCursor(body.NextCursor)
	if err != nil || decoded.EventID != event.ID() ||
		!decoded.OccurredAt.Equal(event.OccurredAt()) {
		t.Fatalf("decoded cursor = %#v, error %v", decoded, err)
	}
}

func TestAuditHandlersEnforceExportAndNonEnumeratingDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &auditHTTPApplication{getErr: ports.ErrAuditEventNotFound}
	router := gin.New()
	registerAuditAPI(router, service, "audit-test-token", nil, "")

	export := auditHTTPRequest(
		t,
		router,
		"/api/v1/audit/export?installationId=default&limit=200",
		"audit-test-token",
	)
	if export.Code != http.StatusOK || service.exportRequest.Limit != 200 ||
		export.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export status/request = %d/%#v", export.Code, service.exportRequest)
	}
	missing := auditHTTPRequest(
		t,
		router,
		"/api/v1/audit/events/event-unknown?installationId=default",
		"audit-test-token",
	)
	if missing.Code != http.StatusNotFound ||
		!containsJSONCode(missing.Body.Bytes(), "RESOURCE_NOT_FOUND") {
		t.Fatalf("missing detail = %d: %s", missing.Code, missing.Body)
	}
	service.getErr = domain.ErrAccessDenied
	forbidden := auditHTTPRequest(
		t,
		router,
		"/api/v1/audit/events/event-unknown?installationId=default",
		"audit-test-token",
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden detail = %d: %s", forbidden.Code, forbidden.Body)
	}
}

func TestAuditDenialMiddlewareQueuesWithoutBlocking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	appender := &auditHTTPAppender{appended: make(chan audit.Event, 1)}
	writer, err := application.NewBoundedAuditWriter(appender, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy := mustHTTPAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() { runResult <- writer.Run(ctx) }()

	router := gin.New()
	router.Use(auditDenialMiddleware(writer, policy))
	var requestMetadata application.AuditRequestMetadata
	router.GET("/api/v1/check", func(c *gin.Context) {
		requestMetadata, _ = application.AuditRequestMetadataFromContext(c.Request.Context())
		c.Status(http.StatusUnauthorized)
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/check", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("User-Agent", "KubeQueue audit test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if requestMetadata.RequestID == "" || requestMetadata.TraceID == "" ||
		requestMetadata.Source.String() != "192.0.2.10" ||
		requestMetadata.UserAgent != "KubeQueue audit test" {
		t.Fatalf("request audit metadata = %#v", requestMetadata)
	}
	select {
	case event := <-appender.appended:
		if event.Decision() != audit.DecisionDeny ||
			event.Result() != audit.ResultFailure ||
			event.Action().String() != "authentication.failed" {
			t.Fatalf("denial event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("denial event was not queued")
	}
	cancel()
	if err := <-runResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("writer error = %v", err)
	}
}

type auditHTTPApplication struct {
	searchPage    application.AuditSearchPage
	searchRequest application.AuditSearchRequest
	searchErr     error
	exportPage    application.AuditSearchPage
	exportRequest application.AuditSearchRequest
	exportErr     error
	event         audit.Event
	getErr        error
}

func (a *auditHTTPApplication) Search(
	_ context.Context,
	request application.AuditSearchRequest,
) (application.AuditSearchPage, error) {
	a.searchRequest = request
	return a.searchPage, a.searchErr
}

func (a *auditHTTPApplication) ExportPage(
	_ context.Context,
	request application.AuditSearchRequest,
) (application.AuditSearchPage, error) {
	a.exportRequest = request
	return a.exportPage, a.exportErr
}

func (a *auditHTTPApplication) Get(
	context.Context,
	audit.InstallationID,
	audit.EventID,
) (audit.Event, error) {
	return a.event, a.getErr
}

type auditHTTPAppender struct {
	appended chan audit.Event
}

func (a *auditHTTPAppender) AppendAuditEvent(
	_ context.Context,
	event audit.Event,
	_ audit.RetentionPolicy,
	_ audit.LegalHold,
) error {
	a.appended <- event
	return nil
}

func auditHTTPRequest(
	t *testing.T,
	router http.Handler,
	path string,
	token string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func containsJSONCode(body []byte, code string) bool {
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &response) == nil && response.Error.Code == code
}

func newHTTPAuditTestEvent(t *testing.T, id string) audit.Event {
	t.Helper()
	actorValue, actorErr := audit.NewActor(
		mustHTTPAuditTestConstruct(t, audit.NewPrincipalID, "principal-one"),
		audit.AuthenticationOIDCSession,
		mustHTTPAuditTestConstruct(t, audit.NewCredentialID, "session-one"),
		nil,
	)
	actor := mustHTTPAuditTestValue(t, actorValue, actorErr)
	targetValue, targetErr := audit.NewTarget(
		mustHTTPAuditTestConstruct(t, audit.NewTargetType, "job"),
		mustHTTPAuditTestConstruct(t, audit.NewTargetID, "job-one"),
	)
	target := mustHTTPAuditTestValue(t, targetValue, targetErr)
	scopeValue, scopeErr := audit.NewScope(
		mustHTTPAuditTestConstruct(t, audit.NewInstallationID, "default"),
		mustHTTPAuditTestConstruct(t, audit.NewProjectID, "project-one"),
		audit.TeamID{},
		mustHTTPAuditTestConstruct(t, audit.NewNamespace, "default"),
	)
	scope := mustHTTPAuditTestValue(t, scopeValue, scopeErr)
	sourceValue, sourceErr := audit.NewTrustworthySource(
		netip.MustParseAddr("192.0.2.10"), audit.SourceDirectPeer, "test",
	)
	source := mustHTTPAuditTestValue(t, sourceValue, sourceErr)
	eventValue, eventErr := audit.NewEvent(audit.EventInput{
		ID: idValue(t, id), OccurredAt: time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC),
		RequestID: mustHTTPAuditTestConstruct(t, audit.NewRequestID, "request-one"),
		TraceID:   mustHTTPAuditTestConstruct(t, audit.NewTraceID, "trace-one"),
		Actor:     actor, Action: mustHTTPAuditTestConstruct(t, audit.NewAction, "jobs.read"),
		Target: target, Scope: scope, Decision: audit.DecisionAllow,
		Result: audit.ResultSuccess,
		Reason: mustHTTPAuditTestConstruct(t, audit.NewReasonCode, "request.accepted"),
		Source: source,
	})
	return mustHTTPAuditTestValue(t, eventValue, eventErr)
}

func idValue(t *testing.T, value string) audit.EventID {
	t.Helper()
	return mustHTTPAuditTestConstruct(t, audit.NewEventID, value)
}

func mustHTTPAuditTestConstruct[T, A any](
	t *testing.T,
	constructor func(A) (T, error),
	input A,
) T {
	t.Helper()
	value, err := constructor(input)
	return mustHTTPAuditTestValue(t, value, err)
}

func mustHTTPAuditTestValue[T any](t *testing.T, value T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
