import { describe, expect, it } from 'vitest';

import { isPublicRoute } from './route';

describe('public BFF proxy allowlist', () => {
  it('allows only the exact public route and method pairs', () => {
    expect(isPublicRoute('GET', ['login-methods'])).toBe(true);
    expect(isPublicRoute('GET', ['setup', 'status'])).toBe(true);
    expect(isPublicRoute('GET', ['setup', 'recovery'])).toBe(true);
    expect(isPublicRoute('POST', ['setup', 'claim'])).toBe(true);

    expect(isPublicRoute('POST', ['setup', 'status'])).toBe(false);
    expect(isPublicRoute('GET', ['setup', 'claim'])).toBe(false);
    expect(isPublicRoute('DELETE', ['setup', 'recovery'])).toBe(false);
    expect(isPublicRoute('GET', ['setup', 'status', 'extra'])).toBe(false);
    expect(isPublicRoute('GET', ['setup', 'anything'])).toBe(false);
  });
});
