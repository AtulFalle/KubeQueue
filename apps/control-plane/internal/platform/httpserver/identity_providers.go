package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type identityProviderRequest struct {
	ID                    string   `json:"id"`
	DisplayName           string   `json:"displayName" binding:"required"`
	Issuer                string   `json:"issuer" binding:"required"`
	Audience              string   `json:"audience" binding:"required"`
	ClientID              string   `json:"clientId" binding:"required"`
	ClientSecret          string   `json:"clientSecret"`
	ClientSecretReference string   `json:"clientSecretReference"`
	RedirectURI           string   `json:"redirectUri" binding:"required"`
	AuthorizedParty       string   `json:"authorizedParty"`
	AllowedAlgorithms     []string `json:"allowedAlgorithms" binding:"required"`
	MappingType           string   `json:"mappingType"`
	MappingValue          string   `json:"mappingValue"`
	GroupsClaim           string   `json:"groupsClaim"`
	EmailClaim            string   `json:"emailClaim"`
	NameClaim             string   `json:"nameClaim"`
	CacheTTLSeconds       int      `json:"cacheTtlSeconds"`
}

func registerIdentityProviderAPI(
	router *gin.Engine,
	service *application.IdentityProviders,
	token string,
	sessions *application.Sessions,
	browserOrigin string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	if service == nil {
		return
	}
	group := router.Group("/api/v1")
	group.Use(
		apiAuthenticationMiddleware(token, sessions, oidcAuthenticators...),
		browserRequestProtectionMiddleware(browserOrigin, sessions),
	)
	group.GET("/identity-providers", func(c *gin.Context) {
		providers, err := service.List(c.Request.Context())
		if err != nil {
			writeIdentityProviderError(c, err)
			return
		}
		items := make([]gin.H, 0, len(providers))
		for _, provider := range providers {
			items = append(items, identityProviderResponse(provider))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.POST("/identity-providers", func(c *gin.Context) {
		var request identityProviderRequest
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.ID) == "" {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid identity-provider configuration is required")
			return
		}
		provider, err := service.Create(c.Request.Context(), request.ID, request.configuration())
		if err != nil {
			writeIdentityProviderError(c, err)
			return
		}
		writeIdentityProvider(c, http.StatusCreated, provider)
	})
	group.GET("/identity-providers/:identityProviderId", func(c *gin.Context) {
		provider, err := service.Get(c.Request.Context(), c.Param("identityProviderId"))
		if err != nil {
			writeIdentityProviderError(c, err)
			return
		}
		writeIdentityProvider(c, http.StatusOK, provider)
	})
	group.PUT("/identity-providers/:identityProviderId", func(c *gin.Context) {
		expected, ok := parseRoleETag(c.GetHeader("If-Match"))
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match must be a quoted provider version")
			return
		}
		var request identityProviderRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid identity-provider configuration is required")
			return
		}
		provider, err := service.Update(
			c.Request.Context(), c.Param("identityProviderId"), expected, request.configuration(),
		)
		if err != nil {
			writeIdentityProviderError(c, err)
			return
		}
		writeIdentityProvider(c, http.StatusOK, provider)
	})
	group.POST("/identity-providers/:identityProviderId/test",
		identityProviderTransition(service.Test))
	group.POST("/identity-providers/:identityProviderId/enable",
		identityProviderTransition(service.Enable))
	group.POST("/identity-providers/:identityProviderId/disable",
		identityProviderTransition(service.Disable))
}

func identityProviderTransition(
	operation func(context.Context, string, uint64) (domain.ManagedIdentityProvider, error),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		expected, ok := parseRoleETag(c.GetHeader("If-Match"))
		if !ok {
			writeError(c, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match must be a quoted provider version")
			return
		}
		provider, err := operation(c.Request.Context(), c.Param("identityProviderId"), expected)
		if err != nil {
			writeIdentityProviderError(c, err)
			return
		}
		writeIdentityProvider(c, http.StatusOK, provider)
	}
}

