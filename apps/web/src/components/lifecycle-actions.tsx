'use client';

import { KubeQueueClient, type Job, type JobAction } from '@kubequeue/api-client';
import Link from 'next/link';
import { useEffect, useRef, useState } from 'react';

const client = new KubeQueueClient();
const convergenceTimeoutMs = 15_000;

type LifecycleActionsProps = {
  job: Job;
  onUpdated: (job: Job, action: JobAction) => void;
  onError: (message: string) => void;
  compact?: boolean;
};

export function LifecycleActions({
  job,
  onUpdated,
  onError,
  compact = false,
}: LifecycleActionsProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [submitting, setSubmitting] = useState<JobAction>();
  const [timedOut, setTimedOut] = useState(false);
  const pending = job.actionPending || submitting !== undefined;

  useEffect(() => {
    if (!job.actionPending) return;
    const timeout = window.setTimeout(() => setTimedOut(true), convergenceTimeoutMs);
    return () => window.clearTimeout(timeout);
  }, [job.actionPending, job.updatedAt]);

  async function run(action: JobAction) {
    setSubmitting(action);
    setTimedOut(false);
    try {
      const updated = await client.command(job.id, action);
      onUpdated(updated, action);
    } catch (reason) {
      onError(reason instanceof Error ? reason.message : 'Action failed');
    } finally {
      setSubmitting(undefined);
    }
  }

  const pendingLabel = getPendingLabel(job, submitting);
  const canManage = job.managementMode === 'MANAGED';

  return (
    <div className={compact ? 'actions' : 'action-bar'}>
      {pendingLabel ? <span className="pending-label">{pendingLabel}</span> : null}
      {job.observedState === 'PAUSED' ? (
        <button
          className={compact ? undefined : 'button primary'}
          disabled={!canManage || pending}
          onClick={() => void run('resume')}
        >
          Resume
        </button>
      ) : null}
      {job.observedState === 'RUNNING' || job.desiredState === 'QUEUED' ? (
        <button
          className={compact ? undefined : 'button ghost'}
          disabled={!canManage || pending}
          onClick={() => void run('pause')}
        >
          Pause
        </button>
      ) : null}
      {job.observedState === 'FAILED' || job.desiredState === 'CANCELLED' ? (
        <button
          className={compact ? undefined : 'button ghost'}
          disabled={!canManage || pending}
          onClick={() => void run('retry')}
        >
          Retry
        </button>
      ) : null}
      {!['COMPLETED', 'CANCELLED'].includes(job.observedState) ? (
        <button
          className={compact ? 'danger' : 'button danger-button'}
          disabled={!canManage || pending}
          onClick={() => dialog.current?.showModal()}
        >
          Terminate
        </button>
      ) : null}
      {!canManage ? <span className="action-note">Observed jobs are read-only</span> : null}
      {timedOut && job.actionPending ? (
        <span className="action-note" role="status">
          Reconciliation is taking longer than expected. <Link href="/settings">View status</Link>.
        </span>
      ) : null}
      <dialog className="confirm-dialog" ref={dialog} aria-labelledby={`terminate-${job.id}`}>
        <h2 id={`terminate-${job.id}`}>Terminate {job.name}?</h2>
        <p>This deletes the Kubernetes Job. KubeQueue keeps its history.</p>
        <form method="dialog">
          <button className="button ghost" value="cancel">
            Keep job
          </button>
          <button
            className="button danger-button"
            value="confirm"
            onClick={() => void run('terminate')}
          >
            Terminate job
          </button>
        </form>
      </dialog>
    </div>
  );
}

export function pendingLifecycleLabel(job: Job) {
  return getPendingLabel(job);
}

function getPendingLabel(job: Job, submitting?: JobAction) {
  if (!job.actionPending && submitting === undefined) return '';
  const action = submitting;
  if (action === 'terminate' || job.desiredState === 'CANCELLED') return 'Terminating';
  if (action === 'resume' || (job.desiredState === 'QUEUED' && job.observedState === 'PAUSED')) {
    return 'Resuming';
  }
  if (action === 'pause' || job.desiredState === 'PAUSED') return 'Pausing';
  return 'Updating';
}
