import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../lib/bff-config';
import {
  isAllowedMutationOrigin,
  sessionCookieOptions,
} from '../../../lib/browser-session-security';
import {
  createLocalBrowserSession,
  safeReturnTo,
  sessionCookieName,
} from '../../../lib/server-session';

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
  const form = await request.formData();
  const username = form.get('username');
  const password = form.get('password');
  const returnTo = safeReturnTo(
    typeof form.get('returnTo') === 'string' ? String(form.get('returnTo')) : '/',
  );
  if (typeof username !== 'string' || typeof password !== 'string') {
    return loginFailure(config.publicOrigin, returnTo);
  }
  try {
    const created = await createLocalBrowserSession(username, password);
    const response = NextResponse.redirect(new URL(returnTo, config.publicOrigin), 303);
    response.cookies.set(
      sessionCookieName,
      created.credential,
      sessionCookieOptions(new Date(created.session.absoluteExpiresAt)),
    );
    response.headers.set('Cache-Control', 'no-store');
    return response;
  } catch {
    return loginFailure(config.publicOrigin, returnTo);
  }
}

function loginFailure(publicOrigin: string, returnTo: string) {
  const target = new URL('/login', publicOrigin);
  target.searchParams.set('error', 'invalid_credentials');
  target.searchParams.set('returnTo', returnTo);
  const response = NextResponse.redirect(target, 303);
  response.headers.set('Cache-Control', 'no-store');
  return response;
}
