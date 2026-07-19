import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../lib/bff-config';
import {
  clearedSessionCookieOptions,
  isAllowedMutationOrigin,
} from '../../../lib/browser-session-security';
import { sessionCookieName, sessionCredential } from '../../../lib/server-session';

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
  const csrfToken = form.get('csrfToken');
  if (!credential || typeof csrfToken !== 'string') {
    return NextResponse.redirect(new URL('/session-expired', config.publicOrigin), 303);
  }
  const response = await fetch(`${config.apiOrigin}/api/v1/session`, {
    method: 'DELETE',
    cache: 'no-store',
    headers: {
      Authorization: `Session ${credential}`,
      Origin: config.publicOrigin,
      'X-CSRF-Token': csrfToken,
    },
  });
  if (!response.ok && response.status !== 401) {
    return NextResponse.json(
      { error: { code: 'LOGOUT_FAILED', message: 'Session could not be revoked', status: 503 } },
      { status: 503 },
    );
  }
  const redirect = NextResponse.redirect(new URL('/login', config.publicOrigin), 303);
  redirect.cookies.set(sessionCookieName, '', clearedSessionCookieOptions());
  return redirect;
}
