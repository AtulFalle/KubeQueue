package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var auditRoutePermissionMatrix = map[string]domain.Permission{
	"GET /api/v1/audit/events":          domain.PermissionAuditRead,
	"GET /api/v1/audit/events/:eventId": domain.PermissionAuditRead,
	"GET /api/v1/audit/export":          domain.PermissionAuditExport,
}

func init() {
	for route, permission := range auditRoutePermissionMatrix {
		routePermissionMatrix[route] = permission
	}
}

type auditApplication interface {
	Search(context.Context, application.AuditSearchRequest) (application.AuditSearchPage, error)
	ExportPage(context.Context, application.AuditSearchRequest) (application.AuditSearchPage, error)
	Get(context.Context, audit.InstallationID, audit.EventID) (audit.Event, error)
}

type auditAPI struct {
	audit auditApplication
}

func registerAuditAPI(
	router *gin.Engine,
	service auditApplication,
	token string,
	sessions *application.Sessions,
	browserOrigin string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	api := &auditAPI{audit: service}
	group := router.Group("/api/v1/audit")
	group.Use(
		apiAuthenticationMiddleware(token, sessions, oidcAuthenticators...),
		browserRequestProtectionMiddleware(browserOrigin, sessions),
	)
	group.GET("/events", api.search)
	group.GET("/events/:eventId", api.get)
	group.GET("/export", api.export)
}

func (a *auditAPI) search(c *gin.Context) {
	request, ok := auditSearchRequest(c)
	if !ok {
		return
	}
	page, err := a.audit.Search(c.Request.Context(), request)
	if err != nil {
		writeAuditError(c, err)
		return
	}
	writeAuditPage(c, page)
}

func (a *auditAPI) export(c *gin.Context) {
	request, ok := auditSearchRequest(c)
	if !ok {
		return
	}
	page, err := a.audit.ExportPage(c.Request.Context(), request)
	if err != nil {
		writeAuditError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeAuditPage(c, page)
}

func (a *auditAPI) get(c *gin.Context) {
	installationID, err := audit.NewInstallationID(strings.TrimSpace(c.Query("installationId")))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_INSTALLATION", "installationId is invalid")
		return
	}
	eventID, err := audit.NewEventID(strings.TrimSpace(c.Param("eventId")))
	if err != nil {
		writeError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Resource not found")
		return
	}
	event, err := a.audit.Get(c.Request.Context(), installationID, eventID)
	if err != nil {
		writeAuditError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, auditEventResponse(event))
}

func auditSearchRequest(c *gin.Context) (application.AuditSearchRequest, bool) {
	installationID, err := audit.NewInstallationID(strings.TrimSpace(c.Query("installationId")))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_INSTALLATION", "installationId is invalid")
		return application.AuditSearchRequest{}, false
	}
	filter := ports.AuditFilter{}
	if raw := strings.TrimSpace(c.Query("projectId")); raw != "" {
		projectID, err := audit.NewProjectID(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_AUDIT_PROJECT", "projectId is invalid")
			return application.AuditSearchRequest{}, false
		}
		filter.ProjectIDs = []audit.ProjectID{projectID}
	}
	if raw := strings.TrimSpace(c.Query("action")); raw != "" {
		action, err := audit.NewAction(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_AUDIT_ACTION", "action is invalid")
			return application.AuditSearchRequest{}, false
		}
		filter.Action = action
	}
	if raw := strings.TrimSpace(c.Query("decision")); raw != "" {
		filter.Decision = audit.AuthorizationDecision(raw)
		if filter.Decision != audit.DecisionAllow && filter.Decision != audit.DecisionDeny {
			writeError(c, http.StatusBadRequest, "INVALID_AUDIT_DECISION", "decision is invalid")
			return application.AuditSearchRequest{}, false
		}
	}
	if raw := strings.TrimSpace(c.Query("result")); raw != "" {
		filter.Result = audit.Result(raw)
		if filter.Result != audit.ResultSuccess && filter.Result != audit.ResultFailure {
			writeError(c, http.StatusBadRequest, "INVALID_AUDIT_RESULT", "result is invalid")
			return application.AuditSearchRequest{}, false
		}
	}
	filter.OccurredFrom, err = parseAuditTime(c.Query("occurredFrom"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_TIME", "occurredFrom must be RFC 3339")
		return application.AuditSearchRequest{}, false
	}
	filter.OccurredTo, err = parseAuditTime(c.Query("occurredTo"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_TIME", "occurredTo must be RFC 3339")
		return application.AuditSearchRequest{}, false
	}
	if !filter.OccurredFrom.IsZero() && !filter.OccurredTo.IsZero() &&
		!filter.OccurredFrom.Before(filter.OccurredTo) {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_TIME_RANGE",
			"occurredFrom must precede occurredTo")
		return application.AuditSearchRequest{}, false
	}
	limit, ok := auditLimit(c.Query("limit"))
	if !ok {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_LIMIT", "limit must be between 1 and 200")
		return application.AuditSearchRequest{}, false
	}
	cursor, err := decodeAuditCursor(strings.TrimSpace(c.Query("cursor")))
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_CURSOR", "cursor is invalid")
		return application.AuditSearchRequest{}, false
	}
	return application.AuditSearchRequest{
		InstallationID: installationID,
		Filter:         filter,
		Limit:          limit,
		After:          cursor,
	}, true
}

