import { cookies } from 'next/headers';
import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../lib/bff-config';
import { sessionCookieOptions } from '../../../lib/browser-session-security';
import { oidcAuthorizationMetadata } from '../../../lib/identity-provider-data';
import { readProviderBoundState } from '../../../lib/oauth';
import {
  consumeOAuthLoginAttempt,
  createBrowserSession,
  exchangeOIDCAuthorizationCode,
  safeReturnTo,
  sessionCookieName,
} from '../../../lib/server-session';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

export async function GET(request: Request) {
  const config = getBFFConfig();
  const url = new URL(request.url);
  const code = url.searchParams.get('code');
  const state = url.searchParams.get('state');
  if (!code || !state || url.searchParams.has('error')) {
    return NextResponse.redirect(new URL('/access-denied', config.publicOrigin));
  }
  try {
    const boundState = readProviderBoundState(state);
    const attempt = await consumeOAuthLoginAttempt(boundState.state);
    const metadata = await oidcAuthorizationMetadata(boundState.providerId);
    const tokens = await exchangeOIDCAuthorizationCode({
      identityProviderId: boundState.providerId,
      code,
      pkceVerifier: attempt.pkceVerifier,
      redirectUri: metadata.redirectUri,
    });
    const cookieStore = await cookies();
    const previousCredential = cookieStore.get(sessionCookieName)?.value;
    const created = await createBrowserSession({
      identityProviderId: tokens.identityProviderId,
      accessToken: tokens.accessToken,
      refreshToken: tokens.refreshToken,
      rotateCredential: previousCredential,
    });
    const response = NextResponse.redirect(
      new URL(safeReturnTo(attempt.returnTo), config.publicOrigin),
    );
    response.cookies.set(
      sessionCookieName,
      created.credential,
      sessionCookieOptions(new Date(created.session.absoluteExpiresAt)),
    );
    response.headers.set('Cache-Control', 'no-store');
    return response;
  } catch {
    return NextResponse.redirect(new URL('/access-denied', config.publicOrigin));
  }
}
