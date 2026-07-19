import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../lib/bff-config';
import {
  oidcAuthorizationMetadata,
  oidcAuthorizationURL,
} from '../../../lib/identity-provider-data';
import { pkceChallenge, providerBoundState } from '../../../lib/oauth';
import { createOAuthLoginAttempt, safeReturnTo } from '../../../lib/server-session';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

export async function GET(request: Request) {
  const config = getBFFConfig();
  const url = new URL(request.url);
  const providerId = url.searchParams.get('provider');
  if (!providerId || !/^[a-z][a-z0-9_]{0,62}$/.test(providerId)) {
    return NextResponse.redirect(new URL('/access-denied', config.publicOrigin));
  }
  try {
    const returnTo = safeReturnTo(url.searchParams.get('returnTo'));
    const [attempt, metadata] = await Promise.all([
      createOAuthLoginAttempt(returnTo),
      oidcAuthorizationMetadata(providerId),
    ]);
    const authorization = new URL(await oidcAuthorizationURL(metadata));
    authorization.search = new URLSearchParams({
      response_type: 'code',
      client_id: metadata.clientId,
      redirect_uri: metadata.redirectUri,
      scope: metadata.scopes,
      state: providerBoundState(attempt.state, providerId),
      nonce: attempt.nonce,
      code_challenge: pkceChallenge(attempt.pkceVerifier),
      code_challenge_method: 'S256',
    }).toString();
    return NextResponse.redirect(authorization);
  } catch {
    return NextResponse.redirect(new URL('/access-denied', config.publicOrigin));
  }
}
