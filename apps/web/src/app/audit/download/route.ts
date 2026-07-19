import {
  ApiError,
  KubeQueueClient,
  type AuditEventFilters,
  type AuditEventPage,
} from '@kubequeue/api-client';
import { NextResponse } from 'next/server';

import { getBFFConfig } from '../../../lib/bff-config';
import { sessionCredential } from '../../../lib/server-session';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

const identifierPattern = /^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$/;
const actionPattern = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;

export async function GET(request: Request) {
  const credential = await sessionCredential();
  if (!credential) {
    return errorResponse(401, 'SESSION_EXPIRED', 'Sign in again');
  }

  let filters: AuditEventFilters;
  try {
    filters = exportFilters(new URL(request.url).searchParams);
  } catch (reason) {
    return errorResponse(
      400,
      'INVALID_AUDIT_FILTER',
      reason instanceof Error ? reason.message : 'Invalid audit export filters',
    );
  }

  const config = getBFFConfig();
  const client = new KubeQueueClient(`${config.apiOrigin}/api/v1`, credential, 'Session');

  try {
    const access = await client.getCurrentAccess();
    if (!access.permissions.some(({ permission }) => permission === 'audit.export')) {
      return errorResponse(403, 'FORBIDDEN', 'Audit export is not permitted');
    }

    const installationId = access.installationId;
    const firstPage = await client.exportAuditEvents(installationId, { ...filters, limit: 200 });
    const body = pagedNDJSON(client, installationId, filters, firstPage);
    const date = new Date().toISOString().slice(0, 10);

    return new Response(body, {
      headers: {
        'Cache-Control': 'no-store',
        'Content-Disposition': `attachment; filename="kubequeue-audit-${date}.ndjson"`,
        'Content-Type': 'application/x-ndjson; charset=utf-8',
        'X-Content-Type-Options': 'nosniff',
      },
    });
  } catch (reason) {
    if (reason instanceof ApiError) {
      return errorResponse(reason.status, reason.code, reason.message);
    }
    return errorResponse(502, 'AUDIT_EXPORT_FAILED', 'Unable to export audit events');
  }
}

function pagedNDJSON(
  client: KubeQueueClient,
  installationId: string,
  filters: AuditEventFilters,
  firstPage: AuditEventPage,
) {
  const encoder = new TextEncoder();
  let page = firstPage;
  let itemIndex = 0;

  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      try {
        while (itemIndex >= page.items.length && page.nextCursor) {
          page = await client.exportAuditEvents(installationId, {
            ...filters,
            limit: 200,
            cursor: page.nextCursor,
          });
          itemIndex = 0;
        }

        const item = page.items[itemIndex];
        if (!item) {
          controller.close();
          return;
        }
        itemIndex += 1;
        controller.enqueue(encoder.encode(`${JSON.stringify(item)}\n`));
      } catch (reason) {
        controller.error(reason);
      }
    },
  });
}

function exportFilters(search: URLSearchParams): AuditEventFilters {
  const projectId = optional(search, 'projectId');
  if (projectId && !identifierPattern.test(projectId)) throw new Error('Project ID is invalid');

  const action = optional(search, 'action');
  if (action && (action.length > 96 || !actionPattern.test(action))) {
    throw new Error('Action is invalid');
  }

  const decision = auditDecision(optional(search, 'decision'));
  const result = auditResult(optional(search, 'result'));

  return {
    projectId,
    action,
    decision,
    result,
    occurredFrom: timestamp(search, 'occurredFrom'),
    occurredTo: timestamp(search, 'occurredTo'),
  };
}

function auditDecision(value: string | undefined): AuditEventFilters['decision'] {
  if (!value) return undefined;
  if (value === 'ALLOW') return 'ALLOW';
  if (value === 'DENY') return 'DENY';
  throw new Error('Decision is invalid');
}

function auditResult(value: string | undefined): AuditEventFilters['result'] {
  if (!value) return undefined;
  if (value === 'SUCCESS') return 'SUCCESS';
  if (value === 'FAILURE') return 'FAILURE';
  throw new Error('Result is invalid');
}

function optional(search: URLSearchParams, name: string) {
  return search.get(name)?.trim() || undefined;
}

function timestamp(search: URLSearchParams, name: string) {
  const value = optional(search, name);
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) throw new Error(`${name} must be a valid timestamp`);
  return date.toISOString();
}

function errorResponse(status: number, code: string, message: string) {
  return NextResponse.json({ error: { code, message, status } }, { status });
}
