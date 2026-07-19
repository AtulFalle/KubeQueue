package application

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const sessionTokenBytes = 32

var (
	sessionTokenAAD      = []byte("kubequeue-browser-session-v1")
	oauthNonceAAD        = []byte("kubequeue-oauth-attempt-nonce-v1")
	oauthPKCEVerifierAAD = []byte("kubequeue-oauth-attempt-pkce-v1")
)

type SessionRepository interface {
	CreateBrowserSession(context.Context, domain.BrowserSession, string) error
	BrowserSessionByDigest(context.Context, string) (domain.BrowserSession, error)
	TouchBrowserSession(context.Context, string, time.Time, time.Time) error
	RevokeBrowserSession(context.Context, string, time.Time) error
	UpdateBrowserSessionTokens(context.Context, string, string, string, string) (bool, error)
	RevokeBrowserSessionIfRefreshToken(context.Context, string, string, time.Time) (bool, error)
	CreateOAuthLoginAttempt(context.Context, domain.OAuthLoginAttempt) error
	ConsumeOAuthLoginAttempt(context.Context, string, time.Time) (domain.OAuthLoginAttempt, error)
}

type SessionConfig struct {
	DigestKey        []byte
	EncryptionKey    []byte
	IdleLifetime     time.Duration
	AbsoluteLifetime time.Duration
	LastUsedInterval time.Duration
	TokenRefresher   SessionTokenRefresher
}

type SessionTokenRefresher interface {
	Refresh(context.Context, string, string) (RefreshedSessionTokens, error)
}

type RefreshedSessionTokens struct {
	AccessToken          string
	RefreshToken         string
	AccessTokenExpiresAt time.Time
}

type CreateSessionInput struct {
	Actor                domain.Actor
	IdentityProviderID   string
	AuthenticationMethod string
	RefreshToken         string
	AccessToken          string
	RotateCredential     string
}

type SessionCredentials struct {
	Credential string
	CSRFToken  string
	Session    domain.BrowserSession
}

type SessionRefresh struct {
	Session   domain.BrowserSession
	Refreshed bool
}

type OAuthLoginStart struct {
	State        string
	Nonce        string
	PKCEVerifier string
	ReturnTo     string
}

type ConsumedOAuthLogin struct {
	Nonce        string
	PKCEVerifier string
	ReturnTo     string
}

type Sessions struct {
	repository SessionRepository
	digestKey  []byte
	aead       cipher.AEAD
	config     SessionConfig
	now        func() time.Time
	random     io.Reader
	refresher  SessionTokenRefresher
}

func NewSessions(repository SessionRepository, config SessionConfig) (*Sessions, error) {
	if repository == nil || len(config.DigestKey) < 32 {
		return nil, errors.New("session repository and 256-bit digest key are required")
	}
	block, err := aes.NewCipher(config.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create session AEAD: %w", err)
	}
	if config.IdleLifetime <= 0 || config.AbsoluteLifetime <= 0 ||
		config.IdleLifetime > config.AbsoluteLifetime || config.LastUsedInterval <= 0 {
		return nil, errors.New("valid session lifetimes are required")
	}
	return &Sessions{
		repository: repository, digestKey: append([]byte(nil), config.DigestKey...),
		aead: aead, config: config, now: time.Now, random: rand.Reader,
		refresher: config.TokenRefresher,
	}, nil
}

func (s *Sessions) WithTokenRefresher(refresher SessionTokenRefresher) {
	s.refresher = refresher
}

