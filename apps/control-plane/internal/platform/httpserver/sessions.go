package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type createSessionRequest struct {
	IdentityProviderID   string `json:"identityProviderId" binding:"required"`
	AuthenticationMethod string `json:"authenticationMethod" binding:"required"`
	RefreshToken         string `json:"refreshToken"`
	AccessToken          string `json:"accessToken" binding:"required"`
	RotateCredential     string `json:"rotateCredential"`
}

type sessionResponse struct {
	PrincipalID          domain.PrincipalID    `json:"principalId"`
	InstallationID       domain.InstallationID `json:"installationId"`
	IdentityProviderID   string                `json:"identityProviderId,omitempty"`
	AuthenticationMethod string                `json:"authenticationMethod"`
	CSRFToken            string                `json:"csrfToken"`
	IdleExpiresAt        time.Time             `json:"idleExpiresAt"`
	AbsoluteExpiresAt    time.Time             `json:"absoluteExpiresAt"`
}

type sessionRefreshResponse struct {
	sessionResponse
	Refreshed bool `json:"refreshed"`
}

type localLoginStatus interface {
	LocalLoginEnabled(context.Context) (bool, error)
}

type oidcLoginMethodStatus interface {
	EnabledLoginMethods(context.Context) ([]application.LoginMethod, error)
}

type oidcAuthorizationMetadataStatus interface {
	AuthorizationMetadata(context.Context, string) (application.OIDCAuthorizationMetadata, error)
}

type runtimeLoginStatus struct {
	local localLoginStatus
	oidc  oidcLoginMethodStatus
}

func (s runtimeLoginStatus) LocalLoginEnabled(ctx context.Context) (bool, error) {
	if s.local == nil {
		return false, nil
	}
	return s.local.LocalLoginEnabled(ctx)
}

func (s runtimeLoginStatus) EnabledLoginMethods(
	ctx context.Context,
) ([]application.LoginMethod, error) {
	if s.oidc == nil {
		return nil, nil
	}
	return s.oidc.EnabledLoginMethods(ctx)
}

func (s runtimeLoginStatus) AuthorizationMetadata(
	ctx context.Context, id string,
) (application.OIDCAuthorizationMetadata, error) {
	if s.oidc == nil {
		return application.OIDCAuthorizationMetadata{}, domain.ErrIdentityProviderNotFound
	}
	if providers, ok := s.oidc.(oidcAuthorizationMetadataStatus); ok {
		return providers.AuthorizationMetadata(ctx, id)
	}
	return application.OIDCAuthorizationMetadata{}, domain.ErrIdentityProviderNotFound
}

