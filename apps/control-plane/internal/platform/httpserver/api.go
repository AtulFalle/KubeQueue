package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/sensitivedata"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type API struct {
	jobs        *application.Jobs
	system      *application.System
	diagnostics *application.SupportDiagnostics
}

var routePermissionMatrix = map[string]domain.Permission{
	"GET /api/v1/system/status":                   domain.PermissionSystemStatusRead,
	"GET /api/v1/support/diagnostics":             domain.PermissionSupportDiagnosticsRead,
	"GET /api/v1/jobs":                            domain.PermissionJobsList,
	"POST /api/v1/jobs":                           domain.PermissionJobsSubmit,
	"GET /api/v1/jobs/facets":                     domain.PermissionJobsList,
	"GET /api/v1/jobs/:id":                        domain.PermissionJobsRead,
	"GET /api/v1/jobs/:id/manifest":               domain.PermissionJobsManifestRead,
	"DELETE /api/v1/jobs/:id":                     domain.PermissionJobsArchive,
	"GET /api/v1/jobs/:id/events":                 domain.PermissionJobEventsRead,
	"POST /api/v1/jobs/:id/actions/start":         domain.PermissionJobsResume,
	"POST /api/v1/jobs/:id/actions/resume":        domain.PermissionJobsResume,
	"POST /api/v1/jobs/:id/actions/pause":         domain.PermissionJobsPause,
	"POST /api/v1/jobs/:id/actions/terminate":     domain.PermissionJobsTerminate,
	"POST /api/v1/jobs/:id/actions/retry":         domain.PermissionJobsRetry,
	"PATCH /api/v1/jobs/:id/queue":                domain.PermissionQueueEntryUpdate,
	"GET /api/v1/queue":                           domain.PermissionQueueRead,
	"PUT /api/v1/queue/order":                     domain.PermissionQueueGlobalReorder,
	"PUT /api/v1/projects/:projectId/queue/order": domain.PermissionQueueProjectReorder,
	"GET /api/v1/events":                          domain.PermissionEventStreamRead,
}

func registerAPI(
	router *gin.Engine,
	jobs *application.Jobs,
	diagnostics *application.SupportDiagnostics,
	repository ports.Repository,
	authorizer application.Authorizer,
	token string,
	sessions *application.Sessions,
	browserOrigin string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	api := &API{
		jobs: jobs, system: application.NewSystem(repository, authorizer),
		diagnostics: diagnostics,
	}
	group := router.Group("/api/v1")
	group.Use(
		apiAuthenticationMiddleware(token, sessions, oidcAuthenticators...),
		browserRequestProtectionMiddleware(browserOrigin, sessions),
	)
	group.GET("/system/status", api.systemStatus)
	group.GET("/support/diagnostics", api.supportDiagnostics)
	group.GET("/jobs", api.list)
	group.POST("/jobs", api.create)
	group.GET("/jobs/facets", api.facets)
	group.GET("/jobs/:id", api.get)
	group.GET("/jobs/:id/manifest", api.manifest)
	group.DELETE("/jobs/:id", api.archive)
	group.GET("/jobs/:id/events", api.events)
	group.POST("/jobs/:id/actions/:action", api.command)
	group.PATCH("/jobs/:id/queue", api.updateQueue)
	group.GET("/queue", api.queue)
	group.PUT("/queue/order", api.reorder)
	group.PUT("/projects/:projectId/queue/order", api.reorderProject)
	group.GET("/events", api.stream)
}

func (a *API) systemStatus(c *gin.Context) {
	status, err := a.system.Status(c.Request.Context())
	if err != nil {
		if writeAuthorizationError(c, err) {
			return
		}
		writeError(c, http.StatusServiceUnavailable, "SYSTEM_STATUS_UNAVAILABLE", err.Error())
		return
	}
	c.JSON(http.StatusOK, status)
}