func (s *Sessions) Create(ctx context.Context, input CreateSessionInput) (SessionCredentials, error) {
	if input.Actor.PrincipalID == "" || input.Actor.InstallationID == "" {
		return SessionCredentials{}, domain.ErrSessionInvalid
	}
	switch input.AuthenticationMethod {
	case "OIDC":
		if input.IdentityProviderID == "" || input.AccessToken == "" ||
			len(input.AccessToken) > 16_384 || len(input.RefreshToken) > 16_384 {
			return SessionCredentials{}, domain.ErrSessionInvalid
		}
	case domain.AuthenticationMethodLocal:
		if input.IdentityProviderID != "" || input.RefreshToken != "" || input.AccessToken != "" {
			return SessionCredentials{}, domain.ErrSessionInvalid
		}
	default:
		return SessionCredentials{}, domain.ErrSessionInvalid
	}
	credential, err := s.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	csrf := s.csrfToken(credential)
	var refreshCiphertext, accessCiphertext string
	var accessExpiry time.Time
	if input.AuthenticationMethod == "OIDC" {
		refreshCiphertext, err = s.encrypt(input.RefreshToken, sessionTokenAAD)
		if err != nil {
			return SessionCredentials{}, err
		}
		accessExpiry = accessTokenExpiry(input.AccessToken)
		accessCiphertext, err = s.encryptAccessToken(input.AccessToken, accessExpiry)
		if err != nil {
			return SessionCredentials{}, err
		}
	}
	id, err := s.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	now := s.now().UTC()
	session := domain.BrowserSession{
		ID: id, CredentialDigest: s.digest("session", credential),
		CSRFDigest: s.digest("csrf", csrf), Actor: input.Actor,
		IdentityProviderID:     input.IdentityProviderID,
		AuthenticationMethod:   input.AuthenticationMethod,
		RefreshTokenCiphertext: refreshCiphertext, AccessTokenCiphertext: accessCiphertext,
		AccessTokenExpiresAt: accessExpiry,
		IdleExpiresAt:        now.Add(s.config.IdleLifetime),
		AbsoluteExpiresAt:    now.Add(s.config.AbsoluteLifetime),
		LastUsedAt:           now, CreatedAt: now,
	}
	rotateDigest := ""
	if input.RotateCredential != "" {
		rotateDigest = s.digest("session", input.RotateCredential)
	}
	changedFields := []string{
		"identity_provider", "authentication_method", "expires_at",
	}
	if rotateDigest != "" {
		changedFields = append(changedFields, "prior_session_revoked")
	}
	ctx, err = withTransactionalAudit(
		ctx, sessionAuditActor(session), "sessions.create", "browser_session",
		sessionAuditID(session.ID), "",
		"CREATED", "authentication.succeeded",
		changedFields...,
	)
	if err != nil {
		return SessionCredentials{}, err
	}
	if err := s.repository.CreateBrowserSession(ctx, session, rotateDigest); err != nil {
		return SessionCredentials{}, fmt.Errorf("create browser session: %w", err)
	}
	stored, err := s.repository.BrowserSessionByDigest(ctx, session.CredentialDigest)
	if err != nil {
		return SessionCredentials{}, fmt.Errorf("read created browser session: %w", err)
	}
	return SessionCredentials{Credential: credential, CSRFToken: csrf, Session: stored}, nil
}

func (s *Sessions) Authenticate(ctx context.Context, credential string) (domain.Actor, error) {
	session, err := s.Current(ctx, credential)
	if err != nil {
		return domain.Actor{}, err
	}
	return session.Actor, nil
}

func (s *Sessions) Current(ctx context.Context, credential string) (domain.BrowserSession, error) {
	if credential == "" {
		return domain.BrowserSession{}, domain.ErrSessionInvalid
	}
	digest := s.digest("session", credential)
	session, err := s.repository.BrowserSessionByDigest(ctx, digest)
	if err != nil {
		return domain.BrowserSession{}, domain.ErrSessionInvalid
	}
	now := s.now().UTC()
	if err := session.Validate(now); err != nil {
		return domain.BrowserSession{}, err
	}
	if now.Sub(session.LastUsedAt) >= s.config.LastUsedInterval {
		idleExpiry := now.Add(s.config.IdleLifetime)
		if idleExpiry.After(session.AbsoluteExpiresAt) {
			idleExpiry = session.AbsoluteExpiresAt
		}
		if err := s.repository.TouchBrowserSession(ctx, digest, now, idleExpiry); err != nil {
			return domain.BrowserSession{}, fmt.Errorf("touch browser session: %w", err)
		}
		session.LastUsedAt = now
		session.IdleExpiresAt = idleExpiry
	}
	return session, nil
}

