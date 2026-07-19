import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { Job } from '@kubequeue/api-client';

import { LifecycleActions } from './lifecycle-actions';

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

describe('LifecycleActions', () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute('open', '');
    });
  });

  it('applies the command response immediately', async () => {
    const updated = { ...job, desiredState: 'PAUSED' as const, actionPending: true };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(updated), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );
    const onUpdated = vi.fn();
    render(<LifecycleActions job={job} onUpdated={onUpdated} onError={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Pause' }));

    await waitFor(() => expect(onUpdated).toHaveBeenCalledWith(updated, 'pause'));
  });

  it('uses a dialog only for destructive termination', () => {
    render(<LifecycleActions job={job} onUpdated={vi.fn()} onError={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Terminate' }));

    expect(HTMLDialogElement.prototype.showModal).toHaveBeenCalledOnce();
    expect(screen.getByRole('heading', { name: 'Terminate daily-report?' })).toBeInTheDocument();
  });

  it('disables lifecycle controls for observed jobs', () => {
    render(
      <LifecycleActions
        job={{ ...job, managementMode: 'OBSERVED' }}
        onUpdated={vi.fn()}
        onError={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled();
    expect(screen.getByText('Observed jobs are read-only')).toBeInTheDocument();
  });
});
