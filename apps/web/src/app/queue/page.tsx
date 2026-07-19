import { KubeQueueClient, type Job, type SystemStatus } from '@kubequeue/api-client';

import { QueueWorkflow } from '../../components/queue-workflow';

export const dynamic = 'force-dynamic';

export default async function QueuePage() {
  const origin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';
  const client = new KubeQueueClient(
    `${origin}/api/v1`,
    process.env.KUBEQUEUE_ADMIN_TOKEN || undefined,
  );
  let initialJobs: Job[] = [];
  let initialQueueVersion = 0;
  let initialSystemStatus: SystemStatus | undefined;
  let initialLoadFailed = false;
  try {
    const [queue, status] = await Promise.all([client.getQueue(), client.getSystemStatus()]);
    initialJobs = queue.items;
    initialQueueVersion = queue.queueVersion;
    initialSystemStatus = status;
  } catch {
    initialLoadFailed = true;
  }
  return (
    <QueueWorkflow
      initialJobs={initialJobs}
      initialQueueVersion={initialQueueVersion}
      initialSystemStatus={initialSystemStatus}
      initialLoadFailed={initialLoadFailed}
    />
  );
}