func (s *Sessions) Refresh(ctx context.Context, credential string) (SessionRefresh, error) {
	session, err := s.Current(ctx, credential)
	if err != nil {
		return SessionRefresh{}, err
	}
	if session.AuthenticationMethod == domain.AuthenticationMethodLocal {
		return SessionRefresh{Session: session}, nil
	}
	_, accessExpiry, err := s.decryptAccessToken(session.AccessTokenCiphertext)
	if err != nil {
		return SessionRefresh{}, err
	}
	session.AccessTokenExpiresAt = accessExpiry
	now := s.now().UTC()
	if !accessExpiry.IsZero() && now.Add(time.Minute).Before(accessExpiry) {
		return SessionRefresh{Session: session}, nil
	}
	if s.refresher == nil {
		return SessionRefresh{}, domain.ErrSessionRefreshUnavailable
	}
	refreshToken, err := s.decrypt(session.RefreshTokenCiphertext, sessionTokenAAD)
	if err != nil || refreshToken == "" {
		return SessionRefresh{}, domain.ErrSessionRefreshUnavailable
	}
	refreshed, err := s.refresher.Refresh(ctx, session.IdentityProviderID, refreshToken)
	if err != nil {
		if !errors.Is(err, domain.ErrSessionRefreshRejected) {
			return SessionRefresh{}, fmt.Errorf("refresh OIDC session token: %w", err)
		}
		ctx, err = withTransactionalAudit(
			ctx, sessionAuditActor(session), "sessions.revoke", "browser_session",
			sessionAuditID(session.ID), "",
			"REVOKED", "session.refresh_rejected", "revoked_at",
		)
		if err != nil {
			return SessionRefresh{}, err
		}
		revoked, revokeErr := s.repository.RevokeBrowserSessionIfRefreshToken(
			ctx, session.CredentialDigest, session.RefreshTokenCiphertext, now,
		)
		if revokeErr != nil {
			return SessionRefresh{}, fmt.Errorf("revoke rejected browser session: %w", revokeErr)
		}
		if revoked {
			return SessionRefresh{}, domain.ErrSessionRefreshRejected
		}
		current, currentErr := s.Current(ctx, credential)
		return SessionRefresh{Session: current}, currentErr
	}
	if refreshed.AccessToken == "" || len(refreshed.AccessToken) > 16_384 {
		return SessionRefresh{}, domain.ErrSessionRefreshUnavailable
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = refreshToken
	}
	if len(refreshed.RefreshToken) > 16_384 {
		return SessionRefresh{}, domain.ErrSessionRefreshUnavailable
	}
	if refreshed.AccessTokenExpiresAt.IsZero() {
		refreshed.AccessTokenExpiresAt = accessTokenExpiry(refreshed.AccessToken)
	}
	if refreshed.AccessTokenExpiresAt.IsZero() || !now.Before(refreshed.AccessTokenExpiresAt) {
		return SessionRefresh{}, domain.ErrSessionRefreshUnavailable
	}
	refreshCiphertext, err := s.encrypt(refreshed.RefreshToken, sessionTokenAAD)
	if err != nil {
		return SessionRefresh{}, err
	}
	accessCiphertext, err := s.encryptAccessToken(
		refreshed.AccessToken, refreshed.AccessTokenExpiresAt,
	)
	if err != nil {
		return SessionRefresh{}, err
	}
	ctx, err = withTransactionalAudit(
		ctx, sessionAuditActor(session), "sessions.refresh", "browser_session",
		sessionAuditID(session.ID), "",
		"REFRESHED", "authentication.succeeded",
		"identity_provider", "access_expiry",
	)
	if err != nil {
		return SessionRefresh{}, err
	}
	updated, err := s.repository.UpdateBrowserSessionTokens(
		ctx, session.CredentialDigest, session.RefreshTokenCiphertext,
		refreshCiphertext, accessCiphertext,
	)
	if err != nil {
		return SessionRefresh{}, fmt.Errorf("persist refreshed browser session: %w", err)
	}
	if !updated {
		current, currentErr := s.Current(ctx, credential)
		return SessionRefresh{Session: current}, currentErr
	}
	session.RefreshTokenCiphertext = refreshCiphertext
	session.AccessTokenCiphertext = accessCiphertext
	session.AccessTokenExpiresAt = refreshed.AccessTokenExpiresAt
	return SessionRefresh{Session: session, Refreshed: true}, nil
}

func (s *Sessions) CSRFToken(credential string) string {
	return s.csrfToken(credential)
}

func (s *Sessions) ValidateCSRF(credential, token string) bool {
	if credential == "" || token == "" {
		return false
	}
	expected := s.csrfToken(credential)
	return hmac.Equal([]byte(expected), []byte(token))
}

func (s *Sessions) Revoke(ctx context.Context, credential string) error {
	if credential == "" {
		return domain.ErrSessionInvalid
	}
	session, err := s.Current(ctx, credential)
	if err != nil {
		return err
	}
	ctx, err = withTransactionalAudit(
		ctx, sessionAuditActor(session), "sessions.logout", "browser_session",
		sessionAuditID(session.ID), "",
		"REVOKED", "request.accepted", "revoked_at",
	)
	if err != nil {
		return err
	}
	return s.repository.RevokeBrowserSession(
		ctx, s.digest("session", credential), s.now().UTC(),
	)
}

func sessionAuditActor(session domain.BrowserSession) domain.Actor {
	actor := session.Actor
	actor.AuthenticationMethod = session.AuthenticationMethod
	actor.CredentialID = sessionAuditID(session.ID)
	return actor
}

func sessionAuditID(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "browser:" + hex.EncodeToString(digest[:16])
}

