package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestBrowserSessionRefreshRotationExpiryAndRejection(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name             string
		accessExpiry     time.Time
		refreshed        RefreshedSessionTokens
		refreshErr       error
		wantRefreshed    bool
		wantRefreshToken string
		wantRevoked      bool
		wantErr          error
	}{
		{
			name: "successful rotation", accessExpiry: now.Add(30 * time.Second),
			refreshed: RefreshedSessionTokens{
				AccessToken:          accessJWT(now.Add(10 * time.Minute)),
				RefreshToken:         "rotated-refresh-token",
				AccessTokenExpiresAt: now.Add(10 * time.Minute),
			},
			wantRefreshed: true, wantRefreshToken: "rotated-refresh-token",
		},
		{
			name: "non-rotating provider", accessExpiry: now.Add(30 * time.Second),
			refreshed: RefreshedSessionTokens{
				AccessToken:          accessJWT(now.Add(10 * time.Minute)),
				AccessTokenExpiresAt: now.Add(10 * time.Minute),
			},
			wantRefreshed: true, wantRefreshToken: "stored-refresh-token",
		},
		{
			name: "unexpired access token", accessExpiry: now.Add(10 * time.Minute),
			wantRefreshToken: "stored-refresh-token",
		},
		{
			name: "invalid grant revokes session", accessExpiry: now.Add(30 * time.Second),
			refreshErr:  domain.ErrSessionRefreshRejected,
			wantRevoked: true, wantErr: domain.ErrSessionRefreshRejected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &refreshSessionRepository{}
			refresher := &stubSessionTokenRefresher{
				result: test.refreshed,
				err:    test.refreshErr,
			}
			sessions, err := NewSessions(repository, SessionConfig{
				DigestKey: make([]byte, 32), EncryptionKey: make([]byte, 32),
				IdleLifetime: time.Hour, AbsoluteLifetime: 8 * time.Hour,
				LastUsedInterval: time.Minute, TokenRefresher: refresher,
			})
			if err != nil {
				t.Fatal(err)
			}
			sessions.now = func() time.Time { return now }
			created, err := sessions.Create(t.Context(), CreateSessionInput{
				Actor: domain.Actor{
					PrincipalID: "person", InstallationID: "default",
				},
				IdentityProviderID: "corporate", AuthenticationMethod: "OIDC",
				AccessToken: accessJWT(test.accessExpiry), RefreshToken: "stored-refresh-token",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := sessions.Refresh(t.Context(), created.Credential)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Refresh() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if repository.session.RevokedAt != nil != test.wantRevoked {
					t.Fatalf("revoked = %t, want %t", repository.session.RevokedAt != nil, test.wantRevoked)
				}
				return
			}
			if result.Refreshed != test.wantRefreshed {
				t.Fatalf("Refreshed = %t, want %t", result.Refreshed, test.wantRefreshed)
			}
			refreshToken, decryptErr := sessions.decrypt(
				repository.session.RefreshTokenCiphertext, sessionTokenAAD,
			)
			if decryptErr != nil || refreshToken != test.wantRefreshToken {
				t.Fatalf("stored refresh token = %q, %v", refreshToken, decryptErr)
			}
			wantCalls := 0
			if test.wantRefreshed {
				wantCalls = 1
			}
			if refresher.calls != wantCalls {
				t.Fatalf("provider calls = %d, want %d", refresher.calls, wantCalls)
			}
		})
	}
}

type stubSessionTokenRefresher struct {
	result RefreshedSessionTokens
	err    error
	calls  int
}

func (s *stubSessionTokenRefresher) Refresh(
	_ context.Context, _, _ string,
) (RefreshedSessionTokens, error) {
	s.calls++
	return s.result, s.err
}

type refreshSessionRepository struct {
	session domain.BrowserSession
}

func (r *refreshSessionRepository) CreateBrowserSession(
	_ context.Context, session domain.BrowserSession, _ string,
) error {
	session.AuthorizationGeneration = 1
	r.session = session
	return nil
}

func (r *refreshSessionRepository) BrowserSessionByDigest(
	_ context.Context, digest string,
) (domain.BrowserSession, error) {
	if r.session.CredentialDigest != digest {
		return domain.BrowserSession{}, domain.ErrSessionInvalid
	}
	return r.session, nil
}

func (r *refreshSessionRepository) TouchBrowserSession(
	_ context.Context, _ string, lastUsedAt, idleExpiresAt time.Time,
) error {
	r.session.LastUsedAt = lastUsedAt
	r.session.IdleExpiresAt = idleExpiresAt
	return nil
}

func (r *refreshSessionRepository) RevokeBrowserSession(
	_ context.Context, _ string, revokedAt time.Time,
) error {
	r.session.RevokedAt = &revokedAt
	return nil
}

func (r *refreshSessionRepository) UpdateBrowserSessionTokens(
	_ context.Context, digest, expectedRefresh, refresh, access string,
) (bool, error) {
	if r.session.CredentialDigest != digest ||
		r.session.RefreshTokenCiphertext != expectedRefresh ||
		r.session.RevokedAt != nil {
		return false, nil
	}
	r.session.RefreshTokenCiphertext = refresh
	r.session.AccessTokenCiphertext = access
	return true, nil
}

func (r *refreshSessionRepository) RevokeBrowserSessionIfRefreshToken(
	_ context.Context, digest, expectedRefresh string, revokedAt time.Time,
) (bool, error) {
	if r.session.CredentialDigest != digest ||
		r.session.RefreshTokenCiphertext != expectedRefresh ||
		r.session.RevokedAt != nil {
		return false, nil
	}
	r.session.RevokedAt = &revokedAt
	return true, nil
}

func (*refreshSessionRepository) CreateOAuthLoginAttempt(
	context.Context, domain.OAuthLoginAttempt,
) error {
	return nil
}

func (*refreshSessionRepository) ConsumeOAuthLoginAttempt(
	context.Context, string, time.Time,
) (domain.OAuthLoginAttempt, error) {
	return domain.OAuthLoginAttempt{}, domain.ErrSessionInvalid
}

func accessJWT(expiresAt time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d}`, expiresAt.Unix())),
	)
	return "header." + payload + ".signature"
}
