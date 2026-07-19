package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
)

var admissionAdministrationRoutePermissions = map[string]domain.Permission{
	"GET /api/v1/installation/admission-policy":            domain.PermissionPoliciesRead,
	"PATCH /api/v1/installation/admission-policy":          domain.PermissionPoliciesManage,
	"GET /api/v1/projects/:projectId/admission-settings":   domain.PermissionPoliciesRead,
	"PATCH /api/v1/projects/:projectId/admission-settings": domain.PermissionPoliciesManage,
	"GET /api/v1/projects/:projectId/quota-usage":          domain.PermissionQuotasManage,
	"GET /api/v1/projects/:projectId/admission-decisions":  domain.PermissionQueueRead,
}

func init() {
	for route, permission := range admissionAdministrationRoutePermissions {
		routePermissionMatrix[route] = permission
	}
}

type admissionAdministrationAPI struct {
	service *application.AdmissionAdministration
}

func registerAdmissionAdministrationAPI(
	router *gin.Engine,
	service *application.AdmissionAdministration,
	token string,
	sessions *application.Sessions,
	browserOrigin string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	api := &admissionAdministrationAPI{service: service}
	group := router.Group("/api/v1")
	group.Use(
		apiAuthenticationMiddleware(token, sessions, oidcAuthenticators...),
		browserRequestProtectionMiddleware(browserOrigin, sessions),
	)
	group.GET("/installation/admission-policy", api.installationPolicy)
	group.PATCH("/installation/admission-policy", api.updateInstallationPolicy)
	group.GET("/projects/:projectId/admission-settings", api.projectSettings)
	group.PATCH("/projects/:projectId/admission-settings", api.updateProjectSettings)
	group.GET("/projects/:projectId/quota-usage", api.projectQuotaUsage)
	group.GET("/projects/:projectId/admission-decisions", api.admissionDecisions)
}

func (a *admissionAdministrationAPI) installationPolicy(c *gin.Context) {
	policy, err := a.service.InstallationPolicy(c.Request.Context())
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	c.Header("ETag", installationPolicyETag(policy.Ref.Version))
	c.JSON(http.StatusOK, admissionPolicyResponse(policy))
}

