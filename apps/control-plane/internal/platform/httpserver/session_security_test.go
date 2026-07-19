package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type stubSessionAuthenticator struct{}

func (stubSessionAuthenticator) Authenticate(
	_ context.Context, credential string,
) (domain.Actor, error) {
	if credential != "session-secret" {
		return domain.Actor{}, domain.ErrSessionInvalid
	}
	return domain.Actor{PrincipalID: "person", InstallationID: "default"}, nil
}

func TestSessionCreationRequiresBFFKeyBeforeValidOIDCBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerSessionAPI(
		router,
		new(application.Sessions),
		nil,
		nil,
		"",
		"https://queue.example",
		"0123456789abcdef0123456789abcdef",
		stubBearerAuthenticator{
			token: "valid-access-token",
			actor: domain.Actor{
				PrincipalID: "person", InstallationID: "default",
				IdentityProviderID: "corporate", AuthenticationMethod: "OIDC",
			},
		},
	)
	for _, provided := range []string{"", "wrong-key"} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/api/v1/sessions",
			bytes.NewBufferString(`{
				"identityProviderId":"corporate",
				"authenticationMethod":"OIDC",
				"accessToken":"valid-access-token"
			}`),
		)
		request.Header.Set("Authorization", "Bearer valid-access-token")
		request.Header.Set("Content-Type", "application/json")
		if provided != "" {
			request.Header.Set("X-KubeQueue-BFF-Key", provided)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("BFF key %q status = %d, want 401", provided, response.Code)
		}
	}
}

func (stubSessionAuthenticator) ValidateCSRF(credential, token string) bool {
	return credential == "session-secret" && token == "csrf-value"
}

func TestBrowserSessionCSRFAndOriginProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, method, origin, csrf string
		want                       int
	}{
		{name: "safe method", method: http.MethodGet, want: http.StatusNoContent},
		{name: "missing origin", method: http.MethodPost, csrf: "csrf-value", want: http.StatusForbidden},
		{name: "cross origin", method: http.MethodPost, origin: "https://evil.example", csrf: "csrf-value", want: http.StatusForbidden},
		{name: "missing CSRF", method: http.MethodPost, origin: "https://queue.example", want: http.StatusForbidden},
		{name: "valid mutation", method: http.MethodPost, origin: "https://queue.example", csrf: "csrf-value", want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			authenticator := stubSessionAuthenticator{}
			router.Use(
				apiAuthenticationMiddleware("", authenticator),
				browserRequestProtectionMiddleware("https://queue.example", authenticator),
			)
			router.Any("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequestWithContext(t.Context(), test.method, "/test", nil)
			request.Header.Set("Authorization", "Session session-secret")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.csrf != "" {
				request.Header.Set("X-CSRF-Token", test.csrf)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}
