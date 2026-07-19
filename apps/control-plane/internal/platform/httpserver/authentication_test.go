package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/gin-gonic/gin"
)

type stubBearerAuthenticator struct {
	token string
	actor domain.Actor
	calls *int
}

func (s stubBearerAuthenticator) Authenticate(
	_ context.Context, token string,
) (domain.Actor, error) {
	if s.calls != nil {
		(*s.calls)++
	}
	if token != s.token {
		return domain.Actor{}, errors.New("invalid token")
	}
	return s.actor, nil
}

type stubServiceAccountAuthenticator struct {
	result application.AuthenticatedServiceAccount
	err    error
	calls  int
}

func (s *stubServiceAccountAuthenticator) Authenticate(
	context.Context, string,
) (application.AuthenticatedServiceAccount, error) {
	s.calls++
	return s.result, s.err
}

func TestCompositeAuthenticationAcceptsOIDCOrLegacyButNeverEmptyLegacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oidcActor := domain.Actor{PrincipalID: "oidc_user", InstallationID: "default"}
	authenticator := stubBearerAuthenticator{token: "oidc-token", actor: oidcActor}
	tests := []struct {
		name        string
		legacyToken string
		provided    string
		wantStatus  int
		wantActor   domain.Actor
	}{
		{
			name: "OIDC token", legacyToken: "legacy-token", provided: "oidc-token",
			wantStatus: http.StatusNoContent, wantActor: oidcActor,
		},
		{
			name: "legacy compatibility token", legacyToken: "legacy-token", provided: "legacy-token",
			wantStatus: http.StatusNoContent,
			wantActor: domain.Actor{
				PrincipalID: "legacy_admin", InstallationID: "default",
				AuthenticationMethod: "LEGACY",
			},
		},
		{
			name:       "missing token with empty legacy configuration",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unknown token", provided: "unknown",
			wantStatus: http.StatusUnauthorized,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.Use(authenticationMiddleware(test.legacyToken, authenticator))
			router.GET("/check", func(c *gin.Context) {
				actor, err := application.ActorFromContext(c.Request.Context())
				if err != nil || !reflect.DeepEqual(actor, test.wantActor) {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/check", nil)
			if test.provided != "" {
				request.Header.Set("Authorization", "Bearer "+test.provided)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestNativeCredentialAuthenticationCarriesOnlyTokenBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	native := &stubServiceAccountAuthenticator{result: application.AuthenticatedServiceAccount{
		Actor: domain.Actor{
			PrincipalID: "build_bot", InstallationID: "default",
			AuthenticationMethod: domain.AuthenticationMethodNativeServiceAccount,
			CredentialID:         "credential-one",
			CredentialPermissions: []domain.Permission{
				domain.PermissionJobsRead,
			},
			CredentialScope: domain.AccessScope{
				InstallationID: "default", ProjectIDs: []domain.ProjectID{"platform"},
			},
		},
		CredentialID: "credential-one",
		Permissions:  []domain.Permission{domain.PermissionJobsRead},
	}}
	router := gin.New()
	router.Use(apiAuthenticationMiddleware(
		"", nil, nativeBearerAuthenticator{serviceAccounts: native},
	))
	router.GET("/check", func(c *gin.Context) {
		actor, err := application.ActorFromContext(c.Request.Context())
		if err != nil ||
			actor.AuthenticationMethod != domain.AuthenticationMethodNativeServiceAccount ||
			actor.CredentialID != "credential-one" ||
			len(actor.CredentialPermissions) != 1 ||
			actor.CredentialPermissions[0] != domain.PermissionJobsRead ||
			len(actor.CredentialScope.ProjectIDs) != 1 ||
			actor.CredentialScope.ProjectIDs[0] != "platform" {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/check", nil)
	request.Header.Set("Authorization", "Bearer kqsa.cHJlZml4.c2VjcmV0")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || native.calls != 1 {
		t.Fatalf("native authentication = status %d, calls %d", response.Code, native.calls)
	}
}

func TestRecognizedNativeCredentialFailureNeverFallsThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "invalid", err: serviceaccountcredential.ErrInvalidCredential},
		{name: "expired", err: serviceaccountcredential.ErrCredentialExpired},
		{name: "revoked", err: serviceaccountcredential.ErrCredentialRevoked},
	} {
		t.Run(failure.name, func(t *testing.T) {
			native := &stubServiceAccountAuthenticator{err: failure.err}
			oidcCalls := 0
			const token = "kqsa.recognized.failure"
			router := gin.New()
			router.Use(apiAuthenticationMiddleware(
				token,
				nil,
				nativeBearerAuthenticator{serviceAccounts: native},
				stubBearerAuthenticator{
					token: token,
					actor: domain.Actor{PrincipalID: "oidc_user", InstallationID: "default"},
					calls: &oidcCalls,
				},
			))
			router.GET("/check", func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/check", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized ||
				native.calls != 1 || oidcCalls != 0 ||
				!strings.Contains(response.Body.String(), `"code":"UNAUTHORIZED"`) {
				t.Fatalf(
					"failure = status %d, native calls %d, OIDC calls %d, body %s",
					response.Code, native.calls, oidcCalls, response.Body.String(),
				)
			}
		})
	}
}
