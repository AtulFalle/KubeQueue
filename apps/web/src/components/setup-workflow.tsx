'use client';

import type { SetupClaimRequest, SetupRecovery, SetupStatus } from '@kubequeue/api-client';
import { useCallback, useEffect, useState, type FormEvent } from 'react';

const defaultPolicy: SetupClaimRequest['policy'] = {
  globalConcurrency: 10,
  namespaceConcurrency: 4,
  queueCapacity: 1000,
  minimumPriority: -100,
  maximumPriority: 100,
  maximumDelaySeconds: 86400,
  maximumRunningJobs: 10,
  maximumQueuedJobs: 1000,
};

export function SetupWorkflow({
  onComplete = () => window.location.assign('/login'),
}: {
  onComplete?: () => void;
}) {
  const [status, setStatus] = useState<SetupStatus>();
  const [recovery, setRecovery] = useState<SetupRecovery>();
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const loadStatus = useCallback(async () => {
    try {
      const response = await fetch('/api/v1/setup/status', { cache: 'no-store' });
      if (!response.ok) throw new Error('Setup readiness could not be loaded.');
      const nextStatus = (await response.json()) as SetupStatus;
      setStatus(nextStatus);
      if (nextStatus.state === 'COMPLETED' || nextStatus.state === 'UNAVAILABLE') {
        const recoveryResponse = await fetch('/api/v1/setup/recovery', { cache: 'no-store' });
        if (recoveryResponse.ok) setRecovery((await recoveryResponse.json()) as SetupRecovery);
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Setup readiness could not be loaded.');
    }
  }, []);

  useEffect(() => {
    // Loading external setup state is the synchronization this effect owns.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadStatus();
  }, [loadStatus]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    setSubmitting(true);
    const form = event.currentTarget;
    const data = new FormData(form);
    const password = String(data.get('password'));
    if (password !== String(data.get('passwordConfirmation'))) {
      setError('Passwords do not match.');
      setSubmitting(false);
      return;
    }
    const request: SetupClaimRequest = {
      installationName: String(data.get('installationName')),
      localAdmin: {
        username: String(data.get('username')),
        password,
      },
      projectName: String(data.get('projectName')),
      namespaces: String(data.get('namespaces'))
        .split(',')
        .map((namespace) => namespace.trim())
        .filter(Boolean),
      policy: defaultPolicy,
    };
    const passwordInputs = ['password', 'passwordConfirmation'].map((name) =>
      form.elements.namedItem(name),
    );
    for (const input of passwordInputs) {
      if (input instanceof HTMLInputElement) input.value = '';
    }
    try {
      const response = await fetch('/api/v1/setup/claim', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(request),
      });
      if (!response.ok) {
        const payload = (await response.json().catch(() => undefined)) as
          { error?: { message?: string } } | undefined;
        throw new Error(payload?.error?.message || 'Setup claim was rejected.');
      }
      request.localAdmin.password = '';
      onComplete();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Setup claim was rejected.');
    } finally {
      request.localAdmin.password = '';
      setSubmitting(false);
    }
  }

  if (!status) {
    return (
      <main className="page-shell narrow" aria-busy="true">
        <h1>Installation setup</h1>
        <p role="status">Checking readiness…</p>
        {error ? <p role="alert">{error}</p> : null}
      </main>
    );
  }

  const checks = [
    ['API', status.api],
    ['Database', status.database],
    ['Schema', status.schema],
    ['Worker', status.worker],
    ['Kubernetes authority', status.kubernetesAuthority],
    ['Release', status.release],
    ['Public URL', status.publicUrl],
  ] as const;

  return (
    <main className="page-shell narrow">
      <div className="page-title">
        <p className="eyebrow">Guarded bootstrap</p>
        <h1>Installation setup</h1>
        <p>Claim this installation once and create its first local administrator.</p>
      </div>

      <section className="surface setup-readiness" aria-labelledby="readiness-title">
        <h2 id="readiness-title">Readiness</h2>
        <ul>
          {checks.map(([label, check]) => (
            <li key={label}>
              <strong>{label}</strong>
              <span className={check.ready ? 'ready' : 'not-ready'}>
                {check.ready ? 'Ready' : check.message || 'Not ready'}
              </span>
            </li>
          ))}
        </ul>
      </section>

      {status.available ? (
        <form className="surface setup-form" onSubmit={submit}>
          <fieldset>
            <legend>Installation</legend>
            <label>
              Installation name
              <input name="installationName" required maxLength={128} />
            </label>
            <label>
              Initial project name
              <input name="projectName" required maxLength={128} />
            </label>
            <label>
              Managed namespaces, comma separated
              <input name="namespaces" required placeholder="default, batch" />
            </label>
          </fieldset>
          <fieldset>
            <legend>Local administrator</legend>
            <label>
              Username
              <input
                name="username"
                defaultValue="admin"
                autoComplete="username"
                required
                maxLength={128}
                pattern="[A-Za-z0-9][A-Za-z0-9._-]*"
              />
            </label>
            <label>
              Password
              <input
                name="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={12}
                maxLength={128}
              />
            </label>
            <label>
              Confirm password
              <input
                name="passwordConfirmation"
                type="password"
                autoComplete="new-password"
                required
                minLength={12}
                maxLength={128}
              />
            </label>
          </fieldset>
          <p className="form-error" role="alert" aria-live="assertive">
            {error}
          </p>
          <button className="button primary" type="submit" disabled={submitting}>
            {submitting ? 'Claiming…' : 'Claim installation'}
          </button>
        </form>
      ) : (
        <section className="surface">
          <h2>Setup is permanently closed</h2>
          <p>A verified installation owner exists. Product APIs cannot reactivate bootstrap.</p>
          {recovery?.completed ? (
            <>
              <h3>Recovery checklist</h3>
              <ul>
                {recovery.checklist.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </>
          ) : null}
        </section>
      )}
    </main>
  );
}
