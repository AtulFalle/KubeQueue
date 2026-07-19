// @vitest-environment node

import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('server-only', () => ({}));

import { refreshBrowserSession } from './session-refresh';

describe('browser session refresh bridge', () => {
  beforeEach(() => {
    const values = {
      KUBEQUEUE_API_INTERNAL_URL: 'https://api.example.com',
      KUBEQUEUE_PUBLIC_URL: 'https://queue.example.com',
      KUBEQUEUE_OIDC_ISSUER: 'https://id.example.com',
      KUBEQUEUE_OIDC_AUTHORIZATION_URL: 'https://id.example.com/authorize',
      KUBEQUEUE_OIDC_TOKEN_URL: 'https://id.example.com/token',
      KUBEQUEUE_OIDC_CLIENT_ID: 'kubequeue',
      KUBEQUEUE_OIDC_CLIENT_SECRET: 'secret',
      KUBEQUEUE_OIDC_REDIRECT_URI: 'https://queue.example.com/auth/callback',
      KUBEQUEUE_OIDC_PROVIDER_ID: 'example',
      KUBEQUEUE_BFF_INTERNAL_KEY: 'a'.repeat(32),
    };
    for (const [name, value] of Object.entries(values)) vi.stubEnv(name, value);
  });

  it('invokes the API refresh operation with only session and CSRF credentials', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      Response.json({
        principalId: 'person',
        installationId: 'default',
        authenticationMethod: 'OIDC',
        csrfToken: 'csrf',
        idleExpiresAt: '2026-07-19T13:00:00Z',
        absoluteExpiresAt: '2026-07-19T20:00:00Z',
        refreshed: true,
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(refreshBrowserSession('session-credential', 'csrf')).resolves.toMatchObject({
      status: 'active',
      refreshed: true,
    });
    expect(fetchMock).toHaveBeenCalledWith(
      'https://api.example.com/api/v1/session/refresh',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({
          Authorization: 'Session session-credential',
          Origin: 'https://queue.example.com',
          'X-CSRF-Token': 'csrf',
        }),
      }),
    );
    const request = fetchMock.mock.lastCall?.[1] as RequestInit | undefined;
    expect(request).toBeDefined();
    expect(request).toMatchObject({ cache: 'no-store' });
    expect(request).not.toHaveProperty('body');
  });

  it('maps definitive rejection to an expired session', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 401 })));

    await expect(refreshBrowserSession('session-credential', 'csrf')).resolves.toEqual({
      status: 'expired',
    });
  });

  it('keeps transient provider failure bounded to the control plane', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 503 })));

    await expect(refreshBrowserSession('session-credential', 'csrf')).resolves.toEqual({
      status: 'unavailable',
    });
  });
});
