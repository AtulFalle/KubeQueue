import { KubeQueueClient, type SystemStatus } from '@kubequeue/api-client';

import { SettingsView } from '../../components/settings-view';

export const dynamic = 'force-dynamic';

export default async function SettingsPage() {
  const origin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';
  const client = new KubeQueueClient(
    `${origin}/api/v1`,
    process.env.KUBEQUEUE_ADMIN_TOKEN || undefined,
  );
  let status: SystemStatus | undefined;
  let loadFailed = false;
  try {
    status = await client.getSystemStatus();
  } catch {
    loadFailed = true;
  }
  return <SettingsView status={status} loadFailed={loadFailed} />;
}
