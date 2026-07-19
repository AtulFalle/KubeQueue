import type { Permission } from '@kubequeue/api-client';

export const permissions: readonly Permission[] = [
  'jobs.list',
  'jobs.read',
  'jobs.manifest.read',
  'jobs.submit',
  'jobs.pause',
  'jobs.resume',
  'jobs.terminate',
  'jobs.retry',
  'jobs.take-control',
  'jobs.archive',
  'job-events.read',
  'events.stream.read',
  'queue.read',
  'queue.entry.update',
  'queue.project.reorder',
  'queue.global.reorder',
  'projects.manage',
  'namespace-bindings.manage',
  'members.read',
  'members.manage',
  'roles.read',
  'roles.assign',
  'roles.define',
  'service-accounts.manage',
  'tokens.manage',
  'policies.read',
  'policies.manage',
  'quotas.manage',
  'audit.read',
  'audit.export',
  'system.status.read',
  'support.diagnostics.read',
];

export function readPermissions(value: FormDataEntryValue | null): Permission[] {
  const requested = String(value ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);
  const invalid = requested.filter(
    (item): item is string => !permissions.includes(item as Permission),
  );
  if (invalid.length > 0) throw new Error(`Unknown permissions: ${invalid.join(', ')}`);
  return requested as Permission[];
}
