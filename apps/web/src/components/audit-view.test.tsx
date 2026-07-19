import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { describe, expect, it, vi } from 'vitest';

import type { AuditEvent } from '@kubequeue/api-client';

import { AuditView } from './audit-view';

const auditEvent: AuditEvent = {
  id: 'event-1',
  occurredAt: '2026-07-19T10:00:00Z',
  requestId: 'request-1',
  traceId: 'trace-1',
  actor: {
    principalId: 'principal-1',
    authenticationMethod: 'OIDC_SESSION',
    credentialId: 'credential-1',
    effectiveGroups: ['operators'],
  },
  action: 'jobs.submit',
  target: { type: 'job', id: 'job-1' },
  scope: { installationId: 'installation-1', projectId: 'research' },
  decision: 'ALLOW',
  result: 'SUCCESS',
  reason: 'authorized',
  source: {
    address: '192.0.2.1',
    provenance: 'DIRECT_PEER',
    userAgent: 'KubeQueue test',
  },
  before: {
    state: 'CREATED',
    changedFields: ['state'],
    redactionCount: 1,
    truncated: false,
  },
  after: {
    state: 'QUEUED',
    changedFields: ['state'],
    redactionCount: 1,
    truncated: false,
  },
};

const baseProps = {
  installationId: 'installation-1',
  canRead: true,
  canExport: true,
  initialEvents: [auditEvent],
  initialNextCursor: 'next-page',
};

describe('AuditView', () => {
  it('renders bounded controls and capability-aware actions accessibly', async () => {
    const { container, rerender } = render(<AuditView {...baseProps} />);

    expect(screen.getByLabelText('Results per page')).toHaveValue('50');
    expect(screen.getByLabelText('Project ID')).toHaveAttribute('maxlength', '128');
    expect(screen.getByRole('link', { name: 'Download filtered events' })).toHaveAttribute(
      'href',
      '/audit/download',
    );
    expect(screen.getByRole('row', { name: /jobs.submit principal-1 job/ })).toBeVisible();
    expect(await axe(container)).toHaveNoViolations();

    rerender(<AuditView {...baseProps} canExport={false} />);
    expect(
      screen.queryByRole('link', { name: 'Download filtered events' }),
    ).not.toBeInTheDocument();
  });

  it('loads one ordered page at a time and keeps filters for export', async () => {
    const nextEvent = { ...auditEvent, id: 'event-2', action: 'jobs.pause' };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ items: [nextEvent], nextCursor: null }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );
    render(<AuditView {...baseProps} />);

    fireEvent.change(screen.getByLabelText('Project ID'), { target: { value: 'research' } });
    fireEvent.change(screen.getByLabelText('Decision'), { target: { value: 'ALLOW' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));

    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('1 audit events loaded'),
    );
    expect(fetch).toHaveBeenCalledWith(
      expect.stringMatching(
        /^\/api\/v1\/audit\/events\?installationId=installation-1&projectId=research&decision=ALLOW&limit=50$/,
      ),
      expect.objectContaining({ cache: 'no-store' }),
    );
    expect(screen.getByRole('link', { name: 'Download filtered events' })).toHaveAttribute(
      'href',
      '/audit/download?projectId=research&decision=ALLOW',
    );
    expect(screen.getByRole('row', { name: /jobs.pause/ })).toBeVisible();
  });

  it('moves focus into event details, closes with Escape, and restores focus', async () => {
    render(<AuditView {...baseProps} />);
    const trigger = screen.getByRole('button', { name: 'View details' });

    fireEvent.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'jobs.submit' });
    const close = screen.getByRole('button', { name: 'Close details' });
    await waitFor(() => expect(close).toHaveFocus());
    expect(screen.getByText('Before summary')).toBeVisible();

    fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it('hides the audit controls when read capability is absent', async () => {
    const { container } = render(<AuditView {...baseProps} canRead={false} />);

    expect(screen.getByText(/do not have a visible capability/)).toBeVisible();
    expect(screen.queryByRole('form')).not.toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });
});