func (a *admissionAdministrationAPI) updateInstallationPolicy(c *gin.Context) {
	version, ok := parseInstallationPolicyETag(c.GetHeader("If-Match"))
	if !ok {
		writeError(c, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match must identify a policy version")
		return
	}
	var request admissionPolicyRequest
	if !bindAccessJSON(c, &request) {
		return
	}
	policy, err := a.service.UpdateInstallationPolicy(
		c.Request.Context(), version, request.Rules.domain(),
	)
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	c.Header("ETag", installationPolicyETag(policy.Ref.Version))
	c.JSON(http.StatusOK, admissionPolicyResponse(policy))
}

func (a *admissionAdministrationAPI) projectSettings(c *gin.Context) {
	settings, err := a.service.ProjectSettings(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")),
	)
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	c.Header("ETag", projectSettingsETag(policyVersion(settings.Policy), settings.Scheduling.Version))
	c.JSON(http.StatusOK, projectSettingsResponse(settings))
}

func (a *admissionAdministrationAPI) updateProjectSettings(c *gin.Context) {
	expectedPolicyVersion, schedulingVersion, ok := parseProjectSettingsETag(c.GetHeader("If-Match"))
	if !ok {
		writeError(c, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match must identify policy and scheduling versions")
		return
	}
	var request struct {
		SchedulingWeight uint64                `json:"schedulingWeight" binding:"required"`
		Rules            admissionRulesRequest `json:"rules" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	if request.SchedulingWeight < 1 || request.SchedulingWeight > 1_000_000 {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "schedulingWeight must be between 1 and 1000000")
		return
	}
	settings, err := a.service.UpdateProjectSettings(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")),
		expectedPolicyVersion, schedulingVersion, request.Rules.domain(), request.SchedulingWeight,
	)
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	c.Header("ETag", projectSettingsETag(policyVersion(settings.Policy), settings.Scheduling.Version))
	c.JSON(http.StatusOK, projectSettingsResponse(settings))
}

func (a *admissionAdministrationAPI) projectQuotaUsage(c *gin.Context) {
	usage, err := a.service.ProjectQuotaUsage(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")),
	)
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	c.JSON(http.StatusOK, quotaCountersResponse(usage))
}

func (a *admissionAdministrationAPI) admissionDecisions(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > ports.MaxAdmissionDecisionPageSize {
			writeError(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200")
			return
		}
		limit = value
	}
	after, ok := decodeAdmissionCursor(c.Query("cursor"))
	if !ok {
		writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "cursor is invalid")
		return
	}
	items, err := a.service.AdmissionDecisions(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")), after, limit,
	)
	if err != nil {
		writeAdmissionAdministrationError(c, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeAdmissionCursor(items[len(items)-1])
		next = &encoded
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		response := gin.H{
			"id": item.ID, "projectId": item.ProjectID, "jobId": item.JobID,
			"accepted": item.Accepted, "reason": item.Reason, "decidedAt": item.DecidedAt,
		}
		if item.PolicyVersion > 0 {
			response["policyVersion"] = item.PolicyVersion
		}
		if item.SchedulingWeight > 0 {
			response["schedulingWeight"] = item.SchedulingWeight
		}
		responses = append(responses, response)
	}
	c.JSON(http.StatusOK, gin.H{"items": responses, "nextCursor": next})
}

type admissionPolicyRequest struct {
	Rules admissionRulesRequest `json:"rules" binding:"required"`
}

type admissionRulesRequest struct {
	Quotas                      admissionQuotaLimitsRequest `json:"quotas"`
	Priority                    *policyquota.PriorityRange  `json:"priority"`
	MaxDelayedStartSeconds      *uint64                     `json:"maxDelayedStartSeconds"`
	MaxExecutionDurationSeconds *uint64                     `json:"maxExecutionDurationSeconds"`
	AllowedImageRegistries      *[]string                   `json:"allowedImageRegistries"`
}

type admissionQuotaLimitsRequest struct {
	Global    admissionQuotaLimitRequest `json:"global"`
	Project   admissionQuotaLimitRequest `json:"project"`
	Namespace admissionQuotaLimitRequest `json:"namespace"`
}

type admissionQuotaLimitRequest struct {
	MaxConcurrent *uint64 `json:"maxConcurrent"`
	MaxQueued     *uint64 `json:"maxQueued"`
	MaxRetained   *uint64 `json:"maxRetained"`
}

func (request admissionRulesRequest) domain() policyquota.Rules {
	rules := policyquota.Rules{
		Quotas: policyquota.QuotaLimits{
			Global:    request.Quotas.Global.domain(),
			Project:   request.Quotas.Project.domain(),
			Namespace: request.Quotas.Namespace.domain(),
		},
		Priority: request.Priority,
	}
	if request.MaxDelayedStartSeconds != nil {
		value := time.Duration(*request.MaxDelayedStartSeconds) * time.Second
		rules.MaxDelayedStart = &value
	}
	if request.MaxExecutionDurationSeconds != nil {
		value := time.Duration(*request.MaxExecutionDurationSeconds) * time.Second
		rules.MaxExecutionDuration = &value
	}
	if request.AllowedImageRegistries != nil {
		rules.HasImageRegistryAllowlist = true
		rules.AllowedImageRegistries = *request.AllowedImageRegistries
	}
	return rules
}

func (request admissionQuotaLimitRequest) domain() policyquota.ScopedLimits {
	return policyquota.ScopedLimits{
		MaxConcurrent: request.MaxConcurrent,
		MaxQueued:     request.MaxQueued,
		MaxRetained:   request.MaxRetained,
	}
}

func admissionPolicyResponse(policy policyquota.Policy) gin.H {
	response := gin.H{
		"id": policy.Ref.ID, "version": policy.Ref.Version,
		"scopeType": policy.Ref.Scope.Kind, "rules": admissionRulesResponse(policy.Rules),
	}
	if policy.Ref.Scope.Project != "" {
		response["projectId"] = policy.Ref.Scope.Project
	}
	return response
}

func projectSettingsResponse(settings application.ProjectAdmissionSettings) gin.H {
	var policyID any
	var rules policyquota.Rules
	if settings.Policy != nil {
		policyID = settings.Policy.Ref.ID
		rules = settings.Policy.Rules
	}
	return gin.H{
		"projectId": settings.ProjectID, "policyId": policyID,
		"policyVersion":     policyVersion(settings.Policy),
		"schedulingWeight":  settings.Scheduling.Weight,
		"schedulingVersion": settings.Scheduling.Version,
		"rules":             admissionRulesResponse(rules),
	}
}

func admissionRulesResponse(rules policyquota.Rules) gin.H {
	response := gin.H{}
	quotas := gin.H{}
	addQuotaLimitResponse(quotas, "global", rules.Quotas.Global)
	addQuotaLimitResponse(quotas, "project", rules.Quotas.Project)
	addQuotaLimitResponse(quotas, "namespace", rules.Quotas.Namespace)
	if len(quotas) > 0 {
		response["quotas"] = quotas
	}
	if rules.Priority != nil {
		response["priority"] = gin.H{
			"min": rules.Priority.Min, "max": rules.Priority.Max,
			"default": rules.Priority.Default,
		}
	}
	if rules.MaxDelayedStart != nil {
		response["maxDelayedStartSeconds"] = uint64(*rules.MaxDelayedStart / time.Second)
	}
	if rules.MaxExecutionDuration != nil {
		response["maxExecutionDurationSeconds"] = uint64(*rules.MaxExecutionDuration / time.Second)
	}
	if rules.HasImageRegistryAllowlist {
		response["allowedImageRegistries"] = rules.AllowedImageRegistries
	}
	return response
}

func addQuotaLimitResponse(target gin.H, key string, limits policyquota.ScopedLimits) {
	values := gin.H{}
	if limits.MaxConcurrent != nil {
		values["maxConcurrent"] = *limits.MaxConcurrent
	}
	if limits.MaxQueued != nil {
		values["maxQueued"] = *limits.MaxQueued
	}
	if limits.MaxRetained != nil {
		values["maxRetained"] = *limits.MaxRetained
	}
	if len(values) > 0 {
		target[key] = values
	}
}

func quotaCountersResponse(counters policyquota.Counters) gin.H {
	return gin.H{
		"concurrent": counters.Concurrent, "queued": counters.Queued,
		"retained": counters.Retained,
	}
}

func policyVersion(policy *policyquota.Policy) uint64 {
	if policy == nil {
		return 0
	}
	return policy.Ref.Version
}

func installationPolicyETag(version uint64) string {
	return fmt.Sprintf(`"policy-%d"`, version)
}

func parseInstallationPolicyETag(value string) (uint64, bool) {
	return parseVersionETag(value, "policy-")
}

func projectSettingsETag(policyVersion, schedulingVersion uint64) string {
	return fmt.Sprintf(`"policy-%d-scheduling-%d"`, policyVersion, schedulingVersion)
}

func parseProjectSettingsETag(value string) (uint64, uint64, bool) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, 0, false
	}
	parts := strings.Split(strings.Trim(value, `"`), "-scheduling-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	policy, ok := parseVersionETag(`"`+parts[0]+`"`, "policy-")
	if !ok {
		return 0, 0, false
	}
	scheduling, err := strconv.ParseUint(parts[1], 10, 64)
	return policy, scheduling, err == nil && scheduling > 0
}

func parseVersionETag(value, prefix string) (uint64, bool) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value[1:len(value)-1], prefix), `"`)
	if !strings.HasPrefix(value[1:len(value)-1], prefix) {
		return 0, false
	}
	version, err := strconv.ParseUint(raw, 10, 64)
	return version, err == nil
}

func encodeAdmissionCursor(record ports.AdmissionDecisionRecord) string {
	value, _ := json.Marshal(ports.AdmissionDecisionCursor{
		DecidedAt: record.DecidedAt, ID: record.ID,
	})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeAdmissionCursor(value string) (*ports.AdmissionDecisionCursor, bool) {
	if value == "" {
		return nil, true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	var cursor ports.AdmissionDecisionCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil ||
		cursor.DecidedAt.IsZero() || cursor.ID == "" {
		return nil, false
	}
	return &cursor, true
}

func writeAdmissionAdministrationError(c *gin.Context, err error) {
	if writeAuthorizationError(c, err) {
		return
	}
	switch {
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, application.ErrPolicyNotConfigured):
		writeError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "resource not found")
	case errors.Is(err, ports.ErrConflict):
		writeError(c, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "resource changed")
	case errors.Is(err, policyquota.ErrInvalidPolicy),
		errors.Is(err, policyquota.ErrPolicyExpansion):
		writeError(c, http.StatusBadRequest, "INVALID_POLICY", err.Error())
	default:
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}
