package persistence

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestBrowserSessionRotationRevocationAndDisabledPrincipal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := Open(ctx, "file:test-browser-sessions?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO installations(id,name,created_at) VALUES('default','Default',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
		 VALUES('person','default','HUMAN','Person',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO identity_providers(
		 id,installation_id,issuer,audience,allowed_algorithms,created_at,updated_at
		 ) VALUES('corporate','default','https://issuer.example','kubequeue','["RS256"]',?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	sessions, err := application.NewSessions(store, application.SessionConfig{
		DigestKey: make([]byte, 32), EncryptionKey: make([]byte, 32),
		IdleLifetime: time.Hour, AbsoluteLifetime: 8 * time.Hour,
		LastUsedInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := sessions.Create(ctx, application.CreateSessionInput{
		Actor:                domain.Actor{PrincipalID: "person", InstallationID: "default"},
		AuthenticationMethod: "OIDC", AccessToken: "access-secret",
		IdentityProviderID: "corporate", RefreshToken: "refresh-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedDigest, storedToken string
	if err := store.db.QueryRowContext(ctx,
		`SELECT credential_digest,refresh_token_ciphertext FROM browser_sessions WHERE id=?`,
		first.Session.ID).Scan(&storedDigest, &storedToken); err != nil {
		t.Fatal(err)
	}
	if storedDigest == first.Credential || storedToken == "refresh-secret" {
		t.Fatal("raw session or refresh credential was stored")
	}
	second, err := sessions.Create(ctx, application.CreateSessionInput{
		Actor:                domain.Actor{PrincipalID: "person", InstallationID: "default"},
		AuthenticationMethod: "OIDC", AccessToken: "replacement-access",
		IdentityProviderID: "corporate", RotateCredential: first.Credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(ctx, first.Credential); err == nil {
		t.Fatal("rotated credential remained valid")
	}
	if _, err := sessions.Authenticate(ctx, second.Credential); err != nil {
		t.Fatalf("replacement credential rejected: %v", err)
	}
	login, err := sessions.StartOAuthLogin(ctx, "/queue")
	if err != nil {
		t.Fatal(err)
	}
	if login.State == "" || login.Nonce == "" || len(login.PKCEVerifier) < 43 {
		t.Fatal("OAuth login did not create strong state, nonce, and PKCE values")
	}
	var nonceDigest, nonceCiphertext, verifierCiphertext string
	if err := store.db.QueryRowContext(ctx,
		`SELECT nonce_digest,nonce_ciphertext,pkce_verifier_ciphertext
		 FROM oauth_login_attempts`).Scan(
		&nonceDigest, &nonceCiphertext, &verifierCiphertext,
	); err != nil {
		t.Fatal(err)
	}
	if nonceDigest == login.Nonce || strings.Contains(nonceCiphertext, login.Nonce) ||
		strings.Contains(verifierCiphertext, login.PKCEVerifier) {
		t.Fatal("OAuth nonce or PKCE verifier was persisted in plaintext")
	}
	consumed, err := sessions.ConsumeOAuthLogin(ctx, login.State)
	if err != nil || consumed.Nonce != login.Nonce || consumed.PKCEVerifier != login.PKCEVerifier {
		t.Fatalf("consume OAuth login = %#v, %v", consumed, err)
	}
	if _, err := sessions.ConsumeOAuthLogin(ctx, login.State); err == nil {
		t.Fatal("OAuth state was replayable")
	}
	wrongKeyLogin, err := sessions.StartOAuthLogin(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := make([]byte, 32)
	wrongKey[0] = 1
	wrongKeySessions, err := application.NewSessions(store, application.SessionConfig{
		DigestKey: make([]byte, 32), EncryptionKey: wrongKey,
		IdleLifetime: time.Hour, AbsoluteLifetime: 8 * time.Hour,
		LastUsedInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKeySessions.ConsumeOAuthLogin(
		ctx, wrongKeyLogin.State,
	); !errors.Is(err, domain.ErrOAuthAttemptDecryption) {
		t.Fatalf("wrong-key consume error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE principals SET disabled_at=? WHERE id='person'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Authenticate(ctx, second.Credential); err == nil {
		t.Fatal("disabled principal session remained valid")
	}
}
