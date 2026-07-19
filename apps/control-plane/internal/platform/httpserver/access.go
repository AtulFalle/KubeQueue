package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var accessRoutePermissionMatrix = map[string]domain.Permission{
	"GET /api/v1/access/me":                                                            domain.PermissionAuthenticated,
	"GET /api/v1/access/effective-grants":                                              domain.PermissionRolesRead,
	"GET /api/v1/projects":                                                             domain.PermissionProjectsManage,
	"POST /api/v1/projects":                                                            domain.PermissionProjectsManage,
	"GET /api/v1/projects/:projectId":                                                  domain.PermissionProjectsManage,
	"GET /api/v1/projects/:projectId/namespace-bindings":                               domain.PermissionNamespaceBindingsManage,
	"POST /api/v1/projects/:projectId/namespace-bindings":                              domain.PermissionNamespaceBindingsManage,
	"PUT /api/v1/projects/:projectId/namespace-bindings/:namespace":                    domain.PermissionNamespaceBindingsManage,
	"DELETE /api/v1/projects/:projectId/namespace-bindings/:namespace":                 domain.PermissionNamespaceBindingsManage,
	"GET /api/v1/teams":                                                                domain.PermissionMembersRead,
	"POST /api/v1/teams":                                                               domain.PermissionMembersManage,
	"GET /api/v1/teams/:teamId":                                                        domain.PermissionMembersRead,
	"GET /api/v1/teams/:teamId/memberships":                                            domain.PermissionMembersRead,
	"POST /api/v1/teams/:teamId/memberships":                                           domain.PermissionMembersManage,
	"GET /api/v1/teams/:teamId/memberships/:principalId":                               domain.PermissionMembersRead,
	"DELETE /api/v1/teams/:teamId/memberships/:principalId":                            domain.PermissionMembersManage,
	"GET /api/v1/role-definitions":                                                     domain.PermissionRolesRead,
	"POST /api/v1/role-definitions":                                                    domain.PermissionRolesDefine,
	"GET /api/v1/role-definitions/:roleDefinitionId":                                   domain.PermissionRolesRead,
	"PATCH /api/v1/role-definitions/:roleDefinitionId":                                 domain.PermissionRolesDefine,
	"GET /api/v1/role-bindings":                                                        domain.PermissionRolesRead,
	"POST /api/v1/role-bindings":                                                       domain.PermissionRolesAssign,
	"GET /api/v1/role-bindings/:roleBindingId":                                         domain.PermissionRolesRead,
	"DELETE /api/v1/role-bindings/:roleBindingId":                                      domain.PermissionRolesAssign,
	"GET /api/v1/service-accounts":                                                     domain.PermissionServiceAccountsManage,
	"POST /api/v1/service-accounts":                                                    domain.PermissionServiceAccountsManage,
	"GET /api/v1/service-accounts/:serviceAccountId":                                   domain.PermissionServiceAccountsManage,
	"PUT /api/v1/service-accounts/:serviceAccountId/oidc-identity":                     domain.PermissionServiceAccountsManage,
	"DELETE /api/v1/service-accounts/:serviceAccountId/oidc-identity":                  domain.PermissionServiceAccountsManage,
	"GET /api/v1/service-accounts/:serviceAccountId/credentials":                       domain.PermissionTokensManage,
	"POST /api/v1/service-accounts/:serviceAccountId/credentials":                      domain.PermissionTokensManage,
	"GET /api/v1/service-accounts/:serviceAccountId/credentials/:credentialId":         domain.PermissionTokensManage,
	"POST /api/v1/service-accounts/:serviceAccountId/credentials/:credentialId/rotate": domain.PermissionTokensManage,
	"DELETE /api/v1/service-accounts/:serviceAccountId/credentials/:credentialId":      domain.PermissionTokensManage,
}

func init() {
	for route, permission := range accessRoutePermissionMatrix {
		routePermissionMatrix[route] = permission
	}
}

