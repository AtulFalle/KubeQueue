import type { CurrentAccess, Project } from '@kubequeue/api-client';

import { ProjectsView } from '../../components/projects-view';
import { serverAPIClient } from '../../lib/server-api-client';

export const dynamic = 'force-dynamic';

export default async function ProjectsPage() {
  const client = await serverAPIClient();
  let access: CurrentAccess | undefined;
  let projects: Project[] = [];
  let nextCursor: string | null = null;
  let loadError = '';

  try {
    access = await client.getCurrentAccess();
    const page = await client.listProjects();
    projects = page.items;
    nextCursor = page.nextCursor;
  } catch (reason) {
    loadError = reason instanceof Error ? reason.message : 'Unable to load projects';
  }

  return (
    <ProjectsView
      access={access}
      initialProjects={projects}
      initialNextCursor={nextCursor}
      loadError={loadError}
    />
  );
}