func auditLimit(raw string) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return 50, true
	}
	limit, err := strconv.Atoi(raw)
	return limit, err == nil && limit >= 1 && limit <= ports.MaxAuditPageSize
}

func parseAuditTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return value.Round(0).UTC(), nil
}

type auditCursorPayload struct {
	OccurredAt string `json:"occurredAt"`
	ID         string `json:"id"`
}

func encodeAuditCursor(cursor *ports.AuditCursor) string {
	if cursor == nil {
		return ""
	}
	payload, _ := json.Marshal(auditCursorPayload{
		OccurredAt: cursor.OccurredAt.UTC().Format(time.RFC3339Nano),
		ID:         cursor.EventID.String(),
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeAuditCursor(encoded string) (*ports.AuditCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 512 {
		return nil, errors.New("cursor exceeds limit")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var decoded auditCursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, decoded.OccurredAt)
	if err != nil {
		return nil, err
	}
	eventID, err := audit.NewEventID(decoded.ID)
	if err != nil {
		return nil, err
	}
	return &ports.AuditCursor{OccurredAt: occurredAt.Round(0).UTC(), EventID: eventID}, nil
}

func writeAuditPage(c *gin.Context, page application.AuditSearchPage) {
	items := make([]any, len(page.Events))
	for index, event := range page.Events {
		items[index] = auditEventResponse(event)
	}
	var nextCursor any
	if page.Next != nil {
		nextCursor = encodeAuditCursor(page.Next)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": items, "nextCursor": nextCursor})
}

func auditEventResponse(event audit.Event) gin.H {
	actor := event.Actor()
	groups := actor.EffectiveGroups()
	groupValues := make([]string, len(groups))
	for index, group := range groups {
		groupValues[index] = group.String()
	}
	response := gin.H{
		"id": event.ID().String(), "occurredAt": event.OccurredAt(),
		"requestId": event.RequestID().String(), "traceId": event.TraceID().String(),
		"actor": gin.H{
			"principalId": actor.PrincipalID().String(), "authenticationMethod": actor.AuthenticationMethod(),
			"credentialId": actor.CredentialID().String(), "effectiveGroups": groupValues,
		},
		"action":   event.Action().String(),
		"target":   gin.H{"type": event.Target().Type().String(), "id": event.Target().ID().String()},
		"scope":    auditScopeResponse(event.Scope()),
		"decision": event.Decision(), "result": event.Result(), "reason": event.Reason().String(),
		"source": gin.H{
			"address": event.Source().Address().String(), "provenance": event.Source().Provenance(),
			"userAgent": event.Source().UserAgent(),
		},
	}
	if summary, ok := event.Before(); ok {
		response["before"] = auditSummaryResponse(summary)
	}
	if summary, ok := event.After(); ok {
		response["after"] = auditSummaryResponse(summary)
	}
	return response
}

func auditScopeResponse(scope audit.Scope) gin.H {
	response := gin.H{"installationId": scope.InstallationID().String()}
	if value := scope.ProjectID().String(); value != "" {
		response["projectId"] = value
	}
	if value := scope.TeamID().String(); value != "" {
		response["teamId"] = value
	}
	if value := scope.Namespace().String(); value != "" {
		response["namespace"] = value
	}
	return response
}

func auditSummaryResponse(summary audit.Summary) gin.H {
	fields := summary.ChangedFields()
	changed := make([]string, len(fields))
	for index, field := range fields {
		changed[index] = field.String()
	}
	response := gin.H{
		"changedFields": changed, "redactionCount": summary.RedactionCount(),
		"truncated": summary.Truncated(),
	}
	if state := summary.State().String(); state != "" {
		response["state"] = state
	}
	return response
}

func writeAuditError(c *gin.Context, err error) {
	if writeAuthorizationError(c, err) {
		return
	}
	switch {
	case errors.Is(err, ports.ErrAuditEventNotFound):
		writeError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Resource not found")
	case errors.Is(err, application.ErrInvalidAuditSearch):
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_SEARCH", "audit search is invalid")
	default:
		writeError(c, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", err.Error())
	}
}

func auditDenialMiddleware(
	writer *application.BoundedAuditWriter,
	policy audit.RetentionPolicy,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ensureRequestCorrelation(c)
		c.Next()
		if writer == nil {
			return
		}
		status := c.Writer.Status()
		var event audit.Event
		var err error
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			event, err = newHTTPDenialAuditEvent(c, status)
		case bearerAuthenticationSucceeded(c):
			event, err = newHTTPSecurityAuditEvent(
				c, "authentication.succeeded", "authentication.succeeded",
				audit.DecisionAllow, audit.ResultSuccess,
			)
		default:
			return
		}
		if err != nil {
			return
		}
		writer.TryAppend(application.AuditWrite{
			Event: event, Policy: policy, Hold: audit.NoLegalHold(),
		})
	}
}

func newHTTPDenialAuditEvent(c *gin.Context, status int) (audit.Event, error) {
	actionValue := "authentication.failed"
	reasonValue := "authentication.failed"
	if status == http.StatusForbidden {
		actionValue = "authorization.denied"
		reasonValue = "authorization.denied"
	}
	return newHTTPSecurityAuditEvent(
		c, actionValue, reasonValue, audit.DecisionDeny, audit.ResultFailure,
	)
}

func newHTTPSecurityAuditEvent(
	c *gin.Context,
	actionValue string,
	reasonValue string,
	decision audit.AuthorizationDecision,
	result audit.Result,
) (audit.Event, error) {
	eventID, err := audit.NewEventID(uuid.NewString())
	if err != nil {
		return audit.Event{}, err
	}
	principalValue := "unauthenticated"
	credentialValue := "unresolved"
	var method audit.AuthenticationMethod
	installationValue := "default"
	if actor, actorErr := application.ActorFromContext(c.Request.Context()); actorErr == nil {
		principalValue = string(actor.PrincipalID)
		credentialValue = actor.CredentialID
		if credentialValue == "" {
			credentialValue = "principal:" + string(actor.PrincipalID)
		}
		installationValue = string(actor.InstallationID)
		method = auditAuthenticationMethod(actor.AuthenticationMethod)
	} else {
		method = attemptedAuditAuthenticationMethod(c.GetHeader("Authorization"))
	}
	actor, err := audit.NewActor(
		mustHTTPAuditValue(audit.NewPrincipalID(principalValue)),
		method,
		mustHTTPAuditValue(audit.NewCredentialID(credentialValue)),
		nil,
	)
	if err != nil {
		return audit.Event{}, err
	}
	route := strings.TrimPrefix(c.FullPath(), "/")
	if route == "" {
		route = "unmatched"
	}
	target, err := audit.NewTarget(
		mustHTTPAuditValue(audit.NewTargetType("http.route")),
		mustHTTPAuditValue(audit.NewTargetID(route)),
	)
	if err != nil {
		return audit.Event{}, err
	}
	scope, err := audit.NewScope(
		mustHTTPAuditValue(audit.NewInstallationID(installationValue)),
		audit.ProjectID{},
		audit.TeamID{},
		audit.Namespace{},
	)
	if err != nil {
		return audit.Event{}, err
	}
	address := directPeerAddress(c.Request.RemoteAddr)
	userAgent := c.Request.UserAgent()
	source, err := audit.NewTrustworthySource(address, audit.SourceDirectPeer, userAgent)
	if err != nil {
		source, err = audit.NewTrustworthySource(address, audit.SourceDirectPeer, "")
		if err != nil {
			return audit.Event{}, err
		}
	}
	requestID := uuid.NewString()
	traceID := uuid.NewString()
	if metadata, ok := application.AuditRequestMetadataFromContext(
		c.Request.Context(),
	); ok {
		requestID = metadata.RequestID
		traceID = metadata.TraceID
	}
	now := time.Now().UTC()
	return audit.NewEvent(audit.EventInput{
		ID: eventID, OccurredAt: now,
		RequestID: mustHTTPAuditValue(audit.NewRequestID(requestID)),
		TraceID:   mustHTTPAuditValue(audit.NewTraceID(traceID)),
		Actor:     actor,
		Action:    mustHTTPAuditValue(audit.NewAction(actionValue)),
		Target:    target,
		Scope:     scope,
		Decision:  decision,
		Result:    result,
		Reason:    mustHTTPAuditValue(audit.NewReasonCode(reasonValue)),
		Source:    source,
	})
}

func bearerAuthenticationSucceeded(c *gin.Context) bool {
	scheme, _, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || scheme != "Bearer" {
		return false
	}
	actor, err := application.ActorFromContext(c.Request.Context())
	if err != nil {
		return false
	}
	return actor.AuthenticationMethod == "OIDC" ||
		actor.AuthenticationMethod == domain.AuthenticationMethodOIDCClientCredentials ||
		actor.AuthenticationMethod == domain.AuthenticationMethodNativeServiceAccount ||
		actor.AuthenticationMethod == domain.AuthenticationMethodBreakGlass
}

func auditAuthenticationMethod(method string) audit.AuthenticationMethod {
	switch method {
	case domain.AuthenticationMethodNativeServiceAccount:
		return audit.AuthenticationServiceAccountToken
	case domain.AuthenticationMethodOIDCClientCredentials:
		return audit.AuthenticationOIDCClientCredentials
	case domain.AuthenticationMethodBreakGlass:
		return audit.AuthenticationBreakGlass
	case "OIDC":
		return audit.AuthenticationOIDCSession
	default:
		return audit.AuthenticationLegacyToken
	}
}

func attemptedAuditAuthenticationMethod(header string) audit.AuthenticationMethod {
	switch {
	case strings.HasPrefix(header, "Session "):
		return audit.AuthenticationOIDCSession
	case strings.HasPrefix(header, "Bearer kqsa."):
		return audit.AuthenticationServiceAccountToken
	case strings.HasPrefix(header, "Bearer kqbg."):
		return audit.AuthenticationBreakGlass
	default:
		return audit.AuthenticationLegacyToken
	}
}

func directPeerAddress(remoteAddress string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		if address, parseErr := netip.ParseAddr(host); parseErr == nil {
			return address.Unmap()
		}
	}
	if address, err := netip.ParseAddr(remoteAddress); err == nil {
		return address.Unmap()
	}
	return netip.MustParseAddr("127.0.0.1")
}

func mustHTTPAuditValue[T any](value T, err error) T {
	if err != nil {
		return *new(T)
	}
	return value
}
