'use client';

import type { SupportDiagnostics } from '@kubequeue/api-client';

export function SupportView({
  diagnostics,
  loadFailed = false,
}: {
  diagnostics?: SupportDiagnostics;
  loadFailed?: boolean;
}) {
  function download() {
    if (!diagnostics) return;
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(diagnostics, null, 2)], { type: 'application/json' }),
    );
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = 'kubequeue-diagnostics.json';
    anchor.click();
    URL.revokeObjectURL(url);
  }

  if (!diagnostics) {
    return (
      <main className="page-shell narrow">
        <div className="page-title">
          <p className="eyebrow">Local runtime support</p>
          <h1>Support</h1>
        </div>
        <div className="alert" role="alert">
          {loadFailed ? 'Support diagnostics are unavailable or access is denied.' : 'Loading…'}
        </div>
      </main>
    );
  }

  return (
    <main className="page-shell narrow">
      <div className="page-title">
        <p className="eyebrow">Local runtime support</p>
        <h1>Support</h1>
        <p>
          Sanitized, bounded diagnostics only. Credentials, connection strings, manifests,
          environment values, and raw errors are excluded.
        </p>
        <button className="button ghost" type="button" onClick={download}>
          Download diagnostics
        </button>
      </div>

      <section className="health-grid" aria-label="Diagnostic health">
        <HealthCard label="Schema" healthy={diagnostics.schema.healthy} />
        <HealthCard label="Leadership" healthy={diagnostics.leadership.held} />
        <HealthCard label="Worker" healthy={diagnostics.worker.state === 'ready'} />
      </section>

      <section className="detail-grid settings-grid">
        <article className="panel facts">
          <h2>Local versions and authority</h2>
          <dl>
            <Fact term="API version" value={diagnostics.versions.api} />
            <Fact term="Worker version" value={diagnostics.versions.worker} />
            <Fact term="Schema current" value={diagnostics.schema.current} />
            <Fact term="Schema latest" value={diagnostics.schema.latest} />
            <Fact term="Leadership generation" value={String(diagnostics.leadership.generation)} />
            <Fact term="Watch mode" value={diagnostics.watch.mode} />
            <Fact
              term="Effective namespaces"
              value={diagnostics.watch.effectiveNamespaces.join(', ') || 'None'}
            />
            <Fact
              term="Excluded namespaces"
              value={diagnostics.watch.excludedNamespaces.join(', ') || 'None'}
            />
          </dl>
        </article>
        <article className="panel facts">
          <h2>Worker and namespaces</h2>
          <dl>
            <Fact term="Worker state" value={diagnostics.worker.state} />
            <Fact term="Heartbeat" value={formatTimestamp(diagnostics.worker.heartbeatAt)} />
            <Fact
              term="Last reconciliation"
              value={formatTimestamp(diagnostics.worker.lastSuccessfulReconciliationAt)}
            />
            {diagnostics.watch.namespaces.map((namespace) => (
              <Fact
                key={namespace.namespace}
                term={namespace.namespace}
                value={`${namespace.authorized ? 'authorized' : 'unauthorized'}, ${
                  namespace.informerSynced ? 'synced' : 'not synced'
                }`}
              />
            ))}
          </dl>
        </article>
      </section>

      <section className="panel settings-errors">
        <h2>Recent bounded error classes</h2>
        {diagnostics.recentErrorClasses.length === 0 ? (
          <p className="empty">No recent error classes.</p>
        ) : (
          <ul>
            {diagnostics.recentErrorClasses.map((item) => (
              <li key={item.class}>
                <code>{item.class}</code>
                <span>{item.count} affected jobs</span>
                <span>{formatTimestamp(item.lastSeenAt)}</span>
              </li>
            ))}
          </ul>
        )}
        <p>Audit-writer overload count: {diagnostics.auditWriterOverloadCount}</p>
      </section>
    </main>
  );
}

function HealthCard({ label, healthy }: { label: string; healthy: boolean }) {
  return (
    <article className={`health-card ${healthy ? 'healthy' : 'unhealthy'}`}>
      <span>{label}</span>
      <strong>{healthy ? 'Healthy' : 'Unavailable'}</strong>
    </article>
  );
}

function Fact({ term, value }: { term: string; value: string }) {
  return (
    <div>
      <dt>{term}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function formatTimestamp(value?: string) {
  if (!value) return 'Not reported';
  return new Intl.DateTimeFormat('en-US', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'UTC',
  }).format(new Date(value));
}
