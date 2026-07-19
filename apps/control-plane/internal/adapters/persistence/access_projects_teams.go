package persistence

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const accessProjectColumns = `id,installation_id,name,created_at`
const accessTeamColumns = `id,installation_id,name,created_at`

func (s *Store) ListProjects(
	ctx context.Context, installationID domain.InstallationID, page domain.AccessPage,
) ([]domain.ManagedProject, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+accessProjectColumns+` FROM projects
		 WHERE installation_id=? AND id>? ORDER BY id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	projects := make([]domain.ManagedProject, 0, page.Limit)
	for rows.Next() {
		project, err := scanManagedProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	return projects, nil
}

func (s *Store) Project(
	ctx context.Context, installationID domain.InstallationID, id domain.ProjectID,
) (domain.ManagedProject, error) {
	project, err := scanManagedProject(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+accessProjectColumns+` FROM projects WHERE installation_id=? AND id=?`,
	), installationID, id))
	if err != nil {
		return domain.ManagedProject{}, err
	}
	return project, nil
}

func (s *Store) CreateProject(
	ctx context.Context, project domain.ManagedProject,
) (domain.ManagedProject, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		), project.ID, project.InstallationID, project.Name,
			project.CreatedAt.Format(accessTimestamp))
		return accessWriteError(err)
	})
	if err != nil {
		return domain.ManagedProject{}, fmt.Errorf("create project: %w", err)
	}
	return s.Project(ctx, project.InstallationID, project.ID)
}

func (s *Store) UpdateProject(
	ctx context.Context, project domain.ManagedProject,
) (domain.ManagedProject, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE projects SET name=? WHERE id=? AND installation_id=?`,
		), project.Name, project.ID, project.InstallationID)
		if err := accessWriteError(err); err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.ErrAccessResourceNotFound
		}
		return nil
	})
	if err != nil {
		return domain.ManagedProject{}, fmt.Errorf("update project: %w", err)
	}
	return s.Project(ctx, project.InstallationID, project.ID)
}

func (s *Store) ListTeams(
	ctx context.Context, installationID domain.InstallationID, page domain.AccessPage,
) ([]domain.Team, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+accessTeamColumns+` FROM teams
		 WHERE installation_id=? AND id>? ORDER BY id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer func() { _ = rows.Close() }()
	teams := make([]domain.Team, 0, page.Limit)
	for rows.Next() {
		team, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read teams: %w", err)
	}
	return teams, nil
}

func (s *Store) Team(
	ctx context.Context, installationID domain.InstallationID, id domain.TeamID,
) (domain.Team, error) {
	return scanTeam(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+accessTeamColumns+` FROM teams WHERE installation_id=? AND id=?`,
	), installationID, id))
}

func (s *Store) CreateTeam(ctx context.Context, team domain.Team) (domain.Team, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		), team.ID, team.InstallationID, team.Name, team.CreatedAt.Format(accessTimestamp))
		return accessWriteError(err)
	})
	if err != nil {
		return domain.Team{}, fmt.Errorf("create team: %w", err)
	}
	return s.Team(ctx, team.InstallationID, team.ID)
}

func (s *Store) UpdateTeam(ctx context.Context, team domain.Team) (domain.Team, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE teams SET name=? WHERE id=? AND installation_id=?`,
		), team.Name, team.ID, team.InstallationID)
		if err := accessWriteError(err); err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.ErrAccessResourceNotFound
		}
		return nil
	})
	if err != nil {
		return domain.Team{}, fmt.Errorf("update team: %w", err)
	}
	return s.Team(ctx, team.InstallationID, team.ID)
}

