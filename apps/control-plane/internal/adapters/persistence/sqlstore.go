package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	postgres bool
	setupMu  sync.Mutex
}

// Open opens a store and applies migrations. It is intended for local composition and tests.
// Production processes must use OpenCompatible or the dedicated Migrate entry point.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	store, err := open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// OpenCompatible opens a store only when its schema is compatible with this binary.
func OpenCompatible(ctx context.Context, databaseURL string) (*Store, error) {
	store, err := open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.VerifySchema(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func open(ctx context.Context, databaseURL string) (*Store, error) {
	driver := "sqlite"
	dsn := databaseURL
	postgres := strings.HasPrefix(databaseURL, "postgres://") ||
		strings.HasPrefix(databaseURL, "postgresql://")
	if postgres {
		driver = "pgx"
	} else if dsn == "" {
		dsn = "file:kubequeue.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	store := &Store{db: db, postgres: postgres}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) UpdateWorkerStatus(ctx context.Context, status domain.WorkerStatus) error {
	effectiveNamespaces, err := json.Marshal(status.EffectiveNamespaces)
	if err != nil {
		return fmt.Errorf("encode effective namespaces: %w", err)
	}
	excludedNamespaces, err := json.Marshal(status.ExcludedNamespaces)
	if err != nil {
		return fmt.Errorf("encode excluded namespaces: %w", err)
	}
	namespaceStatuses, err := json.Marshal(status.Namespaces)
	if err != nil {
		return fmt.Errorf("encode namespace statuses: %w", err)
	}
	lastSuccess := ""
	if status.LastSuccessfulReconciliationAt != nil {
		lastSuccess = status.LastSuccessfulReconciliationAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx, s.bind(
		`UPDATE worker_status SET state=?, heartbeat_at=?,
			last_successful_reconciliation_at=CASE WHEN ?='' THEN last_successful_reconciliation_at ELSE ? END,
			watch_mode=?, effective_namespaces=?, excluded_namespaces=?, namespace_statuses=?,
			global_concurrency=?, namespace_concurrency=?, release_version=?, active_error=?, updated_at=?
			WHERE id=1`),
		status.State, formatTime(status.HeartbeatAt), lastSuccess, lastSuccess,
		status.WatchMode, string(effectiveNamespaces), string(excludedNamespaces),
		string(namespaceStatuses), status.GlobalConcurrency, status.NamespaceConcurrency,
		status.ReleaseVersion, sanitizeDiagnostic(status.ActiveError),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) WorkerStatus(ctx context.Context) (domain.WorkerStatus, error) {
	var status domain.WorkerStatus
	var heartbeat, lastSuccess, effective, excluded, namespaces string
	err := s.db.QueryRowContext(ctx, `SELECT state,COALESCE(heartbeat_at,''),
		COALESCE(last_successful_reconciliation_at,''),watch_mode,effective_namespaces,
		excluded_namespaces,namespace_statuses,global_concurrency,namespace_concurrency,
		release_version,active_error FROM worker_status WHERE id=1`).Scan(
		&status.State, &heartbeat, &lastSuccess, &status.WatchMode, &effective, &excluded,
		&namespaces, &status.GlobalConcurrency, &status.NamespaceConcurrency,
		&status.ReleaseVersion, &status.ActiveError,
	)
	if err != nil {
		return domain.WorkerStatus{}, err
	}
	status.HeartbeatAt = parseTime(sql.NullString{String: heartbeat, Valid: heartbeat != ""})
	status.LastSuccessfulReconciliationAt = parseTime(
		sql.NullString{String: lastSuccess, Valid: lastSuccess != ""},
	)
	if err := json.Unmarshal([]byte(effective), &status.EffectiveNamespaces); err != nil {
		return domain.WorkerStatus{}, fmt.Errorf("decode effective namespaces: %w", err)
	}
	if err := json.Unmarshal([]byte(excluded), &status.ExcludedNamespaces); err != nil {
		return domain.WorkerStatus{}, fmt.Errorf("decode excluded namespaces: %w", err)
	}
	if err := json.Unmarshal([]byte(namespaces), &status.Namespaces); err != nil {
		return domain.WorkerStatus{}, fmt.Errorf("decode namespace statuses: %w", err)
	}
	return status, nil
}

func (s *Store) Create(ctx context.Context, input domain.CreateJob) (domain.Job, error) {
	if err := input.Validate(); err != nil {
		return domain.Job{}, err
	}
	if input.IdempotencyKey != "" {
		existing, found, err := s.jobByIdempotencyKey(
			ctx, input.CreatorPrincipalID, input.IdempotencyKey,
		)
		if err != nil {
			return domain.Job{}, err
		}
		if found {
			if sameJobIntent(existing, input) {
				return existing, nil
			}
			return domain.Job{}, domain.ErrIdempotencyConflict
		}
	}
	job := newJobIntent(input)
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.resolveJobOwnershipTx(ctx, tx, input, &job); err != nil {
			return err
		}
		return s.insertJobIntentTx(ctx, tx, &job)
	})
	if err != nil && input.ParentID != "" {
		existing, findErr := s.retryForParent(ctx, input.ParentID)
		if findErr == nil {
			return existing, nil
		}
	}
	if err != nil && input.IdempotencyKey != "" {
		existing, found, findErr := s.jobByIdempotencyKey(
			ctx, input.CreatorPrincipalID, input.IdempotencyKey,
		)
		if findErr == nil && found {
			if sameJobIntent(existing, input) {
				return existing, nil
			}
			return domain.Job{}, domain.ErrIdempotencyConflict
		}
	}
	return job, err
}

func (s *Store) jobByIdempotencyKey(
	ctx context.Context,
	creator domain.PrincipalID,
	key string,
) (domain.Job, bool, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+jobColumns+` FROM jobs
		 WHERE creator_principal_id=? AND idempotency_key=?`,
	), creator, key))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, nil
	}
	return job, err == nil, err
}

