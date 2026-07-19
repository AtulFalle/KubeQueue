'use client';

import { KubeQueueClient, type AuditEvent, type AuditEventFilters } from '@kubequeue/api-client';
import { useRef, useState, type FormEvent } from 'react';

import { AuditEventDialog } from './audit-event-dialog';

const client = new KubeQueueClient();
const defaultLimit = 50;

export function AuditView({
  installationId,
  canRead,
  canExport,
  initialEvents,
  initialNextCursor,
  loadError = '',
}: {
  installationId: string;
  canRead: boolean;
  canExport: boolean;
  initialEvents: AuditEvent[];
  initialNextCursor: string | null;
  loadError?: string;
}) {
  const [events, setEvents] = useState(initialEvents);
  const [nextCursor, setNextCursor] = useState(initialNextCursor);
  const [cursor, setCursor] = useState<string>();
  const [previousCursors, setPreviousCursors] = useState<(string | undefined)[]>([]);
  const [activeFilters, setActiveFilters] = useState<AuditEventFilters>({ limit: defaultLimit });
  const [selectedEvent, setSelectedEvent] = useState<AuditEvent>();
  const detailTrigger = useRef<HTMLButtonElement | null>(null);
  const [error, setError] = useState(loadError);
  const [status, setStatus] = useState('');
  const [loading, setLoading] = useState(false);

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const filters = readFilters(new FormData(event.currentTarget));
    await loadPage(filters, undefined, []);
  }

  async function loadPage(
    filters: AuditEventFilters,
    nextPageCursor: string | undefined,
    history: (string | undefined)[],
  ) {
    setLoading(true);
    setError('');
    setStatus('');
    try {
      const page = await client.searchAuditEvents(installationId, {
        ...filters,
        cursor: nextPageCursor,
      });
      setEvents(page.items);
      setNextCursor(page.nextCursor);
      setCursor(nextPageCursor);
      setPreviousCursors(history);
      setActiveFilters(filters);
      setStatus(`${page.items.length} audit events loaded.`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load audit events');
    } finally {
      setLoading(false);
    }
  }

  if (!canRead) {
    return (
      <main className="page-shell narrow">
        <div className="page-title">
          <p className="eyebrow">Local security record</p>
          <h1>Audit</h1>
          <p>You do not have a visible capability to read local audit events.</p>
        </div>
      </main>
    );
  }

  const exportUrl = buildExportUrl(activeFilters);

  return (
    <main className="page-shell">
      <div className="page-title">
        <p className="eyebrow">Local security record</p>
        <h1>Audit</h1>
        <p>
          Search the customer-operated audit record. Results remain ordered by occurrence time and
          event ID; the API independently enforces every scope.
        </p>
      </div>

      <section className="surface" aria-labelledby="audit-search-title">
        <h2 id="audit-search-title">Search events</h2>
        <form className="access-form audit-filters" onSubmit={(event) => void search(event)}>
          <label>
            Project ID
            <input name="projectId" maxLength={128} autoComplete="off" />
          </label>
          <label>
            Action
            <input
              name="action"
              maxLength={96}
              pattern="[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*"
              autoComplete="off"
            />
          </label>
          <label>
            Decision
            <select name="decision" defaultValue="">
              <option value="">Any decision</option>
              <option value="ALLOW">Allow</option>
              <option value="DENY">Deny</option>
            </select>
          </label>
          <label>
            Result
            <select name="result" defaultValue="">
              <option value="">Any result</option>
              <option value="SUCCESS">Success</option>
              <option value="FAILURE">Failure</option>
            </select>
          </label>
          <label>
            From
            <input name="occurredFrom" type="datetime-local" />
          </label>
          <label>
            To
            <input name="occurredTo" type="datetime-local" />
          </label>
          <label>
            Results per page
            <select name="limit" defaultValue={String(defaultLimit)}>
              <option value="25">25</option>
              <option value="50">50</option>
              <option value="100">100</option>
              <option value="200">200</option>
            </select>
          </label>
          <div className="audit-filter-actions">
            <button className="button primary" disabled={loading} type="submit">
              {loading ? 'Searching…' : 'Search'}
            </button>
            {canExport ? (
              <a className="button ghost" href={exportUrl}>
                Download filtered events
              </a>
            ) : null}
          </div>
        </form>
      </section>

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

      <section className="surface" aria-labelledby="audit-results-title" aria-busy={loading}>
        <div className="audit-results-heading">
          <div>
            <h2 id="audit-results-title">Ordered results</h2>
            <p>{events.length} events on this page</p>
          </div>
          <div className="audit-pagination" aria-label="Audit result pages">
            <button
              className="button ghost"
              disabled={loading || previousCursors.length === 0}
              type="button"
              onClick={() => {
                const history = previousCursors.slice(0, -1);
                void loadPage(activeFilters, previousCursors.at(-1), history);
              }}
            >
              Previous
            </button>
            <button
              className="button ghost"
              disabled={loading || !nextCursor}
              type="button"
              onClick={() =>
                void loadPage(activeFilters, nextCursor ?? undefined, [...previousCursors, cursor])
              }
            >
              Next
            </button>
          </div>
        </div>

        {events.length === 0 && !error ? (
          <p className="empty">No accessible audit events.</p>
        ) : null}
        {events.length > 0 ? (
          <div className="table-scroll">
            <table className="access-table audit-table">
              <caption>Events in stable occurrence time and ID order</caption>
              <thead>
                <tr>
                  <th scope="col">Occurred</th>
                  <th scope="col">Action</th>
                  <th scope="col">Actor</th>
                  <th scope="col">Target</th>
                  <th scope="col">Outcome</th>
                  <th scope="col">
                    <span className="sr-only">Details</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {events.map((auditEvent) => (
                  <tr key={auditEvent.id}>
                    <td>
                      <time dateTime={auditEvent.occurredAt}>
                        {formatDate(auditEvent.occurredAt)}
                      </time>
                    </td>
                    <th scope="row">
                      <code>{auditEvent.action}</code>
                    </th>
                    <td>
                      <code>{auditEvent.actor.principalId}</code>
                    </td>
                    <td>
                      {auditEvent.target.type}
                      <small>
                        <code>{auditEvent.target.id}</code>
                      </small>
                    </td>
                    <td>
                      <span className={`audit-outcome ${auditEvent.result.toLowerCase()}`}>
                        {auditEvent.decision} · {auditEvent.result}
                      </span>
                    </td>
                    <td>
                      <button
                        className="button ghost"
                        type="button"
                        onClick={(clickEvent) => {
                          detailTrigger.current = clickEvent.currentTarget;
                          setSelectedEvent(auditEvent);
                        }}
                      >
                        View details
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
      </section>

      {selectedEvent ? (
        <AuditEventDialog
          auditEvent={selectedEvent}
          returnFocus={detailTrigger.current}
          onClose={() => setSelectedEvent(undefined)}
        />
      ) : null}
    </main>
  );
}

function readFilters(data: FormData): AuditEventFilters {
  const text = (name: string) => String(data.get(name) ?? '').trim() || undefined;
  const timestamp = (name: string) => {
    const value = text(name);
    return value ? new Date(value).toISOString() : undefined;
  };
  return {
    projectId: text('projectId'),
    action: text('action'),
    decision: text('decision') as AuditEventFilters['decision'],
    result: text('result') as AuditEventFilters['result'],
    occurredFrom: timestamp('occurredFrom'),
    occurredTo: timestamp('occurredTo'),
    limit: Number(data.get('limit') ?? defaultLimit),
  };
}

function buildExportUrl(filters: AuditEventFilters) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) {
    if (key !== 'cursor' && key !== 'limit' && value !== undefined && value !== '') {
      query.set(key, String(value));
    }
  }
  const value = query.toString();
  return value ? `/audit/download?${value}` : '/audit/download';
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'medium',
  }).format(new Date(value));
}
