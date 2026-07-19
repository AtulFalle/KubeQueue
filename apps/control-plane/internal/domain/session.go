package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrSessionInvalid            = errors.New("session invalid")
	ErrSessionExpired            = errors.New("session expired")
	ErrSessionRevoked            = errors.New("session revoked")
	ErrSessionRefreshRejected    = errors.New("session refresh rejected")
	ErrSessionRefreshUnavailable = errors.New("session refresh unavailable")
	ErrOAuthAttemptDecryption    = errors.New("OAuth login attempt decryption failed")
)

type BrowserSession struct {
	ID                      string
	CredentialDigest        string
	CSRFDigest              string
	Actor                   Actor
	IdentityProviderID      string
	AuthenticationMethod    string
	RefreshTokenCiphertext  string
	AccessTokenCiphertext   string
	AccessTokenExpiresAt    time.Time
	AuthorizationGeneration int64
	IdleExpiresAt           time.Time
	AbsoluteExpiresAt       time.Time
	LastUsedAt              time.Time
	RevokedAt               *time.Time
	CreatedAt               time.Time
}

type OAuthLoginAttempt struct {
	StateDigest            string
	NonceDigest            string
	NonceCiphertext        string
	PKCEVerifierCiphertext string
	ReturnTo               string
	ExpiresAt              time.Time
	ConsumedAt             *time.Time
	CreatedAt              time.Time
}

func (s BrowserSession) Validate(now time.Time) error {
	if strings.TrimSpace(s.ID) == "" || s.CredentialDigest == "" || s.CSRFDigest == "" ||
		s.Actor.PrincipalID == "" || s.Actor.InstallationID == "" ||
		s.AuthenticationMethod == "" || s.AuthorizationGeneration < 1 {
		return ErrSessionInvalid
	}
	if s.RevokedAt != nil {
		return ErrSessionRevoked
	}
	if !now.Before(s.IdleExpiresAt) || !now.Before(s.AbsoluteExpiresAt) {
		return ErrSessionExpired
	}
	return nil
}
