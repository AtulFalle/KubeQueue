import 'server-only';

import { createHash, createHmac, timingSafeEqual } from 'node:crypto';

import { getBFFConfig } from './bff-config';

export function pkceChallenge(verifier: string): string {
  return createHash('sha256').update(verifier).digest('base64url');
}

export function providerBoundState(state: string, providerId: string): string {
  const encodedProvider = Buffer.from(providerId).toString('base64url');
  const value = `${state}.${encodedProvider}`;
  const signature = createHmac('sha256', getBFFConfig().internalKey)
    .update(value)
    .digest('base64url');
  return `${value}.${signature}`;
}

export function readProviderBoundState(value: string): { state: string; providerId: string } {
  const [state, encodedProvider, signature, ...extra] = value.split('.');
  if (!state || !encodedProvider || !signature || extra.length > 0)
    throw new Error('INVALID_OAUTH_STATE');
  const signed = `${state}.${encodedProvider}`;
  const expected = createHmac('sha256', getBFFConfig().internalKey).update(signed).digest();
  const provided = Buffer.from(signature, 'base64url');
  if (provided.length !== expected.length || !timingSafeEqual(provided, expected)) {
    throw new Error('INVALID_OAUTH_STATE');
  }
  const providerId = Buffer.from(encodedProvider, 'base64url').toString('utf8');
  if (!/^[a-z][a-z0-9_]{0,62}$/.test(providerId)) throw new Error('INVALID_OAUTH_STATE');
  return { state, providerId };
}
