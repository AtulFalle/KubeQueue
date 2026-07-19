'use client';

import type {
  CurrentAccess,
  Permission,
  RoleBinding,
  RoleDefinition,
  ServiceAccount,
  Team,
} from '@kubequeue/api-client';
import { useState } from 'react';

import { RoleManagement } from './role-management';
import { ServiceAccountManagement } from './service-account-management';
import { TeamManagement } from './team-management';

export function AccessView({
  access,
  initialTeams,
  initialRoles,
  initialBindings,
  initialServiceAccounts,
  loadError = '',
}: {
  access?: CurrentAccess;
  initialTeams: Team[];
  initialRoles: RoleDefinition[];
  initialBindings: RoleBinding[];
  initialServiceAccounts: ServiceAccount[];
  loadError?: string;
}) {
  const [error, setError] = useState(loadError);
  const [status, setStatus] = useState('');
  const can = (permission: Permission) =>
    Boolean(access?.permissions.some((entry) => entry.permission === permission));
  const announce = (message: string) => {
    setError('');
    setStatus(message);
  };
  const fail = (message: string) => {
    setStatus('');
    setError(message);
  };

  return (
    <main className="page-shell">
      <div className="page-title">
        <p className="eyebrow">Installation access</p>
        <h1>Access</h1>
        <p>
          Manage local teams, role assignments, and non-interactive identities. Controls reflect
          known capabilities; the API independently authorizes every request.
        </p>
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

      <section className="surface access-summary" aria-labelledby="current-access-title">
        <div>
          <h2 id="current-access-title">Current access</h2>
          {access ? (
            <p>
              <strong>{access.principal.displayName}</strong>
              <span>{access.principal.kind.replaceAll('_', ' ').toLowerCase()}</span>
              <code>{access.principal.id}</code>
            </p>
          ) : (
            <p>Current access is unavailable.</p>
          )}
        </div>
        {access ? (
          <div>
            <strong>{access.installationOwner ? 'Installation owner' : 'Delegated access'}</strong>
            <span>{access.permissions.length} effective permission scopes</span>
          </div>
        ) : null}
      </section>
      {access ? (
        <details className="surface access-disclosure">
          <summary>View effective permission scopes</summary>
          <ul className="permission-list">
            {access.permissions.map((entry) => (
              <li key={`${entry.permission}-${entry.scopeType}-${entry.projectId ?? ''}`}>
                <code>{entry.permission}</code>
                <span>
                  {entry.scopeType === 'PROJECT' ? `Project ${entry.projectId}` : 'Installation'}
                </span>
              </li>
            ))}
          </ul>
        </details>
      ) : null}

      {can('members.read') ? (
        <TeamManagement
          initialTeams={initialTeams}
          canManage={can('members.manage')}
          onError={fail}
          onStatus={announce}
        />
      ) : null}

      {can('roles.read') ? (
        <RoleManagement
          initialRoles={initialRoles}
          initialBindings={initialBindings}
          canDefine={can('roles.define')}
          canAssign={can('roles.assign')}
          onError={fail}
          onStatus={announce}
        />
      ) : null}

      {can('service-accounts.manage') || can('tokens.manage') ? (
        <ServiceAccountManagement
          initialAccounts={initialServiceAccounts}
          canManageAccounts={can('service-accounts.manage')}
          canManageCredentials={can('tokens.manage')}
          onError={fail}
          onStatus={announce}
        />
      ) : null}
    </main>
  );
}
