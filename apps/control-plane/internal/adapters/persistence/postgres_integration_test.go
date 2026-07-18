package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestPostgresCreateAndClaimIntegration(t *testing.T) {
	databaseURL := os.Getenv("KUBEQUEUE_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("KUBEQUEUE_TEST_POSTGRES_URL is not configured")
	}
	ctx := t.Context()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	resetIntegrationDatabase(t, store)
	name := "integration-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	job, err := store.Create(ctx, domain.CreateJob{
		Name: name, Namespace: "default", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM jobs WHERE id=$1`, job.ID)
	})

	claimed, err := store.ClaimEligible(ctx, "integration-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("ClaimEligible() = %#v", claimed)
	}
}

func TestPostgresConcurrentCreateAssignsUniquePositions(t *testing.T) {
	databaseURL := os.Getenv("KUBEQUEUE_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("KUBEQUEUE_TEST_POSTGRES_URL is not configured")
	}
	ctx := t.Context()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	resetIntegrationDatabase(t, store)
	const count = 8
	prefix := "concurrent-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	positions := make(chan int64, count)
	ids := make(chan string, count)
	errors := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			job, err := store.Create(ctx, domain.CreateJob{
				Name:      fmt.Sprintf("%s-%d", prefix, index),
				Namespace: "default",
				Template: json.RawMessage(`{
					"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
				}`),
			})
			if err != nil {
				errors <- err
				return
			}
			positions <- job.Position
			ids <- job.ID
		}()
	}
	wait.Wait()
	close(errors)
	close(positions)
	close(ids)
	for err := range errors {
		t.Error(err)
	}
	seen := make(map[int64]struct{}, count)
	for position := range positions {
		if _, duplicate := seen[position]; duplicate {
			t.Errorf("duplicate queue position %d", position)
		}
		seen[position] = struct{}{}
	}
	for id := range ids {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM jobs WHERE id=$1`, id)
	}
	if len(seen) != count {
		t.Fatalf("created %d unique positions, want %d", len(seen), count)
	}
}

func resetIntegrationDatabase(t *testing.T, store *Store) {
	t.Helper()
	var databaseName string
	if err := store.db.QueryRowContext(t.Context(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read integration database name: %v", err)
	}
	if !strings.Contains(strings.ToLower(databaseName), "test") {
		t.Fatalf("refusing to reset non-test database %q", databaseName)
	}
	if _, err := store.db.ExecContext(
		t.Context(),
		`TRUNCATE TABLE job_events, scheduler_claims, jobs RESTART IDENTITY CASCADE`,
	); err != nil {
		t.Fatalf("reset integration database: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM scheduler_lease`); err != nil {
		t.Fatalf("reset scheduler lease: %v", err)
	}
}
