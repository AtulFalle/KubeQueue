'use client';

import type { components, SystemStatus } from '@kubequeue/api-client';
import { useCallback, useEffect, useState, type FormEvent } from 'react';

type IdentityProvider = components['schemas']['IdentityProvider'];
type ProviderInput = components['schemas']['ConfigureOIDCIdentityProvider'];

const helmRemediation =
  'helm upgrade --install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue --namespace kubequeue --reuse-values';

export function SettingsView({
  status,
  loadFailed = false,
  csrfToken,
  localSession = false,
  passwordResult,
}: {
  status?: SystemStatus;
  loadFailed?: boolean;
  csrfToken?: string;
  localSession?: boolean;
  passwordResult?: string;
}) {
  const [copied, setCopied] = useState(false);
  const [providers, setProviders] = useState<IdentityProvider[]>([]);
  const [providerError, setProviderError] = useState('');
  const [providerNotice, setProviderNotice] = useState('');
  const [providerBusy, setProviderBusy] = useState('');

  const loadProviders = useCallback(async () => {
    try {
      const response = await fetch('/api/v1/identity-providers', { cache: 'no-store' });
      const payload = await readAPI<{ items: IdentityProvider[] }>(response);
      setProviders(payload.items);
      setProviderError('');
    } catch (cause) {
      setProviderError(boundedError(cause, 'Identity providers could not be loaded.'));
    }
  }, []);

  useEffect(() => {
    // Loading server configuration is the synchronization this effect owns.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadProviders();
  }, [loadProviders]);

  async function copyRemediation() {
    await navigator.clipboard.writeText(helmRemediation);
    setCopied(true);
  }

  async function configureProvider(event: FormEvent<HTMLFormElement>, existing?: IdentityProvider) {
    event.preventDefault();
    const form = event.currentTarget;
    const input = providerInput(new FormData(form));
    const id = existing?.id ?? String(new FormData(form).get('id'));
    if (
      (input.clientSecret && input.clientSecretReference) ||
      (!existing && !input.clientSecret && !input.clientSecretReference)
    ) {
      setProviderError('Provide either a client secret or a secret reference.');
      return;
    }
    setProviderBusy(id);
    setProviderError('');
    setProviderNotice('');
    try {
      const response = await fetch(
        existing
          ? `/api/v1/identity-providers/${encodeURIComponent(existing.id)}`
          : '/api/v1/identity-providers',
        {
          method: existing ? 'PUT' : 'POST',
          headers: mutationHeaders(csrfToken, existing ? `"${existing.version}"` : undefined),
          body: JSON.stringify(existing ? input : { id, ...input }),
        },
      );
      await readAPI<IdentityProvider>(response);
      form.reset();
      setProviderNotice(`${existing ? 'Updated' : 'Created'} ${input.displayName}.`);
      await loadProviders();
    } catch (cause) {
      setProviderError(boundedError(cause, 'Identity provider could not be saved.'));
    } finally {
      setProviderBusy('');
    }
  }

  async function transitionProvider(
    provider: IdentityProvider,
    action: 'test' | 'enable' | 'disable',
  ) {
    setProviderBusy(provider.id);
    setProviderError('');
    setProviderNotice('');
    try {
      const response = await fetch(
        `/api/v1/identity-providers/${encodeURIComponent(provider.id)}/${action}`,
        {
          method: 'POST',
          headers: mutationHeaders(csrfToken, `"${provider.version}"`),
        },
      );
      await readAPI<IdentityProvider>(response);
      setProviderNotice(`${provider.displayName} was ${transitionLabel(action)}.`);
      await loadProviders();
    } catch (cause) {
      setProviderError(boundedError(cause, `Identity provider could not be ${action}ed.`));
    } finally {
      setProviderBusy('');
    }
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

      {localSession ? (
        <section className="panel">
          <h2>Change local password</h2>
          <p>Changing your password revokes your other sessions and rotates this session.</p>
          {passwordResult ? (
            <p role={passwordResult === 'password_changed' ? 'status' : 'alert'}>
              {passwordMessage(passwordResult)}
            </p>
          ) : null}
          <form className="setup-form" action="/auth/local/password" method="post">
            <input type="hidden" name="csrfToken" value={csrfToken} />
            <label>
              Current password
              <input
                name="currentPassword"
                type="password"
                autoComplete="current-password"
                required
              />
            </label>
            <label>
              New password
              <input
                name="newPassword"
                type="password"
                autoComplete="new-password"
                minLength={12}
                maxLength={128}
                required
              />
            </label>
            <label>
              Confirm new password
              <input
                name="passwordConfirmation"
                type="password"
                autoComplete="new-password"
                minLength={12}
                maxLength={128}
                required
              />
            </label>
            <button className="button primary" type="submit">
              Change password
            </button>
          </form>
        </section>
      ) : null}

      <section className="panel">
        <h2>Identity providers</h2>
        <p>Client secrets are write-only. Leave the secret blank when editing to preserve it.</p>
        <ProviderForm
          busy={providerBusy === 'new'}
          onSubmit={(event) => void configureProvider(event)}
        />
        <p className="form-error" role="alert" aria-live="assertive">
          {providerError}
        </p>
        <p role="status" aria-live="polite">
          {providerNotice}
        </p>
        {providers.length === 0 ? (
          <p className="empty">No OIDC providers configured.</p>
        ) : (
          <div className="provider-list">
            {providers.map((provider) => (
              <article className="surface" key={provider.id}>
                <h3>{provider.displayName}</h3>
                <p>
                  {provider.state} · Test: {provider.testResult.status} · Secret:{' '}
                  {provider.clientSecretConfigured ? 'configured' : 'missing'}
                </p>
                {provider.testResult.message ? <p>{provider.testResult.message}</p> : null}
                <ProviderForm
                  provider={provider}
                  busy={providerBusy === provider.id}
                  onSubmit={(event) => void configureProvider(event, provider)}
                />
                <div className="action-row">
                  <button
                    className="button ghost"
                    type="button"
                    disabled={providerBusy === provider.id}
                    onClick={() => void transitionProvider(provider, 'test')}
                  >
                    Test
                  </button>
                  <button
                    className="button ghost"
                    type="button"
                    disabled={providerBusy === provider.id}
                    onClick={() =>
                      void transitionProvider(
                        provider,
                        provider.state === 'ENABLED' ? 'disable' : 'enable',
                      )
                    }
                  >
                    {provider.state === 'ENABLED' ? 'Disable' : 'Enable'}
                  </button>
                </div>
              </article>
            ))}
          </div>
        )}
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

function ProviderForm({
  provider,
  busy,
  onSubmit,
}: {
  provider?: IdentityProvider;
  busy: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  const prefix = provider?.id ?? 'new';
  return (
    <form className="setup-form provider-form" onSubmit={onSubmit}>
      {!provider ? (
        <label>
          Provider ID
          <input name="id" required pattern="[a-z][a-z0-9_]{0,62}" />
        </label>
      ) : null}
      <label>
        Display name
        <input name="displayName" defaultValue={provider?.displayName} required maxLength={128} />
      </label>
      <label>
        Issuer
        <input name="issuer" type="url" defaultValue={provider?.issuer} required maxLength={2048} />
      </label>
      <label>
        Audience
        <input name="audience" defaultValue={provider?.audience} required maxLength={256} />
      </label>
      <label>
        Client ID
        <input name="clientId" defaultValue={provider?.clientId} required maxLength={256} />
      </label>
      <label>
        Client secret {provider?.clientSecretConfigured ? '(leave blank to preserve)' : ''}
        <input name="clientSecret" type="password" autoComplete="new-password" maxLength={4096} />
      </label>
      <label>
        Client secret reference
        <input
          name="clientSecretReference"
          placeholder="Optional alternative to a client secret"
          maxLength={512}
        />
      </label>
      <label>
        Redirect URI
        <input
          name="redirectUri"
          type="url"
          defaultValue={provider?.redirectUri}
          placeholder="https://queue.example.com/auth/callback"
          required
          maxLength={2048}
        />
      </label>
      <label>
        Authorized party
        <input name="authorizedParty" defaultValue={provider?.authorizedParty} maxLength={256} />
      </label>
      <fieldset>
        <legend>Allowed signing algorithms</legend>
        {(['RS256', 'RS384', 'RS512', 'ES256', 'ES384', 'ES512'] as const).map((algorithm) => (
          <label key={algorithm}>
            <input
              name="allowedAlgorithms"
              type="checkbox"
              value={algorithm}
              defaultChecked={
                provider ? provider.allowedAlgorithms.includes(algorithm) : algorithm === 'RS256'
              }
            />
            {algorithm}
          </label>
        ))}
      </fieldset>
      <label>
        Mapping type
        <select name="mappingType" defaultValue={provider?.mappingType ?? ''}>
          <option value="">No just-in-time mapping</option>
          <option value="GROUP">Exact group</option>
          <option value="DOMAIN">Verified email domain</option>
        </select>
      </label>
      <label>
        Mapping value
        <input name="mappingValue" defaultValue={provider?.mappingValue} maxLength={256} />
      </label>
      <details>
        <summary>Advanced claims</summary>
        <label>
          Groups claim
          <input name="groupsClaim" defaultValue={provider?.groupsClaim ?? 'groups'} required />
        </label>
        <label>
          Email claim
          <input name="emailClaim" defaultValue={provider?.emailClaim ?? 'email'} required />
        </label>
        <label>
          Name claim
          <input name="nameClaim" defaultValue={provider?.nameClaim ?? 'name'} required />
        </label>
        <label>
          Cache TTL seconds
          <input
            name="cacheTtlSeconds"
            type="number"
            defaultValue={provider?.cacheTtlSeconds ?? 300}
            min={1}
            max={3600}
            required
          />
        </label>
      </details>
      <button className="button primary" type="submit" disabled={busy}>
        {busy ? 'Saving…' : provider ? `Save ${prefix}` : 'Configure provider'}
      </button>
    </form>
  );
}

function providerInput(data: FormData): ProviderInput {
  const secret = String(data.get('clientSecret'));
  const secretReference = String(data.get('clientSecretReference'));
  const mappingType = String(data.get('mappingType'));
  const allowedAlgorithms = data.getAll('allowedAlgorithms') as ProviderInput['allowedAlgorithms'];
  return {
    displayName: String(data.get('displayName')),
    issuer: String(data.get('issuer')).replace(/\/+$/, ''),
    audience: String(data.get('audience')),
    clientId: String(data.get('clientId')),
    ...(secret ? { clientSecret: secret } : {}),
    ...(secretReference ? { clientSecretReference: secretReference } : {}),
    redirectUri: String(data.get('redirectUri')),
    ...(String(data.get('authorizedParty'))
      ? { authorizedParty: String(data.get('authorizedParty')) }
      : {}),
    allowedAlgorithms,
    ...(mappingType ? { mappingType: mappingType as 'GROUP' | 'DOMAIN' } : {}),
    ...(mappingType ? { mappingValue: String(data.get('mappingValue')) } : {}),
    groupsClaim: String(data.get('groupsClaim')),
    emailClaim: String(data.get('emailClaim')),
    nameClaim: String(data.get('nameClaim')),
    cacheTtlSeconds: Number(data.get('cacheTtlSeconds')),
  };
}

function mutationHeaders(csrfToken?: string, etag?: string) {
  return {
    'Content-Type': 'application/json',
    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    ...(etag ? { 'If-Match': etag } : {}),
  };
}

async function readAPI<T>(response: Response): Promise<T> {
  const text = await response.text();
  if (text.length > 65_536) throw new Error('The server response was too large.');
  const payload = text ? (JSON.parse(text) as unknown) : undefined;
  if (!response.ok) {
    const error = payload as { error?: { message?: unknown } } | undefined;
    throw new Error(
      typeof error?.error?.message === 'string'
        ? error.error.message.slice(0, 256)
        : `Request failed (${response.status}).`,
    );
  }
  return payload as T;
}

function boundedError(cause: unknown, fallback: string) {
  return (cause instanceof Error ? cause.message : fallback).slice(0, 256);
}

function transitionLabel(action: 'test' | 'enable' | 'disable') {
  if (action === 'test') return 'tested';
  return action === 'enable' ? 'enabled' : 'disabled';
}

function passwordMessage(result: string) {
  if (result === 'password_changed') return 'Password changed. Your session has been rotated.';
  if (result === 'password_invalid') return 'New passwords must match.';
  return 'Password could not be changed.';
}