func newJobIntent(input domain.CreateJob) domain.Job {
	now := time.Now().UTC()
	id := input.ID
	if id == "" {
		id = uuid.NewString()
	}
	job := domain.Job{
		ID: id, ParentID: input.ParentID, Name: input.Name,
		ProjectID: input.ProjectID, NamespaceBindingID: input.NamespaceBindingID,
		CreatorPrincipalID: input.CreatorPrincipalID, SubmissionSource: input.SubmissionSource,
		IdempotencyKey: input.IdempotencyKey,
		Namespace:      input.Namespace, Team: input.Team,
		Priority: input.Priority, DesiredState: domain.StateQueued,
		ObservedState: domain.StateCreated, ManagementMode: domain.ManagementManaged,
		SyncStatus: domain.SyncStatusPending, ActionPending: true,
		PendingAction: string(domain.StateQueued), ScheduledFor: input.ScheduledFor,
		Template: input.Template, Attempt: input.Attempt, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if job.Attempt == 0 {
		job.Attempt = 1
	}
	return job
}

func (s *Store) resolveJobOwnershipTx(
	ctx context.Context,
	tx *sql.Tx,
	input domain.CreateJob,
	job *domain.Job,
) error {
	if input.SubmissionSource == domain.SubmissionSourceAPI {
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT nb.id,nb.project_id
			 FROM namespace_bindings nb
			 JOIN principals p ON p.id=? AND p.installation_id=nb.installation_id
			 WHERE nb.namespace=? AND nb.active=? AND p.disabled_at IS NULL`,
		), input.CreatorPrincipalID, input.Namespace, true).
			Scan(&job.NamespaceBindingID, &job.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrNotFound
		}
		return err
	}
	if input.NamespaceBindingID != "" {
		var projectID domain.ProjectID
		if err := tx.QueryRowContext(ctx, s.bind(
			`SELECT project_id FROM namespace_bindings
			 WHERE id=? AND namespace=? AND active=?`,
		), input.NamespaceBindingID, input.Namespace, true).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ports.ErrNotFound
			}
			return err
		}
		if projectID != input.ProjectID {
			return ports.ErrNotFound
		}
	}
	return nil
}

func (s *Store) insertJobIntentTx(
	ctx context.Context,
	tx *sql.Tx,
	job *domain.Job,
) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`); err != nil {
		return err
	}
	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM jobs`).Scan(&next); err != nil {
		return err
	}
	job.Position = next
	if err := s.insert(ctx, tx, *job); err != nil {
		return err
	}
	if job.ParentID != "" {
		return s.addEvent(ctx, tx, job.ID, "JOB_RETRIED", "Retry attempt queued",
			map[string]any{"parentId": job.ParentID, "attempt": job.Attempt})
	}
	return s.addEvent(ctx, tx, job.ID, "JOB_CREATED", "Job queued", nil)
}

func (s *Store) retryForParent(ctx context.Context, parentID string) (domain.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+jobColumns+` FROM jobs WHERE parent_id=? AND archived_at IS NULL`), parentID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, err
}

func (s *Store) Adopt(ctx context.Context, job domain.Job) (domain.Job, error) {
	if job.ID == "" {
		job.ID = uuid.NewString()
	}
	if job.Version == 0 {
		job.Version = 1
	}
	if job.Attempt == 0 {
		job.Attempt = 1
	}
	if job.ManagementMode == "" {
		job.ManagementMode = domain.ManagementObserved
	}
	if job.SyncStatus == "" {
		job.SyncStatus = domain.SyncStatusSynced
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.resolveDiscoveryOwnership(ctx, tx, &job); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value WHERE key='queue_version'`); err != nil {
			return err
		}
		var existingID string
		err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM jobs WHERE kubernetes_uid = ?`), job.KubernetesUID).
			Scan(&existingID)
		if err == nil {
			job.ID = existingID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var next int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM jobs`).Scan(&next); err != nil {
			return err
		}
		job.Position = next
		if err := s.insert(ctx, tx, job); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, job.ID, "JOB_ADOPTED", "Existing Kubernetes Job adopted", nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, job.ID)
}

func (s *Store) resolveDiscoveryOwnership(
	ctx context.Context, tx *sql.Tx, job *domain.Job,
) error {
	if job.ProjectID != "" && job.NamespaceBindingID != "" {
		return nil
	}
	var projectExists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE id='default'`,
	).Scan(&projectExists); err != nil {
		return err
	}
	// Pre-Phase-3 unit stores have no compatibility identity. Production
	// migration backfill creates it before discovery starts.
	if projectExists == 0 {
		return nil
	}
	binding, err := domain.NewNamespaceBinding(defaultProjectID, job.Namespace)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO namespace_bindings
			(id,installation_id,project_id,namespace,created_at)
		 VALUES(?,?,?,?,?) ON CONFLICT(namespace) DO NOTHING`,
	), binding.ID, defaultInstallationID, binding.ProjectID, binding.Namespace, now); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, s.bind(
		`SELECT id,project_id FROM namespace_bindings WHERE namespace=?`,
	), job.Namespace).Scan(&job.NamespaceBindingID, &job.ProjectID); err != nil {
		return err
	}
	job.CreatorPrincipalID = legacyAdminID
	job.SubmissionSource = domain.SubmissionSourceKubernetesDiscovery
	return nil
}

func (s *Store) insert(ctx context.Context, tx *sql.Tx, job domain.Job) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO jobs
		(id,parent_id,name,namespace,team,priority,position,desired_state,observed_state,scheduled_for,
		 kubernetes_uid,template,attempt,version,created_at,updated_at,management_mode,sync_status,
		 resource_version,last_seen_at,observed_at,observed_reason,observed_message,pending_action,
		 last_error,last_error_code,last_error_remediation,reconcile_retries,next_reconcile_at,archived_at,
		 project_id,namespace_binding_id,creator_principal_id,submission_source,idempotency_key)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		job.ID, job.ParentID, job.Name, job.Namespace, job.Team, job.Priority, job.Position,
		job.DesiredState, job.ObservedState, formatTime(job.ScheduledFor), job.KubernetesUID,
		string(job.Template), job.Attempt, job.Version, job.CreatedAt.Format(time.RFC3339Nano),
		job.UpdatedAt.Format(time.RFC3339Nano), job.ManagementMode, job.SyncStatus,
		job.ResourceVersion, formatTime(job.LastSeenAt), formatTime(job.ObservedAt),
		job.ObservedReason, job.ObservedMessage, job.PendingAction, job.LastError,
		job.LastErrorCode, job.ErrorRemediation, job.ReconcileRetries,
		formatTime(job.NextReconcileAt), formatTime(job.ArchivedAt), nullableID(job.ProjectID),
		nullableID(job.NamespaceBindingID), nullableID(job.CreatorPrincipalID),
		nullableID(job.SubmissionSource), job.IdempotencyKey)
	return err
}

