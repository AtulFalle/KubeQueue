import type { components, operations } from './generated';

export type { components, operations, paths } from './generated';

export const apiContractVersion = '1.0.0' as const;

export type JobState = components['schemas']['JobState'];
export type Job = components['schemas']['Job'];
export type JobList = components['schemas']['JobList'];
export type JobEvent = components['schemas']['JobEvent'];
type GeneratedCreateJob = components['schemas']['CreateJob'];
export type CreateJob = Omit<GeneratedCreateJob, 'priority'> & {
  priority?: GeneratedCreateJob['priority'];
};
export type JobAction = operations['commandJob']['parameters']['path']['action'];
export type JobFilters = NonNullable<operations['listJobs']['parameters']['query']>;
export type QueueUpdate =
  operations['updateQueuedJob']['requestBody']['content']['application/json'];

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export class KubeQueueClient {
  constructor(
    private readonly baseUrl = '/api/v1',
    private readonly token?: string,
  ) {}

  listJobs(filters: JobFilters = {}) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) {
      if (value !== undefined && value !== '') query.set(key, String(value));
    }
    return this.request<JobList>(`/jobs?${query}`);
  }

  getJob(id: string) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}`);
  }

  listJobEvents(id: string) {
    return this.request<{ items: JobEvent[] }>(`/jobs/${encodeURIComponent(id)}/events`);
  }

  createJob(input: CreateJob) {
    return this.request<Job>('/jobs', { method: 'POST', body: JSON.stringify(input) });
  }

  command(id: string, action: JobAction) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}/actions/${action}`, {
      method: 'POST',
    });
  }

  updateQueue(id: string, input: QueueUpdate) {
    return this.request<Job>(`/jobs/${encodeURIComponent(id)}/queue`, {
      method: 'PATCH',
      body: JSON.stringify(input),
    });
  }

  reorder(jobIds: string[], version = 0) {
    return this.request<{ version: number }>('/queue/order', {
      method: 'PUT',
      body: JSON.stringify({ jobIds, version }),
    });
  }

  eventsUrl() {
    return `${this.baseUrl}/events`;
  }

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      ...init,
      cache: 'no-store',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
        ...init.headers,
      },
    });
    if (!response.ok) {
      const payload = (await response.json().catch(() => undefined)) as
        { error?: { code?: string; message?: string } } | undefined;
      throw new ApiError(
        response.status,
        payload?.error?.code ?? 'REQUEST_FAILED',
        payload?.error?.message ?? `Request failed with status ${response.status}`,
      );
    }
    return (await response.json()) as T;
  }
}
