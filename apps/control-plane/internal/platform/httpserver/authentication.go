package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/breakglass"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/gin-gonic/gin"
)

type bearerAuthenticator interface {
	Authenticate(context.Context, string) (domain.Actor, error)
}

type sessionAuthenticator interface {
	Authenticate(context.Context, string) (domain.Actor, error)
	ValidateCSRF(string, string) bool
}

type serviceAccountAuthenticator interface {
	Authenticate(context.Context, string) (application.AuthenticatedServiceAccount, error)
}

type nativeBearerAuthenticator struct {
	serviceAccounts serviceAccountAuthenticator
}

func (a nativeBearerAuthenticator) Authenticate(
	ctx context.Context,
	credential string,
) (domain.Actor, error) {
	authenticated, err := a.serviceAccounts.Authenticate(ctx, credential)
	if err != nil {
		return domain.Actor{}, err
	}
	return authenticated.Actor, nil
}

func (nativeBearerAuthenticator) nativeCredentialAuthenticator() {}

type nativeCredentialAuthenticator interface {
	bearerAuthenticator
	nativeCredentialAuthenticator()
}

type breakGlassBearerAuthenticator struct {
	service *application.BreakGlass
}

func (a breakGlassBearerAuthenticator) Authenticate(
	ctx context.Context,
	credential string,
) (domain.Actor, error) {
	return a.service.Authenticate(ctx, credential)
}

func (breakGlassBearerAuthenticator) breakGlassCredentialAuthenticator() {}

type breakGlassCredentialAuthenticator interface {
	bearerAuthenticator
	breakGlassCredentialAuthenticator()
}

const browserSessionCredentialKey = "browserSessionCredential"

func authenticationMiddleware(
	legacyToken string,
	oidcAuthenticators ...bearerAuthenticator,
) gin.HandlerFunc {
	return apiAuthenticationMiddleware(legacyToken, nil, oidcAuthenticators...)
}

func apiAuthenticationMiddleware(
	legacyToken string,
	sessions sessionAuthenticator,
	oidcAuthenticators ...bearerAuthenticator,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheme, credential, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		if !ok || credential == "" {
			c.Abort()
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid credential is required")
			return
		}

		if scheme == "Session" && sessions != nil {
			actor, err := sessions.Authenticate(c.Request.Context(), credential)
			if err == nil {
				c.Set(browserSessionCredentialKey, credential)
				withActor(c, actor)
				return
			}
			c.Abort()
			writeError(c, http.StatusUnauthorized, "SESSION_EXPIRED", "the browser session is invalid or expired")
			return
		}
		if scheme != "Bearer" {
			c.Abort()
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid credential is required")
			return
		}
		if breakglass.IsReserved(credential) {
			for _, authenticator := range oidcAuthenticators {
				native, ok := authenticator.(breakGlassCredentialAuthenticator)
				if !ok || native == nil {
					continue
				}
				actor, err := native.Authenticate(c.Request.Context(), credential)
				if err == nil {
					withActor(c, actor)
					return
				}
				break
			}
			c.Abort()
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid bearer token is required")
			return
		}
		if serviceaccountcredential.IsNative(credential) {
			for _, authenticator := range oidcAuthenticators {
				native, ok := authenticator.(nativeCredentialAuthenticator)
				if !ok || native == nil {
					continue
				}
				actor, err := native.Authenticate(c.Request.Context(), credential)
				if err == nil {
					withActor(c, actor)
					return
				}
				break
			}
			c.Abort()
			writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid bearer token is required")
			return
		}
		legacyHash := sha256.Sum256([]byte(legacyToken))
		providedHash := sha256.Sum256([]byte(credential))
		if legacyToken != "" &&
			subtle.ConstantTimeCompare(legacyHash[:], providedHash[:]) == 1 {
			withActor(c, domain.Actor{
				PrincipalID: "legacy_admin", InstallationID: "default",
				AuthenticationMethod: "LEGACY",
			})
			return
		}

		for _, authenticator := range oidcAuthenticators {
			if authenticator == nil {
				continue
			}
			actor, err := authenticator.Authenticate(c.Request.Context(), credential)
			if err == nil {
				withActor(c, actor)
				return
			}
		}
		c.Abort()
		writeError(c, http.StatusUnauthorized, "UNAUTHORIZED", "a valid bearer token is required")
	}
}

func browserRequestProtectionMiddleware(
	expectedOrigin string,
	sessions sessionAuthenticator,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		credential, browserSession := c.Get(browserSessionCredentialKey)
		if !browserSession || isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}
		if expectedOrigin == "" || c.GetHeader("Origin") != expectedOrigin {
			c.Abort()
			writeError(c, http.StatusForbidden, "INVALID_ORIGIN", "request origin is not allowed")
			return
		}
		rawCredential, ok := credential.(string)
		if !ok || sessions == nil ||
			!sessions.ValidateCSRF(rawCredential, c.GetHeader("X-CSRF-Token")) {
			c.Abort()
			writeError(c, http.StatusForbidden, "INVALID_CSRF", "CSRF token is invalid")
			return
		}
		c.Next()
	}
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func withActor(c *gin.Context, actor domain.Actor) {
	ctx := application.WithActor(c.Request.Context(), actor)
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}
