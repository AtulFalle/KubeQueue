'use client';

import type { SystemStatus } from '@kubequeue/api-client';
import { useState } from 'react';

const helmRemediation =
  'helm upgrade --install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue --namespace kubequeue --reuse-values';

export function SettingsView({
  status,
  loadFailed = false,
}: {
  status?: SystemStatus;
  loadFailed?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  async function copyRemediation() {
    await navigator.clipboard.writeText(helmRemediation);
    setCopied(true);
  }

  if (!status) {
    return (
      <main className="page-shell narrow">
        <div className="page-title">
          <p className="eyebrow">Read-only configuration</p>
          <h1>Settings</h1>
        </div>
        <div className="alert" role="alert">
          {loadFailed ? 'System status is unavailable.' : 'Loading system status…'}
        </div>
      </main>
    );
  }

  return (
    <main className="page-shell narrow">
      <div className="page-title">
        <p className="eyebrow">Read-only configuration</p>
        <h1>Settings</h1>
        <p>Runtime health and effective cluster scope. No Secrets are displayed.</p>
      </div>

      <section className="health-grid" aria-label="Service health">
        <HealthCard label="API" ready={status.api.ready} />
        <HealthCard label="Database" ready={status.database.ready} />
        <HealthCard
          label="Worker"
          ready={status.worker.state === 'ready'}
          detail={status.worker.state}
        />
      </section>

      <section className="detail-grid settings-grid">
        <article className="panel facts">
          <h2>Scope and readiness</h2>
          <dl>
            <Fact term="Watch mode" value={status.watch.mode} />
            <Fact
              term="Effective namespaces"
              value={status.watch.effectiveNamespaces.join(', ') || 'None'}
            />
            <Fact
              term="Excluded namespaces"
              value={status.watch.excludedNamespaces.join(', ') || 'None'}
            />
            {status.watch.namespaces.map((namespace) => (
              <Fact
                key={namespace.namespace}
                term={namespace.namespace}
                value={
                  namespace.authorized && namespace.informerSynced
                    ? 'Ready and authorized'
                    : namespace.message ||
                      `${namespace.authorized ? 'Authorized' : 'Unauthorized'}, ${
                        namespace.informerSynced ? 'synced' : 'not synced'
                      }`
                }
              />
            ))}
          </dl>
        </article>
        <article className="panel facts">
          <h2>Runtime</h2>
          <dl>
            <Fact term="Global concurrency" value={String(status.concurrency.global)} />
            <Fact
              term="Per-namespace concurrency"
              value={String(status.concurrency.perNamespace)}
            />
            <Fact term="Release" value={status.releaseVersion} />
            <Fact term="Worker heartbeat" value={formatTimestamp(status.worker.heartbeatAt)} />
            <Fact
              term="Last reconciliation"
              value={formatTimestamp(status.worker.lastSuccessfulReconciliationAt)}
            />
          </dl>
        </article>
      </section>

      <section className="panel settings-errors">
        <h2>Active reconciliation errors</h2>
        {status.activeErrors.length === 0 ? (
          <p className="empty">No active errors.</p>
        ) : (
          <ul>
            {status.activeErrors.map((error, index) => (
              <li key={`${error.scope}-${error.code}-${index}`}>
                <strong>{error.scope}</strong>
                <code>{error.code}</code>
                <span>{error.message}</span>
              </li>
            ))}
          </ul>
        )}
        <div className="remediation">
          <p>Reapply the current release after correcting the relevant Helm values:</p>
          <code>{helmRemediation}</code>
          <button className="button ghost" type="button" onClick={() => void copyRemediation()}>
            {copied ? 'Copied' : 'Copy Helm command'}
          </button>
        </div>
      </section>
    </main>
  );
}

function HealthCard({ label, ready, detail }: { label: string; ready: boolean; detail?: string }) {
  return (
    <article className={`health-card ${ready ? 'healthy' : 'unhealthy'}`}>
      <span>{label}</span>
      <strong>{detail ?? (ready ? 'Ready' : 'Unavailable')}</strong>
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
