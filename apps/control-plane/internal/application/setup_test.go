package application

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestSetupReadinessAllowsClaimOnlyWhenEveryCheckPasses(t *testing.T) {
	healthy := domain.SetupStatus{
		API:                 domain.SetupReadiness{Ready: true},
		Database:            domain.SetupReadiness{Ready: true},
		Schema:              domain.SetupReadiness{Ready: true},
		Worker:              domain.SetupReadiness{Ready: true},
		KubernetesAuthority: domain.SetupReadiness{Ready: true},
		Release:             domain.SetupReadiness{Ready: true},
		PublicURL:           domain.SetupReadiness{Ready: true},
	}
	if !setupReadinessAllowsClaim(healthy) {
		t.Fatal("healthy readiness rejected setup claim")
	}

	tests := []struct {
		name   string
		mutate func(*domain.SetupStatus)
	}{
		{name: "API", mutate: func(status *domain.SetupStatus) { status.API.Ready = false }},
		{name: "database", mutate: func(status *domain.SetupStatus) { status.Database.Ready = false }},
		{name: "schema", mutate: func(status *domain.SetupStatus) { status.Schema.Ready = false }},
		{name: "worker", mutate: func(status *domain.SetupStatus) { status.Worker.Ready = false }},
		{name: "Kubernetes authority", mutate: func(status *domain.SetupStatus) {
			status.KubernetesAuthority.Ready = false
		}},
		{name: "release", mutate: func(status *domain.SetupStatus) { status.Release.Ready = false }},
		{name: "public URL", mutate: func(status *domain.SetupStatus) { status.PublicURL.Ready = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := healthy
			test.mutate(&status)
			if setupReadinessAllowsClaim(status) {
				t.Fatal("unhealthy readiness allowed setup claim")
			}
		})
	}
}

func TestSetupClaimsLocalOwnerOnlyAfterObservedPreflight(t *testing.T) {
	store, err := persistence.Open(
		t.Context(),
		"file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "setup.db"))+"?_pragma=busy_timeout(5000)",
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
	setup, err := NewSetup(store, "https://queue.example.com")
	if err != nil {
		t.Fatal(err)
	}
	setup.WithLocalPasswordHasher(func(password string) (string, error) {
		if password != "correct horse battery staple" {
			t.Fatal("password hasher received an unexpected value")
		}
		return "$argon2id$test", nil
	})
	status := setup.Status(t.Context())
	if !status.Available || status.State != "AVAILABLE" {
		t.Fatalf("unclaimed setup status = %#v", status)
	}
	if !status.API.Ready || !status.Database.Ready || !status.Schema.Ready || !status.PublicURL.Ready {
		t.Fatalf("observable preflight = %#v", status)
	}
	if status.Worker.Ready || status.KubernetesAuthority.Ready || status.Release.Ready {
		t.Fatalf("unobserved readiness was reported as ready: %#v", status)
	}

	input := domain.SetupClaimInput{
		InstallationName: "Example", ProjectName: "Platform", Namespaces: []string{"default"},
		LocalAdmin: domain.SetupLocalAdmin{
			Username: "Admin", Password: "correct horse battery staple",
		},
		Policy: domain.SetupPolicy{
			GlobalConcurrency: 4, NamespaceConcurrency: 2, QueueCapacity: 100,
			MinimumPriority: -10, MaximumPriority: 10, MaximumDelaySeconds: 3600,
			MaximumRunningJobs: 4, MaximumQueuedJobs: 100,
		},
	}
	t.Run("unhealthy readiness rejects claim", func(t *testing.T) {
		if _, err := setup.Claim(t.Context(), input); !errors.Is(err, domain.ErrSetupUnavailable) {
			t.Fatalf("Claim() error = %v, want ErrSetupUnavailable", err)
		}
	})

	now := time.Now().UTC()
	if err := store.UpdateWorkerStatus(t.Context(), domain.WorkerStatus{
		State:       domain.WorkerStateReady,
		HeartbeatAt: &now,
		Namespaces: []domain.NamespaceAuthorityStatus{{
			Namespace: "default", InformerSynced: true, Authorized: true, ObservedAt: &now,
		}},
		ReleaseVersion: "test",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("healthy readiness allows claim", func(t *testing.T) {
		claim, err := setup.Claim(t.Context(), input)
		if err != nil {
			t.Fatalf("Claim() with healthy readiness error = %v", err)
		}
		if claim.Status != "COMPLETED" || claim.Username != "Admin" {
			t.Fatalf("Claim() = %#v", claim)
		}
	})
}
