import { type SystemStatus } from '@kubequeue/api-client';

import { JobForm } from '../../../components/job-form';
import { serverAPIClient } from '../../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function NewJobPage() {
  const client = await serverAPIClient();
  let systemStatus: SystemStatus | undefined;
  try {
    systemStatus = await client.getSystemStatus();
  } catch {
    // The form explains that submission is unavailable without a ready namespace.
  }
  return <JobForm systemStatus={systemStatus} />;
}