func (a *API) supportDiagnostics(c *gin.Context) {
	if a.diagnostics == nil {
		writeError(c, http.StatusServiceUnavailable, "SUPPORT_DIAGNOSTICS_UNAVAILABLE",
			"Support diagnostics are unavailable")
		return
	}
	diagnostics, err := a.diagnostics.Snapshot(c.Request.Context())
	if err != nil {
		if writeAuthorizationError(c, err) {
			return
		}
		metadata, _ := application.AuditRequestMetadataFromContext(c.Request.Context())
		slog.Error("support diagnostics failed",
			"operation", "support_diagnostics",
			"request_id", metadata.RequestID,
			"trace_id", metadata.TraceID,
			"error", err,
		)
		writeError(c, http.StatusServiceUnavailable, "SUPPORT_DIAGNOSTICS_UNAVAILABLE",
			"Support diagnostics are unavailable")
		return
	}
	encoded, err := json.Marshal(diagnostics)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "SUPPORT_DIAGNOSTICS_ENCODING_FAILED",
			"Support diagnostics are unavailable")
		return
	}
	inspection, err := sensitivedata.InspectManifestJSON(encoded, sensitivedata.Limits{})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "SUPPORT_DIAGNOSTICS_REDACTION_FAILED",
			"Support diagnostics are unavailable")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="kubequeue-diagnostics.json"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", inspection.Redacted)
}

type createRequest struct {
	Name         string          `json:"name" binding:"required"`
	Namespace    string          `json:"namespace" binding:"required"`
	Team         string          `json:"team"`
	Priority     int             `json:"priority"`
	ScheduledFor *time.Time      `json:"scheduledFor"`
	Template     json.RawMessage `json:"template" binding:"required"`
}

func (a *API) create(c *gin.Context) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey != "" && !boundedCorrelationID.MatchString(idempotencyKey) {
		writeError(c, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY",
			"Idempotency-Key must be 1 to 128 safe identifier characters")
		return
	}
	if idempotencyKey == "" {
		idempotencyKey = "generated:" + uuid.NewString()
	}
	var request createRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	job, err := a.jobs.Create(c.Request.Context(), domain.CreateJob{
		Name: request.Name, Namespace: request.Namespace, Team: request.Team,
		Priority: request.Priority, ScheduledFor: request.ScheduledFor, Template: request.Template,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if writeAuthorizationError(c, err) {
			return
		}
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusUnprocessableEntity, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		if errors.Is(err, domain.ErrNamespaceUnavailable) {
			writeError(c, http.StatusServiceUnavailable, "NAMESPACE_UNAVAILABLE", err.Error())
			return
		}
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			writeError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT",
				"idempotency key was already used for different Job intent",
				map[string]string{"header": "Idempotency-Key"})
			return
		}
		var rejection *application.PolicyQuotaRejection
		if errors.As(err, &rejection) &&
			rejection.Detail.Reason == policyquota.ReasonIdempotencyConflict {
			writeError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT",
				"idempotency key was already used for different Job intent",
				map[string]string{"header": "Idempotency-Key"})
			return
		}
		writeError(c, http.StatusUnprocessableEntity, "INVALID_JOB", err.Error())
		return
	}
	c.JSON(http.StatusCreated, newJobResponse(job))
}

