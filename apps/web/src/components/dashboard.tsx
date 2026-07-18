'use client';

import { KubeQueueClient, type Job, type JobAction, type JobState } from '@kubequeue/api-client';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useCallback, useEffect, useMemo, useState } from 'react';

const client = new KubeQueueClient();
const states: Array<JobState | 'ALL'> = [
  'ALL',
  'QUEUED',
  'RUNNING',
  'PAUSED',
  'COMPLETED',
  'FAILED',
  'CANCELLED',
];

export function Dashboard({
  initialJobs,
  initialQueueVersion,
  initialLoadFailed = false,
}: {
  initialJobs: Job[];
  initialQueueVersion: number;
  initialLoadFailed?: boolean;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const query = useSearchParams();
  const status = (query.get('status') as JobState | null) ?? 'ALL';
  const search = query.get('search') ?? '';
  const namespace = query.get('namespace') ?? '';
  const team = query.get('team') ?? '';
  const priority = query.get('priority') ?? '';
  const [jobs, setJobs] = useState<Job[]>(initialJobs);
  const [queueVersion, setQueueVersion] = useState(initialQueueVersion);
  const [loading, setLoading] = useState(initialLoadFailed);
  const [error, setError] = useState(initialLoadFailed ? 'Unable to load jobs' : '');
  const [dragged, setDragged] = useState<string>();

  function setFilter(name: string, value: string) {
    const next = new URLSearchParams(query);
    if (value && value !== 'ALL') next.set(name, value);
    else next.delete(name);
    const suffix = next.toString();
    router.replace(suffix ? `${pathname}?${suffix}` : pathname, { scroll: false });
  }

  const refresh = useCallback(async () => {
    try {
      const response = await client.listJobs({
        ...(status === 'ALL' ? {} : { status }),
        search,
        namespace,
        team,
        ...(priority === '' ? {} : { priority: Number(priority) }),
      });
      setJobs(response.items);
      setQueueVersion(response.queueVersion);
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load jobs');
    } finally {
      setLoading(false);
    }
  }, [namespace, priority, search, status, team]);

  useEffect(() => {
    const timeout = window.setTimeout(() => void refresh(), 0);
    return () => window.clearTimeout(timeout);
  }, [refresh]);

  useEffect(() => {
    const events = new EventSource(client.eventsUrl());
    events.addEventListener('jobs', () => void refresh());
    return () => events.close();
  }, [refresh]);

  const counts = useMemo(
    () =>
      jobs.reduce<Record<string, number>>((result, job) => {
        result[job.observedState] = (result[job.observedState] ?? 0) + 1;
        return result;
      }, {}),
    [jobs],
  );
  const queued = jobs.filter((job) => job.desiredState === 'QUEUED');
  const queueIndexes = new Map(queued.map((job, index) => [job.id, index]));
  const active = jobs.filter((job) => job.observedState === 'RUNNING');

  async function command(job: Job, action: JobAction) {
    if (
      ['pause', 'terminate', 'retry'].includes(action) &&
      !window.confirm(`${action[0]?.toUpperCase()}${action.slice(1)} ${job.name}?`)
    ) {
      return;
    }
    try {
      const updated = await client.command(job.id, action);
      if (action === 'retry' && updated.id !== job.id) {
        router.push(`/jobs/${updated.id}`);
        return;
      }
      await refresh();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Action failed');
    }
  }

  async function move(jobID: string, direction: -1 | 1) {
    const ids = queued.map((job) => job.id);
    const from = ids.indexOf(jobID);
    const to = from + direction;
    if (from < 0 || to < 0 || to >= ids.length) return;
    const current = ids[from];
    const replacement = ids[to];
    if (current === undefined || replacement === undefined) return;
    ids[from] = replacement;
    ids[to] = current;
    const previous = jobs;
    setJobs(applyOptimisticOrder(jobs, ids));
    try {
      await client.reorder(ids, queueVersion);
      await refresh();
    } catch (reason) {
      setJobs(previous);
      setError(reason instanceof Error ? reason.message : 'Queue changed; refresh and try again');
      await refresh();
    }
  }

  async function drop(beforeID: string) {
    if (!dragged || dragged === beforeID) return;
    const ids = queued.map((job) => job.id).filter((id) => id !== dragged);
    ids.splice(ids.indexOf(beforeID), 0, dragged);
    setDragged(undefined);
    const previous = jobs;
    setJobs(applyOptimisticOrder(jobs, ids));
    try {
      await client.reorder(ids, queueVersion);
      await refresh();
    } catch (reason) {
      setJobs(previous);
      setError(reason instanceof Error ? reason.message : 'Could not reorder queue');
      await refresh();
    }
  }

  return (
    <main className="page-shell">
      <section className="hero">
        <div>
          <p className="eyebrow">Cluster control plane</p>
          <h1>Batch jobs, under control.</h1>
          <p className="hero-copy">
            See Kubernetes work in one place, shape the queue, and act without a workflow engine.
          </p>
        </div>
        <a className="button primary" href="/jobs/new">
          Submit job
        </a>
      </section>

      <section className="metrics" aria-label="Job summary">
        <Metric label="In queue" value={queued.length} tone="violet" />
        <Metric label="Running now" value={active.length} tone="cyan" />
        <Metric label="Succeeded" value={counts.COMPLETED ?? 0} tone="green" />
        <Metric label="Needs attention" value={counts.FAILED ?? 0} tone="red" />
      </section>

      <section className="panel">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">Live inventory</p>
            <h2>Jobs</h2>
          </div>
          <button className="button ghost" type="button" onClick={() => void refresh()}>
            Refresh
          </button>
        </div>

        <div className="filters">
          <label>
            <span>Search</span>
            <input
              value={search}
              onChange={(event) => setFilter('search', event.target.value)}
              placeholder="Job name"
            />
          </label>
          <label>
            <span>Status</span>
            <select value={status} onChange={(event) => setFilter('status', event.target.value)}>
              {states.map((state) => (
                <option key={state}>{state}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Namespace</span>
            <input
              value={namespace}
              onChange={(event) => setFilter('namespace', event.target.value)}
              placeholder="All"
            />
          </label>
          <label>
            <span>Team</span>
            <input
              value={team}
              onChange={(event) => setFilter('team', event.target.value)}
              placeholder="All"
            />
          </label>
          <label>
            <span>Priority</span>
            <input
              value={priority}
              onChange={(event) => setFilter('priority', event.target.value)}
              type="number"
              min="-1000"
              max="1000"
              placeholder="All"
            />
          </label>
        </div>

        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        {loading ? <p className="empty">Loading cluster state…</p> : null}
        {!loading && jobs.length === 0 ? (
          <div className="empty">
            <strong>No jobs match this view.</strong>
            <span>Submit a Job or adjust the filters.</span>
          </div>
        ) : null}

        <div className="job-list" aria-live="polite">
          {jobs.map((job) => (
            <article
              className="job-row"
              key={job.id}
              draggable={job.desiredState === 'QUEUED'}
              onDragStart={() => setDragged(job.id)}
              onDragOver={(event) => event.preventDefault()}
              onDrop={() => void drop(job.id)}
            >
              <div className="position" aria-label={`Queue position ${job.position}`}>
                {job.desiredState === 'QUEUED' ? String(job.position).padStart(2, '0') : '—'}
              </div>
              <div className="job-identity">
                <Link href={`/jobs/${job.id}`}>{job.name}</Link>
                <span>
                  {job.namespace} · {job.team || 'unassigned'}
                </span>
              </div>
              <Status state={job.observedState} />
              <div className="job-meta">
                <span>P{job.priority}</span>
                <span>
                  {job.scheduledFor ? formatTimestamp(job.scheduledFor) : 'Ready now'}
                </span>
              </div>
              {job.desiredState === 'QUEUED' || job.desiredState === 'PAUSED' ? (
                <QueueEditor
                  key={`${job.id}-${job.version}`}
                  job={job}
                  onSaved={refresh}
                  onError={(message) => setError(message)}
                />
              ) : null}
              <div className="actions">
                {job.desiredState === 'QUEUED' ? (
                  <>
                    <button
                      aria-label={`Move ${job.name} up`}
                      disabled={queueIndexes.get(job.id) === 0}
                      onClick={() => void move(job.id, -1)}
                    >
                      ↑
                    </button>
                    <button
                      aria-label={`Move ${job.name} down`}
                      disabled={queueIndexes.get(job.id) === queued.length - 1}
                      onClick={() => void move(job.id, 1)}
                    >
                      ↓
                    </button>
                  </>
                ) : null}
                {job.observedState === 'RUNNING' || job.desiredState === 'QUEUED' ? (
                  <button onClick={() => void command(job, 'pause')}>Pause</button>
                ) : null}
                {job.observedState === 'PAUSED' ? (
                  <button onClick={() => void command(job, 'resume')}>Resume</button>
                ) : null}
                {job.observedState === 'FAILED' || job.desiredState === 'CANCELLED' ? (
                  <button onClick={() => void command(job, 'retry')}>Retry</button>
                ) : null}
                {!['COMPLETED', 'CANCELLED'].includes(job.observedState) ? (
                  <button className="danger" onClick={() => void command(job, 'terminate')}>
                    Terminate
                  </button>
                ) : null}
              </div>
            </article>
          ))}
        </div>
      </section>
    </main>
  );
}

function applyOptimisticOrder(jobs: Job[], ids: string[]) {
  const positions = new Map(ids.map((id, index) => [id, index + 1]));
  return jobs
    .map((job) => {
      const position = positions.get(job.id);
      return position === undefined ? job : { ...job, position };
    })
    .sort((left, right) => {
      if (left.desiredState === 'QUEUED' && right.desiredState === 'QUEUED') {
        return left.position - right.position;
      }
      return 0;
    });
}

function QueueEditor({
  job,
  onSaved,
  onError,
}: {
  job: Job;
  onSaved: () => Promise<void>;
  onError: (message: string) => void;
}) {
  const [priority, setPriority] = useState(String(job.priority));
  const [scheduledFor, setScheduledFor] = useState(toDateTimeInput(job.scheduledFor));
  const [saving, setSaving] = useState(false);

  async function save() {
    setSaving(true);
    try {
      await client.updateQueue(job.id, {
        priority: Number(priority),
        position: job.position,
        version: job.version,
        scheduledFor: scheduledFor ? new Date(`${scheduledFor}:00Z`).toISOString() : null,
      });
      await onSaved();
    } catch (reason) {
      onError(reason instanceof Error ? reason.message : 'Unable to update queue settings');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="queue-editor" aria-label={`Queue settings for ${job.name}`}>
      <label>
        <span className="sr-only">Priority</span>
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
        <span className="sr-only">Do not start before, UTC</span>
        <input
          aria-label={`Do not start ${job.name} before, UTC`}
          type="datetime-local"
          value={scheduledFor}
          onChange={(event) => setScheduledFor(event.target.value)}
        />
      </label>
      <button disabled={saving} onClick={() => void save()}>
        {saving ? 'Saving…' : 'Save'}
      </button>
    </div>
  );
}

function toDateTimeInput(value?: string) {
  if (!value) return '';
  return new Date(value).toISOString().slice(0, 16);
}

function formatTimestamp(value: string) {
  return `${new Intl.DateTimeFormat('en-US', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'UTC',
  }).format(new Date(value))} UTC`;
}

function Metric({ label, value, tone }: { label: string; value: number; tone: string }) {
  return (
    <article className={`metric ${tone}`}>
      <span>{label}</span>
      <strong>{value.toString().padStart(2, '0')}</strong>
    </article>
  );
}

export function Status({ state }: { state: JobState }) {
  return <span className={`status status-${state.toLowerCase()}`}>{state}</span>;
}