func (s *Sessions) StartOAuthLogin(
	ctx context.Context, returnTo string,
) (OAuthLoginStart, error) {
	if len(returnTo) > 2048 || len(returnTo) == 0 || returnTo[0] != '/' ||
		(len(returnTo) > 1 && returnTo[1] == '/') {
		return OAuthLoginStart{}, domain.ErrSessionInvalid
	}
	state, err := s.randomToken()
	if err != nil {
		return OAuthLoginStart{}, err
	}
	nonce, err := s.randomToken()
	if err != nil {
		return OAuthLoginStart{}, err
	}
	verifierBytes := make([]byte, 64)
	if _, err := io.ReadFull(s.random, verifierBytes); err != nil {
		return OAuthLoginStart{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	nonceCiphertext, err := s.encrypt(nonce, oauthNonceAAD)
	if err != nil {
		return OAuthLoginStart{}, err
	}
	verifierCiphertext, err := s.encrypt(verifier, oauthPKCEVerifierAAD)
	if err != nil {
		return OAuthLoginStart{}, err
	}
	now := s.now().UTC()
	attempt := domain.OAuthLoginAttempt{
		StateDigest:            s.digest("oauth-state", state),
		NonceDigest:            s.digest("oauth-nonce", nonce),
		NonceCiphertext:        nonceCiphertext,
		PKCEVerifierCiphertext: verifierCiphertext,
		ReturnTo:               returnTo,
		ExpiresAt:              now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := s.repository.CreateOAuthLoginAttempt(ctx, attempt); err != nil {
		return OAuthLoginStart{}, fmt.Errorf("create OAuth login attempt: %w", err)
	}
	return OAuthLoginStart{
		State: state, Nonce: nonce, PKCEVerifier: verifier, ReturnTo: returnTo,
	}, nil
}

func (s *Sessions) ConsumeOAuthLogin(
	ctx context.Context, state string,
) (ConsumedOAuthLogin, error) {
	if state == "" {
		return ConsumedOAuthLogin{}, domain.ErrSessionInvalid
	}
	attempt, err := s.repository.ConsumeOAuthLoginAttempt(
		ctx, s.digest("oauth-state", state), s.now().UTC(),
	)
	if err != nil {
		return ConsumedOAuthLogin{}, err
	}
	nonce, err := s.decrypt(attempt.NonceCiphertext, oauthNonceAAD)
	if err != nil {
		return ConsumedOAuthLogin{}, err
	}
	if !hmac.Equal(
		[]byte(attempt.NonceDigest),
		[]byte(s.digest("oauth-nonce", nonce)),
	) {
		return ConsumedOAuthLogin{}, domain.ErrOAuthAttemptDecryption
	}
	verifier, err := s.decrypt(attempt.PKCEVerifierCiphertext, oauthPKCEVerifierAAD)
	if err != nil {
		return ConsumedOAuthLogin{}, err
	}
	return ConsumedOAuthLogin{
		Nonce: nonce, PKCEVerifier: verifier, ReturnTo: attempt.ReturnTo,
	}, nil
}

func (s *Sessions) digest(purpose, value string) string {
	mac := hmac.New(sha256.New, s.digestKey)
	_, _ = io.WriteString(mac, purpose)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, value)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Sessions) csrfToken(credential string) string {
	return s.digest("csrf-token", credential)
}

func (s *Sessions) randomToken() (string, error) {
	value := make([]byte, sessionTokenBytes)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", fmt.Errorf("generate session credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *Sessions) encrypt(value string, aad []byte) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return "", fmt.Errorf("generate token nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), aad)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Sessions) decrypt(ciphertext string, aad []byte) (string, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil || len(encoded) < s.aead.NonceSize()+s.aead.Overhead() {
		return "", domain.ErrOAuthAttemptDecryption
	}
	nonce, sealed := encoded[:s.aead.NonceSize()], encoded[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return "", domain.ErrOAuthAttemptDecryption
	}
	return string(plaintext), nil
}

type encryptedAccessToken struct {
	Version   int    `json:"version"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

func (s *Sessions) encryptAccessToken(token string, expiresAt time.Time) (string, error) {
	envelope := encryptedAccessToken{Version: 1, Token: token}
	if !expiresAt.IsZero() {
		envelope.ExpiresAt = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode access token envelope: %w", err)
	}
	return s.encrypt(string(encoded), sessionTokenAAD)
}

func (s *Sessions) decryptAccessToken(ciphertext string) (string, time.Time, error) {
	plaintext, err := s.decrypt(ciphertext, sessionTokenAAD)
	if err != nil {
		return "", time.Time{}, err
	}
	var envelope encryptedAccessToken
	if json.Unmarshal([]byte(plaintext), &envelope) == nil &&
		envelope.Version == 1 && envelope.Token != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
		if envelope.ExpiresAt != "" && parseErr != nil {
			return "", time.Time{}, domain.ErrSessionInvalid
		}
		return envelope.Token, expiresAt, nil
	}
	return plaintext, accessTokenExpiry(plaintext), nil
}

func accessTokenExpiry(token string) time.Time {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return time.Time{}
	}
	encoded, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil || len(encoded) > 64<<10 {
		return time.Time{}
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if decoder.Decode(&claims) != nil {
		return time.Time{}
	}
	seconds, err := claims.ExpiresAt.Int64()
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
