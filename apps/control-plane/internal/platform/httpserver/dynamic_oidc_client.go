package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type oidcClientResolver interface {
	ResolveClient(context.Context, string) (domain.ManagedIdentityProvider, string, error)
}

func registerOIDCTokenExchange(
	router *gin.Engine, bffKey string, client *dynamicOIDCClient,
) {
	if client == nil {
		return
	}
	group := router.Group("/api/v1/oauth")
	group.Use(bffAuthenticationMiddleware(bffKey))
	group.POST("/token-exchange", func(c *gin.Context) {
		var request struct {
			IdentityProviderID string `json:"identityProviderId" binding:"required"`
			Code               string `json:"code" binding:"required"`
			PKCEVerifier       string `json:"pkceVerifier" binding:"required"`
			RedirectURI        string `json:"redirectUri" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "valid authorization-code input is required")
			return
		}
		tokens, err := client.ExchangeAuthorizationCode(c.Request.Context(), oidcAuthorizationCodeInput{
			ProviderID: request.IdentityProviderID, Code: request.Code,
			PKCEVerifier: request.PKCEVerifier, RedirectURI: request.RedirectURI,
		})
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "OIDC_TOKEN_EXCHANGE_FAILED",
				"the authorization code could not be exchanged")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"identityProviderId": request.IdentityProviderID,
			"accessToken":        tokens.AccessToken, "refreshToken": tokens.RefreshToken,
		})
	})
}

type dynamicOIDCClient struct {
	resolver oidcClientResolver
	client   *http.Client
	now      func() time.Time
}

type oidcAuthorizationCodeInput struct {
	ProviderID   string
	Code         string
	PKCEVerifier string
	RedirectURI  string
}

func newDynamicOIDCClient(resolver oidcClientResolver) *dynamicOIDCClient {
	return &dynamicOIDCClient{
		resolver: resolver,
		client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

func (c *dynamicOIDCClient) Refresh(
	ctx context.Context, providerID, refreshToken string,
) (application.RefreshedSessionTokens, error) {
	if refreshToken == "" || len(refreshToken) > 16_384 {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	provider, secret, tokenURL, err := c.resolve(ctx, providerID)
	if err != nil {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	return c.exchange(ctx, tokenURL, provider, secret, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	})
}

func (c *dynamicOIDCClient) ExchangeAuthorizationCode(
	ctx context.Context, input oidcAuthorizationCodeInput,
) (application.RefreshedSessionTokens, error) {
	if input.Code == "" || len(input.Code) > 8192 ||
		len(input.PKCEVerifier) < 43 || len(input.PKCEVerifier) > 128 {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	provider, secret, tokenURL, err := c.resolve(ctx, input.ProviderID)
	if err != nil || input.RedirectURI != provider.Configuration.RedirectURI {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	return c.exchange(ctx, tokenURL, provider, secret, url.Values{
		"grant_type": {"authorization_code"}, "code": {input.Code},
		"code_verifier": {input.PKCEVerifier}, "redirect_uri": {input.RedirectURI},
	})
}

func (c *dynamicOIDCClient) resolve(
	ctx context.Context, providerID string,
) (domain.ManagedIdentityProvider, string, string, error) {
	provider, secret, err := c.resolver.ResolveClient(ctx, providerID)
	if err != nil || secret == "" {
		return provider, "", "", errors.New("OIDC client is unavailable")
	}
	var discovery struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	endpoint := provider.Configuration.Issuer + "/.well-known/openid-configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider, "", "", err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return provider, "", "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return provider, "", "", errors.New("OIDC discovery is unavailable")
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(encoded) > 1<<20 || json.Unmarshal(encoded, &discovery) != nil ||
		discovery.Issuer != provider.Configuration.Issuer {
		return provider, "", "", errors.New("OIDC discovery is invalid")
	}
	tokenURL, err := url.Parse(discovery.TokenEndpoint)
	if err != nil || tokenURL.Scheme == "" || tokenURL.Host == "" || tokenURL.User != nil ||
		tokenURL.RawQuery != "" || tokenURL.Fragment != "" ||
		(tokenURL.Scheme != "https" &&
			(tokenURL.Scheme != "http" || !sessionLoopbackHost(tokenURL.Hostname()))) {
		return provider, "", "", errors.New("OIDC token endpoint is invalid")
	}
	return provider, secret, tokenURL.String(), nil
}

func (c *dynamicOIDCClient) exchange(
	ctx context.Context, tokenURL string, provider domain.ManagedIdentityProvider,
	secret string, form url.Values,
) (application.RefreshedSessionTokens, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(provider.Configuration.ClientID, secret)
	response, err := c.client.Do(request)
	if err != nil {
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := readSessionTokenResponse(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || payload.AccessToken == "" ||
		len(payload.AccessToken) > 16_384 || len(payload.RefreshToken) > 16_384 ||
		(payload.TokenType != "" && !strings.EqualFold(payload.TokenType, "Bearer")) {
		if response.StatusCode == http.StatusBadRequest && payload.Error == "invalid_grant" {
			return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshRejected
		}
		return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
	}
	expiresAt := time.Time{}
	if payload.ExpiresIn != "" {
		seconds, parseErr := payload.ExpiresIn.Int64()
		if parseErr != nil || seconds <= 0 || seconds > 31_536_000 {
			return application.RefreshedSessionTokens{}, domain.ErrSessionRefreshUnavailable
		}
		expiresAt = c.now().UTC().Add(time.Duration(seconds) * time.Second)
	}
	return application.RefreshedSessionTokens{
		AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken,
		AccessTokenExpiresAt: expiresAt,
	}, nil
}