func (s *Store) List(ctx context.Context, filter ports.JobFilter) ([]domain.Job, error) {
	query := `SELECT ` + jobColumns + ` FROM jobs WHERE archived_at IS NULL`
	args := make([]any, 0, 16)
	query, args = appendJobFilters(query, args, filter)
	query += ` ORDER BY priority DESC, position, created_at`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ListPage(ctx context.Context, request ports.JobPageRequest) (ports.JobPage, error) {
	if request.Limit < 1 || request.Limit > 200 {
		return ports.JobPage{}, fmt.Errorf("list jobs page: limit must be between 1 and 200")
	}
	if !request.Sort.Valid() {
		return ports.JobPage{}, fmt.Errorf("list jobs page: unsupported sort %q", request.Sort)
	}
	if request.After != nil && request.After.Sort != request.Sort {
		return ports.JobPage{}, fmt.Errorf("list jobs page: cursor sort does not match request sort")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
		ReadOnly:  true,
	})
	if err != nil {
		return ports.JobPage{}, fmt.Errorf("begin jobs page snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	page := ports.JobPage{Items: make([]domain.Job, 0, request.Limit)}
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM control_plane_metadata WHERE key='queue_version'`,
	).Scan(&page.QueueVersion); err != nil {
		return ports.JobPage{}, fmt.Errorf("read jobs page queue version: %w", err)
	}

	query := `SELECT ` + jobColumns + ` FROM jobs WHERE archived_at IS NULL`
	args := make([]any, 0, 16)
	query, args = appendJobFilters(query, args, request.Filter)
	if request.After != nil {
		var clause string
		clause, args = jobPageAfter(request.Sort, *request.After, args)
		query += clause
	}
	query += jobPageOrder(request.Sort) + ` LIMIT ?`
	args = append(args, request.Limit+1)

	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return ports.JobPage{}, fmt.Errorf("query jobs page: %w", err)
	}
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			_ = rows.Close()
			return ports.JobPage{}, fmt.Errorf("scan jobs page: %w", scanErr)
		}
		page.Items = append(page.Items, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ports.JobPage{}, fmt.Errorf("read jobs page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ports.JobPage{}, fmt.Errorf("close jobs page: %w", err)
	}

	if len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		next := jobPageCursor(request.Sort, page.Items[len(page.Items)-1])
		page.Next = &next
	}
	if err := tx.Commit(); err != nil {
		return ports.JobPage{}, fmt.Errorf("commit jobs page snapshot: %w", err)
	}
	return page, nil
}

func appendJobFilters(query string, args []any, filter ports.JobFilter) (string, []any) {
	if filter.Status != "" {
		query += ` AND (desired_state = ? OR observed_state = ?)`
		args = append(args, filter.Status, filter.Status)
	}
	if filter.Namespace != "" {
		query += ` AND namespace = ?`
		args = append(args, filter.Namespace)
	}
	if filter.Team != "" {
		query += ` AND team = ?`
		args = append(args, filter.Team)
	}
	if filter.Priority != nil {
		query += ` AND priority = ?`
		args = append(args, *filter.Priority)
	}
	if filter.Search != "" {
		query += ` AND LOWER(name) LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(strings.ToLower(filter.Search))+"%")
	}
	if filter.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, filter.ProjectID)
	}
	query, args = appendProjectFilter(query, args, filter.ProjectIDs)
	if filter.SyncStatus != "" {
		query += ` AND sync_status = ?`
		args = append(args, filter.SyncStatus)
	}
	if filter.CreatedAfter != nil {
		query += ` AND created_at >= ?`
		args = append(args, filter.CreatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if filter.CreatedBefore != nil {
		query += ` AND created_at < ?`
		args = append(args, filter.CreatedBefore.UTC().Format(time.RFC3339Nano))
	}
	if filter.UpdatedAfter != nil {
		query += ` AND updated_at >= ?`
		args = append(args, filter.UpdatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if filter.UpdatedBefore != nil {
		query += ` AND updated_at < ?`
		args = append(args, filter.UpdatedBefore.UTC().Format(time.RFC3339Nano))
	}
	return query, args
}

func appendProjectFilter(
	query string, args []any, projectIDs []domain.ProjectID,
) (string, []any) {
	return appendProjectFilterColumn(query, args, "project_id", projectIDs)
}

func appendProjectFilterColumn(
	query string, args []any, column string, projectIDs []domain.ProjectID,
) (string, []any) {
	if projectIDs == nil {
		return query, args
	}
	if len(projectIDs) == 0 {
		return query + ` AND 1=0`, args
	}
	query += ` AND ` + column + ` IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(projectIDs)), ",") + `)`
	for _, projectID := range projectIDs {
		args = append(args, projectID)
	}
	return query, args
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func jobPageAfter(
	sortBy ports.JobSort, cursor ports.JobPageCursor, args []any,
) (string, []any) {
	switch sortBy {
	case ports.JobSortQueue:
		return ` AND (priority < ? OR (priority = ? AND position > ?)
			OR (priority = ? AND position = ? AND created_at > ?)
			OR (priority = ? AND position = ? AND created_at = ? AND id > ?))`,
			append(args, cursor.Priority, cursor.Priority, cursor.Position,
				cursor.Priority, cursor.Position, cursor.Value,
				cursor.Priority, cursor.Position, cursor.Value, cursor.ID)
	case ports.JobSortCreatedAt, ports.JobSortUpdatedAt:
		column := "created_at"
		if sortBy == ports.JobSortUpdatedAt {
			column = "updated_at"
		}
		return fmt.Sprintf(` AND (%s > ? OR (%s = ? AND id > ?))`, column, column),
			append(args, cursor.Value, cursor.Value, cursor.ID)
	case ports.JobSortCreatedAtDesc, ports.JobSortUpdatedAtDesc:
		column := "created_at"
		if sortBy == ports.JobSortUpdatedAtDesc {
			column = "updated_at"
		}
		return fmt.Sprintf(` AND (%s < ? OR (%s = ? AND id > ?))`, column, column),
			append(args, cursor.Value, cursor.Value, cursor.ID)
	case ports.JobSortName:
		return ` AND (LOWER(name) > ? OR (LOWER(name) = ? AND name > ?)
			OR (LOWER(name) = ? AND name = ? AND id > ?))`,
			append(args, cursor.Value, cursor.Value, cursor.Secondary,
				cursor.Value, cursor.Secondary, cursor.ID)
	case ports.JobSortNameDesc:
		return ` AND (LOWER(name) < ? OR (LOWER(name) = ? AND name < ?)
			OR (LOWER(name) = ? AND name = ? AND id > ?))`,
			append(args, cursor.Value, cursor.Value, cursor.Secondary,
				cursor.Value, cursor.Secondary, cursor.ID)
	default:
		panic("validated job sort is unsupported")
	}
}

func jobPageOrder(sortBy ports.JobSort) string {
	switch sortBy {
	case ports.JobSortQueue:
		return ` ORDER BY priority DESC,position,created_at,id`
	case ports.JobSortCreatedAt:
		return ` ORDER BY created_at,id`
	case ports.JobSortCreatedAtDesc:
		return ` ORDER BY created_at DESC,id`
	case ports.JobSortUpdatedAt:
		return ` ORDER BY updated_at,id`
	case ports.JobSortUpdatedAtDesc:
		return ` ORDER BY updated_at DESC,id`
	case ports.JobSortName:
		return ` ORDER BY LOWER(name),name,id`
	case ports.JobSortNameDesc:
		return ` ORDER BY LOWER(name) DESC,name DESC,id`
	default:
		panic("validated job sort is unsupported")
	}
}

func jobPageCursor(sortBy ports.JobSort, job domain.Job) ports.JobPageCursor {
	cursor := ports.JobPageCursor{Sort: sortBy, ID: job.ID}
	switch sortBy {
	case ports.JobSortQueue:
		cursor.Priority = job.Priority
		cursor.Position = job.Position
		cursor.Value = job.CreatedAt.UTC().Format(time.RFC3339Nano)
	case ports.JobSortCreatedAt, ports.JobSortCreatedAtDesc:
		cursor.Value = job.CreatedAt.UTC().Format(time.RFC3339Nano)
	case ports.JobSortUpdatedAt, ports.JobSortUpdatedAtDesc:
		cursor.Value = job.UpdatedAt.UTC().Format(time.RFC3339Nano)
	case ports.JobSortName, ports.JobSortNameDesc:
		cursor.Value = strings.ToLower(job.Name)
		cursor.Secondary = job.Name
	default:
		panic("validated job sort is unsupported")
	}
	return cursor
}

func (s *Store) Facets(ctx context.Context) (domain.JobFacets, error) {
	return s.facetsInProjects(ctx, nil)
}

func (s *Store) FacetsInProjects(
	ctx context.Context, projectIDs []domain.ProjectID,
) (domain.JobFacets, error) {
	return s.facetsInProjects(ctx, projectIDs)
}

func (s *Store) facetsInProjects(
	ctx context.Context, projectIDs []domain.ProjectID,
) (domain.JobFacets, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
		ReadOnly:  true,
	})
	if err != nil {
		return domain.JobFacets{}, fmt.Errorf("begin facets snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	facets := domain.JobFacets{
		ObservedStateCounts: make(map[string]int),
		Namespaces:          make([]string, 0),
		Teams:               make([]string, 0),
	}
	where, args := appendProjectFilter(
		` WHERE archived_at IS NULL`, make([]any, 0, len(projectIDs)), projectIDs,
	)
	if err := tx.QueryRowContext(
		ctx, s.bind(`SELECT COUNT(*) FROM jobs`+where), args...,
	).Scan(&facets.Total); err != nil {
		return domain.JobFacets{}, fmt.Errorf("count jobs for facets: %w", err)
	}

	rows, err := tx.QueryContext(ctx, s.bind(
		`SELECT observed_state,COUNT(*) FROM jobs`+where+
			` GROUP BY observed_state ORDER BY observed_state`,
	), args...)
	if err != nil {
		return domain.JobFacets{}, fmt.Errorf("count observed states for facets: %w", err)
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			_ = rows.Close()
			return domain.JobFacets{}, fmt.Errorf("scan observed state facet: %w", err)
		}
		facets.ObservedStateCounts[state] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.JobFacets{}, fmt.Errorf("read observed state facets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return domain.JobFacets{}, fmt.Errorf("close observed state facets: %w", err)
	}

	if err := scanStringFacet(ctx, tx, s.bind(
		`SELECT DISTINCT namespace FROM jobs`+where+
			` AND namespace<>'' ORDER BY namespace LIMIT 100`,
	), args,
		&facets.Namespaces,
	); err != nil {
		return domain.JobFacets{}, fmt.Errorf("read namespace facets: %w", err)
	}
	if err := scanStringFacet(ctx, tx, s.bind(
		`SELECT DISTINCT team FROM jobs`+where+
			` AND team<>'' ORDER BY team LIMIT 100`,
	), args,
		&facets.Teams,
	); err != nil {
		return domain.JobFacets{}, fmt.Errorf("read team facets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.JobFacets{}, fmt.Errorf("commit facets snapshot: %w", err)
	}
	return facets, nil
}

func (s *Store) Queue(ctx context.Context) ([]domain.Job, int64, error) {
	return s.queueInProjects(ctx, nil)
}

func (s *Store) QueueInProjects(
	ctx context.Context, projectIDs []domain.ProjectID,
) ([]domain.Job, int64, error) {
	return s.queueInProjects(ctx, projectIDs)
}

func (s *Store) queueInProjects(
	ctx context.Context, projectIDs []domain.ProjectID,
) ([]domain.Job, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("begin queue snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version int64
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM control_plane_metadata WHERE key='queue_version'`,
	).Scan(&version); err != nil {
		return nil, 0, fmt.Errorf("read queue version: %w", err)
	}
	query := `SELECT ` + jobColumns + ` FROM jobs
		WHERE archived_at IS NULL AND management_mode='MANAGED' AND desired_state='QUEUED'
		AND observed_state IN ('CREATED','PAUSED')
		AND sync_status NOT IN ('MISSING','STALE','OUT_OF_SCOPE','CONFLICTED')`
	args := make([]any, 0, len(projectIDs))
	query, args = appendProjectFilter(query, args, projectIDs)
	query += ` ORDER BY priority DESC,position,created_at,id`
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query queue: %w", err)
	}
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			_ = rows.Close()
			return nil, 0, fmt.Errorf("scan queued job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, fmt.Errorf("read queue: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close queue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit queue snapshot: %w", err)
	}
	return jobs, version, nil
}

