package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type httpIdentityProviderRepository struct {
	provider domain.ManagedIdentityProvider
}

func (s *httpIdentityProviderRepository) ListIdentityProviders(
	context.Context, domain.InstallationID,
) ([]domain.ManagedIdentityProvider, error) {
	return []domain.ManagedIdentityProvider{s.provider}, nil
}
func (s *httpIdentityProviderRepository) IdentityProvider(
	context.Context, domain.InstallationID, string,
) (domain.ManagedIdentityProvider, error) {
	return s.provider, nil
}
func (s *httpIdentityProviderRepository) CreateIdentityProvider(
	_ context.Context, provider domain.ManagedIdentityProvider,
) (domain.ManagedIdentityProvider, error) {
	s.provider = provider
	return provider, nil
}
func (s *httpIdentityProviderRepository) UpdateIdentityProvider(
	_ context.Context, provider domain.ManagedIdentityProvider, _ uint64,
) (domain.ManagedIdentityProvider, error) {
	s.provider = provider
	return provider, nil
}
func (s *httpIdentityProviderRepository) RecordIdentityProviderTest(
	context.Context, domain.InstallationID, string, uint64, bool, string, time.Time,
) (domain.ManagedIdentityProvider, error) {
	return s.provider, nil
}
func (s *httpIdentityProviderRepository) SetIdentityProviderEnabled(
	context.Context, domain.InstallationID, string, uint64, bool, time.Time,
) (domain.ManagedIdentityProvider, error) {
	return s.provider, nil
}
func (*httpIdentityProviderRepository) IsInstallationOwner(
	context.Context, domain.Actor,
) (bool, error) {
	return true, nil
}

type httpOIDCPreflight struct{}

func (httpOIDCPreflight) Preflight(context.Context, domain.OIDCProvider) error { return nil }

func TestIdentityProviderCreateResponseNeverReturnsSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &httpIdentityProviderRepository{}
	service, err := application.NewIdentityProviders(
		repository, httpOIDCPreflight{}, make([]byte, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	registerIdentityProviderAPI(router, service, "admin-token", nil, "")
	body := `{
		"id":"corp","displayName":"Corporate","issuer":"https://id.example.com",
		"audience":"api","clientId":"web","clientSecret":"top-secret",
		"redirectUri":"https://app.example.com/callback","allowedAlgorithms":["RS256"]
	}`
	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/identity-providers", bytes.NewBufferString(body),
	)
	request.Header.Set("Authorization", "Bearer admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("response = %d %s %s", response.Code, response.Header().Get("ETag"), response.Body)
	}
	if strings.Contains(response.Body.String(), "top-secret") ||
		strings.Contains(response.Body.String(), "clientSecret\"") {
		t.Fatalf("secret leaked in response: %s", response.Body)
	}
	if !strings.Contains(response.Body.String(), `"clientSecretConfigured":true`) {
		t.Fatalf("secret configuration marker missing: %s", response.Body)
	}
}
