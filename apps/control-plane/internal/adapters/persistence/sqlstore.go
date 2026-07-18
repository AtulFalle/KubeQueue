package persistence

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db       *sql.DB
	postgres bool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
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
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	eventsID := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.postgres {
		eventsID = "BIGSERIAL PRIMARY KEY"
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if s.postgres {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('kubequeue_migrations'))`); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("lock migrations: %w", err)
			}
		}
		var applied int
		if err := tx.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM schema_migrations WHERE version=?`), name).Scan(&applied); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit migration check %s: %w", name, err)
			}
			continue
		}
		migration, err := migrationFiles.ReadFile(name)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		statement := strings.ReplaceAll(string(migration), "{{EVENTS_ID}}", eventsID)
		for _, command := range strings.Split(statement, ";") {
			if strings.TrimSpace(command) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, command); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`),
			name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Create(ctx context.Context, input domain.CreateJob) (domain.Job, error) {
	if err := input.Validate(); err != nil {
		return domain.Job{}, err
	}
	now := time.Now().UTC()
	job := domain.Job{
		ID: uuid.NewString(), ParentID: input.ParentID, Name: input.Name,
		Namespace: input.Namespace, Team: input.Team,
		Priority: input.Priority, DesiredState: domain.StateQueued,
		ObservedState: domain.StateCreated, ManagementMode: domain.ManagementManaged,
		SyncStatus: domain.SyncStatusPending, ActionPending: true,
		PendingAction: string(domain.StateQueued), ScheduledFor: input.ScheduledFor,
		Template: input.Template, Attempt: input.Attempt, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if job.Attempt == 0 {
		job.Attempt = 1
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE control_plane_metadata SET value=value+1 WHERE key='queue_version'`); err != nil {
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
		if job.ParentID != "" {
			return s.addEvent(ctx, tx, job.ID, "JOB_RETRIED", "Retry attempt queued",
				map[string]any{"parentId": job.ParentID, "attempt": job.Attempt})
		}
		return s.addEvent(ctx, tx, job.ID, "JOB_CREATED", "Job queued", nil)
	})
	if err != nil && input.ParentID != "" {
		existing, findErr := s.retryForParent(ctx, input.ParentID)
		if findErr == nil {
			return existing, nil
		}
	}
	return job, err
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

func (s *Store) insert(ctx context.Context, tx *sql.Tx, job domain.Job) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO jobs
		(id,parent_id,name,namespace,team,priority,position,desired_state,observed_state,scheduled_for,
		 kubernetes_uid,template,attempt,version,created_at,updated_at,management_mode,sync_status,
		 resource_version,last_seen_at,observed_at,observed_reason,observed_message,pending_action,
		 last_error,reconcile_retries,next_reconcile_at,archived_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		job.ID, job.ParentID, job.Name, job.Namespace, job.Team, job.Priority, job.Position,
		job.DesiredState, job.ObservedState, formatTime(job.ScheduledFor), job.KubernetesUID,
		string(job.Template), job.Attempt, job.Version, job.CreatedAt.Format(time.RFC3339Nano),
		job.UpdatedAt.Format(time.RFC3339Nano), job.ManagementMode, job.SyncStatus,
		job.ResourceVersion, formatTime(job.LastSeenAt), formatTime(job.ObservedAt),
		job.ObservedReason, job.ObservedMessage, job.PendingAction, job.LastError,
		job.ReconcileRetries, formatTime(job.NextReconcileAt), formatTime(job.ArchivedAt))
	return err
}

func (s *Store) List(ctx context.Context, filter ports.JobFilter) ([]domain.Job, error) {
	query := `SELECT ` + jobColumns + ` FROM jobs WHERE archived_at IS NULL`
	args := make([]any, 0, 4)
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
		query += ` AND LOWER(name) LIKE ?`
		args = append(args, "%"+strings.ToLower(filter.Search)+"%")
	}
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

func (s *Store) Get(ctx context.Context, id string) (domain.Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+jobColumns+` FROM jobs WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, err
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
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET desired_state=?, sync_status='PENDING', pending_action=?,
			 last_error='', reconcile_retries=0, next_reconcile_at=NULL,
			 version=version+1, updated_at=? WHERE id=?`),
			state, string(state), now, id)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count == 0 {
			return ports.ErrNotFound
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
			 last_error='', reconcile_retries=0, next_reconcile_at=NULL,
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
		return s.addEvent(ctx, tx, id, "KUBERNETES_JOB_MISSING",
			"Associated Kubernetes Job is missing", nil)
	})
	if err != nil {
		return domain.Job{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) RecordReconcileError(
	ctx context.Context, id, expectedResourceVersion, message string, nextRetry time.Time,
) error {
	result, err := s.db.ExecContext(ctx, s.bind(
		`UPDATE jobs SET sync_status='ERROR', last_error=?, reconcile_retries=reconcile_retries+1,
		 next_reconcile_at=?, version=version+1, updated_at=?
		 WHERE id=? AND resource_version=?`),
		sanitizeDiagnostic(message), nextRetry.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano), id, expectedResourceVersion)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count == 0 {
		if err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (s *Store) Archive(ctx context.Context, id string, archivedAt time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
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
		if _, err := tx.ExecContext(
			ctx, s.bind(`DELETE FROM scheduler_claims WHERE job_id=?`), id,
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
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET priority=?, position=?, scheduled_for=?, version=version+1, updated_at=?
			 WHERE id=? AND version=? AND management_mode='MANAGED'
			 AND archived_at IS NULL AND desired_state IN ('QUEUED','PAUSED')`),
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
	err := s.transaction(ctx, func(tx *sql.Tx) error {
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
		nextVersion := version + 1
		for position, id := range ids {
			result, err := tx.ExecContext(ctx, s.bind(
				`UPDATE jobs SET position=?, version=version+1, updated_at=?
				 WHERE id=? AND management_mode='MANAGED'
				 AND archived_at IS NULL AND desired_state IN ('QUEUED','PAUSED')`),
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

func (s *Store) QueueVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM control_plane_metadata WHERE key='queue_version'`,
	).Scan(&version)
	return version, err
}

func (s *Store) Events(ctx context.Context, id string) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT id,job_id,type,message,COALESCE(data,''),created_at
		 FROM job_events WHERE job_id=? ORDER BY id DESC`), id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]domain.Event, 0)
	for rows.Next() {
		var event domain.Event
		var data, created string
		if err := rows.Scan(&event.ID, &event.JobID, &event.Type, &event.Message, &data, &created); err != nil {
			return nil, err
		}
		event.Data = json.RawMessage(data)
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		events = append(events, event)
	}
	return events, rows.Err()
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
	pending_action,last_error,reconcile_retries,next_reconcile_at,archived_at`

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
	err := row.Scan(
		&job.ID, &job.ParentID, &job.Name, &job.Namespace, &job.Team, &job.Priority, &job.Position,
		&desired, &observed, &scheduled, &job.KubernetesUID, &template, &job.Attempt,
		&job.Version, &created, &updated, &management, &syncStatus, &job.ResourceVersion,
		&lastSeen, &observedAt, &job.ObservedReason, &job.ObservedMessage, &job.PendingAction,
		&job.LastError, &job.ReconcileRetries, &nextReconcile, &archived,
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

func sanitizeDiagnostic(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxLength = 1024
	if len(value) > maxLength {
		return value[:maxLength]
	}
	return value
}
