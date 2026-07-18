package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestRetryCreatesLinkedAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-application-retry?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs := application.NewJobs(store)
	original, err := jobs.Create(ctx, domain.CreateJob{
		Name: "report", Namespace: "default", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetObserved(ctx, original.ID, domain.StateFailed, "uid"); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Command(ctx, original.ID, "pause"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("pause failed job error = %v, want invalid transition", err)
	}

	retry, err := jobs.Command(ctx, original.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if retry.ParentID != original.ID || retry.Attempt != 2 {
		t.Fatalf("retry lineage = parent %q attempt %d", retry.ParentID, retry.Attempt)
	}
	repeated, err := jobs.Command(ctx, original.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID != retry.ID {
		t.Fatalf("repeated retry created %q, want existing attempt %q", repeated.ID, retry.ID)
	}
}