func (a *API) list(c *gin.Context) {
	status := domain.State(strings.ToUpper(c.Query("status")))
	if status != "" && !status.Valid() {
		writeError(c, http.StatusBadRequest, "INVALID_STATUS", "status is not a supported Job state")
		return
	}
	var priority *int
	if value := c.Query("priority"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < -1000 || parsed > 1000 {
			writeError(c, http.StatusBadRequest, "INVALID_PRIORITY", "priority must be between -1000 and 1000")
			return
		}
		priority = &parsed
	}
	rawSearch := c.Query("search")
	if utf8.RuneCountInString(rawSearch) > 200 {
		writeError(c, http.StatusBadRequest, "INVALID_SEARCH", "search must not exceed 200 characters")
		return
	}
	search := strings.Join(strings.Fields(rawSearch), " ")
	projectID := domain.ProjectID(strings.TrimSpace(c.Query("projectId")))
	if len(projectID) > 128 {
		writeError(c, http.StatusBadRequest, "INVALID_PROJECT", "project must not exceed 128 characters")
		return
	}
	syncStatus := domain.SyncStatus(strings.ToUpper(strings.TrimSpace(c.Query("synchronization"))))
	if syncStatus != "" && !validSyncStatus(syncStatus) {
		writeError(c, http.StatusBadRequest, "INVALID_SYNCHRONIZATION",
			"synchronization is not a supported status")
		return
	}
	createdAfter, ok := parseOptionalTimeQuery(c, "createdAfter")
	if !ok {
		return
	}
	createdBefore, ok := parseOptionalTimeQuery(c, "createdBefore")
	if !ok {
		return
	}
	updatedAfter, ok := parseOptionalTimeQuery(c, "updatedAfter")
	if !ok {
		return
	}
	updatedBefore, ok := parseOptionalTimeQuery(c, "updatedBefore")
	if !ok {
		return
	}
	if !validTimeRange(createdAfter, createdBefore) || !validTimeRange(updatedAfter, updatedBefore) {
		writeError(c, http.StatusBadRequest, "INVALID_TIME_RANGE",
			"after timestamps must precede before timestamps")
		return
	}
	sortBy := ports.JobSort(c.DefaultQuery("sort", string(ports.JobSortQueue)))
	if !sortBy.Valid() {
		writeError(c, http.StatusBadRequest, "INVALID_SORT", "sort is not supported")
		return
	}
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	filter := ports.JobFilter{
		Status: status, Namespace: c.Query("namespace"), Team: c.Query("team"),
		Search: search, Priority: priority, ProjectID: projectID, SyncStatus: syncStatus,
		CreatedAfter: createdAfter, CreatedBefore: createdBefore,
		UpdatedAfter: updatedAfter, UpdatedBefore: updatedBefore,
	}
	var after *ports.JobPageCursor
	if value := c.Query("cursor"); value != "" {
		decoded, err := decodeJobCursor(value, filter, sortBy)
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "cursor is malformed or does not match this query")
			return
		}
		after = &decoded
	}
	page, err := a.jobs.ListPage(c.Request.Context(), ports.JobPageRequest{
		Filter: filter, Sort: sortBy, Limit: limit, After: after,
	})
	if err != nil {
		if writeAuthorizationError(c, err) {
			return
		}
		writeError(c, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	var nextCursor *string
	if page.Next != nil {
		encoded, err := encodeJobCursor(*page.Next, filter)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "CURSOR_ENCODING_ERROR", err.Error())
			return
		}
		nextCursor = &encoded
	}
	c.JSON(http.StatusOK, gin.H{
		"items": newJobResponses(page.Items), "count": len(page.Items), "queueVersion": page.QueueVersion,
		"nextCursor": nextCursor,
	})
}

type jobCursorEnvelope struct {
	Version    int           `json:"v"`
	Sort       ports.JobSort `json:"s"`
	Priority   int           `json:"p,omitempty"`
	Position   int64         `json:"o,omitempty"`
	Value      string        `json:"k"`
	Secondary  string        `json:"k2,omitempty"`
	ID         string        `json:"id"`
	FilterHash string        `json:"f"`
}

