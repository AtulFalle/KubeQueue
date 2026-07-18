# ADR 0008: Migration ownership and compatibility

- Status: Accepted
- Date: 2026-07-19

## Context

Phase 1 opens the persistence store and runs migrations from API, worker, and migration-hook
processes. Helm also runs a pre-install and pre-upgrade migration Job. This creates multiple
migration entry points, makes the hook an adopted workload when the release namespace is watched,
and does not provide migration checksums or dirty-state detection.

A pre-upgrade migration executes while old application Pods are still running. Safe upgrades
therefore require the expanded schema to remain compatible with the previous release until the new
workloads have rolled out.

## Decision

Use a dedicated migration command as the sole schema writer in production. The Helm pre-install and
pre-upgrade Job invokes that command. Normal API and worker startup verifies that the database
schema is within its supported compatibility range but does not apply migrations.

Migration execution:

- uses ordered immutable migration files;
- records a checksum for every applied migration;
- detects and reports dirty or changed migration state;
- retains PostgreSQL advisory locking and transactional application where supported;
- uses bounded database connection and active deadlines; and
- emits migration identifiers and sanitized failure categories.

Every rolling-upgrade migration follows expand/contract:

1. an expand migration remains compatible with the previous release;
2. new binaries roll out and begin using the expanded schema; and
3. destructive contraction occurs only in a later release after old binaries are unsupported.

The migration Job is explicitly ignored by workload discovery. Failed hooks are retained for
diagnosis and successful hooks are deleted. PostgreSQL remains external; backup and restore remain
operator responsibilities.

## Consequences

- Schema ownership is deterministic and startup races are removed.
- API and worker can fail readiness with a clear incompatible-schema error.
- Releases must test upgrade from the previous published chart and database schema.
- Rollback of application Pods is supported only when the migration satisfies the declared
  compatibility window; Helm cannot reverse database changes.
- Existing migration history requires a one-time baseline and checksum migration.
- A migration runner or parser capable of executing complete migration files is required; naive
  semicolon splitting is no longer sufficient.
- Release documentation must continue requiring a database backup until restore and compatibility
  guarantees are fully automated.
