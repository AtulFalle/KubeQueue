// @vitest-environment node

import { afterEach, describe, expect, it, vi } from 'vitest';

import { getBFFConfig } from './bff-config';

describe('core BFF configuration', () => {
  afterEach(() => vi.unstubAllEnvs());

  it('does not require deployment-time OIDC configuration', () => {
    vi.stubEnv('KUBEQUEUE_API_INTERNAL_URL', 'http://control-plane:8080');
    vi.stubEnv('KUBEQUEUE_PUBLIC_URL', 'https://queue.example');
    vi.stubEnv('KUBEQUEUE_BFF_INTERNAL_KEY', 'a'.repeat(32));
    for (const name of [
      'KUBEQUEUE_OIDC_ISSUER',
      'KUBEQUEUE_OIDC_AUTHORIZATION_URL',
      'KUBEQUEUE_OIDC_TOKEN_URL',
      'KUBEQUEUE_OIDC_CLIENT_ID',
      'KUBEQUEUE_OIDC_CLIENT_SECRET',
      'KUBEQUEUE_OIDC_REDIRECT_URI',
      'KUBEQUEUE_OIDC_PROVIDER_ID',
    ]) {
      vi.stubEnv(name, '');
    }

    expect(getBFFConfig()).toEqual({
      apiOrigin: 'http://control-plane:8080',
      publicOrigin: 'https://queue.example',
      internalKey: 'a'.repeat(32),
    });
  });

  it('rejects a non-loopback public HTTP origin', () => {
    vi.stubEnv('KUBEQUEUE_API_INTERNAL_URL', 'http://control-plane:8080');
    vi.stubEnv('KUBEQUEUE_PUBLIC_URL', 'http://queue.example');
    vi.stubEnv('KUBEQUEUE_BFF_INTERNAL_KEY', 'a'.repeat(32));

    expect(() => getBFFConfig()).toThrow('must use HTTPS except on loopback');
  });
});
