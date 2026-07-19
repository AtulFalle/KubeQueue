package application

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestLocalAccountLoginThrottlePasswordChangeAndOwnerReset(t *testing.T) {
	store, err := persistence.Open(
		t.Context(),
		"file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "local-auth.db"))+"?_pragma=busy_timeout(5000)",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessions(store, SessionConfig{
		DigestKey:        []byte("01234567890123456789012345678901"),
		EncryptionKey:    []byte("01234567890123456789012345678901"),
		IdleLifetime:     30 * time.Minute,
		AbsoluteLifetime: 12 * time.Hour,
		LastUsedInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := NewLocalAccounts(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	initialPassword := "correct horse battery staple"
	hash, err := accounts.HashPassword(initialPassword)
	if err != nil {
		t.Fatal(err)
	}
	input := localSetupInput(hash)
	if _, err := store.ClaimSetup(t.Context(), input, "local-auth-test"); err != nil {
		t.Fatal(err)
	}

	for range localLoginMaxFailures {
		_, err := accounts.Login(t.Context(), LocalLoginInput{
			Username: "missing", Password: "wrong", ClientKey: "client-a",
		})
		if !errors.Is(err, domain.ErrLocalAuthenticationFailed) {
			t.Fatalf("failed Login() error = %v", err)
		}
	}
	if _, err := accounts.Login(t.Context(), LocalLoginInput{
		Username: "missing", Password: "wrong", ClientKey: "client-a",
	}); !errors.Is(err, domain.ErrLocalAuthenticationLimited) {
		t.Fatalf("throttled Login() error = %v", err)
	}

	created, err := accounts.Login(t.Context(), LocalLoginInput{
		Username: "ADMIN", Password: initialPassword, ClientKey: "client-b",
	})
	if err != nil {
		t.Fatalf("local Login() error = %v", err)
	}
	actor, err := sessions.Authenticate(t.Context(), created.Credential)
	if err != nil {
		t.Fatal(err)
	}
	newPassword := "a different secure password"
	rotated, err := accounts.ChangePassword(
		WithActor(t.Context(), actor), created.Credential, initialPassword, newPassword,
	)
	if err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, err := sessions.Authenticate(t.Context(), created.Credential); err == nil {
		t.Fatal("password change did not revoke the prior session")
	}
	if _, err := sessions.Authenticate(t.Context(), rotated.Credential); err != nil {
		t.Fatalf("rotated session error = %v", err)
	}

	resetPassword := "owner reset secure password"
	if err := accounts.ResetPassword(
		WithActor(t.Context(), actor), input.LocalAdmin.PrincipalID, resetPassword,
	); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if _, err := sessions.Authenticate(t.Context(), rotated.Credential); err == nil {
		t.Fatal("owner reset did not revoke target sessions")
	}
	if _, err := accounts.Login(t.Context(), LocalLoginInput{
		Username: "admin", Password: resetPassword, ClientKey: "client-c",
	}); err != nil {
		t.Fatalf("login after owner reset error = %v", err)
	}
}

func TestLocalPasswordVerifierRejectsMalformedOrUnboundedHashes(t *testing.T) {
	for _, encoded := range []string{
		"",
		"$argon2id$v=19$m=1048576,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=99,p=2$c2FsdA$aGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
	} {
		if verifyLocalPassword("password", encoded) {
			t.Fatalf("malformed hash verified: %q", encoded)
		}
	}
}

func TestDevelopmentLocalAdminSeedIsIdempotent(t *testing.T) {
	store, err := persistence.Open(
		t.Context(),
		"file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "development-seed.db"))+
			"?_pragma=busy_timeout(5000)",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessions(store, SessionConfig{
		DigestKey:        []byte("01234567890123456789012345678901"),
		EncryptionKey:    []byte("01234567890123456789012345678901"),
		IdleLifetime:     30 * time.Minute,
		AbsoluteLifetime: 12 * time.Hour,
		LastUsedInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := NewLocalAccounts(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SeedDevelopmentLocalAdmin(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := store.LocalAccountByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SeedDevelopmentLocalAdmin(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := store.LocalAccountByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if first.PrincipalID != second.PrincipalID || first.PasswordHash != second.PasswordHash {
		t.Fatal("development seed was not idempotent")
	}
	if _, err := accounts.Login(t.Context(), LocalLoginInput{
		Username: "admin", Password: "admin", ClientKey: "development-test",
	}); err != nil {
		t.Fatalf("seeded login failed: %v", err)
	}
}

func localSetupInput(hash string) domain.SetupClaimInput {
	return domain.SetupClaimInput{
		InstallationName: "Example",
		LocalAdmin: domain.SetupLocalAdmin{
			PrincipalID:  "local_owner",
			Username:     "Admin",
			PasswordHash: hash,
		},
		ProjectName: "Platform",
		Namespaces:  []string{"default"},
		Policy: domain.SetupPolicy{
			GlobalConcurrency:    10,
			NamespaceConcurrency: 2,
			QueueCapacity:        100,
			MinimumPriority:      -100,
			MaximumPriority:      100,
			MaximumDelaySeconds:  3600,
			MaximumRunningJobs:   10,
			MaximumQueuedJobs:    100,
		},
	}
}
