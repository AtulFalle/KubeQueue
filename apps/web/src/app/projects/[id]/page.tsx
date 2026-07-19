import type {
  AdmissionDecisionPage,
  CurrentAccess,
  Job,
  NamespaceBindingPage,
  Project,
  ProjectAdmissionSettings,
  QuotaCounters,
} from '@kubequeue/api-client';

import { ProjectAdmissionView } from '../../../components/project-admission-view';
import { serverAPIClient } from '../../../lib/server-api-client';

export const dynamic = 'force-dynamic';

type PageProps = {
  params: Promise<{ id: string }>;
};

export default async function ProjectPage({ params }: PageProps) {
  const { id } = await params;
  const client = await serverAPIClient();
  let project: Project | undefined;
  let access: CurrentAccess | undefined;
  let settings: ProjectAdmissionSettings | undefined;
  let settingsETag = '';
  let usage: QuotaCounters | undefined;
  let decisions: AdmissionDecisionPage = { items: [], nextCursor: null };
  let bindings: NamespaceBindingPage = { items: [], nextCursor: null };
  let projectQueue: Job[] = [];
  let queueVersion = 0;
  let loadError = '';

  try {
    [project, access] = await Promise.all([
      client.getProject(id).catch(() => undefined),
      client.getCurrentAccess(),
    ]);
    const [versionedSettings, quotaUsage, decisionPage, bindingPage, queue] = await Promise.all([
      client.getProjectAdmissionSettings(id),
      client.getProjectQuotaUsage(id),
      client.listProjectAdmissionDecisions(id),
      client.listProjectNamespaceBindings(id),
      client.getQueue(),
    ]);
    settings = versionedSettings.value;
    settingsETag = versionedSettings.etag;
    usage = quotaUsage;
    decisions = decisionPage;
    bindings = bindingPage;
    projectQueue = queue.items.filter((job) => job.projectId === id);
    queueVersion = queue.queueVersion;
  } catch (reason) {
    loadError = reason instanceof Error ? reason.message : 'Unable to load project admission data';
  }

  return (
    <ProjectAdmissionView
      projectId={id}
      project={project}
      access={access}
      initialSettings={settings}
      initialSettingsETag={settingsETag}
      initialUsage={usage}
      initialDecisions={decisions}
      initialBindings={bindings.items}
      initialQueue={projectQueue}
      initialQueueVersion={queueVersion}
      loadError={loadError}
    />
  );
}
