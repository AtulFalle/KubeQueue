import type { SupportDiagnostics } from '@kubequeue/api-client';

import { SupportView } from '../../components/support-view';
import { serverAPIClient } from '../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function SupportPage() {
  const client = await serverAPIClient();
  let diagnostics: SupportDiagnostics | undefined;
  let loadFailed = false;
  try {
    diagnostics = await client.getSupportDiagnostics();
  } catch {
    loadFailed = true;
  }
  return <SupportView diagnostics={diagnostics} loadFailed={loadFailed} />;
}
