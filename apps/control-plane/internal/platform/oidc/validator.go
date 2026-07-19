package oidc

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/golang-jwt/jwt/v5"
)

const (
	maxOIDCProviders = 32
	maxJWKSKeys      = 32
	maxMetadataBytes = 1 << 20
	maxAccessToken   = 64 << 10
	minCacheTTL      = time.Minute
	maxCacheTTL      = 24 * time.Hour
	rotationRetryTTL = 30 * time.Second
)

var ErrInvalidAccessToken = errors.New("invalid OIDC access token")

type Validator struct {
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]cachedProvider
}

type cachedProvider struct {
	discovery          discoveryDocument
	keys               map[string]verificationKey
	expiresAt          time.Time
	rotationRetryAfter time.Time
}

type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwksDocument struct {
	Keys []jsonWebKey `json:"keys"`
}

type jsonWebKey struct {
	Kid    string   `json:"kid"`
	Kty    string   `json:"kty"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
}

type verificationKey struct {
	publicKey any
	algorithm string
}

func NewValidator(client *http.Client) *Validator {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	} else {
		bounded := *client
		if bounded.Timeout <= 0 || bounded.Timeout > 10*time.Second {
			bounded.Timeout = 5 * time.Second
		}
		client = &bounded
	}
	return &Validator{
		client: client,
		now:    time.Now,
		cache:  make(map[string]cachedProvider),
	}
}

// Preflight validates discovery metadata and obtains at least one usable JWKS key.
func (v *Validator) Preflight(ctx context.Context, provider domain.OIDCProvider) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	keys, err := v.providerKeys(ctx, provider, true)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("OIDC JWKS contains no usable asymmetric signing keys")
	}
	return nil
}

func (v *Validator) Verify(
	ctx context.Context,
	rawToken string,
	providers []domain.OIDCProvider,
) (domain.OIDCProvider, domain.OIDCIdentityClaims, error) {
	if rawToken == "" || len(rawToken) > maxAccessToken ||
		len(providers) == 0 || len(providers) > maxOIDCProviders {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	unverified := jwt.MapClaims{}
	token, _, err := jwt.NewParser().ParseUnverified(rawToken, unverified)
	if err != nil {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	issuer, err := unverified.GetIssuer()
	if err != nil || issuer == "" {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	provider, found := providerForIssuer(providers, issuer)
	if !found || provider.Validate() != nil || token.Method == nil ||
		!contains(provider.AllowedAlgorithms, token.Method.Alg()) {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	if tokenType, _ := token.Header["typ"].(string); !strings.EqualFold(tokenType, "at+jwt") {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}

	claims, err := v.verifyWithCache(ctx, rawToken, provider, false)
	if err != nil {
		claims, err = v.verifyWithCache(ctx, rawToken, provider, true)
	}
	if err != nil {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	identity, err := identityClaims(provider, claims)
	if err != nil {
		return domain.OIDCProvider{}, domain.OIDCIdentityClaims{}, ErrInvalidAccessToken
	}
	return provider, identity, nil
}

func (v *Validator) verifyWithCache(
	ctx context.Context,
	rawToken string,
	provider domain.OIDCProvider,
	forceRefresh bool,
) (jwt.MapClaims, error) {
	keys, err := v.providerKeys(ctx, provider, forceRefresh)
	if err != nil {
		return nil, err
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(
		rawToken,
		claims,
		func(token *jwt.Token) (any, error) {
			keyID, _ := token.Header["kid"].(string)
			if keyID == "" {
				return nil, errors.New("JWT key ID is required")
			}
			key, ok := keys[keyID]
			if !ok || (key.algorithm != "" && key.algorithm != token.Method.Alg()) {
				return nil, errors.New("JWT verification key is unavailable")
			}
			return key.publicKey, nil
		},
		jwt.WithValidMethods(provider.AllowedAlgorithms),
		jwt.WithIssuer(provider.Issuer),
		jwt.WithAudience(provider.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || !token.Valid {
		return nil, ErrInvalidAccessToken
	}
	audiences, err := claims.GetAudience()
	if err != nil || len(audiences) != 1 || audiences[0] != provider.Audience {
		return nil, ErrInvalidAccessToken
	}
	if provider.AuthorizedParty != "" {
		authorizedParty, _ := claims["azp"].(string)
		if authorizedParty != provider.AuthorizedParty {
			return nil, ErrInvalidAccessToken
		}
	}
	return claims, nil
}

func (v *Validator) providerKeys(
	ctx context.Context,
	provider domain.OIDCProvider,
	forceRefresh bool,
) (map[string]verificationKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cached, ok := v.cache[provider.ID]; ok && !forceRefresh &&
		v.now().Before(cached.expiresAt) {
		return cached.keys, nil
	}
	if cached, ok := v.cache[provider.ID]; ok && forceRefresh &&
		v.now().Before(cached.rotationRetryAfter) {
		return cached.keys, nil
	}
	discovery, err := v.fetchDiscovery(ctx, provider)
	if err != nil {
		return nil, err
	}
	keys, err := v.fetchJWKS(ctx, discovery.JWKSURI)
	if err != nil {
		return nil, err
	}
	if len(v.cache) >= maxOIDCProviders {
		for key := range v.cache {
			delete(v.cache, key)
			break
		}
	}
	ttl := max(provider.CacheTTL, minCacheTTL)
	ttl = min(ttl, maxCacheTTL)
	rotationRetryAfter := time.Time{}
	if forceRefresh {
		rotationRetryAfter = v.now().Add(rotationRetryTTL)
	}
	v.cache[provider.ID] = cachedProvider{
		discovery:          discovery,
		keys:               keys,
		expiresAt:          v.now().Add(ttl),
		rotationRetryAfter: rotationRetryAfter,
	}
	return keys, nil
}

func (v *Validator) fetchDiscovery(
	ctx context.Context,
	provider domain.OIDCProvider,
) (discoveryDocument, error) {
	var document discoveryDocument
	endpoint := provider.Issuer + "/.well-known/openid-configuration"
	if err := v.getJSON(ctx, endpoint, &document); err != nil {
		return document, fmt.Errorf("fetch OIDC discovery: %w", err)
	}
	if document.Issuer != provider.Issuer {
		return document, errors.New("OIDC discovery issuer does not match configuration")
	}
	jwksURL, err := url.Parse(document.JWKSURI)
	if err != nil || jwksURL.Scheme == "" || jwksURL.Host == "" ||
		(jwksURL.Scheme != "https" && jwksURL.Scheme != "http") {
		return document, errors.New("OIDC discovery has an invalid JWKS URI")
	}
	if jwksURL.Scheme != "https" && !loopbackHost(jwksURL.Hostname()) {
		return document, errors.New("OIDC JWKS URI must use HTTPS except on loopback")
	}
	return document, nil
}

func (v *Validator) fetchJWKS(
	ctx context.Context,
	endpoint string,
) (map[string]verificationKey, error) {
	var document jwksDocument
	if err := v.getJSON(ctx, endpoint, &document); err != nil {
		return nil, fmt.Errorf("fetch OIDC JWKS: %w", err)
	}
	if len(document.Keys) == 0 || len(document.Keys) > maxJWKSKeys {
		return nil, errors.New("OIDC JWKS key count is outside the allowed bounds")
	}
	keys := make(map[string]verificationKey, len(document.Keys))
	for _, encoded := range document.Keys {
		if encoded.Kid == "" || len(encoded.Kid) > 256 {
			continue
		}
		if encoded.Use != "" && encoded.Use != "sig" {
			continue
		}
		if len(encoded.KeyOps) > 0 && !contains(encoded.KeyOps, "verify") {
			continue
		}
		publicKey, err := parsePublicKey(encoded)
		if err != nil {
			continue
		}
		if _, duplicate := keys[encoded.Kid]; duplicate {
			return nil, errors.New("OIDC JWKS contains a duplicate key ID")
		}
		keys[encoded.Kid] = verificationKey{publicKey: publicKey, algorithm: encoded.Alg}
	}
	if len(keys) == 0 {
		return nil, errors.New("OIDC JWKS contains no usable signature keys")
	}
	return keys, nil
}

func (v *Validator) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, maxMetadataBytes+1)
	encoded, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if len(encoded) > maxMetadataBytes {
		return errors.New("OIDC metadata response is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("OIDC metadata contains trailing JSON data")
	}
	return nil
}

func parsePublicKey(key jsonWebKey) (any, error) {
	switch key.Kty {
	case "RSA":
		modulusBytes, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil || len(modulusBytes) < 256 {
			return nil, errors.New("invalid RSA modulus")
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
			return nil, errors.New("invalid RSA exponent")
		}
		exponent := 0
		for _, value := range exponentBytes {
			exponent = exponent<<8 | int(value)
		}
		if exponent < 3 {
			return nil, errors.New("invalid RSA exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(modulusBytes), E: exponent}, nil
	case "EC":
		curves := map[string]struct {
			signature elliptic.Curve
			exchange  ecdh.Curve
			size      int
		}{
			"P-256": {signature: elliptic.P256(), exchange: ecdh.P256(), size: 32},
			"P-384": {signature: elliptic.P384(), exchange: ecdh.P384(), size: 48},
			"P-521": {signature: elliptic.P521(), exchange: ecdh.P521(), size: 66},
		}
		curve, supported := curves[key.Crv]
		if !supported {
			return nil, errors.New("unsupported EC curve")
		}
		xBytes, xErr := base64.RawURLEncoding.DecodeString(key.X)
		yBytes, yErr := base64.RawURLEncoding.DecodeString(key.Y)
		if xErr != nil || yErr != nil || len(xBytes) != curve.size || len(yBytes) != curve.size {
			return nil, errors.New("invalid EC point")
		}
		encodedPoint := make([]byte, 1+2*curve.size)
		encodedPoint[0] = 4
		copy(encodedPoint[1:1+curve.size], xBytes)
		copy(encodedPoint[1+curve.size:], yBytes)
		if _, err := curve.exchange.NewPublicKey(encodedPoint); err != nil {
			return nil, errors.New("EC point is not on its curve")
		}
		x, y := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes)
		return &ecdsa.PublicKey{Curve: curve.signature, X: x, Y: y}, nil
	default:
		return nil, errors.New("unsupported JWK key type")
	}
}

func identityClaims(
	provider domain.OIDCProvider,
	claims jwt.MapClaims,
) (domain.OIDCIdentityClaims, error) {
	subject, err := claims.GetSubject()
	if err != nil || strings.TrimSpace(subject) == "" || len(subject) > 512 {
		return domain.OIDCIdentityClaims{}, errors.New("OIDC subject is required")
	}
	email, _ := claims[provider.EmailClaim].(string)
	displayName, _ := claims[provider.NameClaim].(string)
	if len(email) > 320 || len(displayName) > 512 {
		return domain.OIDCIdentityClaims{}, errors.New("OIDC identity claim exceeds its allowed bound")
	}
	emailVerified, err := booleanClaim(claims["email_verified"])
	if err != nil {
		return domain.OIDCIdentityClaims{}, err
	}
	groups, err := stringClaimList(claims[provider.GroupsClaim])
	if err != nil {
		return domain.OIDCIdentityClaims{}, err
	}
	return domain.OIDCIdentityClaims{
		Issuer:        provider.Issuer,
		Subject:       subject,
		Email:         email,
		EmailVerified: emailVerified,
		DisplayName:   displayName,
		Groups:        groups,
	}, nil
}

func booleanClaim(value any) (bool, error) {
	if value == nil {
		return false, nil
	}
	verified, ok := value.(bool)
	if !ok {
		return false, errors.New("OIDC email_verified claim must be boolean")
	}
	return verified, nil
}

func stringClaimList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 200 {
		return nil, errors.New("OIDC groups claim must be an array")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		item, ok := value.(string)
		if !ok || item == "" || len(item) > 512 {
			return nil, errors.New("OIDC groups claim must contain non-empty strings")
		}
		result = append(result, item)
	}
	return result, nil
}

func providerForIssuer(
	providers []domain.OIDCProvider,
	issuer string,
) (domain.OIDCProvider, bool) {
	for _, provider := range providers {
		if provider.Issuer == issuer {
			return provider, true
		}
	}
	return domain.OIDCProvider{}, false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func loopbackHost(host string) bool {
	address := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (address != nil && address.IsLoopback())
}
