import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { describe, expect, it, vi } from 'vitest';

import type { Job, SystemStatus } from '@kubequeue/api-client';

import { QueueWorkflow } from './queue-workflow';

function queuedJob(id: string, name: string, position: number): Job {
  return {
    id,
    projectId: 'default-project',
    name,
    namespace: 'default',
    priority: 0,
    position,
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
}

const status: SystemStatus = {
  api: { ready: true },
  database: { ready: true },
  worker: { state: 'ready' },
  watch: {
    mode: 'selected',
    effectiveNamespaces: ['default'],
    excludedNamespaces: [],
    namespaces: [{ namespace: 'default', informerSynced: true, authorized: true }],
  },
  concurrency: { global: 2, perNamespace: 1 },
  releaseVersion: '2.0.0',
  activeErrors: [],
};

describe('QueueWorkflow', () => {
  it('serializes the full order and announces the saved position', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ version: 8 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const jobs = [queuedJob('first', 'first-job', 1), queuedJob('second', 'second-job', 2)];
    const { container } = render(
      <QueueWorkflow initialJobs={jobs} initialQueueVersion={7} initialSystemStatus={status} />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Move first-job down' }));

    await waitFor(() =>
      expect(screen.getByText('first-job saved at queue position 2.')).toBeVisible(),
    );
    const request = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(JSON.parse(String(request.body))).toEqual({
      jobIds: ['second', 'first'],
      version: 7,
    });
    expect(await axe(container)).toHaveNoViolations();
  });

  it('disables ordering when the worker cannot confirm a complete queue', () => {
    render(
      <QueueWorkflow
        initialJobs={[queuedJob('first', 'first-job', 1), queuedJob('second', 'second-job', 2)]}
        initialQueueVersion={7}
        initialSystemStatus={{ ...status, worker: { state: 'degraded' } }}
      />,
    );

    expect(screen.getByRole('button', { name: 'Move first-job down' })).toBeDisabled();
    expect(screen.getByText(/Queue editing is unavailable/)).toBeInTheDocument();
  });
});
