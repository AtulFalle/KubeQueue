import { fireEvent, render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { Job } from '@kubequeue/api-client';

import { Dashboard } from './dashboard';

const replace = vi.fn();

vi.mock('next/navigation', () => ({
  usePathname: () => '/',
  useRouter: () => ({ replace }),
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
  template: { spec: {} },
  attempt: 1,
  version: 1,
  createdAt: '2026-07-18T00:00:00Z',
  updatedAt: '2026-07-18T00:00:00Z',
};

describe('Dashboard', () => {
  beforeEach(() => {
    replace.mockClear();
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
    const { container } = render(<Dashboard initialJobs={[job]} initialQueueVersion={1} />);

    expect(screen.getByRole('link', { name: 'daily-report' })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it('stores filters in the URL', async () => {
    render(<Dashboard initialJobs={[job]} initialQueueVersion={1} />);

    fireEvent.change(screen.getByPlaceholderText('Job name'), { target: { value: 'report' } });

    expect(replace).toHaveBeenLastCalledWith('/?search=report', { scroll: false });
  });
});