func encodeJobCursor(cursor ports.JobPageCursor, filter ports.JobFilter) (string, error) {
	envelope := jobCursorEnvelope{
		Version: 1, Sort: cursor.Sort, Priority: cursor.Priority, Position: cursor.Position,
		Value: cursor.Value, Secondary: cursor.Secondary, ID: cursor.ID,
		FilterHash: jobFilterHash(filter, cursor.Sort),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode job cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeJobCursor(
	value string, filter ports.JobFilter, sortBy ports.JobSort,
) (ports.JobPageCursor, error) {
	if len(value) > 2048 {
		return ports.JobPageCursor{}, errors.New("cursor exceeds maximum length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ports.JobPageCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope jobCursorEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return ports.JobPageCursor{}, fmt.Errorf("decode cursor payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ports.JobPageCursor{}, errors.New("cursor contains trailing data")
	}
	if envelope.Version != 1 || envelope.Sort != sortBy ||
		envelope.FilterHash != jobFilterHash(filter, sortBy) ||
		uuid.Validate(envelope.ID) != nil || envelope.Value == "" {
		return ports.JobPageCursor{}, errors.New("cursor metadata is invalid")
	}
	switch sortBy {
	case ports.JobSortQueue:
		if envelope.Position < 1 || envelope.Priority < -1000 || envelope.Priority > 1000 {
			return ports.JobPageCursor{}, errors.New("queue cursor key is invalid")
		}
		if _, err := time.Parse(time.RFC3339Nano, envelope.Value); err != nil {
			return ports.JobPageCursor{}, fmt.Errorf("parse queue cursor time: %w", err)
		}
	case ports.JobSortCreatedAt, ports.JobSortCreatedAtDesc,
		ports.JobSortUpdatedAt, ports.JobSortUpdatedAtDesc:
		if _, err := time.Parse(time.RFC3339Nano, envelope.Value); err != nil {
			return ports.JobPageCursor{}, fmt.Errorf("parse cursor time: %w", err)
		}
	case ports.JobSortName, ports.JobSortNameDesc:
		if envelope.Secondary == "" || strings.ToLower(envelope.Secondary) != envelope.Value {
			return ports.JobPageCursor{}, errors.New("name cursor key is invalid")
		}
	default:
		return ports.JobPageCursor{}, errors.New("cursor sort is invalid")
	}
	return ports.JobPageCursor{
		Sort: sortBy, Priority: envelope.Priority, Position: envelope.Position,
		Value: envelope.Value, Secondary: envelope.Secondary, ID: envelope.ID,
	}, nil
}

func jobFilterHash(filter ports.JobFilter, sortBy ports.JobSort) string {
	canonical := struct {
		Status        domain.State      `json:"status"`
		Namespace     string            `json:"namespace"`
		Team          string            `json:"team"`
		Search        string            `json:"search"`
		Priority      *int              `json:"priority"`
		ProjectID     domain.ProjectID  `json:"projectId"`
		SyncStatus    domain.SyncStatus `json:"synchronization"`
		CreatedAfter  *time.Time        `json:"createdAfter"`
		CreatedBefore *time.Time        `json:"createdBefore"`
		UpdatedAfter  *time.Time        `json:"updatedAfter"`
		UpdatedBefore *time.Time        `json:"updatedBefore"`
		Sort          ports.JobSort     `json:"sort"`
	}{
		Status: filter.Status, Namespace: filter.Namespace, Team: filter.Team,
		Search: filter.Search, Priority: filter.Priority, ProjectID: filter.ProjectID,
		SyncStatus: filter.SyncStatus, CreatedAfter: filter.CreatedAfter,
		CreatedBefore: filter.CreatedBefore, UpdatedAfter: filter.UpdatedAfter,
		UpdatedBefore: filter.UpdatedBefore, Sort: sortBy,
	}
	payload, _ := json.Marshal(canonical)
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func parseOptionalTimeQuery(c *gin.Context, name string) (*time.Time, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_TIME",
			name+" must be an RFC 3339 timestamp")
		return nil, false
	}
	parsed = parsed.UTC()
	return &parsed, true
}

func validTimeRange(after, before *time.Time) bool {
	return after == nil || before == nil || after.Before(*before)
}

func validSyncStatus(status domain.SyncStatus) bool {
	switch status {
	case domain.SyncStatusSynced, domain.SyncStatusPending, domain.SyncStatusMissing,
		domain.SyncStatusStale, domain.SyncStatusError, domain.SyncStatusOutOfScope,
		domain.SyncStatusConflicted:
		return true
	default:
		return false
	}
}

func (a *API) facets(c *gin.Context) {
	facets, err := a.jobs.Facets(c.Request.Context())
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, facets)
}

func (a *API) queue(c *gin.Context) {
	jobs, version, err := a.jobs.Queue(c.Request.Context())
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": newJobResponses(jobs), "count": len(jobs), "queueVersion": version,
	})
}

func (a *API) get(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	job, err := a.jobs.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, newJobResponse(job))
}

func (a *API) manifest(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	stored, err := a.jobs.Manifest(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	response, err := newJobManifestResponse(c.Param("id"), stored)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "INVALID_STORED_MANIFEST", err.Error())
		return
	}
	c.JSON(http.StatusOK, response)
}

