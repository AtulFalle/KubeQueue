import { afterEach, describe, expect, it, vi } from 'vitest';

import type { AuditEvent } from '@kubequeue/api-client';

vi.mock('../../../lib/bff-config', () => ({
  getBFFConfig: () => ({ apiOrigin: 'http://control-plane.test' }),
}));
vi.mock('../../../lib/server-session', () => ({
  sessionCredential: () => Promise.resolve('session-credential'),
}));

import { GET } from './route';

const auditEvent: AuditEvent = {
  id: 'event-1',
  occurredAt: '2026-07-19T10:00:00Z',
  requestId: 'request-1',
  traceId: 'trace-1',
  actor: {
    principalId: 'principal-1',
    authenticationMethod: 'OIDC_SESSION',
    credentialId: 'credential-1',
    effectiveGroups: [],
  },
  action: 'jobs.submit',
  target: { type: 'job', id: 'job-1' },
  scope: { installationId: 'installation-1', projectId: 'research' },
  decision: 'ALLOW',
  result: 'SUCCESS',
  reason: 'authorized',
  source: { address: '192.0.2.1', provenance: 'DIRECT_PEER', userAgent: 'test' },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('audit download route', () => {
  it('streams ordered export pages as NDJSON through the session client', async () => {
    const nextEvent = { ...auditEvent, id: 'event-2' };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse({
          installationId: 'installation-1',
          installationOwner: false,
          principal: {
            id: 'principal-1',
            kind: 'HUMAN',
            displayName: 'Ada',
            status: 'ACTIVE',
          },
          permissions: [{ permission: 'audit.export', scopeType: 'INSTALLATION' }],
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ items: [auditEvent], nextCursor: 'page-2' }))
      .mockResolvedValueOnce(jsonResponse({ items: [nextEvent], nextCursor: null }));
    vi.stubGlobal('fetch', fetchMock);

    const response = await GET(
      new Request('http://web.test/audit/download?projectId=research&decision=ALLOW'),
    );

    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toContain('application/x-ndjson');
    expect(response.headers.get('content-disposition')).toContain('attachment');
    expect(
      (await response.text())
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line)),
    ).toEqual([auditEvent, nextEvent]);
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      'http://control-plane.test/api/v1/audit/export?installationId=installation-1&projectId=research&decision=ALLOW&limit=200',
      expect.objectContaining({ cache: 'no-store' }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      expect.stringContaining('cursor=page-2'),
      expect.objectContaining({ cache: 'no-store' }),
    );
  });

  it('rejects invalid filters before starting an export', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const response = await GET(new Request('http://web.test/audit/download?decision=UNKNOWN'));

    expect(response.status).toBe(400);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}
