package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestAdministrativeMutationRollsBackWhenAuditAppendFails(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-transaction-rollback")
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	event := newPersistenceAuditEvent(
		t,
		"event-transaction-conflict",
		"projects.create",
		time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC),
	)
	if err := store.AppendAuditEvent(
		t.Context(), event, policy, audit.NoLegalHold(),
	); err != nil {
		t.Fatal(err)
	}
	project, err := domain.NewManagedProject(
		"rollback_project", "default", "Rollback Project", time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ports.WithTransactionalAudit(t.Context(), ports.TransactionalAudit{
		Event: event, Policy: policy, Hold: audit.NoLegalHold(),
	})
	if _, err := store.CreateProject(ctx, project); !errors.Is(err, ports.ErrAuditEventExists) {
		t.Fatalf("create error = %v, want duplicate audit event", err)
	}
	if _, err := store.Project(
		t.Context(), "default", "rollback_project",
	); !errors.Is(err, domain.ErrAccessResourceNotFound) {
		t.Fatalf("rolled-back project error = %v, want not found", err)
	}
}

func TestAdministrativeAuditRollsBackWhenMutationFails(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-transaction-business-failure")
	project, err := domain.NewManagedProject(
		"duplicate_project", "default", "Existing Project", time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	event := newPersistenceAuditEvent(
		t,
		"event-business-failure",
		"projects.create",
		time.Date(2026, time.July, 19, 14, 15, 0, 0, time.UTC),
	)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	ctx := ports.WithTransactionalAudit(t.Context(), ports.TransactionalAudit{
		Event: event, Policy: policy, Hold: audit.NoLegalHold(),
	})
	if _, err := store.CreateProject(ctx, project); err == nil {
		t.Fatal("duplicate project creation succeeded")
	}
	if _, err := store.GetAuditEvent(
		t.Context(), event.Scope().InstallationID(), event.ID(),
	); !errors.Is(err, ports.ErrAuditEventNotFound) {
		t.Fatalf("rolled-back audit error = %v, want not found", err)
	}
}

func TestAdministrativeMutationCommitsAttributedAuditEvent(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-transaction-success")
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	event := newPersistenceAuditEvent(
		t,
		"event-transaction-success",
		"projects.create",
		time.Date(2026, time.July, 19, 14, 30, 0, 0, time.UTC),
	)
	project, err := domain.NewManagedProject(
		"committed_project", "default", "Committed Project", time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ports.WithTransactionalAudit(t.Context(), ports.TransactionalAudit{
		Event: event, Policy: policy, Hold: audit.NoLegalHold(),
	})
	if _, err := store.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetAuditEvent(t.Context(), event.Scope().InstallationID(), event.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Actor().PrincipalID().String() != "legacy_admin" ||
		stored.Action().String() != "projects.create" ||
		stored.Result() != audit.ResultSuccess {
		t.Fatalf("stored audit attribution = %#v", stored)
	}
}
