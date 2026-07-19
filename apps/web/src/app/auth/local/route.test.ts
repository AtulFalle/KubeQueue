// @vitest-environment node

import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('server-only', () => ({}));
vi.mock('../../../lib/bff-config', () => ({
  getBFFConfig: () => ({
    apiOrigin: 'http://control-plane.test',
    publicOrigin: 'https://queue.example',
    internalKey: 'a'.repeat(32),
  }),
}));
const mocks = vi.hoisted(() => ({ createLocalBrowserSession: vi.fn() }));
vi.mock('../../../lib/server-session', () => {
  return {
    createLocalBrowserSession: mocks.createLocalBrowserSession,
    safeReturnTo: (value: string) => value,
    sessionCookieName: '__Host-kubequeue-session',
  };
});

import { POST } from './route';

describe('local login route', () => {
  beforeEach(() => {
    mocks.createLocalBrowserSession.mockReset();
    mocks.createLocalBrowserSession.mockResolvedValue({
      credential: 'opaque-session',
      session: { absoluteExpiresAt: '2026-07-20T00:00:00Z' },
    });
  });

  it('exchanges credentials server-side and returns only a host cookie redirect', async () => {
    const response = await POST(loginRequest('admin', 'not-returned'));
    expect(mocks.createLocalBrowserSession).toHaveBeenCalledWith('admin', 'not-returned');
    expect(response.status).toBe(303);
    expect(response.headers.get('location')).toBe('https://queue.example/');
    expect(response.headers.get('set-cookie')).toContain('__Host-kubequeue-session=opaque-session');
    expect(await response.text()).not.toContain('not-returned');
  });

  it('rejects cross-origin credential submission before exchange', async () => {
    const request = loginRequest('admin', 'secret', 'https://evil.example');
    const response = await POST(request);
    expect(response.status).toBe(403);
    expect(mocks.createLocalBrowserSession).not.toHaveBeenCalled();
  });
});

function loginRequest(username: string, password: string, origin = 'https://queue.example') {
  return new Request('https://queue.example/auth/local', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      Origin: origin,
    },
    body: new URLSearchParams({ username, password, returnTo: '/' }),
  });
}
