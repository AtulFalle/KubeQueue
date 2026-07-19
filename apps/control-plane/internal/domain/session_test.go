package domain

import (
	"errors"
	"testing"
	"time"
)

func TestBrowserSessionValidateExpiryAndRevocation(t *testing.T) {
	now := time.Now().UTC()
	base := BrowserSession{
		ID: "session", CredentialDigest: "digest", CSRFDigest: "csrf",
		Actor:                Actor{PrincipalID: "person", InstallationID: "default"},
		AuthenticationMethod: "OIDC", AuthorizationGeneration: 1,
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	}
	if err := base.Validate(now); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}
	expired := base
	expired.IdleExpiresAt = now
	if err := expired.Validate(now); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("idle expiry error = %v", err)
	}
	revoked := base
	revoked.RevokedAt = &now
	if err := revoked.Validate(now); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revocation error = %v", err)
	}
}
