'use client';

import { ApiError, KubeQueueClient, type Job, type SystemStatus } from '@kubequeue/api-client';
import Link from 'next/link';
import { useCallback, useEffect, useState } from 'react';

import { Badge, Status } from './dashboard';

const client = new KubeQueueClient();

export function QueueWorkflow({
  initialJobs,
  initialQueueVersion,
  initialSystemStatus,
  initialLoadFailed = false,
}: {
  initialJobs: Job[];
  initialQueueVersion: number;
  initialSystemStatus?: SystemStatus;
  initialLoadFailed?: boolean;
}) {
  const [jobs, setJobs] = useState(initialJobs);
  const [queueVersion, setQueueVersion] = useState(initialQueueVersion);
  const [systemStatus, setSystemStatus] = useState(initialSystemStatus);
  const [savingOrder, setSavingOrder] = useState(false);
  const [loading, setLoading] = useState(initialLoadFailed);
  const [message, setMessage] = useState('');
  const [error, setError] = useState(initialLoadFailed ? 'Unable to load the queue' : '');

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const [queue, status] = await Promise.all([client.getQueue(), client.getSystemStatus()]);
      setJobs(queue.items);
      setQueueVersion(queue.queueVersion);
      setSystemStatus(status);
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load the queue');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const events = new EventSource(client.eventsUrl());
    events.addEventListener('jobs', () => {
      if (!savingOrder) void refresh();
    });
    return () => events.close();
  }, [refresh, savingOrder]);

  const complete =
    systemStatus?.worker.state === 'ready' &&
    jobs.every((job) => job.managementMode === 'MANAGED' && job.syncStatus === 'SYNCED');

  async function move(index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target < 0 || target >= jobs.length || savingOrder || !complete) return;
    const reordered = [...jobs];
    const current = reordered[index];
    const replacement = reordered[target];
    if (!current || !replacement) return;
    reordered[index] = replacement;
    reordered[target] = current;
    const optimistic = reordered.map((job, position) => ({ ...job, position: position + 1 }));
    const previous = jobs;
    setJobs(optimistic);
    setSavingOrder(true);
    setError('');
    setMessage('');
    try {
      const result = await client.reorder(
        optimistic.map((job) => job.id),
        queueVersion,
      );
      setQueueVersion(result.version);
      setMessage(`${current.name} saved at queue position ${target + 1}.`);
    } catch (reason) {
      setJobs(previous);
      const conflict = reason instanceof ApiError && reason.status === 409;
      setError(
        conflict
          ? 'The queue changed before this order was saved. The latest order has been restored.'
          : reason instanceof Error
            ? reason.message
            : 'Unable to save queue order',
      );
      await refresh();
    } finally {
      setSavingOrder(false);
    }
  }

  function applyUpdated(updated: Job) {
    setJobs((current) => current.map((job) => (job.id === updated.id ? updated : job)));
  }

  return (
    <main className="page-shell">
      <div className="page-title queue-title">
        <div>
          <p className="eyebrow">Global scheduler</p>
          <h1>Queue</h1>
          <p>
            This view operates on the complete waiting queue. Manual ordering applies within a
            priority.
          </p>
        </div>
        <button className="button ghost" type="button" onClick={() => void refresh()}>
          Refresh
        </button>
      </div>

      <section className="panel">
        {!complete && !loading ? (
          <div className="notice" role="status">
            Queue editing is unavailable until the worker and every queue entry are fully
            synchronized. <Link href="/settings">View operational status</Link>.
          </div>
        ) : null}
        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        {message ? (
          <div className="success" role="status">
            {message}
          </div>
        ) : null}
        {loading ? <p className="empty">Loading the full queue…</p> : null}
        {!loading && jobs.length === 0 ? (
          <div className="empty">
            <strong>The queue is empty.</strong>
            <span>Queued jobs will appear here in scheduler order.</span>
          </div>
        ) : null}
        {!loading ? (
          <ol className="queue-list">
            {jobs.map((job, index) => (
              <li className="queue-row" key={job.id}>
                <span className="queue-position" aria-label={`Queue position ${index + 1}`}>
                  {String(index + 1).padStart(2, '0')}
                </span>
                <div className="job-identity">
                  <Link href={`/jobs/${job.id}`}>{job.name}</Link>
                  <span>
                    {job.namespace} · {job.team || 'unassigned'}
                  </span>
                </div>
                <div className="badge-stack">
                  <Status state={job.observedState} />
                  <Badge value={job.managementMode} />
                  <Badge value={job.syncStatus} />
                </div>
                <QueueEditor
                  disabled={!complete || savingOrder}
                  job={job}
                  onError={setError}
                  onSaved={(updated) => {
                    applyUpdated(updated);
                    setMessage(`${updated.name} queue settings saved.`);
                  }}
                />
                <div className="actions">
                  <button
                    aria-label={`Move ${job.name} up`}
                    disabled={
                      !complete ||
                      savingOrder ||
                      index === 0 ||
                      jobs[index - 1]?.priority !== job.priority
                    }
                    onClick={() => void move(index, -1)}
                  >
                    ↑
                  </button>
                  <button
                    aria-label={`Move ${job.name} down`}
                    disabled={
                      !complete ||
                      savingOrder ||
                      index === jobs.length - 1 ||
                      jobs[index + 1]?.priority !== job.priority
                    }
                    onClick={() => void move(index, 1)}
                  >
                    ↓
                  </button>
                </div>
              </li>
            ))}
          </ol>
        ) : null}
      </section>
    </main>
  );
}

function QueueEditor({
  job,
  disabled,
  onSaved,
  onError,
}: {
  job: Job;
  disabled: boolean;
  onSaved: (job: Job) => void;
  onError: (message: string) => void;
}) {
  const [priority, setPriority] = useState(String(job.priority));
  const [scheduledFor, setScheduledFor] = useState(toDateTimeInput(job.scheduledFor));
  const [saving, setSaving] = useState(false);

  async function save() {
    setSaving(true);
    onError('');
    try {
      const updated = await client.updateQueue(job.id, {
        priority: Number(priority),
        position: job.position,
        version: job.version,
        scheduledFor: scheduledFor ? new Date(scheduledFor).toISOString() : null,
      });
      onSaved(updated);
    } catch (reason) {
      onError(
        reason instanceof ApiError && reason.status === 409
          ? `${job.name} changed before the edit was saved. Refresh and try again.`
          : reason instanceof Error
            ? reason.message
            : 'Unable to update queue settings',
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <fieldset className="queue-editor" disabled={disabled || saving}>
      <legend className="sr-only">Queue settings for {job.name}</legend>
      <label>
        <span>Priority</span>
        <input
          aria-label={`Priority for ${job.name}`}
          type="number"
          min="-1000"
          max="1000"
          value={priority}
          onChange={(event) => setPriority(event.target.value)}
        />
      </label>
      <label>
        <span>Delay until</span>
        <input
          aria-label={`Do not start ${job.name} before`}
          type="datetime-local"
          value={scheduledFor}
          onChange={(event) => setScheduledFor(event.target.value)}
        />
      </label>
      <button type="button" onClick={() => void save()}>
        {saving ? 'Saving…' : 'Save'}
      </button>
    </fieldset>
  );
}

function toDateTimeInput(value?: string) {
  if (!value) return '';
  const date = new Date(value);
  date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  return date.toISOString().slice(0, 16);
}
