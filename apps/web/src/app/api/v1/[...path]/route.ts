import { NextResponse } from 'next/server';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

type RouteContext = {
  params: Promise<{ path: string[] }>;
};

async function proxy(request: Request, context: RouteContext) {
  const { path } = await context.params;
  const origin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';
  const target = new URL(`/api/v1/${path.map(encodeURIComponent).join('/')}`, origin);
  target.search = new URL(request.url).search;

  const headers = new Headers();
  for (const name of ['accept', 'content-type', 'if-match']) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }
  const token = process.env.KUBEQUEUE_ADMIN_TOKEN;
  if (token) headers.set('authorization', `Bearer ${token}`);

  const body =
    request.method === 'GET' || request.method === 'HEAD'
      ? undefined
      : await request.arrayBuffer();
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
