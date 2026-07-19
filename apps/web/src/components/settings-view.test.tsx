import type { SystemStatus } from '@kubequeue/api-client';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { describe, expect, it, vi } from 'vitest';

import { SettingsView } from './settings-view';

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
  concurrency: { global: 10, perNamespace: 4 },
  releaseVersion: '3.0.0',
  activeErrors: [],
};

const provider = {
  id: 'corporate',
  type: 'OIDC',
  displayName: 'Corporate SSO',
  issuer: 'https://id.example.com',
  audience: 'kubequeue',
  clientId: 'web',
  clientSecretConfigured: true,
  redirectUri: 'https://queue.example/auth/callback',
  allowedAlgorithms: ['RS256'],
  groupsClaim: 'groups',
  emailClaim: 'email',
  nameClaim: 'name',
  cacheTtlSeconds: 300,
  state: 'DISABLED',
  testResult: { status: 'NOT_TESTED' },
  version: 7,
  createdAt: '2026-07-19T00:00:00Z',
  updatedAt: '2026-07-19T00:00:00Z',
};

describe('SettingsView identity providers', () => {
  it('keeps secrets write-only and sends ETag plus CSRF on test', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ items: [provider] }))
      .mockResolvedValueOnce(json({ ...provider, version: 8, testResult: { status: 'PASSED' } }))
      .mockResolvedValueOnce(json({ items: [{ ...provider, version: 8 }] }));
    vi.stubGlobal('fetch', fetchMock);
    const { container } = render(
      <SettingsView status={status} csrfToken="csrf-value" localSession />,
    );

    expect(await screen.findByRole('heading', { name: 'Corporate SSO' })).toBeInTheDocument();
    expect(screen.queryByDisplayValue(/secret/i)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Test' }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'If-Match': '"7"',
        'X-CSRF-Token': 'csrf-value',
      },
    });
    expect(await axe(container)).toHaveNoViolations();
  });
});

function json(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}
