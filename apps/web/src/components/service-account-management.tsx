'use client';

import {
  KubeQueueClient,
  type ServiceAccount,
  type ServiceAccountCredential,
} from '@kubequeue/api-client';
import { useEffect, useRef, useState, type FormEvent } from 'react';

import { readPermissions } from './access-permissions';

const client = new KubeQueueClient();

export function ServiceAccountManagement({
  initialAccounts,
  canManageAccounts,
  canManageCredentials,
  onError,
  onStatus,
}: {
  initialAccounts: ServiceAccount[];
  canManageAccounts: boolean;
  canManageCredentials: boolean;
  onError: (message: string) => void;
  onStatus: (message: string) => void;
}) {
  const [accounts, setAccounts] = useState(initialAccounts);
  const [selectedId, setSelectedId] = useState('');
  const [credentials, setCredentials] = useState<ServiceAccountCredential[]>([]);
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const secretDialog = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    if (token) secretDialog.current?.showModal();
  }, [token]);

  useEffect(() => {
    const clear = () => setToken('');
    window.addEventListener('pagehide', clear);
    return () => window.removeEventListener('pagehide', clear);
  }, []);

  function beginAction() {
    setToken('');
    secretDialog.current?.close();
    setBusy(true);
  }

  async function createAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    beginAction();
    try {
      const projectId = text(data, 'projectId');
      const account = await client.createServiceAccount({
        principalId: text(data, 'principalId'),
        displayName: text(data, 'displayName'),
        ...(projectId ? { projectId } : {}),
      });
      setAccounts((current) => [...current, account]);
      form.reset();
      onStatus(`${account.displayName} created without a credential.`);
    } catch (reason) {
      onError(message(reason, 'Unable to create service account'));
    } finally {
      setBusy(false);
    }
  }

  async function selectAccount(accountId: string) {
    beginAction();
    setSelectedId(accountId);
    setCredentials([]);
    if (!accountId || !canManageCredentials) {
      setBusy(false);
      return;
    }
    try {
      setCredentials((await client.listServiceAccountCredentials(accountId)).items);
    } catch (reason) {
      onError(message(reason, 'Unable to load credential metadata'));
    } finally {
      setBusy(false);
    }
  }

  async function issue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedId) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    beginAction();
    try {
      const issued = await client.issueServiceAccountCredential(selectedId, {
        permissions: readPermissions(data.get('permissions')),
        expiresAt: toISOString(data, 'expiresAt'),
      });
      setCredentials((current) => [...current, issued.credential]);
      setToken(issued.token);
      form.reset();
      onStatus('Credential issued. Its plaintext is shown once in the open dialog.');
    } catch (reason) {
      onError(message(reason, 'Unable to issue credential'));
    } finally {
      setBusy(false);
    }
  }

  async function bindOIDCIdentity(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedId) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    beginAction();
    try {
      const updated = await client.bindServiceAccountOIDCIdentity(selectedId, {
        issuer: text(data, 'issuer'),
        subject: text(data, 'subject'),
      });
      setAccounts((current) =>
        current.map((account) => (account.principalId === selectedId ? updated : account)),
      );
      onStatus('OIDC client identity bound to the service account.');
    } catch (reason) {
      onError(message(reason, 'Unable to bind OIDC client identity'));
    } finally {
      setBusy(false);
    }
  }

  async function removeOIDCIdentity() {
    if (!selectedId) return;
    beginAction();
    try {
      await client.removeServiceAccountOIDCIdentity(selectedId);
      setAccounts((current) =>
        current.map((account) =>
          account.principalId === selectedId ? { ...account, oidcIdentity: null } : account,
        ),
      );
      onStatus('OIDC client identity binding removed.');
    } catch (reason) {
      onError(message(reason, 'Unable to remove OIDC client identity'));
    } finally {
      setBusy(false);
    }
  }

  async function rotate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedId) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const credentialId = text(data, 'credentialId');
    beginAction();
    try {
      const rotated = await client.rotateServiceAccountCredential(selectedId, credentialId, {
        permissions: readPermissions(data.get('permissions')),
        expiresAt: toISOString(data, 'expiresAt'),
        overlapSeconds: Number(data.get('overlapSeconds')),
      });
      setCredentials((current) => [
        ...current.map((item) =>
          item.id === rotated.previousCredentialId
            ? { ...item, status: 'OVERLAP' as const, overlapExpiresAt: rotated.overlapExpiresAt }
            : item,
        ),
        rotated.replacement,
      ]);
      setToken(rotated.token);
      form.reset();
      onStatus('Credential rotated. Its replacement plaintext is shown once in the open dialog.');
    } catch (reason) {
      onError(message(reason, 'Unable to rotate credential'));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(credential: ServiceAccountCredential) {
    if (!selectedId) return;
    beginAction();
    try {
      await client.revokeServiceAccountCredential(selectedId, credential.id);
      setCredentials((current) =>
        current.map((item) =>
          item.id === credential.id
            ? { ...item, status: 'REVOKED' as const, revokedAt: new Date().toISOString() }
            : item,
        ),
      );
      onStatus(`Credential ${credential.safePrefix} revoked.`);
    } catch (reason) {
      onError(message(reason, 'Unable to revoke credential'));
    } finally {
      setBusy(false);
    }
  }

  async function copyToken() {
    if (!token) return;
    try {
      await navigator.clipboard.writeText(token);
      onStatus('Credential copied. Close this dialog after storing it securely.');
    } catch {
      onError('Unable to copy automatically. Select and copy the credential manually.');
    }
  }

  const selected = accounts.find((account) => account.principalId === selectedId);
  const activeCredentials = credentials.filter(({ status }) => status === 'ACTIVE');

  return (
    <section className="surface" aria-labelledby="service-accounts-title">
      <h2 id="service-accounts-title">Service accounts and credentials</h2>
      {canManageAccounts ? (
        <details className="access-disclosure">
          <summary>Create service account</summary>
          <form className="access-form form-grid" onSubmit={(event) => void createAccount(event)}>
            <label>
              Principal ID
              <input name="principalId" required autoComplete="off" />
            </label>
            <label>
              Display name
              <input name="displayName" required autoComplete="off" />
            </label>
            <label>
              Project ID (optional)
              <input name="projectId" autoComplete="off" />
            </label>
            <button className="button primary" disabled={busy} type="submit">
              Create account
            </button>
          </form>
        </details>
      ) : null}

      <label className="account-picker">
        Manage account
        <select
          value={selectedId}
          disabled={busy}
          onChange={(event) => void selectAccount(event.target.value)}
        >
          <option value="">Select a service account</option>
          {accounts.map((account) => (
            <option key={account.principalId} value={account.principalId}>
              {account.displayName}
            </option>
          ))}
        </select>
      </label>

      {selected ? (
        <div className="credential-panel">
          <p>
            <strong>{selected.displayName}</strong> <code>{selected.principalId}</code>
            {selected.projectId ? (
              <span>Project {selected.projectId}</span>
            ) : (
              <span>Installation scoped</span>
            )}
          </p>
          {canManageAccounts ? (
            <form
              className="access-form"
              key={`${selected.principalId}-${selected.oidcIdentity?.issuer ?? ''}-${selected.oidcIdentity?.subject ?? ''}`}
              onSubmit={(event) => void bindOIDCIdentity(event)}
            >
              <h3>OIDC client identity</h3>
              <p>
                Use the exact issuer and immutable subject emitted by a configured OIDC provider.
              </p>
              <label>
                Issuer
                <input
                  name="issuer"
                  type="url"
                  required
                  autoComplete="off"
                  defaultValue={selected.oidcIdentity?.issuer ?? ''}
                />
              </label>
              <label>
                Subject
                <input
                  name="subject"
                  required
                  autoComplete="off"
                  defaultValue={selected.oidcIdentity?.subject ?? ''}
                />
              </label>
              <div className="dialog-actions">
                <button className="button ghost" disabled={busy} type="submit">
                  {selected.oidcIdentity ? 'Update binding' : 'Create binding'}
                </button>
                {selected.oidcIdentity ? (
                  <button
                    className="button danger-button"
                    disabled={busy}
                    type="button"
                    onClick={() => void removeOIDCIdentity()}
                  >
                    Remove binding
                  </button>
                ) : null}
              </div>
            </form>
          ) : null}
          {canManageCredentials ? (
            <>
              <div className="credential-forms">
                <CredentialForm
                  title="Issue credential"
                  submitLabel="Issue credential"
                  busy={busy}
                  onSubmit={issue}
                />
                <CredentialForm
                  title="Rotate credential"
                  submitLabel="Rotate credential"
                  busy={busy}
                  onSubmit={rotate}
                  credentials={activeCredentials}
                  rotating
                />
              </div>
              <div className="table-scroll">
                <table className="access-table">
                  <caption>Credential metadata (plaintext is never available here)</caption>
                  <thead>
                    <tr>
                      <th scope="col">Prefix</th>
                      <th scope="col">Status</th>
                      <th scope="col">Expires</th>
                      <th scope="col">Last used</th>
                      <th scope="col">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {credentials.map((credential) => (
                      <tr key={credential.id}>
                        <th scope="row">
                          <code>{credential.safePrefix}</code>
                        </th>
                        <td>{credential.status.toLowerCase()}</td>
                        <td>
                          <time dateTime={credential.expiresAt}>
                            {formatDate(credential.expiresAt)}
                          </time>
                        </td>
                        <td>
                          {credential.lastUsedAt ? formatDate(credential.lastUsedAt) : 'Never'}
                        </td>
                        <td>
                          {credential.status !== 'REVOKED' ? (
                            <button
                              className="button danger-button"
                              disabled={busy}
                              type="button"
                              onClick={() => void revoke(credential)}
                            >
                              Revoke
                            </button>
                          ) : (
                            '—'
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <p>Credential lifecycle access is not available.</p>
          )}
        </div>
      ) : null}

      <dialog
        className="confirm-dialog secret-dialog"
        ref={secretDialog}
        aria-labelledby="one-time-secret-title"
        onClose={() => setToken('')}
        onCancel={() => setToken('')}
      >
        <h2 id="one-time-secret-title">Store this credential now</h2>
        <p>It is shown only for this issue or rotation response and cannot be retrieved later.</p>
        <code className="one-time-secret">{token}</code>
        <div className="dialog-actions">
          <button className="button primary" type="button" onClick={() => void copyToken()}>
            Copy credential
          </button>
          <button
            className="button ghost"
            type="button"
            onClick={() => secretDialog.current?.close()}
          >
            I have stored it
          </button>
        </div>
      </dialog>
    </section>
  );
}

function CredentialForm({
  title,
  submitLabel,
  busy,
  onSubmit,
  credentials = [],
  rotating = false,
}: {
  title: string;
  submitLabel: string;
  busy: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  credentials?: ServiceAccountCredential[];
  rotating?: boolean;
}) {
  return (
    <form className="access-form" onSubmit={onSubmit}>
      <h3>{title}</h3>
      {rotating ? (
        <label>
          Current credential
          <select name="credentialId" required defaultValue="">
            <option value="" disabled>
              Select credential
            </option>
            {credentials.map((credential) => (
              <option key={credential.id} value={credential.id}>
                {credential.safePrefix}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <label>
        Permissions, comma separated
        <textarea name="permissions" required rows={2} />
      </label>
      <label>
        Expires at
        <input name="expiresAt" required type="datetime-local" />
      </label>
      {rotating ? (
        <label>
          Overlap seconds
          <input
            name="overlapSeconds"
            required
            type="number"
            min="0"
            max="86400"
            defaultValue="300"
          />
        </label>
      ) : null}
      <button
        className="button ghost"
        disabled={busy || (rotating && credentials.length === 0)}
        type="submit"
      >
        {busy ? 'Working…' : submitLabel}
      </button>
    </form>
  );
}

function text(data: FormData, name: string) {
  return String(data.get(name) ?? '').trim();
}

function toISOString(data: FormData, name: string) {
  return new Date(text(data, name)).toISOString();
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(
    new Date(value),
  );
}

function message(reason: unknown, fallback: string) {
  return reason instanceof Error ? reason.message : fallback;
}