type accessAPI struct {
	access          *application.AccessManagement
	serviceAccounts *application.ServiceAccounts
}

func registerAccessAPI(
	router *gin.Engine,
	access *application.AccessManagement,
	serviceAccounts *application.ServiceAccounts,
	token string,
	sessions *application.Sessions,
	browserOrigin string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	api := &accessAPI{access: access, serviceAccounts: serviceAccounts}
	group := router.Group("/api/v1")
	group.Use(
		apiAuthenticationMiddleware(token, sessions, oidcAuthenticators...),
		browserRequestProtectionMiddleware(browserOrigin, sessions),
	)
	group.GET("/access/me", api.currentAccess)
	group.GET("/access/effective-grants", api.effectiveGrants)
	group.GET("/projects", api.projects)
	group.POST("/projects", api.createProject)
	group.GET("/projects/:projectId", api.project)
	group.GET("/projects/:projectId/namespace-bindings", api.namespaceBindings)
	group.POST("/projects/:projectId/namespace-bindings", api.createNamespaceBinding)
	group.PUT("/projects/:projectId/namespace-bindings/:namespace", api.reassignNamespaceBinding)
	group.DELETE("/projects/:projectId/namespace-bindings/:namespace", api.removeNamespaceBinding)
	group.GET("/teams", api.teams)
	group.POST("/teams", api.createTeam)
	group.GET("/teams/:teamId", api.team)
	group.GET("/teams/:teamId/memberships", api.memberships)
	group.POST("/teams/:teamId/memberships", api.createMembership)
	group.GET("/teams/:teamId/memberships/:principalId", api.membership)
	group.DELETE("/teams/:teamId/memberships/:principalId", api.deleteMembership)
	group.GET("/role-definitions", api.roleDefinitions)
	group.POST("/role-definitions", api.createRoleDefinition)
	group.GET("/role-definitions/:roleDefinitionId", api.roleDefinition)
	group.PATCH("/role-definitions/:roleDefinitionId", api.updateRoleDefinition)
	group.GET("/role-bindings", api.roleBindings)
	group.POST("/role-bindings", api.createRoleBinding)
	group.GET("/role-bindings/:roleBindingId", api.roleBinding)
	group.DELETE("/role-bindings/:roleBindingId", api.deleteRoleBinding)
	group.GET("/service-accounts", api.serviceAccountList)
	group.POST("/service-accounts", api.createServiceAccount)
	group.GET("/service-accounts/:serviceAccountId", api.serviceAccount)
	group.PUT("/service-accounts/:serviceAccountId/oidc-identity", api.bindServiceAccountOIDCIdentity)
	group.DELETE("/service-accounts/:serviceAccountId/oidc-identity", api.removeServiceAccountOIDCIdentity)
	group.GET("/service-accounts/:serviceAccountId/credentials", api.credentialList)
	group.POST("/service-accounts/:serviceAccountId/credentials", api.issueCredential)
	group.GET("/service-accounts/:serviceAccountId/credentials/:credentialId", api.credential)
	group.POST("/service-accounts/:serviceAccountId/credentials/:credentialId/rotate", api.rotateCredential)
	group.DELETE("/service-accounts/:serviceAccountId/credentials/:credentialId", api.revokeCredential)
}

