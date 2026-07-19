import { fireEvent, render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { Job, JobFacets, SystemStatus } from '@kubequeue/api-client';

import { Dashboard } from './dashboard';

const replace = vi.fn();
const push = vi.fn();

vi.mock('next/navigation', () => ({
  usePathname: () => '/',
  useRouter: () => ({ replace, push }),
  useSearchParams: () => new URLSearchParams(),
}));

const job: Job = {
  id: '46c9c2e2-ab4f-4bf6-9191-743d28b57412',
  name: 'daily-report',
  namespace: 'default',
  team: 'data',
  priority: 10,
  position: 1,
  desiredState: 'QUEUED',
  observedState: 'CREATED',
  managementMode: 'MANAGED',
  syncStatus: 'SYNCED',
  actionPending: false,
  template: { spec: {} },
  attempt: 1,
  version: 1,
  createdAt: '2026-07-18T00:00:00Z',
  updatedAt: '2026-07-18T00:00:00Z',
};
const facets: JobFacets = {
  total: 8,
  observedStateCounts: { RUNNING: 3, COMPLETED: 2, FAILED: 1 },
  namespaces: ['default', 'reports'],
  teams: ['data'],
};
const systemStatus: SystemStatus = {
  api: { ready: true },
  database: { ready: true },
  worker: { state: 'ready' },
  watch: {
    mode: 'selected',
    effectiveNamespaces: ['default', 'reports'],
    excludedNamespaces: [],
    namespaces: [
      { namespace: 'default', informerSynced: true, authorized: true },
      { namespace: 'reports', informerSynced: true, authorized: true },
    ],
  },
  concurrency: { global: 4, perNamespace: 2 },
  releaseVersion: '2.0.0',
  activeErrors: [],
};

describe('Dashboard', () => {
  beforeEach(() => {
    replace.mockClear();
    push.mockClear();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [job],
            count: 1,
            queueVersion: 1,
          }),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    );
  });

  it('renders the inventory without accessibility violations', async () => {
    const { container } = render(
      <Dashboard
        initialJobs={[job]}
        initialQueueVersion={1}
        initialFacets={facets}
        initialSystemStatus={systemStatus}
      />,
    );

    expect(screen.getByRole('link', { name: 'daily-report' })).toBeInTheDocument();
    expect(screen.getByText('08')).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'reports' })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it('stores filters in the URL', async () => {
    render(
      <Dashboard
        initialJobs={[job]}
        initialQueueVersion={1}
        initialFacets={facets}
        initialSystemStatus={systemStatus}
      />,
    );

    fireEvent.change(screen.getByPlaceholderText('Job name'), { target: { value: 'report' } });

    expect(replace).toHaveBeenLastCalledWith('/?search=report', { scroll: false });
  });
});
