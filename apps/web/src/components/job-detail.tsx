'use client';

import {
  KubeQueueClient,
  type Job,
  type JobAction,
  type JobEvent,
  type JobManifest,
} from '@kubequeue/api-client';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useCallback, useEffect, useState } from 'react';

import { Badge, Status } from './dashboard';
import { LifecycleActions, pendingLifecycleLabel } from './lifecycle-actions';

const client = new KubeQueueClient();

export function JobDetail({
  id,
  initialJob,
  initialEvents,
  initialManifest,
  initialLoadFailed,
  initialManifestLoadFailed,
}: {
  id: string;
  initialJob?: Job;
  initialEvents: JobEvent[];
  initialManifest?: JobManifest;
  initialLoadFailed: boolean;
  initialManifestLoadFailed: boolean;
}) {
  const router = useRouter();
  const [job, setJob] = useState<Job | undefined>(initialJob);
  const [events, setEvents] = useState<JobEvent[]>(initialEvents);
  const [manifest, setManifest] = useState<JobManifest | undefined>(initialManifest);
  const [manifestUnavailable, setManifestUnavailable] = useState(initialManifestLoadFailed);
  const [error, setError] = useState(initialLoadFailed ? 'Unable to load Job' : '');

  const refresh = useCallback(async () => {
    try {
      const [nextJob, history] = await Promise.all([client.getJob(id), client.listJobEvents(id)]);
      setJob(nextJob);
      setEvents(history.items);
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load Job');
    }
  }, [id]);

  const refreshManifest = useCallback(async () => {
    try {
      setManifest(await client.getJobManifest(id));
      setManifestUnavailable(false);
    } catch {
      setManifestUnavailable(true);
    }
  }, [id]);

  useEffect(() => {
    if (!initialLoadFailed) return;
    const timeout = window.setTimeout(() => void refresh(), 0);
    return () => window.clearTimeout(timeout);
  }, [initialLoadFailed, refresh]);

  useEffect(() => {
    if (!initialManifestLoadFailed) return;
    const timeout = window.setTimeout(() => void refreshManifest(), 0);
    return () => window.clearTimeout(timeout);
  }, [initialManifestLoadFailed, refreshManifest]);

  useEffect(() => {
    const events = new EventSource(client.eventsUrl());
    events.addEventListener('jobs', () => void refresh());
    return () => events.close();
  }, [refresh]);

  function applyCommand(updated: Job, action: JobAction) {
    if (action === 'retry' && updated.id !== id) {
      router.push(`/jobs/${updated.id}`);
      return;
    }
    setJob(updated);
  }

  if (!job) {
    return (
      <main className="page-shell">
        {error ? <div className="alert">{error}</div> : <p>Loading…</p>}
      </main>
    );
  }

  return (
    <main className="page-shell narrow">
      <Link className="back-link" href="/">
        ← All jobs
      </Link>
      <section className="detail-header">
        <div>
          <p className="eyebrow">
            {job.namespace} / attempt {job.attempt}
          </p>
          <h1>{job.name}</h1>
          <div className="badge-stack">
            <Status state={job.observedState} label={pendingLifecycleLabel(job)} />
            <Badge value={job.managementMode} />
            <Badge value={job.syncStatus} />
          </div>
        </div>
        <LifecycleActions job={job} onUpdated={applyCommand} onError={setError} />
      </section>
      {error ? (
        <div className="alert" role="alert">
          {error}
        </div>
      ) : null}

      <section className="detail-grid">
        <article className="panel facts">
          <h2>Execution</h2>
          <dl>
            <div>
              <dt>Desired state</dt>
              <dd>{job.desiredState}</dd>
            </div>
            <div>
              <dt>Queue position</dt>
              <dd>{job.position}</dd>
            </div>
            <div>
              <dt>Priority</dt>
              <dd>{job.priority}</dd>
            </div>
            <div>
              <dt>Team</dt>
              <dd>{job.team || 'Unassigned'}</dd>
            </div>
            <div>
              <dt>Scheduled</dt>
              <dd>{job.scheduledFor ? formatTimestamp(job.scheduledFor) : 'Immediately'}</dd>
            </div>
            <div>
              <dt>Kubernetes UID</dt>
              <dd className="mono">{job.kubernetesUid || 'Pending admission'}</dd>
            </div>
          </dl>
        </article>
        <article className="panel timeline">
          <h2>History</h2>
          {events.length === 0 ? <p className="empty">No events recorded yet.</p> : null}
          <ol>
            {events.map((event) => (
              <li key={event.id}>
                <i aria-hidden="true" />
                <div>
                  <strong>{event.message}</strong>
                  <span>{event.type}</span>
                </div>
                <time dateTime={event.createdAt}>{formatTimestamp(event.createdAt)}</time>
              </li>
            ))}
          </ol>
        </article>
      </section>
      <section className="panel manifest">
        <h2>Stored Job template</h2>
        {manifest ? (
          <pre>
            <code>{JSON.stringify(manifest.manifest, null, 2)}</code>
          </pre>
        ) : (
          <p className="empty">
            {manifestUnavailable ? 'Manifest access is unavailable.' : 'Loading manifest…'}
          </p>
        )}
      </section>
    </main>
  );
}

function formatTimestamp(value: string) {
  return `${new Intl.DateTimeFormat('en-US', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'UTC',
  }).format(new Date(value))} UTC`;
}