func (a *accessAPI) currentAccess(c *gin.Context) {
	result, err := a.access.CurrentAccess(c.Request.Context())
	if err != nil {
		writeAccessError(c, err)
		return
	}
	permissions := make([]gin.H, 0)
	for _, effective := range result.Permissions {
		if effective.Scope.InstallationWide {
			permissions = append(permissions, gin.H{
				"permission": effective.Permission, "scopeType": domain.RoleScopeInstallation,
			})
			continue
		}
		for _, projectID := range effective.Scope.ProjectIDs {
			permissions = append(permissions, gin.H{
				"permission": effective.Permission, "scopeType": domain.RoleScopeProject,
				"projectId": projectID,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"installationId":    result.Principal.InstallationID,
		"principal":         principalResponse(result.Principal),
		"installationOwner": result.InstallationOwner,
		"permissions":       permissions,
	})
}

func (a *accessAPI) effectiveGrants(c *gin.Context) {
	principalID := domain.PrincipalID(strings.TrimSpace(c.Query("principalId")))
	if principalID == "" {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "principalId is required")
		return
	}
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	result, err := a.access.EffectiveAccess(c.Request.Context(), principalID, page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	items := make([]gin.H, 0, len(result.Grants))
	for _, grant := range result.Grants {
		source := "DIRECT"
		item := gin.H{
			"roleBindingId": grant.RoleBindingID, "roleDefinitionId": grant.RoleDefinitionID,
			"roleName": grant.RoleName, "scope": scopeResponse(grant.Scope, grant.ProjectID),
			"permissions": grant.Permissions, "source": source,
		}
		if !grant.Direct {
			item["source"] = "TEAM"
			item["viaTeamId"] = grant.ViaTeamID
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, pageResponse(items, nextGrantCursor(result.Grants, page.Limit), "principalId", principalID))
}

func (a *accessAPI) projects(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListProjects(c.Request.Context(), page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, projectResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextProjectCursor(items, page.Limit)))
}

func (a *accessAPI) createProject(c *gin.Context) {
	var request struct {
		ID   domain.ProjectID `json:"id" binding:"required"`
		Name string           `json:"name" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	project, err := a.access.CreateProject(c.Request.Context(), request.ID, request.Name)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, projectResponse(project))
}

func (a *accessAPI) project(c *gin.Context) {
	project, err := a.access.Project(c.Request.Context(), domain.ProjectID(c.Param("projectId")))
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, projectResponse(project))
}

func (a *accessAPI) namespaceBindings(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListNamespaceBindings(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")), page,
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, namespaceBindingResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(
		responses, nextNamespaceBindingCursor(items, page.Limit),
	))
}

func (a *accessAPI) createNamespaceBinding(c *gin.Context) {
	var request struct {
		Namespace string `json:"namespace" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	binding, err := a.access.CreateNamespaceBinding(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")), request.Namespace,
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, namespaceBindingResponse(binding))
}

func (a *accessAPI) reassignNamespaceBinding(c *gin.Context) {
	binding, err := a.access.ReassignNamespaceBinding(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")), c.Param("namespace"),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, namespaceBindingResponse(binding))
}

func (a *accessAPI) removeNamespaceBinding(c *gin.Context) {
	err := a.access.RemoveNamespaceBinding(
		c.Request.Context(), domain.ProjectID(c.Param("projectId")), c.Param("namespace"),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *accessAPI) teams(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListTeams(c.Request.Context(), page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, teamResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextTeamCursor(items, page.Limit)))
}

func (a *accessAPI) createTeam(c *gin.Context) {
	var request struct {
		ID   domain.TeamID `json:"id" binding:"required"`
		Name string        `json:"name" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	team, err := a.access.CreateTeam(c.Request.Context(), request.ID, request.Name)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, teamResponse(team))
}

func (a *accessAPI) team(c *gin.Context) {
	team, err := a.access.Team(c.Request.Context(), domain.TeamID(c.Param("teamId")))
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, teamResponse(team))
}

func (a *accessAPI) memberships(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListMemberships(
		c.Request.Context(), domain.TeamID(c.Param("teamId")), page,
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, membershipResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextMembershipCursor(items, page.Limit)))
}

func (a *accessAPI) createMembership(c *gin.Context) {
	var request struct {
		PrincipalID domain.PrincipalID `json:"principalId" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	membership, err := a.access.PutMembership(c.Request.Context(), application.PutMembershipInput{
		TeamID: domain.TeamID(c.Param("teamId")), PrincipalID: request.PrincipalID, Active: true,
	})
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, membershipResponse(membership))
}

func (a *accessAPI) membership(c *gin.Context) {
	membership, err := a.access.Membership(
		c.Request.Context(), domain.TeamID(c.Param("teamId")),
		domain.PrincipalID(c.Param("principalId")),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, membershipResponse(membership))
}

func (a *accessAPI) deleteMembership(c *gin.Context) {
	_, err := a.access.PutMembership(c.Request.Context(), application.PutMembershipInput{
		TeamID:      domain.TeamID(c.Param("teamId")),
		PrincipalID: domain.PrincipalID(c.Param("principalId")),
		Active:      false,
	})
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *accessAPI) roleDefinitions(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListRoleDefinitions(c.Request.Context(), page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, roleDefinitionResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextRoleCursor(items, page.Limit)))
}

func (a *accessAPI) createRoleDefinition(c *gin.Context) {
	var request struct {
		ID          domain.RoleDefinitionID `json:"id" binding:"required"`
		Name        string                  `json:"name" binding:"required"`
		Scope       domain.RoleScope        `json:"scopeType" binding:"required"`
		Permissions []domain.Permission     `json:"permissions" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	role, err := a.access.CreateRoleDefinition(c.Request.Context(), application.PutRoleDefinitionInput{
		ID: request.ID, Name: request.Name, Scope: request.Scope, Permissions: request.Permissions,
	})
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.Header("ETag", roleETag(role.Revision))
	c.JSON(http.StatusCreated, roleDefinitionResponse(role))
}

func (a *accessAPI) roleDefinition(c *gin.Context) {
	role, err := a.access.RoleDefinition(
		c.Request.Context(), domain.RoleDefinitionID(c.Param("roleDefinitionId")),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	if !role.BuiltIn {
		c.Header("ETag", roleETag(role.Revision))
	}
	c.JSON(http.StatusOK, roleDefinitionResponse(role))
}

func (a *accessAPI) updateRoleDefinition(c *gin.Context) {
	revision, ok := parseRoleETag(c.GetHeader("If-Match"))
	if !ok {
		writeError(c, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match must be a quoted role revision")
		return
	}
	var request struct {
		Name        string              `json:"name" binding:"required"`
		Scope       domain.RoleScope    `json:"scopeType" binding:"required"`
		Permissions []domain.Permission `json:"permissions" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	role, err := a.access.UpdateRoleDefinition(
		c.Request.Context(),
		application.PutRoleDefinitionInput{
			ID: domain.RoleDefinitionID(c.Param("roleDefinitionId")), Name: request.Name,
			Scope: request.Scope, Permissions: request.Permissions, ExpectedRevision: revision,
		},
	)
	if errors.Is(err, domain.ErrAccessConflict) {
		writeError(c, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "role revision changed")
		return
	}
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.Header("ETag", roleETag(role.Revision))
	c.JSON(http.StatusOK, roleDefinitionResponse(role))
}

func (a *accessAPI) roleBindings(c *gin.Context) {
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.access.ListRoleBindings(c.Request.Context(), page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, roleBindingResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextBindingCursor(items, page.Limit)))
}

type roleBindingRequest struct {
	ID               domain.RoleBindingID    `json:"id"`
	RoleDefinitionID domain.RoleDefinitionID `json:"roleDefinitionId" binding:"required"`
	Scope            struct {
		Type      domain.RoleScope `json:"scopeType" binding:"required"`
		ProjectID domain.ProjectID `json:"projectId"`
	} `json:"scope" binding:"required"`
	Subject struct {
		Kind domain.BindingSubjectKind `json:"kind" binding:"required"`
		ID   string                    `json:"id" binding:"required"`
	} `json:"subject" binding:"required"`
}

func (r roleBindingRequest) input() application.PutRoleBindingInput {
	input := application.PutRoleBindingInput{
		ID: r.ID, RoleDefinitionID: r.RoleDefinitionID, Scope: r.Scope.Type,
		ProjectID: r.Scope.ProjectID, SubjectKind: r.Subject.Kind,
	}
	if r.Subject.Kind == domain.BindingSubjectPrincipal {
		input.PrincipalID = domain.PrincipalID(r.Subject.ID)
	} else {
		input.TeamID = domain.TeamID(r.Subject.ID)
	}
	return input
}

func (a *accessAPI) createRoleBinding(c *gin.Context) {
	var request roleBindingRequest
	if !bindAccessJSON(c, &request) || request.ID == "" {
		if !c.IsAborted() {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "binding id is required")
		}
		return
	}
	binding, err := a.access.CreateRoleBinding(c.Request.Context(), request.input())
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, roleBindingResponse(binding))
}

func (a *accessAPI) roleBinding(c *gin.Context) {
	binding, err := a.access.RoleBinding(
		c.Request.Context(), domain.RoleBindingID(c.Param("roleBindingId")),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, roleBindingResponse(binding))
}

func (a *accessAPI) deleteRoleBinding(c *gin.Context) {
	if err := a.access.DeleteRoleBinding(
		c.Request.Context(), domain.RoleBindingID(c.Param("roleBindingId")),
	); err != nil {
		writeAccessError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *accessAPI) serviceAccountList(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	page, ok := accessPage(c, false)
	if !ok {
		return
	}
	items, err := a.serviceAccounts.List(c.Request.Context(), page)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, serviceAccountResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextServiceAccountCursor(items, page.Limit)))
}