func (a *API) archive(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	if err := a.jobs.Archive(c.Request.Context(), c.Param("id")); err != nil {
		if errors.Is(err, domain.ErrNotArchivable) {
			writeError(c, http.StatusConflict, "JOB_NOT_ARCHIVABLE", err.Error())
			return
		}
		writeRepositoryError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *API) events(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	var before int64
	if value := c.Query("cursor"); value != "" {
		decoded, err := decodeEventCursor(value, c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "cursor is malformed or belongs to another job")
			return
		}
		before = decoded
	}
	page, err := a.jobs.EventsPage(c.Request.Context(), c.Param("id"), ports.EventPageRequest{
		Limit: limit, Before: before,
	})
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	var nextCursor *string
	if page.Next != nil {
		encoded, err := encodeEventCursor(c.Param("id"), *page.Next)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "CURSOR_ENCODING_ERROR", err.Error())
			return
		}
		nextCursor = &encoded
	}
	c.JSON(http.StatusOK, gin.H{"items": page.Items, "nextCursor": nextCursor})
}

type eventCursorEnvelope struct {
	Version int    `json:"v"`
	JobID   string `json:"jobId"`
	Before  int64  `json:"before"`
}

func encodeEventCursor(jobID string, before int64) (string, error) {
	payload, err := json.Marshal(eventCursorEnvelope{Version: 1, JobID: jobID, Before: before})
	if err != nil {
		return "", fmt.Errorf("encode event cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeEventCursor(value, jobID string) (int64, error) {
	if len(value) > 2048 {
		return 0, errors.New("cursor exceeds maximum length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("decode event cursor: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope eventCursorEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return 0, fmt.Errorf("decode event cursor payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, errors.New("event cursor contains trailing data")
	}
	if envelope.Version != 1 || envelope.JobID != jobID || envelope.Before < 1 {
		return 0, errors.New("event cursor metadata is invalid")
	}
	return envelope.Before, nil
}

func (a *API) command(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	switch c.Param("action") {
	case "start", "resume", "pause", "terminate", "retry":
	default:
		writeError(c, http.StatusBadRequest, "INVALID_ACTION", "action is not supported")
		return
	}
	job, err := a.jobs.Command(c.Request.Context(), c.Param("id"), c.Param("action"))
	if err != nil {
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusConflict, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		if errors.Is(err, domain.ErrUnmanagedJob) {
			writeError(c, http.StatusConflict, "JOB_NOT_MANAGED", err.Error())
			return
		}
		if errors.Is(err, domain.ErrInvalidTransition) {
			writeError(c, http.StatusConflict, "INVALID_TRANSITION", err.Error())
			return
		}
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, newJobResponse(job))
}

type queueRequest struct {
	Priority     int        `json:"priority" binding:"min=-1000,max=1000"`
	Position     int64      `json:"position" binding:"min=1"`
	Version      int64      `json:"version" binding:"min=1"`
	ScheduledFor *time.Time `json:"scheduledFor"`
}

func (a *API) updateQueue(c *gin.Context) {
	if !validateJobID(c) {
		return
	}
	var request queueRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	job, err := a.jobs.UpdateQueue(
		c.Request.Context(), c.Param("id"), request.Priority, request.Position, request.Version,
		request.ScheduledFor,
	)
	if err != nil {
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusConflict, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		if errors.Is(err, domain.ErrUnmanagedJob) {
			writeError(c, http.StatusConflict, "JOB_NOT_MANAGED", err.Error())
			return
		}
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, newJobResponse(job))
}

func validateJobID(c *gin.Context) bool {
	if err := uuid.Validate(c.Param("id")); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_JOB_ID", "job id must be a UUID")
		return false
	}
	return true
}

type reorderRequest struct {
	JobIDs  []string `json:"jobIds" binding:"required,min=1"`
	Version int64    `json:"version"`
}

func (a *API) reorder(c *gin.Context) {
	request, ok := bindReorderRequest(c)
	if !ok {
		return
	}
	version, err := a.jobs.Reorder(c.Request.Context(), request.JobIDs, request.Version)
	if err != nil {
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusConflict, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func (a *API) reorderProject(c *gin.Context) {
	request, ok := bindReorderRequest(c)
	if !ok {
		return
	}
	version, err := a.jobs.ReorderProject(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")),
		request.JobIDs, request.Version,
	)
	if err != nil {
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusConflict, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func bindReorderRequest(c *gin.Context) (reorderRequest, bool) {
	var request reorderRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return reorderRequest{}, false
	}
	seen := make(map[string]struct{}, len(request.JobIDs))
	for _, id := range request.JobIDs {
		if uuid.Validate(id) != nil {
			writeError(c, http.StatusBadRequest, "INVALID_JOB_ID", "every job id must be a UUID")
			return reorderRequest{}, false
		}
		if _, duplicate := seen[id]; duplicate {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "jobIds must be unique")
			return reorderRequest{}, false
		}
		seen[id] = struct{}{}
	}
	return request, true
}

func (a *API) stream(c *gin.Context) {
	ctx := c.Request.Context()
	latest, err := a.jobs.LatestStreamCursor(ctx)
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	cursor := latest
	if value := strings.TrimSpace(c.GetHeader("Last-Event-ID")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			writeError(c, http.StatusBadRequest, "INVALID_EVENT_CURSOR",
				"Last-Event-ID must be a non-negative integer")
			return
		}
		cursor = parsed
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ready, _ := json.Marshal(gin.H{"type": "ready", "cursor": cursor})
	_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: jobs\ndata: %s\n\n", cursor, ready)
	c.Writer.Flush()

	const changePageLimit = 100
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		page, err := a.jobs.StreamChanges(ctx, cursor, changePageLimit)
		if err != nil {
			return
		}
		if len(page.Changes) > 0 {
			jobIDs := uniqueChangedJobIDs(page.Changes)
			payload, _ := json.Marshal(gin.H{
				"type": "invalidate", "jobIds": jobIDs, "cursor": page.Cursor,
			})
			_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: jobs\ndata: %s\n\n", page.Cursor, payload)
			c.Writer.Flush()
			cursor = page.Cursor
			if page.More {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func uniqueChangedJobIDs(changes []ports.JobChange) []string {
	result := make([]string, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if _, exists := seen[change.JobID]; exists {
			continue
		}
		seen[change.JobID] = struct{}{}
		result = append(result, change.JobID)
	}
	return result
}

func writeAuthorizationError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, application.ErrMissingPrincipal):
		writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
	case errors.Is(err, domain.ErrAccessDenied):
		writeError(c, http.StatusForbidden, "FORBIDDEN", "permission is required")
	default:
		return false
	}
	return true
}

func writeRepositoryError(c *gin.Context, err error) {
	if writeAuthorizationError(c, err) {
		return
	}
	switch {
	case errors.Is(err, ports.ErrNotFound):
		writeError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, ports.ErrConflict):
		writeError(c, http.StatusConflict, "VERSION_CONFLICT", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func writeError(c *gin.Context, status int, code, message string, supplied ...map[string]string) {
	metadata, _ := application.AuditRequestMetadataFromContext(c.Request.Context())
	if metadata.RequestID == "" {
		metadata.RequestID = uuid.NewString()
		c.Header(requestIDHeader, metadata.RequestID)
	}
	if status >= http.StatusInternalServerError {
		slog.Error("http request failed",
			"operation", "write_error",
			"request_id", metadata.RequestID,
			"trace_id", metadata.TraceID,
			"path", c.FullPath(),
			"code", code,
			"error", message,
		)
		message = "the request could not be completed"
	}
	details := boundedErrorDetails(supplied...)
	c.JSON(status, gin.H{
		"requestId": metadata.RequestID,
		"error": gin.H{
			"code": code, "message": truncateRunes(message, 512), "status": status,
			"details": details,
		},
	})
}

func boundedErrorDetails(supplied ...map[string]string) map[string]string {
	details := make(map[string]string)
	if len(supplied) == 0 {
		return details
	}
	for key, value := range supplied[0] {
		if len(details) == 16 {
			break
		}
		if !boundedErrorDetailKey.MatchString(key) {
			continue
		}
		details[key] = truncateRunes(value, 512)
	}
	return details
}

var boundedErrorDetailKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