func scanStringFacet(
	ctx context.Context, tx *sql.Tx, query string, args []any, values *[]string,
) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		*values = append(*values, value)
	}
	return rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (domain.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+jobColumns+` FROM jobs WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, err
}

func (s *Store) GetInProjects(
	ctx context.Context, id string, projectIDs []domain.ProjectID,
) (domain.Job, error) {
	query := `SELECT ` + jobColumns + ` FROM jobs WHERE id=?`
	args := []any{id}
	query, args = appendProjectFilter(query, args, projectIDs)
	job, err := scanJob(s.db.QueryRowContext(ctx, s.bind(query), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, err
}

func (s *Store) NamespaceBinding(
	ctx context.Context, namespace string,
) (domain.NamespaceBinding, domain.InstallationID, error) {
	var binding domain.NamespaceBinding
	var installationID domain.InstallationID
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT id,project_id,namespace,installation_id FROM namespace_bindings
		 WHERE namespace=? AND active=?`,
	), namespace, true).Scan(&binding.ID, &binding.ProjectID, &binding.Namespace, &installationID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NamespaceBinding{}, "", ports.ErrNotFound
	}
	return binding, installationID, err
}

func (s *Store) SetDesiredState(ctx context.Context, id string, state domain.State) (domain.Job, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if !domain.CanTransition(current.DesiredState, state) {
		return domain.Job{}, domain.ErrInvalidTransition
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET desired_state=?, sync_status='PENDING', pending_action=?,
			 last_error='', last_error_code='', last_error_remediation='',
			 reconcile_retries=0, next_reconcile_at=NULL,
			 version=version+1, updated_at=? WHERE id=?`),
			state, string(state), now, id)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count == 0 {
			return ports.ErrNotFound
		}
		if current.DesiredState == domain.StateQueued || state == domain.StateQueued {
			if _, err := tx.ExecContext(ctx,
				`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
			); err != nil {
				return err
			}
		}
		if state == domain.StateCancelled {
			if err := s.releaseAnyJobQuotaTx(
				ctx, tx, id, policyquota.ReleaseCancelled,
			); err != nil {
				return err
			}
		}
		return s.addEvent(ctx, tx, id, "DESIRED_STATE_CHANGED",
			"Desired state changed to "+string(state), nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) SetObserved(
	ctx context.Context, id string, observation domain.Observation,
) (domain.Job, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	if observation.ManagementMode == "" {
		observation.ManagementMode = current.ManagementMode
	}
	if observation.SyncStatus == "" {
		observation.SyncStatus = domain.SynchronizationStatus(
			current.DesiredState, observation.State,
		)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET observed_state=?,
			 kubernetes_uid=CASE WHEN ?='' THEN kubernetes_uid ELSE ? END,
			 management_mode=?, sync_status=?, resource_version=?, last_seen_at=?, observed_at=?,
			 observed_reason=?, observed_message=?,
			 pending_action=CASE WHEN ?='SYNCED' THEN '' ELSE pending_action END,
			 last_error='', last_error_code='', last_error_remediation='',
			 reconcile_retries=0, next_reconcile_at=NULL,
			 version=version+1, updated_at=?
			 WHERE id=? AND resource_version=?`),
			observation.State, observation.KubernetesUID, observation.KubernetesUID,
			observation.ManagementMode, observation.SyncStatus, observation.ResourceVersion,
			observation.ObservedAt.Format(time.RFC3339Nano),
			observation.ObservedAt.Format(time.RFC3339Nano),
			sanitizeDiagnostic(observation.Reason), sanitizeDiagnostic(observation.Message),
			observation.SyncStatus, now, id, observation.ExpectedResourceVersion)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		switch observation.State {
		case domain.StateCreated, domain.StateQueued, domain.StateRunning, domain.StatePaused:
			// Non-terminal observations retain their quota reservation.
		case domain.StateCompleted:
			if err := s.releaseAnyJobQuotaTx(
				ctx, tx, id, policyquota.ReleaseCompleted,
			); err != nil {
				return err
			}
		case domain.StateFailed:
			if err := s.releaseAnyJobQuotaTx(
				ctx, tx, id, policyquota.ReleaseFailed,
			); err != nil {
				return err
			}
		case domain.StateCancelled:
			if err := s.releaseAnyJobQuotaTx(
				ctx, tx, id, policyquota.ReleaseCancelled,
			); err != nil {
				return err
			}
		}
		if queueMember(
			current.DesiredState, current.ObservedState, current.ManagementMode, current.SyncStatus,
		) != queueMember(
			current.DesiredState, observation.State,
			observation.ManagementMode, observation.SyncStatus,
		) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
			); err != nil {
				return err
			}
		}
		return s.addEvent(ctx, tx, id, "OBSERVED_STATE_CHANGED",
			"Kubernetes state changed to "+string(observation.State), nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) MarkMissing(
	ctx context.Context, id, expectedUID, expectedResourceVersion string, observedAt time.Time,
) (domain.Job, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET sync_status='MISSING', observed_at=?, version=version+1, updated_at=?
			 WHERE id=? AND kubernetes_uid=? AND resource_version=? AND sync_status<>'MISSING'`),
			observedAt.UTC().Format(time.RFC3339Nano), now, id, expectedUID,
			expectedResourceVersion)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, s.bind(`DELETE FROM scheduler_claims WHERE job_id=?`), id,
		); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, id, "KUBERNETES_JOB_MISSING",
			"Associated Kubernetes Job is missing", nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) MarkOutOfScope(
	ctx context.Context, id, expectedResourceVersion string, observedAt time.Time,
) (domain.Job, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET sync_status='OUT_OF_SCOPE', observed_at=?,
			 pending_action='', version=version+1, updated_at=?
			 WHERE id=? AND resource_version=? AND sync_status<>'OUT_OF_SCOPE'`),
			observedAt.UTC().Format(time.RFC3339Nano), now, id, expectedResourceVersion)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, s.bind(`DELETE FROM scheduler_claims WHERE job_id=?`), id,
		); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, id, "NAMESPACE_OUT_OF_SCOPE",
			"Namespace is outside the effective KubeQueue scope", nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) RecordReconcileError(
	ctx context.Context, id, expectedResourceVersion, code, message, remediation string,
	nextRetry time.Time,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET sync_status='ERROR', last_error=?, reconcile_retries=reconcile_retries+1,
			 last_error_code=?, last_error_remediation=?, next_reconcile_at=?,
			 version=version+1, updated_at=?
			 WHERE id=? AND resource_version=?`),
			sanitizeDiagnostic(message), sanitizeDiagnostic(code), sanitizeDiagnostic(remediation),
			nextRetry.UTC().Format(time.RFC3339Nano),
			time.Now().UTC().Format(time.RFC3339Nano), id, expectedResourceVersion)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		return s.addEvent(ctx, tx, id, "RECONCILIATION_ERROR",
			"Job reconciliation requires attention", nil)
	})
}