func (s *Store) ListTeamMemberships(
	ctx context.Context,
	installationID domain.InstallationID,
	teamID domain.TeamID,
	page domain.AccessPage,
) ([]domain.TeamMembership, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT tm.team_id,tm.principal_id,
		        COALESCE(tm.source_identity_provider_id,''),tm.created_at
		 FROM team_memberships tm
		 JOIN teams t ON t.id=tm.team_id
		 JOIN principals p ON p.id=tm.principal_id
		 WHERE t.installation_id=? AND p.installation_id=t.installation_id
		   AND tm.team_id=? AND tm.principal_id>?
		 ORDER BY tm.principal_id LIMIT ?`,
	), installationID, teamID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list team memberships: %w", err)
	}
	defer func() { _ = rows.Close() }()
	memberships := make([]domain.TeamMembership, 0, page.Limit)
	for rows.Next() {
		var membership domain.TeamMembership
		var createdAt string
		if err := rows.Scan(
			&membership.TeamID, &membership.PrincipalID,
			&membership.SourceIdentityProviderID, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan team membership: %w", err)
		}
		membership.CreatedAt = accessTime(createdAt)
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read team memberships: %w", err)
	}
	return memberships, nil
}

func (s *Store) PutTeamMembership(
	ctx context.Context,
	installationID domain.InstallationID,
	membership domain.TeamMembership,
) (domain.TeamMembership, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var valid int
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM teams t JOIN principals p ON p.installation_id=t.installation_id
			 WHERE t.id=? AND p.id=? AND t.installation_id=?`,
		), membership.TeamID, membership.PrincipalID, installationID).Scan(&valid)
		if err != nil {
			return err
		}
		if valid != 1 {
			return domain.ErrAccessResourceNotFound
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO team_memberships(team_id,principal_id,created_at)
			 VALUES(?,?,?) ON CONFLICT(team_id,principal_id) DO NOTHING`,
		), membership.TeamID, membership.PrincipalID,
			membership.CreatedAt.Format(accessTimestamp))
		if err != nil {
			return accessWriteError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return s.invalidatePrincipal(ctx, tx, installationID, membership.PrincipalID)
		}
		return nil
	})
	if err != nil {
		return domain.TeamMembership{}, fmt.Errorf("put team membership: %w", err)
	}
	return s.TeamMembership(
		ctx, installationID, membership.TeamID, membership.PrincipalID,
	)
}

func (s *Store) TeamMembership(
	ctx context.Context,
	installationID domain.InstallationID,
	teamID domain.TeamID,
	principalID domain.PrincipalID,
) (domain.TeamMembership, error) {
	membership := domain.TeamMembership{TeamID: teamID, PrincipalID: principalID}
	var createdAt string
	var source sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT tm.created_at,tm.source_identity_provider_id
		 FROM team_memberships tm JOIN teams t ON t.id=tm.team_id
		 JOIN principals p ON p.id=tm.principal_id
		 WHERE t.installation_id=? AND p.installation_id=t.installation_id
		   AND tm.team_id=? AND tm.principal_id=?`,
	), installationID, teamID, principalID).Scan(&createdAt, &source)
	if err != nil {
		return domain.TeamMembership{}, accessNotFound(err)
	}
	membership.CreatedAt = accessTime(createdAt)
	membership.SourceIdentityProviderID = source.String
	return membership, nil
}

func (s *Store) RemoveTeamMembership(
	ctx context.Context,
	installationID domain.InstallationID,
	teamID domain.TeamID,
	principalID domain.PrincipalID,
) error {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var source sql.NullString
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT tm.source_identity_provider_id
			 FROM team_memberships tm JOIN teams t ON t.id=tm.team_id
			 JOIN principals p ON p.id=tm.principal_id
			 WHERE t.installation_id=? AND p.installation_id=t.installation_id
			   AND tm.team_id=? AND tm.principal_id=?`,
		), installationID, teamID, principalID).Scan(&source)
		if err != nil {
			return accessNotFound(err)
		}
		if source.Valid {
			return domain.ErrAccessConflict
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM team_memberships WHERE team_id=? AND principal_id=?`,
		), teamID, principalID); err != nil {
			return err
		}
		if err := s.ensureInstallationOwner(ctx, tx, installationID); err != nil {
			return err
		}
		return s.invalidatePrincipal(ctx, tx, installationID, principalID)
	})
	if err != nil {
		return fmt.Errorf("remove team membership: %w", err)
	}
	return nil
}

const accessTimestamp = "2006-01-02T15:04:05.999999999Z07:00"
