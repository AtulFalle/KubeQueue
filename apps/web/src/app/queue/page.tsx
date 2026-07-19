import { type Job, type SystemStatus } from '@kubequeue/api-client';

import { QueueWorkflow } from '../../components/queue-workflow';
import { serverAPIClient } from '../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function QueuePage() {
  const client = await serverAPIClient();
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