func registerSessionAPI(
	router *gin.Engine,
	sessions *application.Sessions,
	localAccounts *application.LocalAccounts,
	localStatus localLoginStatus,
	_ string,
	expectedOrigin string,
	bffKey string,
	oidcAuthenticators ...bearerAuthenticator,
) {
	if sessions == nil {
		return
	}
	router.GET("/api/v1/login-methods", func(c *gin.Context) {
		enabled := false
		var err error
		if localStatus != nil {
			enabled, err = localStatus.LocalLoginEnabled(c.Request.Context())
		}
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "LOGIN_METHODS_UNAVAILABLE",
				"login methods are unavailable")
			return
		}
		items := []gin.H{}
		if enabled {
			items = append(items, gin.H{"type": "LOCAL", "id": "local", "label": "Local account"})
		}
		if providers, ok := localStatus.(oidcLoginMethodStatus); ok {
			methods, providerErr := providers.EnabledLoginMethods(c.Request.Context())
			if providerErr != nil {
				writeError(c, http.StatusServiceUnavailable, "LOGIN_METHODS_UNAVAILABLE",
					"login methods are unavailable")
				return
			}
			for _, method := range methods {
				items = append(items, gin.H{"type": method.Type, "id": method.ID, "label": method.Label})
			}
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"items": items})
	})

	if localAccounts != nil {
		localLogin := router.Group("/api/v1")
		localLogin.Use(bffAuthenticationMiddleware(bffKey))
		localLogin.POST("/sessions/local", func(c *gin.Context) {
			var request struct {
				Username string `json:"username" binding:"required"`
				Password string `json:"password" binding:"required"`
			}
			if err := c.ShouldBindJSON(&request); err != nil {
				writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid local credentials are required")
				return
			}
			created, err := localAccounts.Login(c.Request.Context(), application.LocalLoginInput{
				Username:  request.Username,
				Password:  request.Password,
				ClientKey: c.ClientIP(),
			})
			if errors.Is(err, domain.ErrLocalAuthenticationLimited) {
				writeError(c, http.StatusTooManyRequests, "LOCAL_LOGIN_THROTTLED",
					"local authentication is temporarily unavailable")
				return
			}
			if err != nil {
				writeError(c, http.StatusUnauthorized, "LOCAL_LOGIN_FAILED",
					"local credentials are invalid")
				return
			}
			c.JSON(http.StatusCreated, gin.H{
				"credential": created.Credential,
				"session":    newSessionResponse(created.Session, created.CSRFToken),
			})
		})
	}
	oauth := router.Group("/api/v1/oauth")
	oauth.Use(bffAuthenticationMiddleware(bffKey))
	oauth.GET("/providers/:identityProviderId", func(c *gin.Context) {
		providers, ok := localStatus.(oidcAuthorizationMetadataStatus)
		if !ok {
			writeError(c, http.StatusNotFound, "IDENTITY_PROVIDER_NOT_FOUND",
				"identity provider was not found")
			return
		}
		metadata, err := providers.AuthorizationMetadata(
			c.Request.Context(), c.Param("identityProviderId"),
		)
		if err != nil {
			writeError(c, http.StatusNotFound, "IDENTITY_PROVIDER_NOT_FOUND",
				"identity provider was not found")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"id": metadata.ID, "issuer": metadata.Issuer, "clientId": metadata.ClientID,
			"redirectUri": metadata.RedirectURI, "scopes": metadata.Scopes,
		})
	})
	oauth.POST("/login-attempts", func(c *gin.Context) {
		var request struct {
			ReturnTo string `json:"returnTo"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid login input is required")
			return
		}
		attempt, err := sessions.StartOAuthLogin(c.Request.Context(), request.ReturnTo)
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "LOGIN_UNAVAILABLE", "login could not be started")
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"state": attempt.State, "nonce": attempt.Nonce,
			"pkceVerifier": attempt.PKCEVerifier, "returnTo": attempt.ReturnTo,
		})
	})
	oauth.POST("/login-attempts/consume", func(c *gin.Context) {
		var request struct {
			State string `json:"state" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid callback state is required")
			return
		}
		attempt, err := sessions.ConsumeOAuthLogin(c.Request.Context(), request.State)
		if err != nil {
			writeError(c, http.StatusUnauthorized, "INVALID_OAUTH_STATE", "OAuth state is invalid or expired")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"nonce": attempt.Nonce, "pkceVerifier": attempt.PKCEVerifier,
			"returnTo": attempt.ReturnTo,
		})
	})

	create := router.Group("/api/v1")
	create.Use(
		bffAuthenticationMiddleware(bffKey),
		authenticationMiddleware("", oidcAuthenticators...),
	)
	create.POST("/sessions", func(c *gin.Context) {
		var request createSessionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid session input is required")
			return
		}
		actor, err := application.ActorFromContext(c.Request.Context())
		if err != nil {
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
			return
		}
		if strings.TrimSpace(request.IdentityProviderID) != actor.IdentityProviderID ||
			strings.TrimSpace(request.AuthenticationMethod) != actor.AuthenticationMethod {
			writeError(c, http.StatusUnauthorized, "SESSION_CREATION_FAILED", "session identity does not match the validated access token")
			return
		}
		created, err := sessions.Create(c.Request.Context(), application.CreateSessionInput{
			Actor: actor, IdentityProviderID: actor.IdentityProviderID,
			AuthenticationMethod: actor.AuthenticationMethod,
			RefreshToken:         request.RefreshToken, AccessToken: request.AccessToken,
			RotateCredential: request.RotateCredential,
		})
		if err != nil {
			writeError(c, http.StatusUnauthorized, "SESSION_CREATION_FAILED", "session could not be created")
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"credential": created.Credential,
			"session":    newSessionResponse(created.Session, created.CSRFToken),
		})
	})

	current := router.Group("/api/v1")
	current.Use(
		apiAuthenticationMiddleware("", sessions),
		browserRequestProtectionMiddleware(expectedOrigin, sessions),
	)
	current.GET("/session", func(c *gin.Context) {
		credential, ok := sessionCredential(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "SESSION_EXPIRED", "the browser session is invalid or expired")
			return
		}
		session, err := sessions.Current(c.Request.Context(), credential)
		if err != nil {
			writeSessionError(c, err)
			return
		}
		c.JSON(http.StatusOK, newSessionResponse(session, sessions.CSRFToken(credential)))
	})
	current.POST("/session/refresh", func(c *gin.Context) {
		credential, ok := sessionCredential(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "SESSION_EXPIRED", "the browser session is invalid or expired")
			return
		}
		refreshed, err := sessions.Refresh(c.Request.Context(), credential)
		if err != nil {
			writeSessionError(c, err)
			return
		}
		c.JSON(http.StatusOK, sessionRefreshResponse{
			sessionResponse: newSessionResponse(
				refreshed.Session, sessions.CSRFToken(credential),
			),
			Refreshed: refreshed.Refreshed,
		})
	})
	current.DELETE("/session", func(c *gin.Context) {
		credential, ok := sessionCredential(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "SESSION_EXPIRED", "the browser session is invalid or expired")
			return
		}
		if err := sessions.Revoke(c.Request.Context(), credential); err != nil {
			writeSessionError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	if localAccounts != nil {
		current.PUT("/local-account/password", func(c *gin.Context) {
			var request struct {
				CurrentPassword string `json:"currentPassword" binding:"required"`
				NewPassword     string `json:"newPassword" binding:"required"`
			}
			if err := c.ShouldBindJSON(&request); err != nil {
				writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid password input is required")
				return
			}
			credential, ok := sessionCredential(c)
			if !ok {
				writeSessionError(c, domain.ErrSessionInvalid)
				return
			}
			created, err := localAccounts.ChangePassword(
				c.Request.Context(), credential, request.CurrentPassword, request.NewPassword,
			)
			if err != nil {
				if errors.Is(err, domain.ErrLocalAuthenticationFailed) {
					writeError(c, http.StatusUnauthorized, "LOCAL_PASSWORD_CHANGE_FAILED",
						"the local password could not be changed")
					return
				}
				if errors.Is(err, domain.ErrLocalPasswordConflict) {
					writeError(c, http.StatusTooManyRequests, "LOCAL_PASSWORD_CHANGE_RETRY",
						"the local password could not be changed")
					return
				}
				writeError(c, http.StatusBadRequest, "INVALID_LOCAL_PASSWORD",
					"the local password does not meet requirements")
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"credential": created.Credential,
				"session":    newSessionResponse(created.Session, created.CSRFToken),
			})
		})
		current.POST("/principals/:principalId/local-account/password-reset", func(c *gin.Context) {
			var request struct {
				NewPassword string `json:"newPassword" binding:"required"`
			}
			if err := c.ShouldBindJSON(&request); err != nil {
				writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid password input is required")
				return
			}
			err := localAccounts.ResetPassword(
				c.Request.Context(), domain.PrincipalID(c.Param("principalId")), request.NewPassword,
			)
			switch {
			case err == nil:
				c.Status(http.StatusNoContent)
			case errors.Is(err, domain.ErrAccessDenied):
				writeError(c, http.StatusForbidden, "FORBIDDEN", "installation owner access is required")
			case errors.Is(err, domain.ErrLocalAccountNotFound):
				writeError(c, http.StatusNotFound, "LOCAL_ACCOUNT_NOT_FOUND", "local account was not found")
			default:
				writeError(c, http.StatusBadRequest, "INVALID_LOCAL_PASSWORD",
					"the local password does not meet requirements")
			}
		})
	}
}