func (s *Store) Archive(ctx context.Context, id string, archivedAt time.Time) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET archived_at=?, pending_action='', version=version+1, updated_at=?
			 WHERE id=? AND archived_at IS NULL`),
			archivedAt.UTC().Format(time.RFC3339Nano),
			archivedAt.UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			var exists int
			if err := tx.QueryRowContext(
				ctx, s.bind(`SELECT COUNT(*) FROM jobs WHERE id=?`), id,
			).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ports.ErrNotFound
			}
			return nil
		}
		if err := s.releaseAnyRetainedJobQuotaTx(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx, s.bind(`DELETE FROM scheduler_claims WHERE job_id=?`), id,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
		); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, id, "JOB_ARCHIVED", "Stale Job record archived", nil)
	})
}

func (s *Store) UpdateQueue(
	ctx context.Context, id string, priority int, position int64, version int64,
	scheduledFor *time.Time,
) (domain.Job, error) {
	if priority < -1000 || priority > 1000 {
		return domain.Job{}, fmt.Errorf("priority must be between -1000 and 1000")
	}
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET priority=?, position=?, scheduled_for=?, version=version+1, updated_at=?
			 WHERE id=? AND version=? AND management_mode='MANAGED'
			 AND sync_status<>'OUT_OF_SCOPE'
			 AND archived_at IS NULL AND desired_state='QUEUED'`),
			priority, position, formatTime(scheduledFor),
			time.Now().UTC().Format(time.RFC3339Nano), id, version)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count == 0 {
			return ports.ErrConflict
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, id, "QUEUE_UPDATED",
			"Queue priority, position, or schedule changed", nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Reorder(ctx context.Context, ids []string, expectedVersion int64) (int64, error) {
	var version int64
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if s.postgres {
			if _, err := tx.ExecContext(ctx, `LOCK TABLE jobs IN EXCLUSIVE MODE`); err != nil {
				return err
			}
		}
		lock := ""
		if s.postgres {
			lock = " FOR UPDATE"
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT value FROM control_plane_metadata WHERE key='queue_version'`+lock,
		).Scan(&version); err != nil {
			return err
		}
		if version != expectedVersion {
			return ports.ErrConflict
		}
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, duplicate := seen[id]; duplicate {
				return ports.ErrConflict
			}
			seen[id] = struct{}{}
		}
		query := `SELECT id,priority FROM jobs
			WHERE archived_at IS NULL AND management_mode='MANAGED' AND desired_state='QUEUED'
			AND observed_state IN ('CREATED','PAUSED')
			AND sync_status NOT IN ('MISSING','STALE','OUT_OF_SCOPE','CONFLICTED')
			ORDER BY priority DESC,position,created_at,id` + lock
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		currentIDs := make([]string, 0, len(ids))
		priorities := make(map[string]int, len(ids))
		for rows.Next() {
			var id string
			var priority int
			if err := rows.Scan(&id, &priority); err != nil {
				_ = rows.Close()
				return err
			}
			currentIDs = append(currentIDs, id)
			priorities[id] = priority
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(currentIDs) != len(ids) {
			return ports.ErrConflict
		}
		for _, id := range currentIDs {
			if _, exists := seen[id]; !exists {
				return ports.ErrConflict
			}
		}
		for index := 1; index < len(ids); index++ {
			if priorities[ids[index-1]] < priorities[ids[index]] {
				return ports.ErrConflict
			}
		}
		nextVersion := version + 1
		for position, id := range ids {
			result, err := tx.ExecContext(ctx, s.bind(
				`UPDATE jobs SET position=?, version=version+1, updated_at=?
				 WHERE id=? AND management_mode='MANAGED'
				 AND sync_status NOT IN ('MISSING','STALE','OUT_OF_SCOPE','CONFLICTED')
				 AND archived_at IS NULL AND desired_state='QUEUED'`),
				position+1, time.Now().UTC().Format(time.RFC3339Nano), id)
			if err != nil {
				return err
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 1 {
				return ports.ErrConflict
			}
			if err := s.addEvent(ctx, tx, id, "QUEUE_REORDERED", "Queue order changed", nil); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`); err != nil {
			return err
		}
		version = nextVersion
		return nil
	})
	return version, err
}

