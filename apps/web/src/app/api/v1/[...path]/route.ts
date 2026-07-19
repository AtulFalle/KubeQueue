import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../../lib/bff-config';
import { isAllowedMutationOrigin } from '../../../../lib/browser-session-security';
import { sessionCredential } from '../../../../lib/server-session';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

type RouteContext = {
  params: Promise<{ path: string[] }>;
};

async function proxy(request: Request, context: RouteContext) {
  const { path } = await context.params;
  const config = getBFFConfig();
  const credential = await sessionCredential();
  const publicRoute = isPublicRoute(request.method, path);
  if (!credential && !publicRoute) {
    return NextResponse.json(
      { error: { code: 'SESSION_EXPIRED', message: 'Sign in again', status: 401 } },
      { status: 401 },
    );
  }
  const target = new URL(`/api/v1/${path.map(encodeURIComponent).join('/')}`, config.apiOrigin);
  target.search = new URL(request.url).search;

  const headers = new Headers();
  for (const name of ['accept', 'content-type', 'if-match', 'x-csrf-token']) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }
  if (credential) headers.set('authorization', `Session ${credential}`);
  if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method)) {
    if (!isAllowedMutationOrigin(request, config.publicOrigin)) {
      return NextResponse.json(
        {
          error: {
            code: 'INVALID_ORIGIN',
            message: 'Request origin is not allowed',
            status: 403,
          },
        },
        { status: 403 },
      );
    }
    headers.set('origin', config.publicOrigin);
  }

  const body =
    request.method === 'GET' || request.method === 'HEAD' ? undefined : await request.arrayBuffer();
  const response = await fetch(target, {
    method: request.method,
    headers,
    body,
    cache: 'no-store',
  });
  const responseHeaders = new Headers(response.headers);
  for (const name of ['connection', 'content-encoding', 'content-length', 'transfer-encoding']) {
    responseHeaders.delete(name);
  }

  return new NextResponse(response.body, {
    status: response.status,
    headers: responseHeaders,
  });
}

export const GET = proxy;
export const POST = proxy;
export const PUT = proxy;
export const PATCH = proxy;
export const DELETE = proxy;

export function isPublicRoute(method: string, path: string[]) {
  const route = path.join('/');
  return (
    (method === 'GET' &&
      (route === 'login-methods' || route === 'setup/status' || route === 'setup/recovery')) ||
    (method === 'POST' && route === 'setup/claim')
  );
}
