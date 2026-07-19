import 'server-only';

import type {
  BrowserSession,
  ConsumedOAuthLoginAttempt,
  CreatedBrowserSession,
  components,
  OAuthLoginAttempt,
} from '@kubequeue/api-client';
import { cookies } from 'next/headers';

import { getBFFConfig } from './bff-config';
import { refreshBrowserSession } from './session-refresh';

export const sessionCookieName = '__Host-kubequeue-session';

export async function createOAuthLoginAttempt(returnTo: string): Promise<OAuthLoginAttempt> {
  return internalRequest('/oauth/login-attempts', {
    method: 'POST',
    body: JSON.stringify({ returnTo: safeReturnTo(returnTo) }),
  });
}

export async function consumeOAuthLoginAttempt(state: string): Promise<ConsumedOAuthLoginAttempt> {
  return internalRequest('/oauth/login-attempts/consume', {
    method: 'POST',
    body: JSON.stringify({ state }),
  });
}

export async function createBrowserSession(input: {
  identityProviderId: string;
  accessToken: string;
  refreshToken?: string;
  rotateCredential?: string;
}): Promise<CreatedBrowserSession> {
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1/sessions`, {
    method: 'POST',
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      Authorization: `Bearer ${input.accessToken}`,
      'Content-Type': 'application/json',
      'X-KubeQueue-BFF-Key': config.internalKey,
    },
    body: JSON.stringify({
      identityProviderId: input.identityProviderId,
      authenticationMethod: 'OIDC',
      accessToken: input.accessToken,
      refreshToken: input.refreshToken,
      rotateCredential: input.rotateCredential,
    }),
  });
  return readResponse<CreatedBrowserSession>(response);
}

export async function createLocalBrowserSession(
  username: string,
  password: string,
): Promise<CreatedBrowserSession> {
  return internalRequest('/sessions/local', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  });
}

export async function exchangeOIDCAuthorizationCode(input: {
  identityProviderId: string;
  code: string;
  pkceVerifier: string;
  redirectUri: string;
}): Promise<components['schemas']['OIDCTokenExchange']> {
  return internalRequest('/oauth/token-exchange', {
    method: 'POST',
    body: JSON.stringify(input),
  });
}

export async function currentBrowserSession(
  suppliedCredential?: string,
): Promise<BrowserSession | undefined> {
  const credential = suppliedCredential ?? (await sessionCredential());
  if (!credential) return undefined;
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1/session`, {
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      Authorization: `Session ${credential}`,
    },
  });
  if (response.status === 401) return undefined;
  return readResponse<BrowserSession>(response);
}

export async function revokeBrowserSession(credential: string): Promise<void> {
  const session = await currentBrowserSession(credential);
  if (!session) return;
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1/session`, {
    method: 'DELETE',
    cache: 'no-store',
    headers: {
      Authorization: `Session ${credential}`,
      Origin: config.publicOrigin,
      'X-CSRF-Token': session.csrfToken,
    },
  });
  if (response.status === 401) return;
  if (!response.ok) await readResponse<never>(response);
}

export async function sessionCredential(): Promise<string | undefined> {
  const credential = (await cookies()).get(sessionCookieName)?.value;
  if (!credential) return undefined;
  let session: BrowserSession | undefined;
  try {
    session = await currentBrowserSession(credential);
  } catch {
    return credential;
  }
  if (!session) return undefined;
  const refreshed = await refreshBrowserSession(credential, session.csrfToken);
  return refreshed.status === 'expired' ? undefined : credential;
}

export function safeReturnTo(value: string | null | undefined): string {
  if (!value || !value.startsWith('/') || value.startsWith('//')) return '/';
  try {
    const parsed = new URL(value, 'https://kubequeue.invalid');
    if (parsed.origin !== 'https://kubequeue.invalid') return '/';
    return `${parsed.pathname}${parsed.search}${parsed.hash}`;
  } catch {
    return '/';
  }
}

async function internalRequest<T>(path: string, init: RequestInit): Promise<T> {
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1${path}`, {
    ...init,
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'X-KubeQueue-BFF-Key': config.internalKey,
      ...init.headers,
    },
  });
  return readResponse<T>(response);
}

async function readResponse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const payload = (await response.json().catch(() => undefined)) as
      { error?: { code?: string } } | undefined;
    throw new Error(payload?.error?.code || `BFF request failed (${response.status})`);
  }
  return (await response.json()) as T;
}
