import { KubeQueueClient, type Job, type JobFilters } from '@kubequeue/api-client';

import { Dashboard } from '../components/dashboard';

export const dynamic = 'force-dynamic';

type HomePageProps = {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
};

export default async function HomePage({ searchParams }: HomePageProps) {
  const query = await searchParams;
  const filters: JobFilters = {
    status: typeof query.status === 'string' ? (query.status as JobFilters['status']) : undefined,
    namespace: typeof query.namespace === 'string' ? query.namespace : undefined,
    team: typeof query.team === 'string' ? query.team : undefined,
    search: typeof query.search === 'string' ? query.search : undefined,
    priority: typeof query.priority === 'string' ? Number(query.priority) : undefined,
  };
  const origin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';
  const client = new KubeQueueClient(
    `${origin}/api/v1`,
    process.env.KUBEQUEUE_ADMIN_TOKEN || undefined,
  );
  let initialJobs: Job[] = [];
  let initialQueueVersion = 0;
  let initialLoadFailed = false;
  try {
    const response = await client.listJobs(filters);
    initialJobs = response.items;
    initialQueueVersion = response.queueVersion;
  } catch {
    initialLoadFailed = true;
  }
  return (
    <Dashboard
      initialJobs={initialJobs}
      initialQueueVersion={initialQueueVersion}
      initialLoadFailed={initialLoadFailed}
    />
  );
}
