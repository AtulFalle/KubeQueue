import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ServiceAccount, ServiceAccountCredential } from '@kubequeue/api-client';

import { ServiceAccountManagement } from './service-account-management';

const account: ServiceAccount = {
  principalId: 'robot-1',
  installationId: 'installation-1',
  displayName: 'Release robot',
  createdByPrincipalId: 'principal-1',
  createdAt: '2026-07-19T00:00:00Z',
};

const credential: ServiceAccountCredential = {
  id: '10000000-0000-4000-8000-000000000000',
  serviceAccountPrincipalId: account.principalId,
  safePrefix: 'kq_live_abcd',
  permissions: ['jobs.submit'],
  status: 'ACTIVE',
  expiresAt: '2026-08-19T00:00:00Z',
  createdAt: '2026-07-19T00:00:00Z',
};

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn();
  HTMLDialogElement.prototype.close = vi.fn();
});

describe('ServiceAccountManagement', () => {
  it('shows plaintext once and clears it before the next action', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ items: [], nextCursor: null }))
      .mockResolvedValueOnce(jsonResponse({ credential, token: 'kq_secret_once' }, 201))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    const onStatus = vi.fn();
    const { container } = render(
      <ServiceAccountManagement
        initialAccounts={[account]}
        canManageAccounts
        canManageCredentials
        onError={vi.fn()}
        onStatus={onStatus}
      />,
    );

    fireEvent.change(screen.getByLabelText('Manage account'), {
      target: { value: account.principalId },
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getAllByLabelText('Permissions, comma separated')[0]!, {
      target: { value: 'jobs.submit' },
    });
    fireEvent.change(screen.getAllByLabelText('Expires at')[0]!, {
      target: { value: '2026-08-19T00:00' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Issue credential' }));

    await waitFor(() => expect(screen.getByText('kq_secret_once')).toBeInTheDocument());
    expect(window.localStorage).toHaveLength(0);
    expect(window.sessionStorage).toHaveLength(0);
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/service-accounts/robot-1/credentials');

    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(screen.queryByText('kq_secret_once')).not.toBeInTheDocument());
    expect(await axe(container)).toHaveNoViolations();
  });
});

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
