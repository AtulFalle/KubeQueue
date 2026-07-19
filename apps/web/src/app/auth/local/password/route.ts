import type { CreatedBrowserSession } from '@kubequeue/api-client';
import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../../lib/bff-config';
import {
  isAllowedMutationOrigin,
  sessionCookieOptions,
} from '../../../../lib/browser-session-security';
import { sessionCookieName, sessionCredential } from '../../../../lib/server-session';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

export async function POST(request: Request) {
  const config = getBFFConfig();
  if (!isAllowedMutationOrigin(request, config.publicOrigin)) {
    return NextResponse.json(
      { error: { code: 'INVALID_ORIGIN', message: 'Request origin is not allowed', status: 403 } },
      { status: 403 },
    );
  }
  const credential = await sessionCredential();
  const form = await request.formData();
  const currentPassword = form.get('currentPassword');
  const newPassword = form.get('newPassword');
  const confirmation = form.get('passwordConfirmation');
  const csrfToken = form.get('csrfToken');
  if (
    !credential ||
    typeof currentPassword !== 'string' ||
    typeof newPassword !== 'string' ||
    newPassword !== confirmation ||
    typeof csrfToken !== 'string'
  ) {
    return settingsRedirect(config.publicOrigin, 'password_invalid');
  }
  const upstream = await fetch(`${config.apiOrigin}/api/v1/local-account/password`, {
    method: 'PUT',
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      Authorization: `Session ${credential}`,
      'Content-Type': 'application/json',
      Origin: config.publicOrigin,
      'X-CSRF-Token': csrfToken,
    },
    body: JSON.stringify({ currentPassword, newPassword }),
  });
  if (!upstream.ok) return settingsRedirect(config.publicOrigin, 'password_failed');
  const created = (await upstream.json()) as CreatedBrowserSession;
  const response = settingsRedirect(config.publicOrigin, 'password_changed');
  response.cookies.set(
    sessionCookieName,
    created.credential,
    sessionCookieOptions(new Date(created.session.absoluteExpiresAt)),
  );
  return response;
}

function settingsRedirect(publicOrigin: string, result: string) {
  const target = new URL('/settings', publicOrigin);
  target.searchParams.set('result', result);
  const response = NextResponse.redirect(target, 303);
  response.headers.set('Cache-Control', 'no-store');
  return response;
}
