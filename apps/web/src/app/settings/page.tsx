import { type SystemStatus } from '@kubequeue/api-client';

import { SettingsView } from '../../components/settings-view';
import { serverAPIClient } from '../../lib/server-api-client';
import { currentBrowserSession } from '../../lib/server-session';

export const dynamic = 'force-dynamic';

export default async function SettingsPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const client = await serverAPIClient();
  const session = await currentBrowserSession();
  const query = await searchParams;
  let status: SystemStatus | undefined;
  let loadFailed = false;
  try {
    status = await client.getSystemStatus();
  } catch {
    loadFailed = true;
  }
  return (
    <SettingsView
      status={status}
      loadFailed={loadFailed}
      csrfToken={session?.csrfToken}
      localSession={session?.authenticationMethod === 'LOCAL'}
      passwordResult={typeof query.result === 'string' ? query.result : undefined}
    />
  );
}
