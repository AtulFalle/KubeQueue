import 'server-only';

import { KubeQueueClient } from '@kubequeue/api-client';
import { redirect } from 'next/navigation';

import { getBFFConfig } from './bff-config';
import { sessionCredential } from './server-session';

export async function serverAPIClient(): Promise<KubeQueueClient> {
  const credential = await sessionCredential();
  if (!credential) redirect('/session-expired');
  const config = getBFFConfig();
  return new KubeQueueClient(`${config.apiOrigin}/api/v1`, credential, 'Session');
}
