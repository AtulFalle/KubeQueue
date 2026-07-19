import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { Job, JobManifest } from '@kubequeue/api-client';

import { JobDetail } from './job-detail';

vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

const job: Job = {
  id: '46c9c2e2-ab4f-4bf6-9191-743d28b57412',
  projectId: 'reporting',
  name: 'daily-report',
  namespace: 'default',
  priority: 0,
  position: 1,
  desiredState: 'QUEUED',
  observedState: 'CREATED',
  managementMode: 'MANAGED',
  syncStatus: 'SYNCED',
  actionPending: false,
  attempt: 1,
  version: 1,
  createdAt: '2026-07-18T00:00:00Z',
  updatedAt: '2026-07-18T00:00:00Z',
};

const manifest: JobManifest = {
  jobId: job.id,
  manifest: {
    apiVersion: 'batch/v1',
    kind: 'Job',
    spec: { template: { spec: { containers: [{ image: 'busybox:1.36' }] } } },
  },
};

describe('JobDetail', () => {
  it('renders metadata and an independently loaded manifest', () => {
    render(
      <JobDetail
        id={job.id}
        initialJob={job}
        initialEvents={[]}
        initialManifest={manifest}
        initialLoadFailed={false}
        initialManifestLoadFailed={false}
      />,
    );

    expect(screen.getByRole('heading', { name: 'daily-report' })).toBeInTheDocument();
    expect(screen.getByText(/busybox:1\.36/)).toBeInTheDocument();
  });
});