func (a *accessAPI) createServiceAccount(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	var request struct {
		PrincipalID domain.PrincipalID `json:"principalId" binding:"required"`
		ProjectID   domain.ProjectID   `json:"projectId"`
		DisplayName string             `json:"displayName" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	account, err := a.serviceAccounts.Create(c.Request.Context(), application.CreateServiceAccountInput{
		PrincipalID: request.PrincipalID, ProjectID: request.ProjectID, DisplayName: request.DisplayName,
	})
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, serviceAccountResponse(account))
}

func (a *accessAPI) serviceAccount(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	account, err := a.serviceAccounts.Get(
		c.Request.Context(), domain.PrincipalID(c.Param("serviceAccountId")),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, serviceAccountResponse(account))
}

func (a *accessAPI) bindServiceAccountOIDCIdentity(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	var request struct {
		Issuer  string `json:"issuer" binding:"required"`
		Subject string `json:"subject" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	account, err := a.serviceAccounts.BindOIDCIdentity(
		c.Request.Context(),
		domain.PrincipalID(c.Param("serviceAccountId")),
		serviceaccountcredential.OIDCIdentity{
			Issuer: request.Issuer, Subject: request.Subject,
		},
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, serviceAccountResponse(account))
}

func (a *accessAPI) removeServiceAccountOIDCIdentity(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	if err := a.serviceAccounts.RemoveOIDCIdentity(
		c.Request.Context(), domain.PrincipalID(c.Param("serviceAccountId")),
	); err != nil {
		writeAccessError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *accessAPI) credentialList(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	page, ok := accessPage(c, true)
	if !ok {
		return
	}
	items, err := a.serviceAccounts.ListCredentials(
		c.Request.Context(), domain.PrincipalID(c.Param("serviceAccountId")), page,
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	responses := make([]gin.H, 0, len(items))
	for _, item := range items {
		responses = append(responses, credentialResponse(item))
	}
	c.JSON(http.StatusOK, pageResponse(responses, nextCredentialCursor(items, page.Limit)))
}

func (a *accessAPI) credential(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	metadata, err := a.serviceAccounts.CredentialMetadata(
		c.Request.Context(), domain.PrincipalID(c.Param("serviceAccountId")),
		c.Param("credentialId"),
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, credentialResponse(metadata))
}

func (a *accessAPI) issueCredential(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	var request struct {
		Permissions []domain.Permission `json:"permissions" binding:"required"`
		ExpiresAt   time.Time           `json:"expiresAt" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	issued, err := a.serviceAccounts.Issue(
		c.Request.Context(),
		application.IssueServiceAccountCredentialInput{
			ServiceAccountPrincipalID: domain.PrincipalID(c.Param("serviceAccountId")),
			Permissions:               request.Permissions, ExpiresAt: request.ExpiresAt,
		},
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		writeAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"credential": credentialResponse(issued.Credential.Metadata()),
		"token":      plaintext,
	})
}

func (a *accessAPI) rotateCredential(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	var request struct {
		Permissions   []domain.Permission `json:"permissions" binding:"required"`
		ExpiresAt     time.Time           `json:"expiresAt" binding:"required"`
		OverlapSecond *int64              `json:"overlapSeconds" binding:"required"`
	}
	if !bindAccessJSON(c, &request) {
		return
	}
	if *request.OverlapSecond < 0 ||
		*request.OverlapSecond >
			int64(serviceaccountcredential.DefaultPolicy().MaxRotationOverlap/time.Second) {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST",
			"overlapSeconds is outside the configured credential policy")
		return
	}
	rotation, err := a.serviceAccounts.Rotate(
		c.Request.Context(),
		application.RotateServiceAccountCredentialInput{
			ServiceAccountPrincipalID: domain.PrincipalID(c.Param("serviceAccountId")),
			CredentialID:              c.Param("credentialId"),
			Permissions:               request.Permissions,
			ExpiresAt:                 request.ExpiresAt,
			Overlap:                   time.Duration(*request.OverlapSecond) * time.Second,
		},
	)
	if err != nil {
		writeAccessError(c, err)
		return
	}
	plaintext, err := rotation.Replacement.Plaintext.Reveal()
	if err != nil {
		writeAccessError(c, err)
		return
	}
	response := gin.H{
		"previousCredentialId": rotation.Previous.ID,
		"replacement":          credentialResponse(rotation.Replacement.Credential.Metadata()),
		"token":                plaintext,
	}
	if rotation.Previous.Stored.OverlapExpiresAt != nil {
		response["overlapExpiresAt"] = rotation.Previous.Stored.OverlapExpiresAt
	}
	c.JSON(http.StatusCreated, response)
}

func (a *accessAPI) revokeCredential(c *gin.Context) {
	if !a.requireServiceAccounts(c) {
		return
	}
	if err := a.serviceAccounts.RevokeForAccount(
		c.Request.Context(), domain.PrincipalID(c.Param("serviceAccountId")),
		c.Param("credentialId"),
	); err != nil {
		writeAccessError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *accessAPI) requireServiceAccounts(c *gin.Context) bool {
	if a.serviceAccounts != nil {
		return true
	}
	writeError(c, http.StatusServiceUnavailable, "SERVICE_ACCOUNTS_UNAVAILABLE",
		"service-account credential management is not configured")
	return false
}

func accessPage(c *gin.Context, credentialCursor bool) (domain.AccessPage, bool) {
	limit := domain.DefaultAccessPageSize
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > domain.MaxAccessPageSize {
			writeError(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200")
			return domain.AccessPage{}, false
		}
		limit = parsed
	}
	after := strings.TrimSpace(c.Query("cursor"))
	if credentialCursor && after != "" {
		if _, err := uuid.Parse(after); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "cursor must be a credential UUID")
			return domain.AccessPage{}, false
		}
	}
	page, err := (domain.AccessPage{Limit: limit, After: after}).Normalize()
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_CURSOR", "cursor is invalid")
		return domain.AccessPage{}, false
	}
	return page, true
}

func bindAccessJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return false
	}
	return true
}

func writeAccessError(c *gin.Context, err error) {
	if writeAuthorizationError(c, err) {
		return
	}
	switch {
	case errors.Is(err, domain.ErrAccessResourceNotFound),
		errors.Is(err, ports.ErrServiceAccountNotFound),
		errors.Is(err, ports.ErrCredentialNotFound):
		writeError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "resource not found")
	case errors.Is(err, domain.ErrAccessConflict),
		errors.Is(err, domain.ErrFinalInstallationOwner),
		errors.Is(err, ports.ErrConflict),
		errors.Is(err, ports.ErrCredentialConflict):
		writeError(c, http.StatusConflict, "RESOURCE_CONFLICT", "resource conflict")
	case errors.Is(err, domain.ErrDelegationCeiling),
		errors.Is(err, domain.ErrNonDelegablePermission),
		errors.Is(err, serviceaccountcredential.ErrDelegationDenied):
		writeError(c, http.StatusForbidden, "FORBIDDEN", "delegation is not permitted")
	case errors.Is(err, domain.ErrInvalidAccessChange),
		errors.Is(err, serviceaccountcredential.ErrInvalidRequest):
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	default:
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func roleETag(revision uint64) string { return fmt.Sprintf(`"%d"`, revision) }

func parseRoleETag(value string) (uint64, bool) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, false
	}
	revision, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	return revision, err == nil && revision > 0
}

func pageResponse(items any, next *string, extra ...any) gin.H {
	response := gin.H{"items": items, "nextCursor": next}
	for index := 0; index+1 < len(extra); index += 2 {
		response[fmt.Sprint(extra[index])] = extra[index+1]
	}
	return response
}

func principalResponse(principal domain.ManagedPrincipal) gin.H {
	status := "ACTIVE"
	if principal.DisabledAt != nil {
		status = "DISABLED"
	}
	return gin.H{
		"id": principal.ID, "kind": principal.Kind,
		"displayName": principal.DisplayName, "status": status,
	}
}

func projectResponse(project domain.ManagedProject) gin.H {
	return gin.H{
		"id": project.ID, "installationId": project.InstallationID,
		"name": project.Name, "createdAt": project.CreatedAt,
	}
}

func namespaceBindingResponse(binding domain.NamespaceBinding) gin.H {
	return gin.H{
		"id": binding.ID, "projectId": binding.ProjectID,
		"namespace": binding.Namespace, "desired": binding.Desired,
		"authorityState": binding.AuthorityState,
		"informerSynced": binding.InformerSynced, "authorized": binding.Authorized,
		"message": binding.Message, "observedAt": binding.ObservedAt,
		"createdAt": binding.CreatedAt,
	}
}

func teamResponse(team domain.Team) gin.H {
	return gin.H{
		"id": team.ID, "installationId": team.InstallationID,
		"name": team.Name, "createdAt": team.CreatedAt,
	}
}

func membershipResponse(membership domain.TeamMembership) gin.H {
	source := "MANUAL"
	response := gin.H{
		"teamId": membership.TeamID, "principalId": membership.PrincipalID,
		"source": source, "createdAt": membership.CreatedAt,
	}
	if membership.SourceIdentityProviderID != "" {
		response["source"] = "OIDC"
		response["sourceIdentityProviderId"] = membership.SourceIdentityProviderID
	}
	return response
}

func roleDefinitionResponse(role domain.RoleDefinition) gin.H {
	return gin.H{
		"id": role.ID, "installationId": role.InstallationID, "name": role.Name,
		"scopeType": role.Scope, "permissions": role.Permissions, "builtIn": role.BuiltIn,
		"revision": role.Revision, "createdAt": role.CreatedAt,
	}
}

func roleBindingResponse(binding domain.RoleBinding) gin.H {
	subjectID := string(binding.PrincipalID)
	if binding.SubjectKind == domain.BindingSubjectTeam {
		subjectID = string(binding.TeamID)
	}
	return gin.H{
		"id": binding.ID, "installationId": binding.InstallationID,
		"roleDefinitionId": binding.RoleDefinitionID,
		"scope":            scopeResponse(binding.Scope, binding.ProjectID),
		"subject":          gin.H{"kind": binding.SubjectKind, "id": subjectID},
		"createdAt":        binding.CreatedAt,
	}
}

func scopeResponse(scope domain.RoleScope, projectID domain.ProjectID) gin.H {
	result := gin.H{"scopeType": scope}
	if projectID != "" {
		result["projectId"] = projectID
	}
	return result
}

func serviceAccountResponse(account serviceaccountcredential.ServiceAccount) gin.H {
	response := gin.H{
		"principalId": account.PrincipalID, "installationId": account.InstallationID,
		"displayName": account.DisplayName, "createdByPrincipalId": account.CreatedBy,
		"createdAt": account.CreatedAt,
	}
	if account.OIDCIdentity != nil {
		response["oidcIdentity"] = gin.H{
			"issuer": account.OIDCIdentity.Issuer, "subject": account.OIDCIdentity.Subject,
		}
	}
	if account.ProjectID != "" {
		response["projectId"] = account.ProjectID
	} else {
		response["projectId"] = nil
	}
	return response
}

func credentialResponse(metadata serviceaccountcredential.CredentialMetadata) gin.H {
	status := "ACTIVE"
	now := time.Now().UTC()
	switch {
	case metadata.RevokedAt != nil:
		status = "REVOKED"
	case !metadata.ExpiresAt.After(now):
		status = "EXPIRED"
	case metadata.RotatedAt != nil && metadata.OverlapExpiresAt != nil &&
		metadata.OverlapExpiresAt.After(now):
		status = "OVERLAP"
	case metadata.RotatedAt != nil:
		status = "EXPIRED"
	}
	response := gin.H{
		"id": metadata.ID, "serviceAccountPrincipalId": metadata.ServiceAccountPrincipalID,
		"safePrefix": metadata.Prefix, "permissions": metadata.Permissions,
		"status": status, "expiresAt": metadata.ExpiresAt, "createdAt": metadata.CreatedAt,
	}
	optionalTime(response, "lastUsedAt", metadata.LastUsedAt)
	optionalTimePointer(response, "rotatedAt", metadata.RotatedAt)
	optionalTimePointer(response, "overlapExpiresAt", metadata.OverlapExpiresAt)
	optionalTimePointer(response, "revokedAt", metadata.RevokedAt)
	return response
}

func optionalTime(response gin.H, key string, value time.Time) {
	if !value.IsZero() {
		response[key] = value
	}
}

func optionalTimePointer(response gin.H, key string, value *time.Time) {
	if value != nil {
		response[key] = value
	}
}

func nextProjectCursor(items []domain.ManagedProject, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].ID)
	return &value
}

func nextNamespaceBindingCursor(items []domain.NamespaceBinding, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	cursor := items[len(items)-1].Namespace
	return &cursor
}

func nextTeamCursor(items []domain.Team, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].ID)
	return &value
}

func nextMembershipCursor(items []domain.TeamMembership, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].PrincipalID)
	return &value
}

func nextRoleCursor(items []domain.RoleDefinition, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].ID)
	return &value
}

func nextBindingCursor(items []domain.RoleBinding, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].ID)
	return &value
}

func nextGrantCursor(items []domain.EffectiveGrant, limit int) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].RoleBindingID)
	return &value
}

func nextServiceAccountCursor(
	items []serviceaccountcredential.ServiceAccount, limit int,
) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := string(items[len(items)-1].PrincipalID)
	return &value
}

func nextCredentialCursor(
	items []serviceaccountcredential.CredentialMetadata, limit int,
) *string {
	if len(items) < limit || len(items) == 0 {
		return nil
	}
	value := items[len(items)-1].ID
	return &value
}
