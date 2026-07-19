import 'server-only';

import type { components } from '@kubequeue/api-client';

import { getBFFConfig } from './bff-config';

export type LoginMethod = components['schemas']['LoginMethod'];
export type LoginMethodList = components['schemas']['LoginMethodList'];

export type OIDCAuthorizationMetadata = {
  id: string;
  issuer: string;
  clientId: string;
  redirectUri: string;
  scopes: string;
};

export async function enabledLoginMethods(): Promise<LoginMethod[]> {
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1/login-methods`, {
    cache: 'no-store',
    headers: { Accept: 'application/json' },
  });
  return (await boundedJSON<LoginMethodList>(response, 'LOGIN_METHODS_UNAVAILABLE')).items;
}

export async function oidcAuthorizationMetadata(
  providerId: string,
): Promise<OIDCAuthorizationMetadata> {
  const config = getBFFConfig();
  const response = await fetch(
    `${config.apiOrigin}/api/v1/oauth/providers/${encodeURIComponent(providerId)}`,
    {
      cache: 'no-store',
      headers: {
        Accept: 'application/json',
        'X-KubeQueue-BFF-Key': config.internalKey,
      },
    },
  );
  return boundedJSON<OIDCAuthorizationMetadata>(response, 'OIDC_PROVIDER_UNAVAILABLE');
}

export async function oidcAuthorizationURL(metadata: OIDCAuthorizationMetadata): Promise<string> {
  const response = await fetch(`${metadata.issuer}/.well-known/openid-configuration`, {
    cache: 'no-store',
    headers: { Accept: 'application/json' },
    signal: AbortSignal.timeout(5_000),
  });
  const document = await boundedJSON<{ issuer?: unknown; authorization_endpoint?: unknown }>(
    response,
    'OIDC_DISCOVERY_UNAVAILABLE',
  );
  if (document.issuer !== metadata.issuer || typeof document.authorization_endpoint !== 'string') {
    throw new Error('OIDC_DISCOVERY_INVALID');
  }
  const authorization = new URL(document.authorization_endpoint);
  const issuer = new URL(metadata.issuer);
  if (
    authorization.username ||
    authorization.password ||
    (authorization.protocol !== 'https:' && issuer.protocol === 'https:')
  ) {
    throw new Error('OIDC_DISCOVERY_INVALID');
  }
  return authorization.toString();
}

async function boundedJSON<T>(response: Response, fallbackCode: string): Promise<T> {
  if (!response.ok) {
    const payload = (await response.json().catch(() => undefined)) as
      { error?: { code?: string } } | undefined;
    throw new Error(payload?.error?.code || fallbackCode);
  }
  const text = await response.text();
  if (text.length > 65_536) throw new Error(fallbackCode);
  return JSON.parse(text) as T;
}
