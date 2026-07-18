import { expect, test, type APIRequestContext } from '@playwright/test';

test.describe.configure({ mode: 'serial' });
test.setTimeout(120_000);

const template = (command: string) => ({
  apiVersion: 'batch/v1',
  kind: 'Job',
  spec: {
    template: {
      spec: {
        restartPolicy: 'Never',
        containers: [{ name: 'job', image: 'busybox:1.36', command: ['sh', '-c', command] }],
      },
    },
  },
});

async function createJob(
  request: APIRequestContext,
  name: string,
  command: string,
  scheduledFor?: string,
) {
  const response = await request.post('/api/v1/jobs', {
    data: { name, namespace: 'default', template: template(command), scheduledFor },
  });
  expect(response.status()).toBe(201);
  return response.json() as Promise<{ id: string; version: number }>;
}

async function waitForState(
  request: APIRequestContext,
  id: string,
  expected: string[],
) {
  await expect.poll(async () => {
    const response = await request.get(`/api/v1/jobs/${id}`);
    if (!response.ok()) return false;
    const job = await response.json() as { observedState: string };
    return expected.includes(job.observedState);
  }, { timeout: 90_000 }).toBeTruthy();
}

test('reorders delayed work and creates immutable retry lineage', async ({ request }) => {
  const suffix = Date.now();
  const scheduledFor = new Date(Date.now() + 60 * 60 * 1000).toISOString();
  const first = await createJob(request, `e2e-delayed-a-${suffix}`, 'echo a', scheduledFor);
  const second = await createJob(request, `e2e-delayed-b-${suffix}`, 'echo b', scheduledFor);
  const list = await request.get('/api/v1/jobs?status=QUEUED');
  const jobs = await list.json() as {
    items: Array<{ id: string; version: number }>;
    queueVersion: number;
  };
  const selected = jobs.items.filter((job) => job.id === first.id || job.id === second.id);

  const reorder = await request.put('/api/v1/queue/order', {
    data: { jobIds: selected.map((job) => job.id).reverse(), version: jobs.queueVersion },
  });
  expect(reorder.ok()).toBeTruthy();
  const terminate = await request.post(`/api/v1/jobs/${first.id}/actions/terminate`);
  expect(terminate.ok()).toBeTruthy();
  const retry = await request.post(`/api/v1/jobs/${first.id}/actions/retry`);
  expect(retry.ok()).toBeTruthy();
  const retried = await retry.json() as { parentId?: string; attempt: number };
  expect(retried.parentId).toBe(first.id);
  expect(retried.attempt).toBe(2);
});

test('pauses, resumes, and terminates running work', async ({ request }) => {
  const job = await createJob(request, `e2e-lifecycle-${Date.now()}`, 'sleep 300');
  await waitForState(request, job.id, ['RUNNING']);

  expect((await request.post(`/api/v1/jobs/${job.id}/actions/pause`)).ok()).toBeTruthy();
  await waitForState(request, job.id, ['PAUSED']);
  expect((await request.post(`/api/v1/jobs/${job.id}/actions/resume`)).ok()).toBeTruthy();
  await waitForState(request, job.id, ['RUNNING']);
  expect((await request.post(`/api/v1/jobs/${job.id}/actions/terminate`)).ok()).toBeTruthy();
  await waitForState(request, job.id, ['CANCELLED']);
});
