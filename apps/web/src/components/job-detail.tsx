'use client';

import { KubeQueueClient, type Job, type JobAction, type JobEvent } from '@kubequeue/api-client';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useCallback, useEffect, useState } from 'react';

import { Status } from './dashboard';

const client = new KubeQueueClient();

export function JobDetail({
  id,
  initialJob,
  initialEvents,
  initialLoadFailed,
}: {
  id: string;
  initialJob?: Job;
  initialEvents: JobEvent[];
  initialLoadFailed: boolean;
}) {
  const router = useRouter();
  const [job, setJob] = useState<Job | undefined>(initialJob);
  const [events, setEvents] = useState<JobEvent[]>(initialEvents);
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

  useEffect(() => {
    if (!initialLoadFailed) return;
    const timeout = window.setTimeout(() => void refresh(), 0);
    return () => window.clearTimeout(timeout);
  }, [initialLoadFailed, refresh]);

  useEffect(() => {
    const events = new EventSource(client.eventsUrl());
    events.addEventListener('jobs', () => void refresh());
    return () => events.close();
  }, [refresh]);

  async function command(action: JobAction) {
    if (
      ['pause', 'terminate', 'retry'].includes(action) &&
      !window.confirm(`${action[0]?.toUpperCase()}${action.slice(1)} ${job?.name ?? 'this job'}?`)
    ) {
      return;
    }
    try {
      const updated = await client.command(id, action);
      if (action === 'retry' && updated.id !== id) {
        router.push(`/jobs/${updated.id}`);
        return;
      }
      await refresh();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Action failed');
    }
  }

  if (!job) {
    return <main className="page-shell">{error ? <div className="alert">{error}</div> : <p>Loading…</p>}</main>;
  }

  return (
    <main className="page-shell narrow">
      <Link className="back-link" href="/">← All jobs</Link>
      <section className="detail-header">
        <div>
          <p className="eyebrow">{job.namespace} / attempt {job.attempt}</p>
          <h1>{job.name}</h1>
          <Status state={job.observedState} />
        </div>
        <div className="action-bar">
          {job.observedState === 'PAUSED' ? (
            <button className="button primary" onClick={() => void command('resume')}>Resume</button>
          ) : null}
          {job.observedState === 'RUNNING' || job.desiredState === 'QUEUED' ? (
            <button className="button ghost" onClick={() => void command('pause')}>Pause</button>
          ) : null}
          {job.observedState === 'FAILED' || job.desiredState === 'CANCELLED' ? (
            <button className="button ghost" onClick={() => void command('retry')}>Retry</button>
          ) : null}
          {!['COMPLETED', 'CANCELLED'].includes(job.observedState) ? (
            <button className="button danger-button" onClick={() => void command('terminate')}>Terminate</button>
          ) : null}
        </div>
      </section>
      {error ? <div className="alert" role="alert">{error}</div> : null}

      <section className="detail-grid">
        <article className="panel facts">
          <h2>Execution</h2>
          <dl>
            <div><dt>Desired state</dt><dd>{job.desiredState}</dd></div>
            <div><dt>Queue position</dt><dd>{job.position}</dd></div>
            <div><dt>Priority</dt><dd>{job.priority}</dd></div>
            <div><dt>Team</dt><dd>{job.team || 'Unassigned'}</dd></div>
            <div><dt>Scheduled</dt><dd>{job.scheduledFor ? new Date(job.scheduledFor).toLocaleString() : 'Immediately'}</dd></div>
            <div><dt>Kubernetes UID</dt><dd className="mono">{job.kubernetesUid || 'Pending admission'}</dd></div>
          </dl>
        </article>
        <article className="panel timeline">
          <h2>History</h2>
          {events.length === 0 ? <p className="empty">No events recorded yet.</p> : null}
          <ol>
            {events.map((event) => (
              <li key={event.id}>
                <i aria-hidden="true" />
                <div><strong>{event.message}</strong><span>{event.type}</span></div>
                <time dateTime={event.createdAt}>{new Date(event.createdAt).toLocaleString()}</time>
              </li>
            ))}
          </ol>
        </article>
      </section>
      <section className="panel manifest">
        <h2>Stored Job template</h2>
        <pre><code>{JSON.stringify(job.template, null, 2)}</code></pre>
      </section>
    </main>
  );
}
