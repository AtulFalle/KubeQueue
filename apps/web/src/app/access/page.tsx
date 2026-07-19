import type {
  CurrentAccess,
  RoleBinding,
  RoleDefinition,
  ServiceAccount,
  Team,
} from '@kubequeue/api-client';

import { AccessView } from '../../components/access-view';
import { serverAPIClient } from '../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function AccessPage() {
  const client = await serverAPIClient();
  let access: CurrentAccess | undefined;
  let teams: Team[] = [];
  let roles: RoleDefinition[] = [];
  let bindings: RoleBinding[] = [];
  let serviceAccounts: ServiceAccount[] = [];
  let loadError = '';

  try {
    access = await client.getCurrentAccess();
    const can = (permission: CurrentAccess['permissions'][number]['permission']) =>
      access?.permissions.some((entry) => entry.permission === permission);
    const [teamPage, rolePage, bindingPage, accountPage] = await Promise.all([
      can('members.read') ? client.listTeams() : undefined,
      can('roles.read') ? client.listRoleDefinitions() : undefined,
      can('roles.read') ? client.listRoleBindings() : undefined,
      can('service-accounts.manage') || can('tokens.manage')
        ? client.listServiceAccounts()
        : undefined,
    ]);
    teams = teamPage?.items ?? [];
    roles = rolePage?.items ?? [];
    bindings = bindingPage?.items ?? [];
    serviceAccounts = accountPage?.items ?? [];
  } catch (reason) {
    loadError = reason instanceof Error ? reason.message : 'Unable to load access management';
  }

  return (
    <AccessView
      access={access}
      initialTeams={teams}
      initialRoles={roles}
      initialBindings={bindings}
      initialServiceAccounts={serviceAccounts}
      loadError={loadError}
    />
  );
}