func (r identityProviderRequest) configuration() domain.IdentityProviderConfiguration {
	cacheTTL := r.CacheTTLSeconds
	if cacheTTL == 0 {
		cacheTTL = 300
	}
	groups, email, name := r.GroupsClaim, r.EmailClaim, r.NameClaim
	if groups == "" {
		groups = "groups"
	}
	if email == "" {
		email = "email"
	}
	if name == "" {
		name = "name"
	}
	return domain.IdentityProviderConfiguration{
		DisplayName: strings.TrimSpace(r.DisplayName), Issuer: strings.TrimSpace(r.Issuer),
		Audience: strings.TrimSpace(r.Audience), ClientID: strings.TrimSpace(r.ClientID),
		ClientSecret: r.ClientSecret, ClientSecretRef: strings.TrimSpace(r.ClientSecretReference),
		RedirectURI: strings.TrimSpace(r.RedirectURI), AuthorizedParty: strings.TrimSpace(r.AuthorizedParty),
		AllowedAlgorithms: r.AllowedAlgorithms, MappingType: r.MappingType, MappingValue: r.MappingValue,
		GroupsClaim: groups, EmailClaim: email, NameClaim: name,
		CacheTTL: time.Duration(cacheTTL) * time.Second,
	}
}

func writeIdentityProvider(c *gin.Context, status int, provider domain.ManagedIdentityProvider) {
	c.Header("ETag", roleETag(provider.Version))
	c.Header("Cache-Control", "no-store")
	c.JSON(status, identityProviderResponse(provider))
}

func identityProviderResponse(provider domain.ManagedIdentityProvider) gin.H {
	configuration := provider.Configuration
	response := gin.H{
		"id": provider.ID, "type": "OIDC", "displayName": configuration.DisplayName,
		"issuer": configuration.Issuer, "audience": configuration.Audience,
		"clientId": configuration.ClientID, "clientSecretConfigured": provider.ClientSecretConfigured,
		"redirectUri": configuration.RedirectURI, "allowedAlgorithms": configuration.AllowedAlgorithms,
		"groupsClaim": configuration.GroupsClaim, "emailClaim": configuration.EmailClaim,
		"nameClaim": configuration.NameClaim, "cacheTtlSeconds": int(configuration.CacheTTL / time.Second),
		"state": provider.State, "version": provider.Version,
		"createdAt": provider.CreatedAt, "updatedAt": provider.UpdatedAt,
		"testResult": gin.H{"status": provider.TestStatus},
	}
	if configuration.AuthorizedParty != "" {
		response["authorizedParty"] = configuration.AuthorizedParty
	}
	if configuration.MappingType != "" {
		response["mappingType"], response["mappingValue"] =
			configuration.MappingType, configuration.MappingValue
	}
	if provider.TestedAt != nil {
		response["testResult"].(gin.H)["testedAt"] = provider.TestedAt
	}
	if provider.TestMessage != "" {
		response["testResult"].(gin.H)["message"] = provider.TestMessage
	}
	return response
}

func writeIdentityProviderError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrAccessDenied), errors.Is(err, application.ErrMissingPrincipal):
		writeError(c, http.StatusForbidden, "FORBIDDEN", "installation owner access is required")
	case errors.Is(err, domain.ErrIdentityProviderNotFound):
		writeError(c, http.StatusNotFound, "IDENTITY_PROVIDER_NOT_FOUND", "identity provider was not found")
	case errors.Is(err, domain.ErrIdentityProviderConflict):
		writeError(c, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "identity provider changed")
	case errors.Is(err, domain.ErrIdentityProviderUnsafeChange):
		writeError(c, http.StatusConflict, "FINAL_LOGIN_PATH", "the final usable owner login path cannot be disabled")
	case errors.Is(err, domain.ErrIdentityProviderTestRequired):
		writeError(c, http.StatusConflict, "CURRENT_TEST_REQUIRED", "a current-version provider test is required")
	default:
		writeError(c, http.StatusUnprocessableEntity, "INVALID_IDENTITY_PROVIDER", "identity-provider configuration is invalid")
	}
}
