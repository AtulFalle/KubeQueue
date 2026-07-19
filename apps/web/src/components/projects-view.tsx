'use client';

import { KubeQueueClient, type CurrentAccess, type Project } from '@kubequeue/api-client';
import Link from 'next/link';
import { useState, type FormEvent } from 'react';

const client = new KubeQueueClient();

export function ProjectsView({
  access,
  initialProjects,
  initialNextCursor = null,
  loadError = '',
}: {
  access?: CurrentAccess;
  initialProjects: Project[];
  initialNextCursor?: string | null;
  loadError?: string;
}) {
  const [projects, setProjects] = useState(initialProjects);
  const [nextCursor, setNextCursor] = useState(initialNextCursor);
  const [error, setError] = useState(loadError);
  const [status, setStatus] = useState('');
  const [saving, setSaving] = useState(false);
  const canManage = Boolean(
    access?.permissions.some(
      ({ permission, scopeType }) =>
        permission === 'projects.manage' && scopeType === 'INSTALLATION',
    ),
  );

  async function createProject(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setSaving(true);
    setError('');
    setStatus('');
    try {
      const project = await client.createProject({
        id: String(data.get('id') ?? '').trim(),
        name: String(data.get('name') ?? '').trim(),
      });
      setProjects((current) => [...current, project]);
      setStatus(`${project.name} created.`);
      form.reset();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to create project');
    } finally {
      setSaving(false);
    }
  }

  async function loadMore() {
    if (!nextCursor) return;
    setSaving(true);
    setError('');
    setStatus('');
    try {
      const page = await client.listProjects(nextCursor);
      setProjects((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor);
      setStatus(`${page.items.length} more projects loaded.`);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load more projects');
    } finally {
      setSaving(false);
    }
  }

  return (
    <main className="page-shell">
      <div className="page-title">
        <p className="eyebrow">Delegated boundaries</p>
        <h1>Projects</h1>
        <p>Bounded workload and administration scopes visible to your current access.</p>
      </div>

      <div className="access-feedback" aria-live="polite">
        {error ? (
          <div className="alert" role="alert">
            {error}
          </div>
        ) : null}
        {status ? (
          <div className="success" role="status">
            {status}
          </div>
        ) : null}
      </div>

      {canManage ? (
        <section className="surface" aria-labelledby="create-project-title">
          <h2 id="create-project-title">Create project</h2>
          <form className="access-form inline-form" onSubmit={(event) => void createProject(event)}>
            <label>
              Project ID
              <input name="id" required autoComplete="off" />
            </label>
            <label>
              Display name
              <input name="name" required autoComplete="off" />
            </label>
            <button className="button primary" disabled={saving} type="submit">
              {saving ? 'Creating…' : 'Create project'}
            </button>
          </form>
        </section>
      ) : null}

      <section className="surface" aria-labelledby="project-inventory-title">
        <h2 id="project-inventory-title">Project inventory</h2>
        {projects.length === 0 && !error ? <p className="empty">No accessible projects.</p> : null}
        {projects.length > 0 ? (
          <div className="table-scroll">
            <table className="access-table">
              <caption className="sr-only">Projects visible to the current principal</caption>
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">ID</th>
                  <th scope="col">Created</th>
                </tr>
              </thead>
              <tbody>
                {projects.map((project) => (
                  <tr key={project.id}>
                    <th scope="row">
                      <Link href={`/projects/${encodeURIComponent(project.id)}`}>
                        {project.name}
                      </Link>
                    </th>
                    <td>
                      <code>{project.id}</code>
                    </td>
                    <td>
                      <time dateTime={project.createdAt}>{formatDate(project.createdAt)}</time>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {nextCursor ? (
          <button
            className="button ghost"
            disabled={saving}
            type="button"
            onClick={() => void loadMore()}
          >
            {saving ? 'Loading…' : 'Load more projects'}
          </button>
        ) : null}
      </section>
    </main>
  );
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(new Date(value));
}
