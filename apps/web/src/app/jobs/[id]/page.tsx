import { KubeQueueClient, type Job, type JobEvent } from '@kubequeue/api-client';

import { JobDetail } from '../../../components/job-detail';

export const dynamic = 'force-dynamic';

export default async function JobDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const origin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';
  const client = new KubeQueueClient(
    `${origin}/api/v1`,
    process.env.KUBEQUEUE_ADMIN_TOKEN || undefined,
  );
  let initialJob: Job | undefined;
  let initialEvents: JobEvent[] = [];
  let initialLoadFailed = false;
  try {
    const [job, history] = await Promise.all([client.getJob(id), client.listJobEvents(id)]);
    initialJob = job;
    initialEvents = history.items;
  } catch {
    initialLoadFailed = true;
  }
  return (
    <JobDetail
      id={id}
      initialJob={initialJob}
      initialEvents={initialEvents}
      initialLoadFailed={initialLoadFailed}
    />
  );
}