func bffAuthenticationMiddleware(expected string) gin.HandlerFunc {
	expectedHash := sha256.Sum256([]byte(expected))
	return func(c *gin.Context) {
		providedHash := sha256.Sum256([]byte(c.GetHeader("X-KubeQueue-BFF-Key")))
		if expected == "" || subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
			c.Abort()
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "BFF authentication is required")
			return
		}
		c.Next()
	}
}

func newSessionResponse(session domain.BrowserSession, csrf string) sessionResponse {
	return sessionResponse{
		PrincipalID: session.Actor.PrincipalID, InstallationID: session.Actor.InstallationID,
		IdentityProviderID:   session.IdentityProviderID,
		AuthenticationMethod: session.AuthenticationMethod, CSRFToken: csrf,
		IdleExpiresAt: session.IdleExpiresAt, AbsoluteExpiresAt: session.AbsoluteExpiresAt,
	}
}

func sessionCredential(c *gin.Context) (string, bool) {
	value, ok := c.Get(browserSessionCredentialKey)
	if !ok {
		return "", false
	}
	credential, ok := value.(string)
	return credential, ok && credential != ""
}

func writeSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrSessionExpired), errors.Is(err, domain.ErrSessionRevoked),
		errors.Is(err, domain.ErrSessionInvalid),
		errors.Is(err, domain.ErrSessionRefreshRejected):
		writeError(c, http.StatusUnauthorized, "SESSION_EXPIRED", "the browser session is invalid or expired")
	case errors.Is(err, domain.ErrSessionRefreshUnavailable):
		writeError(c, http.StatusServiceUnavailable, "SESSION_REFRESH_UNAVAILABLE", "the session could not be refreshed")
	default:
		writeError(c, http.StatusServiceUnavailable, "SESSION_UNAVAILABLE", "the session service is unavailable")
	}
}