func (s *Store) ReorderProject(
	ctx context.Context,
	projectID domain.ProjectID,
	ids []string,
	expectedVersion int64,
) (int64, error) {
	var version int64
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if s.postgres {
			if _, err := tx.ExecContext(ctx, `LOCK TABLE jobs IN EXCLUSIVE MODE`); err != nil {
				return err
			}
		}
		lock := ""
		if s.postgres {
			lock = " FOR UPDATE"
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT value FROM control_plane_metadata WHERE key='queue_version'`+lock,
		).Scan(&version); err != nil {
			return err
		}
		if version != expectedVersion {
			return ports.ErrConflict
		}
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, duplicate := seen[id]; duplicate {
				return ports.ErrConflict
			}
			seen[id] = struct{}{}
		}
		rows, err := tx.QueryContext(ctx, s.bind(
			`SELECT id,priority,position FROM jobs
			 WHERE project_id=? AND archived_at IS NULL
			   AND management_mode='MANAGED' AND desired_state='QUEUED'
			   AND observed_state IN ('CREATED','PAUSED')
			   AND sync_status NOT IN ('MISSING','STALE','OUT_OF_SCOPE','CONFLICTED')
			 ORDER BY priority DESC,position,created_at,id`+lock,
		), projectID)
		if err != nil {
			return err
		}
		currentIDs := make([]string, 0, len(ids))
		priorities := make(map[string]int, len(ids))
		positions := make([]int64, 0, len(ids))
		for rows.Next() {
			var id string
			var priority int
			var position int64
			if err := rows.Scan(&id, &priority, &position); err != nil {
				_ = rows.Close()
				return err
			}
			currentIDs = append(currentIDs, id)
			priorities[id] = priority
			positions = append(positions, position)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(currentIDs) != len(ids) {
			return ports.ErrConflict
		}
		for _, id := range currentIDs {
			if _, exists := seen[id]; !exists {
				return ports.ErrConflict
			}
		}
		for index := 1; index < len(ids); index++ {
			if priorities[ids[index-1]] < priorities[ids[index]] {
				return ports.ErrConflict
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for index, id := range ids {
			result, err := tx.ExecContext(ctx, s.bind(
				`UPDATE jobs SET position=?,version=version+1,updated_at=?
				 WHERE id=? AND project_id=? AND management_mode='MANAGED'
				   AND sync_status NOT IN ('MISSING','STALE','OUT_OF_SCOPE','CONFLICTED')
				   AND archived_at IS NULL AND desired_state='QUEUED'`,
			), positions[index], now, id, projectID)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				return ports.ErrConflict
			}
			if err := s.addEvent(
				ctx, tx, id, "QUEUE_REORDERED", "Project queue order changed", nil,
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`,
		); err != nil {
			return err
		}
		version++
		return nil
	})
	return version, err
}

