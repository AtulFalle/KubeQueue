export function sessionCookieOptions(expires: Date) {
  return {
    secure: true,
    httpOnly: true,
    sameSite: 'lax' as const,
    path: '/',
    expires,
  };
}

export function clearedSessionCookieOptions() {
  return {
    secure: true,
    httpOnly: true,
    sameSite: 'lax' as const,
    path: '/',
    maxAge: 0,
  };
}

export function isAllowedMutationOrigin(request: Request, expectedOrigin: string): boolean {
  return request.headers.get('origin') === expectedOrigin;
}
