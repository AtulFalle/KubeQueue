package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type API struct {
	jobs       *application.Jobs
	system     *application.System
	repository ports.Repository
}

func registerAPI(router *gin.Engine, jobs *application.Jobs, repository ports.Repository, token string) {
	api := &API{jobs: jobs, system: application.NewSystem(repository), repository: repository}
	group := router.Group("/api/v1")
	group.Use(adminToken(token))
	group.GET("/system/status", api.systemStatus)
	group.GET("/jobs", api.list)
	group.POST("/jobs", api.create)
	group.GET("/jobs/facets", api.facets)
	group.GET("/jobs/:id", api.get)
	group.DELETE("/jobs/:id", api.archive)
	group.GET("/jobs/:id/events", api.events)
	group.POST("/jobs/:id/actions/:action", api.command)
	group.PATCH("/jobs/:id/queue", api.updateQueue)
	group.GET("/queue", api.queue)
	group.PUT("/queue/order", api.reorder)
	group.GET("/events", api.stream)
}

func (a *API) systemStatus(c *gin.Context) {
	status, err := a.system.Status(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "SYSTEM_STATUS_UNAVAILABLE", err.Error())
		return
	}
	c.JSON(http.StatusOK, status)
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
	var request createRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	job, err := a.jobs.Create(c.Request.Context(), domain.CreateJob{
		Name: request.Name, Namespace: request.Namespace, Team: request.Team,
		Priority: request.Priority, ScheduledFor: request.ScheduledFor, Template: request.Template,
	})
	if err != nil {
		if errors.Is(err, domain.ErrNamespaceOutOfScope) {
			writeError(c, http.StatusUnprocessableEntity, "NAMESPACE_OUT_OF_SCOPE", err.Error())
			return
		}
		if errors.Is(err, domain.ErrNamespaceUnavailable) {
			writeError(c, http.StatusServiceUnavailable, "NAMESPACE_UNAVAILABLE", err.Error())
			return
		}
		writeError(c, http.StatusUnprocessableEntity, "INVALID_JOB", err.Error())
		return
	}
	c.JSON(http.StatusCreated, job)
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
	ctx := c.Request.Context()
	jobs, err := a.jobs.List(ctx, ports.JobFilter{
		Status: status, Namespace: c.Query("namespace"), Team: c.Query("team"),
		Search: c.Query("search"), Priority: priority,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	queueVersion, err := a.repository.QueueVersion(ctx)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "STORE_ERROR", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": jobs, "count": len(jobs), "queueVersion": queueVersion,
	})
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
		"items": jobs, "count": len(jobs), "queueVersion": version,
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
	c.JSON(http.StatusOK, job)
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
	events, err := a.jobs.Events(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": events})
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
	c.JSON(http.StatusOK, job)
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
	c.JSON(http.StatusOK, job)
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
	var request reorderRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	seen := make(map[string]struct{}, len(request.JobIDs))
	for _, id := range request.JobIDs {
		if uuid.Validate(id) != nil {
			writeError(c, http.StatusBadRequest, "INVALID_JOB_ID", "every job id must be a UUID")
			return
		}
		if _, duplicate := seen[id]; duplicate {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "jobIds must be unique")
			return
		}
		seen[id] = struct{}{}
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

func (a *API) stream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	ctx := c.Request.Context()
	lastPayload := ""
	for {
		jobs, err := a.repository.List(ctx, ports.JobFilter{})
		if err != nil {
			return
		}
		version := int64(0)
		for _, job := range jobs {
			version += job.Version
		}
		payload, _ := json.Marshal(gin.H{"type": "snapshot", "items": jobs, "version": version})
		if string(payload) != lastPayload {
			_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: jobs\ndata: %s\n\n", version, payload)
			c.Writer.Flush()
			lastPayload = string(payload)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func adminToken(configured string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if configured == "" || c.GetHeader("Authorization") == "Bearer "+configured {
			c.Next()
			return
		}
		c.Abort()
		writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid bearer token is required")
	}
}

func writeRepositoryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		writeError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, ports.ErrConflict):
		writeError(c, http.StatusConflict, "VERSION_CONFLICT", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	if status >= http.StatusInternalServerError {
		slog.Error("http request failed",
			"operation", "write_error",
			"path", c.FullPath(),
			"code", code,
			"error", message,
		)
		message = "the request could not be completed"
	}
	c.JSON(status, gin.H{"error": gin.H{
		"code": code, "message": message, "status": status,
	}})
}