func (s *Store) QueueVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM control_plane_metadata WHERE key='queue_version'`,
	).Scan(&version)
	return version, err
}

func queueMember(
	desired, observed domain.State, management domain.ManagementMode, syncStatus domain.SyncStatus,
) bool {
	if desired != domain.StateQueued || management != domain.ManagementManaged {
		return false
	}
	if observed != domain.StateCreated && observed != domain.StatePaused {
		return false
	}
	switch syncStatus {
	case domain.SyncStatusMissing, domain.SyncStatusStale, domain.SyncStatusOutOfScope,
		domain.SyncStatusConflicted:
		return false
	case domain.SyncStatusSynced, domain.SyncStatusPending, domain.SyncStatusError:
		return true
	}
	return false
}

func (s *Store) EventsPage(
	ctx context.Context, id string, request ports.EventPageRequest,
) (ports.EventPage, error) {
	if request.Limit < 1 || request.Limit > 200 {
		return ports.EventPage{}, fmt.Errorf("list job events page: limit must be between 1 and 200")
	}
	if request.Before < 0 {
		return ports.EventPage{}, fmt.Errorf("list job events page: cursor must not be negative")
	}
	query := `SELECT id,job_id,type,message,COALESCE(data,''),created_at
		FROM job_events WHERE job_id=?`
	args := []any{id}
	if request.Before > 0 {
		query += ` AND id<?`
		args = append(args, request.Before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return ports.EventPage{}, err
	}
	defer func() { _ = rows.Close() }()
	page := ports.EventPage{Items: make([]domain.Event, 0, request.Limit)}
	for rows.Next() {
		var event domain.Event
		var data, created string
		if err := rows.Scan(&event.ID, &event.JobID, &event.Type, &event.Message, &data, &created); err != nil {
			return ports.EventPage{}, err
		}
		event.Data = json.RawMessage(data)
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		page.Items = append(page.Items, event)
	}
	if err := rows.Err(); err != nil {
		return ports.EventPage{}, err
	}
	if len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		next := page.Items[len(page.Items)-1].ID
		page.Next = &next
	}
	return page, nil
}

func (s *Store) LatestJobChangeCursor(
	ctx context.Context, projectIDs []domain.ProjectID,
) (int64, error) {
	query := `SELECT COALESCE(MAX(e.id),0) FROM job_events e
		JOIN jobs j ON j.id=e.job_id WHERE 1=1`
	args := make([]any, 0, len(projectIDs))
	query, args = appendProjectFilterColumn(query, args, "j.project_id", projectIDs)
	var cursor int64
	if err := s.db.QueryRowContext(ctx, s.bind(query), args...).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read latest job change cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) JobChanges(
	ctx context.Context, projectIDs []domain.ProjectID, after int64, limit int,
) (ports.JobChangePage, error) {
	if after < 0 {
		return ports.JobChangePage{}, fmt.Errorf("list job changes: cursor must not be negative")
	}
	if limit < 1 || limit > 200 {
		return ports.JobChangePage{}, fmt.Errorf("list job changes: limit must be between 1 and 200")
	}
	query := `SELECT e.id,e.job_id FROM job_events e
		JOIN jobs j ON j.id=e.job_id WHERE e.id>?`
	args := []any{after}
	query, args = appendProjectFilterColumn(query, args, "j.project_id", projectIDs)
	query += ` ORDER BY e.id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return ports.JobChangePage{}, fmt.Errorf("query job changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := ports.JobChangePage{
		Changes: make([]ports.JobChange, 0, limit),
		Cursor:  after,
	}
	for rows.Next() {
		var change ports.JobChange
		if err := rows.Scan(&change.Cursor, &change.JobID); err != nil {
			return ports.JobChangePage{}, fmt.Errorf("scan job change: %w", err)
		}
		page.Changes = append(page.Changes, change)
	}
	if err := rows.Err(); err != nil {
		return ports.JobChangePage{}, fmt.Errorf("read job changes: %w", err)
	}
	if len(page.Changes) > limit {
		page.Changes = page.Changes[:limit]
		page.More = true
	}
	if len(page.Changes) > 0 {
		page.Cursor = page.Changes[len(page.Changes)-1].Cursor
	}
	return page, nil
}

