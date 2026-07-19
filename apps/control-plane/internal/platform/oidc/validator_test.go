package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/golang-jwt/jwt/v5"
)

func TestValidatorCachesMetadataAndRefreshesRotatedKeys(t *testing.T) {
	firstKey := generateRSAKey(t)
	secondKey := generateRSAKey(t)
	var mu sync.Mutex
	activeKey, activeKeyID := firstKey, "first"
	jwksRequests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": server.URL, "jwks_uri": server.URL + "/jwks",
			})
		case "/jwks":
			mu.Lock()
			defer mu.Unlock()
			jwksRequests++
			writeJSON(t, w, map[string]any{
				"keys": []any{rsaJWK(activeKeyID, &activeKey.PublicKey)},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := testProvider(server.URL)
	validator := NewValidator(server.Client())
	firstToken := signAccessToken(t, firstKey, "first", provider, nil)
	if _, _, err := validator.Verify(t.Context(), firstToken, []domain.OIDCProvider{provider}); err != nil {
		t.Fatalf("first Verify() error = %v", err)
	}
	if _, _, err := validator.Verify(t.Context(), firstToken, []domain.OIDCProvider{provider}); err != nil {
		t.Fatalf("cached Verify() error = %v", err)
	}

	mu.Lock()
	activeKey, activeKeyID = secondKey, "second"
	mu.Unlock()
	rotatedToken := signAccessToken(t, secondKey, "second", provider, nil)
	if _, claims, err := validator.Verify(
		t.Context(), rotatedToken, []domain.OIDCProvider{provider},
	); err != nil {
		t.Fatalf("rotated Verify() error = %v", err)
	} else if claims.Subject != "user-123" || !claims.EmailVerified {
		t.Fatalf("claims = %#v, want verified user-123", claims)
	}

	mu.Lock()
	defer mu.Unlock()
	if jwksRequests != 2 {
		t.Fatalf("JWKS requests = %d, want 2", jwksRequests)
	}
}

func TestValidatorRejectsInvalidAccessTokenConstraints(t *testing.T) {
	key := generateRSAKey(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": server.URL, "jwks_uri": server.URL + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{
				"keys": []any{rsaJWK("key", &key.PublicKey)},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider := testProvider(server.URL)

	tests := []struct {
		name   string
		mutate func(jwt.MapClaims)
		header string
	}{
		{
			name: "issuer",
			mutate: func(claims jwt.MapClaims) {
				claims["iss"] = server.URL + "/other"
			},
		},
		{
			name: "exact audience",
			mutate: func(claims jwt.MapClaims) {
				claims["aud"] = []string{provider.Audience, "another-api"}
			},
		},
		{
			name: "expiry",
			mutate: func(claims jwt.MapClaims) {
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			},
		},
		{
			name: "not before",
			mutate: func(claims jwt.MapClaims) {
				claims["nbf"] = time.Now().Add(time.Hour).Unix()
			},
		},
		{
			name: "authorized party",
			mutate: func(claims jwt.MapClaims) {
				claims["azp"] = "other-client"
			},
		},
		{
			name: "email verification type",
			mutate: func(claims jwt.MapClaims) {
				claims["email_verified"] = "true"
			},
		},
		{name: "ID token type", header: "JWT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := signAccessToken(t, key, "key", provider, func(claims jwt.MapClaims) {
				if test.mutate != nil {
					test.mutate(claims)
				}
			})
			if test.header != "" {
				token = signTokenWithType(t, key, "key", provider, test.header)
			}
			if _, _, err := NewValidator(server.Client()).Verify(
				t.Context(), token, []domain.OIDCProvider{provider},
			); err == nil {
				t.Fatal("Verify() accepted an invalid token")
			}
		})
	}

	symmetric := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims(provider))
	symmetric.Header["typ"] = "at+jwt"
	symmetric.Header["kid"] = "key"
	raw, err := symmetric.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewValidator(server.Client()).Verify(
		t.Context(), raw, []domain.OIDCProvider{provider},
	); err == nil {
		t.Fatal("Verify() accepted a symmetric signing algorithm")
	}
}

func testProvider(issuer string) domain.OIDCProvider {
	return domain.OIDCProvider{
		ID:                "workforce",
		InstallationID:    "default",
		Issuer:            issuer,
		Audience:          "kubequeue-api",
		AuthorizedParty:   "kubequeue-bff",
		AllowedAlgorithms: []string{"RS256"},
		GroupsClaim:       "groups",
		EmailClaim:        "email",
		NameClaim:         "name",
		CacheTTL:          5 * time.Minute,
	}
}

func validClaims(provider domain.OIDCProvider) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            provider.Issuer,
		"sub":            "user-123",
		"aud":            provider.Audience,
		"azp":            provider.AuthorizedParty,
		"exp":            now.Add(5 * time.Minute).Unix(),
		"nbf":            now.Add(-time.Minute).Unix(),
		"iat":            now.Unix(),
		"email":          "user@example.com",
		"email_verified": true,
		"name":           "Example User",
		"groups":         []string{"operators"},
	}
}

func signAccessToken(
	t *testing.T,
	key *rsa.PrivateKey,
	keyID string,
	provider domain.OIDCProvider,
	mutate func(jwt.MapClaims),
) string {
	t.Helper()
	claims := validClaims(provider)
	if mutate != nil {
		mutate(claims)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = keyID
	token.Header["typ"] = "at+jwt"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func signTokenWithType(
	t *testing.T,
	key *rsa.PrivateKey,
	keyID string,
	provider domain.OIDCProvider,
	tokenType string,
) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, validClaims(provider))
	token.Header["kid"] = keyID
	token.Header["typ"] = tokenType
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func generateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func rsaJWK(keyID string, key *rsa.PublicKey) map[string]any {
	exponent := big.NewInt(int64(key.E)).Bytes()
	return map[string]any{
		"kid": keyID,
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(exponent),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}
