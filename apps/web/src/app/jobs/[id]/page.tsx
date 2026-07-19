import { type Job, type JobEvent, type JobManifest } from '@kubequeue/api-client';

import { JobDetail } from '../../../components/job-detail';
import { serverAPIClient } from '../../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function JobDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const client = await serverAPIClient();
  let initialJob: Job | undefined;
  let initialEvents: JobEvent[] = [];
  let initialManifest: JobManifest | undefined;
  let initialLoadFailed = false;
  let initialManifestLoadFailed = false;
  try {
    const [job, history] = await Promise.all([client.getJob(id), client.listJobEvents(id)]);
    initialJob = job;
    initialEvents = history.items;
  } catch {
    initialLoadFailed = true;
  }
  try {
    initialManifest = await client.getJobManifest(id);
  } catch {
    initialManifestLoadFailed = true;
  }
  return (
    <JobDetail
      id={id}
      initialJob={initialJob}
      initialEvents={initialEvents}
      initialManifest={initialManifest}
      initialLoadFailed={initialLoadFailed}
      initialManifestLoadFailed={initialManifestLoadFailed}
    />
  );
}
