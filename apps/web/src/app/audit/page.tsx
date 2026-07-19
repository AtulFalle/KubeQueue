import type { AuditEvent, CurrentAccess } from '@kubequeue/api-client';

import { AuditView } from '../../components/audit-view';
import { serverAPIClient } from '../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function AuditPage() {
  const client = await serverAPIClient();
  let access: CurrentAccess | undefined;
  let events: AuditEvent[] = [];
  let nextCursor: string | null = null;
  let loadError = '';

  try {
    access = await client.getCurrentAccess();
    const canRead = access.permissions.some(({ permission }) => permission === 'audit.read');
    if (canRead) {
      const page = await client.searchAuditEvents(access.installationId, { limit: 50 });
      events = page.items;
      nextCursor = page.nextCursor;
    }
  } catch (reason) {
    loadError = reason instanceof Error ? reason.message : 'Unable to load audit events';
  }

  const can = (permission: 'audit.read' | 'audit.export') =>
    Boolean(access?.permissions.some((entry) => entry.permission === permission));

  return (
    <AuditView
      installationId={access?.installationId ?? ''}
      canRead={can('audit.read')}
      canExport={can('audit.export')}
      initialEvents={events}
      initialNextCursor={nextCursor}
      loadError={loadError}
    />
  );
}
