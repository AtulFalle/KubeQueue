import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import { describe, expect, it, vi } from 'vitest';

import { SetupWorkflow } from './setup-workflow';

const readyStatus = {
  available: true,
  state: 'AVAILABLE',
  api: { ready: true },
  database: { ready: true },
  schema: { ready: true },
  worker: { ready: true },
  kubernetesAuthority: { ready: true },
  release: { ready: true },
  publicUrl: { ready: true },
};

describe('SetupWorkflow', () => {
  it('creates a local owner and clears both password fields', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(readyStatus), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            installationId: 'installation_1',
            ownerPrincipalId: 'principal_1',
            username: 'admin',
            status: 'COMPLETED',
            createdAt: '2026-07-19T00:00:00Z',
          }),
          { status: 201, headers: { 'Content-Type': 'application/json' } },
        ),
      );
    vi.stubGlobal('fetch', fetchMock);
    const onComplete = vi.fn();
    const { container } = render(<SetupWorkflow onComplete={onComplete} />);
    const password = await screen.findByLabelText('Password');
    const confirmation = screen.getByLabelText('Confirm password');
    fireEvent.change(password, { target: { value: 'correct-horse-battery' } });
    fireEvent.change(confirmation, { target: { value: 'correct-horse-battery' } });
    fireEvent.change(screen.getByLabelText('Installation name'), {
      target: { value: 'Example' },
    });
    fireEvent.change(screen.getByLabelText('Initial project name'), {
      target: { value: 'Platform' },
    });
    fireEvent.change(screen.getByLabelText('Managed namespaces, comma separated'), {
      target: { value: 'default' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Claim installation' }));

    await waitFor(() => expect(password).toHaveValue(''));
    expect(confirmation).toHaveValue('');
    const request = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)) as {
      localAdmin: { username: string; password: string };
      namespaces: string[];
    };
    expect(request.localAdmin).toEqual({
      username: 'admin',
      password: 'correct-horse-battery',
    });
    expect(request.namespaces).toEqual(['default']);
    expect(onComplete).toHaveBeenCalledOnce();
    expect(localStorage.length).toBe(0);
    expect(await axe(container)).toHaveNoViolations();
  });
});
