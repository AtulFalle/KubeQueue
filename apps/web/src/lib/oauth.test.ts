// @vitest-environment node

import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('server-only', () => ({}));

import { pkceChallenge, providerBoundState, readProviderBoundState } from './oauth';

describe('OAuth callback primitives', () => {
  beforeEach(() => {
    const values = {
      KUBEQUEUE_API_INTERNAL_URL: 'https://api.example.com',
      KUBEQUEUE_PUBLIC_URL: 'https://queue.example.com',
      KUBEQUEUE_BFF_INTERNAL_KEY: 'a'.repeat(32),
    };
    for (const [name, value] of Object.entries(values)) vi.stubEnv(name, value);
  });

  it('creates the RFC 7636 S256 challenge', () => {
    expect(pkceChallenge('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe(
      'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
    );
  });

  it('binds the selected provider to OAuth state', () => {
    const bound = providerBoundState('opaque-state', 'example');
    expect(readProviderBoundState(bound)).toEqual({
      state: 'opaque-state',
      providerId: 'example',
    });
    expect(() => readProviderBoundState(`${bound.slice(0, -1)}x`)).toThrow('INVALID_OAUTH_STATE');
  });
});
