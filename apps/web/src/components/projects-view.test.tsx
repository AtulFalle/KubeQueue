import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { describe, expect, it, vi } from 'vitest';

import type { CurrentAccess, Project } from '@kubequeue/api-client';

import { ProjectsView } from './projects-view';

const project: Project = {
  id: 'research',
  installationId: 'installation-1',
  name: 'Research',
  createdAt: '2026-07-19T00:00:00Z',
};

const access: CurrentAccess = {
  installationId: 'installation-1',
  installationOwner: false,
  principal: {
    id: 'principal-1',
    kind: 'HUMAN',
    displayName: 'Ada',
    status: 'ACTIVE',
  },
  permissions: [{ permission: 'projects.manage', scopeType: 'INSTALLATION' }],
};

describe('ProjectsView', () => {
  it('renders bounded inventory accessibly and capability-gates creation', async () => {
    const { container, rerender } = render(
      <ProjectsView access={access} initialProjects={[project]} />,
    );

    expect(screen.getByRole('heading', { name: 'Project inventory' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Create project' })).toBeEnabled();
    expect(screen.getByRole('row', { name: /Research research/ })).toBeVisible();
    expect(await axe(container)).toHaveNoViolations();

    rerender(<ProjectsView access={{ ...access, permissions: [] }} initialProjects={[project]} />);
    expect(screen.queryByRole('button', { name: 'Create project' })).not.toBeInTheDocument();
  });

  it('creates through the BFF client and announces success', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(project), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );
    render(<ProjectsView access={access} initialProjects={[]} />);

    fireEvent.change(screen.getByLabelText('Project ID'), { target: { value: 'research' } });
    fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Research created.'));
    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/projects',
      expect.objectContaining({ method: 'POST' }),
    );
  });
});
