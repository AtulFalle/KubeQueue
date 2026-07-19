'use client';

import { KubeQueueClient, type RoleBinding, type RoleDefinition } from '@kubequeue/api-client';
import { useRef, useState, type FormEvent } from 'react';

import { readPermissions } from './access-permissions';

const client = new KubeQueueClient();

export function RoleManagement({
  initialRoles,
  initialBindings,
  canDefine,
  canAssign,
  onError,
  onStatus,
}: {
  initialRoles: RoleDefinition[];
  initialBindings: RoleBinding[];
  canDefine: boolean;
  canAssign: boolean;
  onError: (message: string) => void;
  onStatus: (message: string) => void;
}) {
  const [roles, setRoles] = useState(initialRoles);
  const [bindings, setBindings] = useState(initialBindings);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<RoleDefinition>();
  const editDialog = useRef<HTMLDialogElement>(null);

  async function createRole(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    try {
      const role = await client.createRoleDefinition({
        id: text(data, 'id'),
        name: text(data, 'name'),
        scopeType: text(data, 'scopeType') === 'PROJECT' ? 'PROJECT' : 'INSTALLATION',
        permissions: readPermissions(data.get('permissions')),
      });
      setRoles((current) => [...current, role]);
      form.reset();
      onStatus(`${role.name} created.`);
    } catch (reason) {
      onError(message(reason, 'Unable to create role'));
    } finally {
      setBusy(false);
    }
  }

  async function updateRole(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing) return;
    const data = new FormData(event.currentTarget);
    setBusy(true);
    try {
      const role = await client.updateRoleDefinition(editing.id, editing.revision, {
        name: text(data, 'name'),
        scopeType: text(data, 'scopeType') === 'PROJECT' ? 'PROJECT' : 'INSTALLATION',
        permissions: readPermissions(data.get('permissions')),
      });
      setRoles((current) => current.map((item) => (item.id === role.id ? role : item)));
      editDialog.current?.close();
      setEditing(undefined);
      onStatus(`${role.name} updated to revision ${role.revision}.`);
    } catch (reason) {
      onError(message(reason, 'Unable to update role'));
    } finally {
      setBusy(false);
    }
  }

  async function createBinding(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const scopeType = text(data, 'scopeType') === 'PROJECT' ? 'PROJECT' : 'INSTALLATION';
    setBusy(true);
    try {
      const binding = await client.createRoleBinding({
        id: text(data, 'id'),
        roleDefinitionId: text(data, 'roleDefinitionId'),
        subject: {
          kind: text(data, 'subjectKind') === 'TEAM' ? 'TEAM' : 'PRINCIPAL',
          id: text(data, 'subjectId'),
        },
        scope: {
          scopeType,
          ...(scopeType === 'PROJECT' ? { projectId: text(data, 'projectId') } : {}),
        },
      });
      setBindings((current) => [...current, binding]);
      form.reset();
      onStatus('Role binding created.');
    } catch (reason) {
      onError(message(reason, 'Unable to create role binding'));
    } finally {
      setBusy(false);
    }
  }

  async function deleteBinding(binding: RoleBinding) {
    setBusy(true);
    try {
      await client.deleteRoleBinding(binding.id);
      setBindings((current) => current.filter((item) => item.id !== binding.id));
      onStatus('Role binding removed.');
    } catch (reason) {
      onError(message(reason, 'Unable to remove role binding'));
    } finally {
      setBusy(false);
    }
  }

  const roleNames = new Map(roles.map((role) => [role.id, role.name]));

  return (
    <section className="surface" aria-labelledby="roles-title">
      <h2 id="roles-title">Roles and bindings</h2>
      {canDefine ? (
        <details className="access-disclosure">
          <summary>Create custom role</summary>
          <RoleForm submitLabel="Create role" busy={busy} onSubmit={createRole} />
        </details>
      ) : null}
      {canAssign ? (
        <details className="access-disclosure">
          <summary>Create role binding</summary>
          <form className="access-form form-grid" onSubmit={(event) => void createBinding(event)}>
            <label>
              Binding ID
              <input name="id" required autoComplete="off" />
            </label>
            <label>
              Role
              <select name="roleDefinitionId" required defaultValue="">
                <option value="" disabled>
                  Select role
                </option>
                {roles.map((role) => (
                  <option key={role.id} value={role.id}>
                    {role.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Subject type
              <select name="subjectKind">
                <option value="PRINCIPAL">Principal</option>
                <option value="TEAM">Team</option>
              </select>
            </label>
            <label>
              Subject ID
              <input name="subjectId" required autoComplete="off" />
            </label>
            <label>
              Scope
              <select name="scopeType">
                <option value="INSTALLATION">Installation</option>
                <option value="PROJECT">Project</option>
              </select>
            </label>
            <label>
              Project ID (project scope only)
              <input name="projectId" autoComplete="off" />
            </label>
            <button className="button primary" disabled={busy} type="submit">
              Create binding
            </button>
          </form>
        </details>
      ) : null}

      <h3>Role definitions</h3>
      <div className="table-scroll">
        <table className="access-table">
          <caption className="sr-only">Built-in and custom role definitions</caption>
          <thead>
            <tr>
              <th scope="col">Role</th>
              <th scope="col">Scope</th>
              <th scope="col">Permissions</th>
              <th scope="col">Revision</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {roles.map((role) => (
              <tr key={role.id}>
                <th scope="row">
                  {role.name}
                  <small>{role.builtIn ? 'Built in' : role.id}</small>
                </th>
                <td>{role.scopeType.toLowerCase()}</td>
                <td className="permission-cell">{role.permissions.join(', ')}</td>
                <td>{role.revision}</td>
                <td>
                  {canDefine && !role.builtIn ? (
                    <button
                      className="button ghost"
                      type="button"
                      onClick={() => {
                        setEditing(role);
                        editDialog.current?.showModal();
                      }}
                    >
                      Edit
                    </button>
                  ) : (
                    '—'
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3>Role bindings</h3>
      <div className="table-scroll">
        <table className="access-table">
          <caption className="sr-only">Installation and project role bindings</caption>
          <thead>
            <tr>
              <th scope="col">Role</th>
              <th scope="col">Subject</th>
              <th scope="col">Scope</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {bindings.map((binding) => (
              <tr key={binding.id}>
                <th scope="row">
                  {roleNames.get(binding.roleDefinitionId) ?? binding.roleDefinitionId}
                </th>
                <td>
                  {binding.subject.kind.toLowerCase()} <code>{binding.subject.id}</code>
                </td>
                <td>
                  {binding.scope.scopeType.toLowerCase()}
                  {binding.scope.projectId ? ` · ${binding.scope.projectId}` : ''}
                </td>
                <td>
                  {canAssign ? (
                    <button
                      className="button danger-button"
                      disabled={busy}
                      type="button"
                      onClick={() => void deleteBinding(binding)}
                    >
                      Remove
                    </button>
                  ) : (
                    '—'
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <dialog
        className="confirm-dialog access-dialog"
        ref={editDialog}
        aria-labelledby="edit-role-title"
        onClose={() => setEditing(undefined)}
      >
        <h2 id="edit-role-title">Edit custom role</h2>
        {editing ? (
          <RoleForm role={editing} submitLabel="Save revision" busy={busy} onSubmit={updateRole} />
        ) : null}
        <button className="button ghost" type="button" onClick={() => editDialog.current?.close()}>
          Cancel
        </button>
      </dialog>
    </section>
  );
}

function RoleForm({
  role,
  submitLabel,
  busy,
  onSubmit,
}: {
  role?: RoleDefinition;
  submitLabel: string;
  busy: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <form className="access-form" onSubmit={onSubmit}>
      {!role ? (
        <label>
          Role ID
          <input name="id" required autoComplete="off" />
        </label>
      ) : null}
      <label>
        Role name
        <input name="name" required defaultValue={role?.name} autoComplete="off" />
      </label>
      <label>
        Scope
        <select name="scopeType" defaultValue={role?.scopeType ?? 'INSTALLATION'}>
          <option value="INSTALLATION">Installation</option>
          <option value="PROJECT">Project</option>
        </select>
      </label>
      <label>
        Permissions, comma separated
        <textarea
          name="permissions"
          required
          rows={3}
          defaultValue={role?.permissions.join(', ')}
        />
      </label>
      <button className="button primary" disabled={busy} type="submit">
        {busy ? 'Saving…' : submitLabel}
      </button>
    </form>
  );
}

function text(data: FormData, name: string) {
  return String(data.get(name) ?? '').trim();
}

function message(reason: unknown, fallback: string) {
  return reason instanceof Error ? reason.message : fallback;
}
