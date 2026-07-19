import { describe, expect, it } from 'vitest';

import { isAllowedMutationOrigin, sessionCookieOptions } from './browser-session-security';

describe('browser session boundary', () => {
  it('uses a host-only secure HttpOnly Lax cookie', () => {
    const expires = new Date('2026-07-20T00:00:00Z');
    expect(sessionCookieOptions(expires)).toEqual({
      secure: true,
      httpOnly: true,
      sameSite: 'lax',
      path: '/',
      expires,
    });
  });

  it('matches mutation origins exactly', () => {
    expect(
      isAllowedMutationOrigin(
        new Request('https://queue.example/api', {
          method: 'POST',
          headers: { Origin: 'https://queue.example' },
        }),
        'https://queue.example',
      ),
    ).toBe(true);
    expect(
      isAllowedMutationOrigin(
        new Request('https://queue.example/api', {
          method: 'POST',
          headers: { Origin: 'https://queue.example.evil' },
        }),
        'https://queue.example',
      ),
    ).toBe(false);
  });
});