func (s *Store) Eligible(ctx context.Context, limit int) ([]domain.Job, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+jobColumns+` FROM jobs
		WHERE desired_state='QUEUED' AND observed_state IN ('CREATED','PAUSED')
		AND management_mode='MANAGED' AND sync_status NOT IN ('MISSING','OUT_OF_SCOPE','CONFLICTED')
		AND archived_at IS NULL AND (next_reconcile_at IS NULL OR next_reconcile_at<=?)
		AND (scheduled_for IS NULL OR scheduled_for='' OR scheduled_for<=?)
		ORDER BY priority DESC, position, created_at LIMIT ?`), now, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) AcquireSchedulerLease(
	ctx context.Context, holder string, ttl time.Duration,
) (bool, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(ttl).Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO scheduler_lease(id,holder,expires_at)
		VALUES(1,?,?)
		ON CONFLICT(id) DO UPDATE SET holder=excluded.holder, expires_at=excluded.expires_at
		WHERE scheduler_lease.expires_at < ? OR scheduler_lease.holder = ?`),
		holder, expiresAt, now.Format(time.RFC3339Nano), holder)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *Store) ClaimEligible(
	ctx context.Context, holder string, limit int, ttl time.Duration,
) ([]domain.Job, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(ttl).Format(time.RFC3339Nano)
	jobs := make([]domain.Job, 0)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM scheduler_claims WHERE expires_at<=?`), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		query := `SELECT ` + prefixedJobColumns("j") + ` FROM jobs j
			LEFT JOIN scheduler_claims c ON c.job_id=j.id
			WHERE j.desired_state='QUEUED' AND j.observed_state IN ('CREATED','PAUSED')
			AND j.management_mode='MANAGED'
			AND j.sync_status NOT IN ('MISSING','OUT_OF_SCOPE','CONFLICTED')
			AND j.archived_at IS NULL
			AND (j.next_reconcile_at IS NULL OR j.next_reconcile_at<=?)
			AND (j.scheduled_for IS NULL OR j.scheduled_for='' OR j.scheduled_for<=?)
			AND c.job_id IS NULL
			ORDER BY j.priority DESC,j.position,j.created_at LIMIT ?`
		if s.postgres {
			query += ` FOR UPDATE OF j SKIP LOCKED`
		}
		rows, err := tx.QueryContext(
			ctx, s.bind(query), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), limit,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, job := range jobs {
			if _, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO scheduler_claims(job_id,holder,expires_at) VALUES(?,?,?)`),
				job.ID, holder, expiresAt); err != nil {
				return err
			}
		}
		return nil
	})
	return jobs, err
}

func (s *Store) ReleaseSchedulerClaim(ctx context.Context, jobID, holder string) error {
	_, err := s.db.ExecContext(ctx, s.bind(
		`DELETE FROM scheduler_claims WHERE job_id=? AND holder=?`), jobID, holder)
	return err
}

func (s *Store) addEvent(
	ctx context.Context, tx *sql.Tx, id, eventType, message string, data any,
) error {
	var encoded []byte
	if data != nil {
		encoded, _ = json.Marshal(data)
	}
	_, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO job_events(job_id,type,message,data,created_at) VALUES(?,?,?,?,?)`),
		id, eventType, message, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	for index := 1; strings.Contains(query, "?"); index++ {
		query = strings.Replace(query, "?", "$"+strconv.Itoa(index), 1)
	}
	return query
}

const jobColumns = `id,parent_id,name,namespace,team,priority,position,desired_state,observed_state,
	scheduled_for,kubernetes_uid,template,attempt,version,created_at,updated_at,management_mode,
	sync_status,resource_version,last_seen_at,observed_at,observed_reason,observed_message,
	pending_action,last_error,last_error_code,last_error_remediation,reconcile_retries,
	next_reconcile_at,archived_at,project_id,namespace_binding_id,creator_principal_id,submission_source,
	idempotency_key`

func prefixedJobColumns(prefix string) string {
	columns := strings.Split(strings.ReplaceAll(jobColumns, "\n", ""), ",")
	for index, column := range columns {
		columns[index] = prefix + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ",")
}

type scanner interface {
	Scan(...any) error
}

func scanJob(row scanner) (domain.Job, error) {
	var job domain.Job
	var desired, observed, management, syncStatus, template, created, updated string
	var scheduled, lastSeen, observedAt, nextReconcile, archived sql.NullString
	var projectID, bindingID, creatorID, submissionSource sql.NullString
	err := row.Scan(
		&job.ID, &job.ParentID, &job.Name, &job.Namespace, &job.Team, &job.Priority, &job.Position,
		&desired, &observed, &scheduled, &job.KubernetesUID, &template, &job.Attempt,
		&job.Version, &created, &updated, &management, &syncStatus, &job.ResourceVersion,
		&lastSeen, &observedAt, &job.ObservedReason, &job.ObservedMessage, &job.PendingAction,
		&job.LastError, &job.LastErrorCode, &job.ErrorRemediation, &job.ReconcileRetries,
		&nextReconcile, &archived, &projectID, &bindingID, &creatorID, &submissionSource,
		&job.IdempotencyKey,
	)
	if err != nil {
		return domain.Job{}, err
	}
	job.DesiredState, job.ObservedState = domain.State(desired), domain.State(observed)
	job.ManagementMode = domain.ManagementMode(management)
	job.SyncStatus = domain.SyncStatus(syncStatus)
	job.ActionPending = job.PendingAction != ""
	job.Template = json.RawMessage(template)
	job.ScheduledFor = parseTime(scheduled)
	job.LastSeenAt = parseTime(lastSeen)
	job.ObservedAt = parseTime(observedAt)
	job.NextReconcileAt = parseTime(nextReconcile)
	job.ArchivedAt = parseTime(archived)
	job.ProjectID = domain.ProjectID(projectID.String)
	job.NamespaceBindingID = domain.NamespaceBindingID(bindingID.String)
	job.CreatorPrincipalID = domain.PrincipalID(creatorID.String)
	job.SubmissionSource = domain.SubmissionSource(submissionSource.String)
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return job, nil
}

func parseTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func formatTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableID[T ~string](value T) any {
	if value == "" {
		return nil
	}
	return string(value)
}

func sanitizeDiagnostic(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxLength = 1024
	if len(value) > maxLength {
		return value[:maxLength]
	}
	return value
}
