'use client';

import {
  KubeQueueClient,
  type Job,
  type JobAction,
  type JobFacets,
  type JobFilters,
  type JobState,
  type SystemStatus,
} from '@kubequeue/api-client';
import Link from 'next/link';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useCallback, useEffect, useRef, useState } from 'react';

import { LifecycleActions, pendingLifecycleLabel } from './lifecycle-actions';

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

type InventoryFilters = {
  status: JobState | 'ALL';
  search: string;
  namespace: string;
  team: string;
  priority: string;
};

type DashboardProps = {
  initialJobs: Job[];
  initialQueueVersion: number;
  initialFacets: JobFacets;
  initialSystemStatus?: SystemStatus;
  initialLoadFailed?: boolean;
};

export function Dashboard({
  initialJobs,
  initialFacets,
  initialSystemStatus,
  initialLoadFailed = false,
}: DashboardProps) {
  const router = useRouter();
  const pathname = usePathname();
  const query = useSearchParams();
  const [filters, setFilters] = useState<InventoryFilters>(() => {
    const requestedStatus = query.get('status');
    return {
      status:
        requestedStatus && states.includes(requestedStatus as JobState)
          ? (requestedStatus as JobState)
          : 'ALL',
      search: query.get('search') ?? '',
      namespace: query.get('namespace') ?? '',
      team: query.get('team') ?? '',
      priority: query.get('priority') ?? '',
    };
  });
  const [jobs, setJobs] = useState(initialJobs);
  const [facets, setFacets] = useState(initialFacets);
  const [systemStatus, setSystemStatus] = useState(initialSystemStatus);
  const [loading, setLoading] = useState(initialLoadFailed);
  const [error, setError] = useState(initialLoadFailed ? 'Unable to load job inventory' : '');
  const requestSequence = useRef(0);

  const refresh = useCallback(async (nextFilters: InventoryFilters, showLoading = false) => {
    const request = ++requestSequence.current;
    if (showLoading) setLoading(true);
    try {
      const [response, nextFacets, nextStatus] = await Promise.all([
        client.listJobs(toApiFilters(nextFilters)),
        client.getJobFacets(),
        client.getSystemStatus(),
      ]);
      if (request !== requestSequence.current) return;
      setJobs(response.items);
      setFacets(nextFacets);
      setSystemStatus(nextStatus);
      setError('');
    } catch (reason) {
      if (request !== requestSequence.current) return;
      setError(reason instanceof Error ? reason.message : 'Unable to load job inventory');
    } finally {
      if (request === requestSequence.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    const timeout = window.setTimeout(() => void refresh(filters, true), 300);
    return () => window.clearTimeout(timeout);
  }, [filters, refresh]);

  useEffect(() => {
    const events = new EventSource(client.eventsUrl());
    events.addEventListener('jobs', () => void refresh(filters));
    return () => events.close();
  }, [filters, refresh]);

  function setFilter(name: keyof InventoryFilters, value: string) {
    const next = { ...filters, [name]: value } as InventoryFilters;
    setFilters(next);
    const params = new URLSearchParams();
    if (next.status !== 'ALL') params.set('status', next.status);
    if (next.search) params.set('search', next.search);
    if (next.namespace) params.set('namespace', next.namespace);
    if (next.team) params.set('team', next.team);
    if (next.priority) params.set('priority', next.priority);
    const suffix = params.toString();
    router.replace(suffix ? `${pathname}?${suffix}` : pathname, { scroll: false });
  }

  function applyCommand(updated: Job, action: JobAction) {
    if (action === 'retry' && !jobs.some((job) => job.id === updated.id)) {
      router.push(`/jobs/${updated.id}`);
      return;
    }
    setJobs((current) => current.map((job) => (job.id === updated.id ? updated : job)));
  }

  const isFiltered = Object.entries(filters).some(
    ([name, value]) => value !== '' && !(name === 'status' && value === 'ALL'),
  );
  const selectedNamespace = systemStatus?.watch.namespaces.find(
    (item) => item.namespace === filters.namespace,
  );
  const workerState = systemStatus?.worker.state ?? 'unavailable';

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
        <Link className="button primary" href="/jobs/new">
          Submit job
        </Link>
      </section>

      <section className="metrics" aria-label="Global job summary">
        <Metric label="All jobs" value={facets.total} tone="violet" />
        <Metric label="Running now" value={facets.observedStateCounts.RUNNING ?? 0} tone="cyan" />
        <Metric label="Succeeded" value={facets.observedStateCounts.COMPLETED ?? 0} tone="green" />
        <Metric label="Needs attention" value={facets.observedStateCounts.FAILED ?? 0} tone="red" />
      </section>

      <section className="panel">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">Inventory</p>
            <h2>Jobs</h2>
          </div>
          <button
            className="button ghost"
            type="button"
            onClick={() => void refresh(filters, true)}
          >
            Refresh
          </button>
        </div>

        <div className="filters">
          <label>
            <span>Search</span>
            <input
              value={filters.search}
              onChange={(event) => setFilter('search', event.target.value)}
              placeholder="Job name"
            />
          </label>
          <label>
            <span>Status</span>
            <select
              value={filters.status}
              onChange={(event) => setFilter('status', event.target.value)}
            >
              {states.map((state) => (
                <option key={state}>{state}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Namespace</span>
            <select
              value={filters.namespace}
              onChange={(event) => setFilter('namespace', event.target.value)}
            >
              <option value="">All namespaces</option>
              {facets.namespaces.map((namespace) => (
                <option key={namespace}>{namespace}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Team</span>
            <select
              value={filters.team}
              onChange={(event) => setFilter('team', event.target.value)}
            >
              <option value="">All teams</option>
              {facets.teams.map((team) => (
                <option key={team}>{team}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Priority</span>
            <input
              value={filters.priority}
              onChange={(event) => setFilter('priority', event.target.value)}
              type="number"
              min="-1000"
              max="1000"
              placeholder="All"
            />
          </label>
        </div>

        {workerState === 'degraded' ? (
          <div className="notice" role="status">
            Inventory is incomplete while the worker is degraded.
          </div>
        ) : null}
        {selectedNamespace &&
        (!selectedNamespace.authorized || !selectedNamespace.informerSynced) ? (
          <div className="notice" role="status">
            {selectedNamespace.authorized
              ? `${selectedNamespace.namespace} is still synchronizing.`
              : `${selectedNamespace.namespace} is not authorized for management.`}
          </div>
        ) : null}
        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        {loading ? <p className="empty">Loading cluster state…</p> : null}
        {!loading && workerState === 'unavailable' ? (
          <div className="empty">
            <strong>Worker unavailable.</strong>
            <span>Inventory cannot be confirmed. Check operational status before acting.</span>
            <Link href="/settings">View settings and health</Link>
          </div>
        ) : null}
        {!loading && workerState !== 'unavailable' && jobs.length === 0 ? (
          <div className="empty">
            <strong>{isFiltered ? 'No jobs match these filters.' : 'No jobs yet.'}</strong>
            <span>
              {isFiltered ? 'Clear or adjust the filters.' : 'Submit a Job to start the queue.'}
            </span>
          </div>
        ) : null}

        {!loading && workerState !== 'unavailable' ? (
          <div className="job-list" aria-live="polite">
            {jobs.map((job) => (
              <article className="job-row inventory-row" key={job.id}>
                <div className="position" aria-label={`Queue position ${job.position}`}>
                  {job.desiredState === 'QUEUED' ? String(job.position).padStart(2, '0') : '—'}
                </div>
                <div className="job-identity">
                  <Link href={`/jobs/${job.id}`}>{job.name}</Link>
                  <span>
                    {job.namespace} · {job.team || 'unassigned'}
                  </span>
                </div>
                <div className="badge-stack">
                  <Status state={job.observedState} label={pendingLifecycleLabel(job)} />
                  <Badge value={job.managementMode} />
                  <Badge value={job.syncStatus} />
                </div>
                <div className="job-meta">
                  <span>P{job.priority}</span>
                  <span>{job.scheduledFor ? formatTimestamp(job.scheduledFor) : 'Ready now'}</span>
                </div>
                <LifecycleActions compact job={job} onUpdated={applyCommand} onError={setError} />
              </article>
            ))}
          </div>
        ) : null}
      </section>
    </main>
  );
}

function toApiFilters(filters: InventoryFilters): JobFilters {
  return {
    ...(filters.status === 'ALL' ? {} : { status: filters.status }),
    ...(filters.search ? { search: filters.search } : {}),
    ...(filters.namespace ? { namespace: filters.namespace } : {}),
    ...(filters.team ? { team: filters.team } : {}),
    ...(filters.priority ? { priority: Number(filters.priority) } : {}),
  };
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

export function Status({ state, label }: { state: JobState; label?: string }) {
  return (
    <span className={`status status-${state.toLowerCase()}`}>
      {label ? `${label} · ${state}` : state}
    </span>
  );
}

export function Badge({ value }: { value: string }) {
  return <span className={`status badge badge-${value.toLowerCase()}`}>{value}</span>;
}
