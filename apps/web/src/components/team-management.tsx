'use client';

import { KubeQueueClient, type Team, type TeamMembership } from '@kubequeue/api-client';
import { useState, type FormEvent } from 'react';

const client = new KubeQueueClient();

export function TeamManagement({
  initialTeams,
  canManage,
  onError,
  onStatus,
}: {
  initialTeams: Team[];
  canManage: boolean;
  onError: (message: string) => void;
  onStatus: (message: string) => void;
}) {
  const [teams, setTeams] = useState(initialTeams);
  const [members, setMembers] = useState<Record<string, TeamMembership[]>>({});
  const [expandedTeam, setExpandedTeam] = useState('');
  const [busy, setBusy] = useState(false);

  async function createTeam(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    try {
      const team = await client.createTeam({
        id: String(data.get('id') ?? '').trim(),
        name: String(data.get('name') ?? '').trim(),
      });
      setTeams((current) => [...current, team]);
      form.reset();
      onStatus(`${team.name} created.`);
    } catch (reason) {
      onError(message(reason, 'Unable to create team'));
    } finally {
      setBusy(false);
    }
  }

  async function toggleMembers(team: Team) {
    if (expandedTeam === team.id) {
      setExpandedTeam('');
      return;
    }
    setBusy(true);
    try {
      const page = await client.listTeamMemberships(team.id);
      setMembers((current) => ({ ...current, [team.id]: page.items }));
      setExpandedTeam(team.id);
    } catch (reason) {
      onError(message(reason, 'Unable to load team members'));
    } finally {
      setBusy(false);
    }
  }

  async function addMember(event: FormEvent<HTMLFormElement>, team: Team) {
    event.preventDefault();
    const form = event.currentTarget;
    const principalId = String(new FormData(form).get('principalId') ?? '').trim();
    setBusy(true);
    try {
      const membership = await client.addTeamMembership(team.id, principalId);
      setMembers((current) => ({
        ...current,
        [team.id]: [...(current[team.id] ?? []), membership],
      }));
      form.reset();
      onStatus(`Principal ${principalId} added to ${team.name}.`);
    } catch (reason) {
      onError(message(reason, 'Unable to add team member'));
    } finally {
      setBusy(false);
    }
  }

  async function removeMember(team: Team, principalId: string) {
    setBusy(true);
    try {
      await client.removeTeamMembership(team.id, principalId);
      setMembers((current) => ({
        ...current,
        [team.id]: (current[team.id] ?? []).filter((item) => item.principalId !== principalId),
      }));
      onStatus(`Principal ${principalId} removed from ${team.name}.`);
    } catch (reason) {
      onError(message(reason, 'Unable to remove team member'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface" aria-labelledby="teams-title">
      <h2 id="teams-title">Teams and members</h2>
      {canManage ? (
        <form className="access-form inline-form" onSubmit={(event) => void createTeam(event)}>
          <label>
            Team ID
            <input name="id" required autoComplete="off" />
          </label>
          <label>
            Team name
            <input name="name" required autoComplete="off" />
          </label>
          <button className="button primary" disabled={busy} type="submit">
            Create team
          </button>
        </form>
      ) : null}
      {teams.length === 0 ? (
        <p className="empty">No teams.</p>
      ) : (
        <div className="table-scroll">
          <table className="access-table">
            <caption className="sr-only">Installation teams</caption>
            <thead>
              <tr>
                <th scope="col">Team</th>
                <th scope="col">ID</th>
                <th scope="col">Members</th>
              </tr>
            </thead>
            <tbody>
              {teams.map((team) => (
                <TeamRow
                  key={team.id}
                  team={team}
                  members={members[team.id]}
                  expanded={expandedTeam === team.id}
                  busy={busy}
                  canManage={canManage}
                  onToggle={() => void toggleMembers(team)}
                  onAdd={(event) => void addMember(event, team)}
                  onRemove={(principalId) => void removeMember(team, principalId)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function TeamRow({
  team,
  members,
  expanded,
  busy,
  canManage,
  onToggle,
  onAdd,
  onRemove,
}: {
  team: Team;
  members?: TeamMembership[];
  expanded: boolean;
  busy: boolean;
  canManage: boolean;
  onToggle: () => void;
  onAdd: (event: FormEvent<HTMLFormElement>) => void;
  onRemove: (principalId: string) => void;
}) {
  const panelId = `team-${team.id}-members`;
  return (
    <>
      <tr>
        <th scope="row">{team.name}</th>
        <td>
          <code>{team.id}</code>
        </td>
        <td>
          <button
            className="button ghost"
            type="button"
            aria-expanded={expanded}
            aria-controls={panelId}
            disabled={busy}
            onClick={onToggle}
          >
            {expanded ? 'Hide members' : 'View members'}
          </button>
        </td>
      </tr>
      {expanded ? (
        <tr className="expanded-row">
          <td colSpan={3} id={panelId}>
            {members?.length ? (
              <ul className="member-list">
                {members.map((member) => (
                  <li key={member.principalId}>
                    <code>{member.principalId}</code>
                    <span>{member.source.toLowerCase()}</span>
                    {canManage && member.source === 'MANUAL' ? (
                      <button
                        className="button danger-button"
                        disabled={busy}
                        type="button"
                        onClick={() => onRemove(member.principalId)}
                      >
                        Remove
                      </button>
                    ) : null}
                  </li>
                ))}
              </ul>
            ) : (
              <p>No members.</p>
            )}
            {canManage ? (
              <form className="access-form compact-form" onSubmit={onAdd}>
                <label>
                  Principal ID
                  <input name="principalId" required autoComplete="off" />
                </label>
                <button className="button ghost" disabled={busy} type="submit">
                  Add member
                </button>
              </form>
            ) : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function message(reason: unknown, fallback: string) {
  return reason instanceof Error ? reason.message : fallback;
}
