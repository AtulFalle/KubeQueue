import 'server-only';

import type { BrowserSession } from '@kubequeue/api-client';

import { getBFFConfig } from './bff-config';

export type SessionRefreshResult =
  | { status: 'active'; session: BrowserSession; refreshed: boolean }
  | { status: 'expired' }
  | { status: 'unavailable' };

export async function refreshBrowserSession(
  credential: string,
  csrfToken: string,
): Promise<SessionRefreshResult> {
  const config = getBFFConfig();
  const response = await fetch(`${config.apiOrigin}/api/v1/session/refresh`, {
    method: 'POST',
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      Authorization: `Session ${credential}`,
      Origin: config.publicOrigin,
      'X-CSRF-Token': csrfToken,
    },
  }).catch(() => undefined);
  if (!response || response.status >= 500) return { status: 'unavailable' };
  if (response.status === 401) return { status: 'expired' };
  if (!response.ok) return { status: 'unavailable' };
  const payload = (await response.json()) as BrowserSession & { refreshed?: unknown };
  return {
    status: 'active',
    session: payload,
    refreshed: payload.refreshed === true,
  };
}
