'use client';

import {
  KubeQueueClient,
  type AdmissionDecisionPage,
  type CurrentAccess,
  type Job,
  type NamespaceBinding,
  type Project,
  type ProjectAdmissionSettings,
  type QuotaCounters,
  type UpdateProjectAdmissionSettings,
} from '@kubequeue/api-client';
import { useState, type FormEvent } from 'react';

const client = new KubeQueueClient();

export function ProjectAdmissionView({
  projectId,
  project,
  access,
  initialSettings,
  initialSettingsETag,
  initialUsage,
  initialDecisions,
  initialBindings,
  initialQueue,
  initialQueueVersion,
  loadError,
}: {
  projectId: string;
  project?: Project;
  access?: CurrentAccess;
  initialSettings?: ProjectAdmissionSettings;
  initialSettingsETag: string;
  initialUsage?: QuotaCounters;
  initialDecisions: AdmissionDecisionPage;
  initialBindings: NamespaceBinding[];
  initialQueue: Job[];
  initialQueueVersion: number;
  loadError: string;
}) {
  const [settings, setSettings] = useState(initialSettings);
  const [etag, setETag] = useState(initialSettingsETag);
  const [usage] = useState(initialUsage);
  const [decisions, setDecisions] = useState(initialDecisions.items);
  const [nextCursor, setNextCursor] = useState(initialDecisions.nextCursor);
  const [bindings, setBindings] = useState(initialBindings);
  const [projectQueue, setProjectQueue] = useState(initialQueue);
  const [queueVersion, setQueueVersion] = useState(initialQueueVersion);
  const [error, setError] = useState(loadError);
  const [status, setStatus] = useState('');
  const [busy, setBusy] = useState(false);
  const hasPermission = (permission: string, scopeProjectId = projectId) =>
    access?.permissions.some(
      (grant) =>
        grant.permission === permission &&
        (grant.scopeType === 'INSTALLATION' || grant.projectId === scopeProjectId),
    ) ?? false;
  const canManage = ['policies.manage', 'quotas.manage'].every((permission) =>
    hasPermission(permission),
  );
  const canManageBindings = hasPermission('namespace-bindings.manage');
  const canReorderQueue = hasPermission('queue.project.reorder');
  const rules = settings?.rules;
  const projectQuota = rules?.quotas?.project;

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!settings || !etag) return;
    const data = new FormData(event.currentTarget);
    setBusy(true);
    setError('');
    setStatus('');
    try {
      const nextRules: UpdateProjectAdmissionSettings['rules'] = {
        ...rules,
        quotas: {
          ...rules?.quotas,
          project: {
            maxConcurrent: nonNegativeInteger(data, 'maxConcurrent'),
            maxQueued: nonNegativeInteger(data, 'maxQueued'),
            maxRetained: nonNegativeInteger(data, 'maxRetained'),
          },
        },
      };
      if (data.get('overridePriority') === 'on') {
        nextRules.priority = {
          min: integer(data, 'priorityMin'),
          max: integer(data, 'priorityMax'),
          default: integer(data, 'priorityDefault'),
        };
      } else {
        delete nextRules.priority;
      }
      optionalPositiveInteger(data, 'maxDelayedStartSeconds', nextRules);
      optionalPositiveInteger(data, 'maxExecutionDurationSeconds', nextRules);
      if (data.get('enforceRegistries') === 'on') {
        nextRules.allowedImageRegistries = text(data, 'allowedImageRegistries')
          .split(/\r?\n|,/)
          .map((value) => value.trim())
          .filter(Boolean);
      } else {
        delete nextRules.allowedImageRegistries;
      }
      const updated = await client.updateProjectAdmissionSettings(projectId, etag, {
        schedulingWeight: positiveInteger(data, 'schedulingWeight'),
        rules: nextRules,
      });
      setSettings(updated.value);
      setETag(updated.etag);
      setStatus('Project admission settings updated.');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to update admission settings');
    } finally {
      setBusy(false);
    }
  }

  async function loadMoreDecisions() {
    if (!nextCursor) return;
    setBusy(true);
    setError('');
    try {
      const page = await client.listProjectAdmissionDecisions(projectId, nextCursor);
      setDecisions((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load admission decisions');
    } finally {
      setBusy(false);
    }
  }

  async function createBinding(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const namespace = text(new FormData(event.currentTarget), 'namespace');
    setBusy(true);
    setError('');
    setStatus('');
    try {
      const created = await client.createProjectNamespaceBinding(projectId, { namespace });
      setBindings((current) => [...current, created].sort(compareNamespaces));
      event.currentTarget.reset();
      setStatus(`Namespace ${namespace} is now desired for this project.`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to bind namespace');
    } finally {
      setBusy(false);
    }
  }

  async function reassignBinding(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const namespace = text(data, 'reassignNamespace');
    const targetProjectId = text(data, 'targetProjectId');
    setBusy(true);
    setError('');
    setStatus('');
    try {
      await client.reassignProjectNamespaceBinding(targetProjectId, namespace);
      setBindings((current) => current.filter((binding) => binding.namespace !== namespace));
      event.currentTarget.reset();
      setStatus(`Namespace ${namespace} was reassigned to ${targetProjectId}.`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to reassign namespace');
    } finally {
      setBusy(false);
    }
  }

  async function removeBinding(namespace: string) {
    setBusy(true);
    setError('');
    setStatus('');
    try {
      await client.removeProjectNamespaceBinding(projectId, namespace);
      setBindings((current) => current.filter((binding) => binding.namespace !== namespace));
      setStatus(`Namespace ${namespace} is no longer desired for this project.`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to remove namespace binding');
    } finally {
      setBusy(false);
    }
  }

  async function moveQueueJob(index: number, offset: -1 | 1) {
    const target = index + offset;
    const currentJob = projectQueue[index];
    const targetJob = projectQueue[target];
    if (!currentJob || !targetJob || currentJob.priority !== targetJob.priority) {
      return;
    }
    const previous = projectQueue;
    const reordered = [...projectQueue];
    reordered[index] = targetJob;
    reordered[target] = currentJob;
    setProjectQueue(reordered);
    setBusy(true);
    setError('');
    setStatus('');
    try {
      const result = await client.reorderProject(
        projectId,
        reordered.map((job) => job.id),
        queueVersion,
      );
      setQueueVersion(result.version);
      setStatus('Project queue order updated.');
    } catch (reason) {
      setProjectQueue(previous);
      setError(reason instanceof Error ? reason.message : 'Unable to reorder project queue');
      try {
        const latest = await client.getQueue();
        setProjectQueue(latest.items.filter((job) => job.projectId === projectId));
        setQueueVersion(latest.queueVersion);
      } catch {
        // Keep the last locally known project order when refresh is unavailable.
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="page-shell">
      <div className="page-title">
        <p className="eyebrow">Project admission</p>
        <h1>{project?.name ?? projectId}</h1>
        <p>Direct project policy, quota usage, fair-scheduling weight, and recent decisions.</p>
      </div>

      <div className="access-feedback" aria-live="polite">
        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        {status ? (
          <div className="success" role="status">
            {status}
          </div>
        ) : null}
      </div>

      {settings ? (
        <section className="surface" aria-labelledby="admission-settings-title">
          <h2 id="admission-settings-title">Policy and quota</h2>
          <form className="access-form" onSubmit={(event) => void save(event)}>
            <div className="inline-form">
              <NumberField
                name="maxConcurrent"
                label="Maximum concurrent jobs"
                minimum={0}
                value={projectQuota?.maxConcurrent}
              />
              <NumberField
                name="maxQueued"
                label="Maximum queued jobs"
                minimum={0}
                value={projectQuota?.maxQueued}
              />
              <NumberField
                name="maxRetained"
                label="Maximum retained jobs"
                minimum={0}
                value={projectQuota?.maxRetained}
              />
              <NumberField
                name="schedulingWeight"
                label="Positive scheduling weight"
                minimum={1}
                value={settings.schedulingWeight}
              />
            </div>

            <fieldset>
              <legend>Priority override</legend>
              <label>
                <input
                  name="overridePriority"
                  type="checkbox"
                  defaultChecked={rules?.priority !== undefined}
                />
                Override inherited priority range
              </label>
              <div className="inline-form">
                <NumberField name="priorityMin" label="Minimum" value={rules?.priority?.min} />
                <NumberField name="priorityMax" label="Maximum" value={rules?.priority?.max} />
                <NumberField
                  name="priorityDefault"
                  label="Default"
                  value={rules?.priority?.default}
                />
              </div>
            </fieldset>

            <div className="inline-form">
              <NumberField
                name="maxDelayedStartSeconds"
                label="Maximum delayed start (seconds, optional)"
                minimum={1}
                required={false}
                value={rules?.maxDelayedStartSeconds}
              />
              <NumberField
                name="maxExecutionDurationSeconds"
                label="Maximum execution (seconds, optional)"
                minimum={1}
                required={false}
                value={rules?.maxExecutionDurationSeconds}
              />
            </div>

            <label>
              <input
                name="enforceRegistries"
                type="checkbox"
                defaultChecked={rules?.allowedImageRegistries !== undefined}
              />
              Enforce a project image registry allowlist
            </label>
            <label>
              Allowed registries, one per line
              <textarea
                name="allowedImageRegistries"
                rows={4}
                defaultValue={rules?.allowedImageRegistries?.join('\n') ?? ''}
              />
            </label>
            {canManage ? (
              <button className="button primary" type="submit" disabled={busy}>
                {busy ? 'Saving…' : 'Save admission settings'}
              </button>
            ) : (
              <p className="empty">You have read-only access to these settings.</p>
            )}
          </form>
        </section>
      ) : null}

      <section className="surface" aria-labelledby="namespace-bindings-title">
        <h2 id="namespace-bindings-title">Namespace bindings</h2>
        <p>Desired project ownership and the worker&apos;s last observed Kubernetes authority.</p>
        {canManageBindings ? (
          <>
            <form className="inline-form" onSubmit={(event) => void createBinding(event)}>
              <label>
                Namespace
                <input name="namespace" required maxLength={63} />
              </label>
              <button className="button primary" type="submit" disabled={busy}>
                Bind namespace
              </button>
            </form>
            <form className="inline-form" onSubmit={(event) => void reassignBinding(event)}>
              <label>
                Namespace to reassign
                <input name="reassignNamespace" required maxLength={63} />
              </label>
              <label>
                Target project ID
                <input name="targetProjectId" required maxLength={63} />
              </label>
              <button className="button ghost" type="submit" disabled={busy}>
                Reassign
              </button>
            </form>
          </>
        ) : null}
        {bindings.length ? (
          <div className="table-scroll">
            <table className="access-table">
              <thead>
                <tr>
                  <th scope="col">Namespace</th>
                  <th scope="col">Desired</th>
                  <th scope="col">Observed authority</th>
                  <th scope="col">Observation</th>
                  <th scope="col">Action</th>
                </tr>
              </thead>
              <tbody>
                {bindings.map((binding) => (
                  <tr key={binding.id}>
                    <th scope="row">
                      <code>{binding.namespace}</code>
                    </th>
                    <td>{binding.desired ? 'Bound' : 'Removed'}</td>
                    <td>{binding.authorityState}</td>
                    <td>{binding.message || 'No diagnostic reported'}</td>
                    <td>
                      {canManageBindings ? (
                        <button
                          className="button ghost"
                          type="button"
                          disabled={busy}
                          onClick={() => void removeBinding(binding.namespace)}
                        >
                          Remove
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="empty">No namespaces are bound to this project.</p>
        )}
      </section>

      <section className="surface" aria-labelledby="project-queue-title">
        <h2 id="project-queue-title">Project queue</h2>
        <p>Reordering changes only this project&apos;s slots within the global queue.</p>
        {projectQueue.length ? (
          <ol>
            {projectQueue.map((job, index) => (
              <li key={job.id}>
                <strong>{job.name}</strong> <code>{job.namespace}</code> priority {job.priority}
                {canReorderQueue ? (
                  <>
                    <button
                      className="button ghost"
                      type="button"
                      disabled={
                        busy || index === 0 || projectQueue[index - 1]?.priority !== job.priority
                      }
                      onClick={() => void moveQueueJob(index, -1)}
                    >
                      Move up
                    </button>
                    <button
                      className="button ghost"
                      type="button"
                      disabled={
                        busy ||
                        index === projectQueue.length - 1 ||
                        projectQueue[index + 1]?.priority !== job.priority
                      }
                      onClick={() => void moveQueueJob(index, 1)}
                    >
                      Move down
                    </button>
                  </>
                ) : null}
              </li>
            ))}
          </ol>
        ) : (
          <p className="empty">This project has no queued managed jobs.</p>
        )}
      </section>

      <section className="surface" aria-labelledby="quota-usage-title">
        <h2 id="quota-usage-title">Current quota usage</h2>
        {usage ? (
          <dl className="metric-grid">
            <Metric label="Concurrent" value={usage.concurrent} />
            <Metric label="Queued" value={usage.queued} />
            <Metric label="Retained" value={usage.retained} />
          </dl>
        ) : (
          <p className="empty">Quota usage is unavailable.</p>
        )}
      </section>

      <section className="surface" aria-labelledby="admission-decisions-title">
        <h2 id="admission-decisions-title">Recent admission decisions</h2>
        {decisions.length ? (
          <div className="table-scroll">
            <table className="access-table">
              <caption className="sr-only">Recent project admission decisions</caption>
              <thead>
                <tr>
                  <th scope="col">Decision</th>
                  <th scope="col">Job</th>
                  <th scope="col">Reason</th>
                  <th scope="col">Time</th>
                </tr>
              </thead>
              <tbody>
                {decisions.map((decision) => (
                  <tr key={decision.id}>
                    <th scope="row">{decision.accepted ? 'Accepted' : 'Rejected'}</th>
                    <td>
                      <code>{decision.jobId}</code>
                    </td>
                    <td>
                      <code>{decision.reason}</code>
                    </td>
                    <td>
                      <time dateTime={decision.decidedAt}>{formatDate(decision.decidedAt)}</time>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="empty">No recent admission decisions.</p>
        )}
        {nextCursor ? (
          <button
            className="button ghost"
            type="button"
            disabled={busy}
            onClick={() => void loadMoreDecisions()}
          >
            {busy ? 'Loading…' : 'Load more decisions'}
          </button>
        ) : null}
      </section>
    </main>
  );
}

function NumberField({
  name,
  label,
  value,
  minimum,
  required = true,
}: {
  name: string;
  label: string;
  value?: number;
  minimum?: number;
  required?: boolean;
}) {
  return (
    <label>
      {label}
      <input name={name} type="number" min={minimum} required={required} defaultValue={value} />
    </label>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function text(data: FormData, name: string) {
  return String(data.get(name) ?? '').trim();
}

function integer(data: FormData, name: string) {
  const value = Number(text(data, name));
  if (!Number.isInteger(value)) throw new Error(`${name} must be an integer`);
  return value;
}

function nonNegativeInteger(data: FormData, name: string) {
  const value = integer(data, name);
  if (value < 0) throw new Error(`${name} cannot be negative`);
  return value;
}

function positiveInteger(data: FormData, name: string) {
  const value = integer(data, name);
  if (value < 1) throw new Error(`${name} must be positive`);
  return value;
}

function optionalPositiveInteger(
  data: FormData,
  name: 'maxDelayedStartSeconds' | 'maxExecutionDurationSeconds',
  rules: UpdateProjectAdmissionSettings['rules'],
) {
  if (text(data, name)) {
    rules[name] = positiveInteger(data, name);
  } else {
    delete rules[name];
  }
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value));
}

function compareNamespaces(left: NamespaceBinding, right: NamespaceBinding) {
  return left.namespace.localeCompare(right.namespace);
}
