// Package projects implements projects, open and private visibility, direct and
// team grants, and logical archiving. It is the authority on who can read a
// project; callers must not bypass it.
package projects

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"jiralike/internal/workflow"
)

var (
	ErrNotFound          = errors.New("project not found")
	ErrNameEmpty         = errors.New("project name is required")
	ErrNameTooLong       = errors.New("project name is too long")
	ErrInvalidVisibility = errors.New("visibility must be open or private")
	ErrAlreadyArchived   = errors.New("project is already archived")
	ErrUserNotFound      = errors.New("user not found")
	ErrTeamNotFound      = errors.New("team not found")
	ErrMemberNotFound    = errors.New("project member not found")
	ErrTeamNotLinked     = errors.New("project team not found")
)

const maxNameLength = 200

// Project is a project row enriched with the data the list and detail views
// need. Counts are computed from the grant tables.
type Project struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	CreatedBy     int64  `json:"created_by"`
	CreatedByName string `json:"created_by_name"`
	CreatedAt     string `json:"created_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
	MemberCount   int    `json:"member_count"`
	TeamCount     int    `json:"team_count"`
}

type Member struct {
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	AddedAt string `json:"added_at"`
}

type Team struct {
	TeamID  int64  `json:"team_id"`
	Name    string `json:"name"`
	AddedAt string `json:"added_at"`
}

type ListFilter struct {
	Query           string
	Visibility      string
	IncludeArchived bool
}

// Person is the minimal directory shape used by assignee and mention
// pickers. It deliberately omits email addresses: the picker only needs a
// name, and the full people directory stays an admin view.
type Person struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	GuestExpiresOn string `json:"guest_expires_on"`
	Disabled       bool   `json:"disabled"`
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(name, description, visibility string, createdBy int64) (Project, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return Project{}, ErrNameEmpty
	}
	if len(name) > maxNameLength {
		return Project{}, ErrNameTooLong
	}
	if visibility == "" {
		visibility = "open"
	}
	if visibility != "open" && visibility != "private" {
		return Project{}, ErrInvalidVisibility
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO projects (name, description, visibility, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, description, visibility, createdBy, now,
	)
	if err != nil {
		return Project{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Project{}, err
	}
	// The creator can always reach their own project, including when it is
	// private. Making the grant explicit keeps the access query uniform.
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO project_members (project_id, user_id, added_at) VALUES (?, ?, ?)`,
		id, createdBy, now,
	); err != nil {
		return Project{}, err
	}
	// Every project starts with its own copy of the default flow, bound to the
	// four item types. Items always have a workflow to start from.
	if err := workflow.SeedDefault(tx, id); err != nil {
		return Project{}, fmt.Errorf("seed project workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.Get(id)
}

func (s *Store) Get(id int64) (Project, error) {
	row := s.db.QueryRow(`
		SELECT p.id, p.name, p.description, p.visibility, p.created_by, u.name,
		       p.created_at, p.archived_at,
		       (SELECT COUNT(*) FROM project_members pm WHERE pm.project_id = p.id),
		       (SELECT COUNT(*) FROM project_teams pt WHERE pt.project_id = p.id)
		FROM projects p
		JOIN users u ON u.id = p.created_by
		WHERE p.id = ?`, id)
	return scanProject(row)
}

func (s *Store) List(userID int64, role string, filter ListFilter) ([]Project, error) {
	admin := 0
	if role == "admin" {
		admin = 1
	}
	includeArchived := 0
	if filter.IncludeArchived {
		includeArchived = 1
	}
	visibility := strings.TrimSpace(filter.Visibility)
	if visibility != "open" && visibility != "private" {
		visibility = ""
	}
	query := strings.TrimSpace(filter.Query)
	like := "%" + escapeLike(query) + "%"
	rows, err := s.db.Query(`
		SELECT p.id, p.name, p.description, p.visibility, p.created_by, u.name,
		       p.created_at, p.archived_at,
		       (SELECT COUNT(*) FROM project_members pm WHERE pm.project_id = p.id),
		       (SELECT COUNT(*) FROM project_teams pt WHERE pt.project_id = p.id)
		FROM projects p
		JOIN users u ON u.id = p.created_by
		WHERE (? = 1 OR p.archived_at IS NULL)
		  AND (? = '' OR p.visibility = ?)
		  AND (? = '' OR p.name LIKE ? ESCAPE '\' OR p.description LIKE ? ESCAPE '\')
		  AND (? = 1
		       OR p.visibility = 'open'
		       OR p.created_by = ?
		       OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = ?)
		       OR EXISTS (SELECT 1 FROM project_teams pt
		                  JOIN team_members tm ON tm.team_id = pt.team_id
		                  JOIN teams t ON t.id = pt.team_id
		                  WHERE pt.project_id = p.id AND tm.user_id = ? AND t.archived_at IS NULL))
		ORDER BY p.name COLLATE NOCASE`,
		includeArchived, visibility, visibility, query, like, like, admin, userID, userID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update rewrites the editable fields of a project. Callers must check
// CanManage first.
func (s *Store) Update(id int64, name, description, visibility string) error {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return ErrNameEmpty
	}
	if len(name) > maxNameLength {
		return ErrNameTooLong
	}
	if visibility != "open" && visibility != "private" {
		return ErrInvalidVisibility
	}
	res, err := s.db.Exec(
		`UPDATE projects SET name = ?, description = ?, visibility = ? WHERE id = ? AND archived_at IS NULL`,
		name, description, visibility, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Archive(id int64) error {
	res, err := s.db.Exec(
		`UPDATE projects SET archived_at = ? WHERE id = ? AND archived_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAlreadyArchived
	}
	return nil
}

func (s *Store) Members(projectID int64) ([]Member, error) {
	rows, err := s.db.Query(`
		SELECT pm.user_id, u.name, u.email, u.role, pm.added_at
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = ?
		ORDER BY u.name COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddMember(projectID, userID int64) error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrUserNotFound
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO project_members (project_id, user_id, added_at) VALUES (?, ?, ?)`,
		projectID, userID, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *Store) RemoveMember(projectID, userID int64) error {
	res, err := s.db.Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMemberNotFound
	}
	return nil
}

func (s *Store) Teams(projectID int64) ([]Team, error) {
	rows, err := s.db.Query(`
		SELECT pt.team_id, t.name, pt.added_at
		FROM project_teams pt
		JOIN teams t ON t.id = pt.team_id
		WHERE pt.project_id = ?
		ORDER BY t.name COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.TeamID, &t.Name, &t.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AddTeam(projectID, teamID int64) error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams WHERE id = ? AND archived_at IS NULL`, teamID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrTeamNotFound
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO project_teams (project_id, team_id, added_at) VALUES (?, ?, ?)`,
		projectID, teamID, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *Store) RemoveTeam(projectID, teamID int64) error {
	res, err := s.db.Exec(`DELETE FROM project_teams WHERE project_id = ? AND team_id = ?`, projectID, teamID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTeamNotLinked
	}
	return nil
}

// CanView reports whether the user may read the project. Archived projects are
// handled by CanSee, which also lets the managers reach their archived work.
func (s *Store) CanView(p Project, userID int64, role string) (bool, error) {
	if role == "admin" {
		return true, nil
	}
	if p.CreatedBy == userID {
		return true, nil
	}
	if p.Visibility == "open" {
		return true, nil
	}
	return s.IsMember(p.ID, userID)
}

// CanSee adds the archived rule: archived projects disappear from views for
// everyone except admins and the managers who manage them.
func (s *Store) CanSee(p Project, userID int64, role string) (bool, error) {
	if p.ArchivedAt != "" && role != "admin" {
		manage, err := s.CanManage(p, userID, role)
		if err != nil || !manage {
			return false, err
		}
	}
	return s.CanView(p, userID, role)
}

// CanManage reports whether the user may change the project and its grants.
// Admins always can; managers only on projects they created or belong to.
func (s *Store) CanManage(p Project, userID int64, role string) (bool, error) {
	if role == "admin" {
		return true, nil
	}
	if role != "manager" {
		return false, nil
	}
	if p.CreatedBy == userID {
		return true, nil
	}
	return s.IsMember(p.ID, userID)
}

// CanWrite is the rule for creating or editing work items. Guests never write;
// other roles write only on projects they can reach.
func (s *Store) CanWrite(p Project, userID int64, role string) (bool, error) {
	if role == "guest" {
		return false, nil
	}
	return s.CanView(p, userID, role)
}

// IsMember reports direct membership or membership through a non-archived
// team assigned to the project.
func (s *Store) IsMember(projectID, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM project_members WHERE project_id = ? AND user_id = ?
			UNION ALL
			SELECT 1 FROM project_teams pt
			JOIN team_members tm ON tm.team_id = pt.team_id
			JOIN teams t ON t.id = pt.team_id
			WHERE pt.project_id = ? AND tm.user_id = ? AND t.archived_at IS NULL
		)`, projectID, userID, projectID, userID).Scan(&count)
	return count > 0, err
}

// AssignableUsers returns the people who can view a project, which is exactly
// the set the API accepts as assignees. Open projects expose the whole
// installation; private ones expose admins, the creator, direct members and
// members of a non-archived assigned team.
func (s *Store) AssignableUsers(p Project) ([]Person, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT u.id, u.name, u.role, COALESCE(u.guest_expires_on, ''),
		       CASE WHEN u.disabled_at IS NULL THEN 0 ELSE 1 END
		FROM users u
		WHERE ? = 'open'
		   OR u.role = 'admin'
		   OR u.id = ?
		   OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = ? AND pm.user_id = u.id)
		   OR EXISTS (SELECT 1 FROM project_teams pt
		              JOIN team_members tm ON tm.team_id = pt.team_id
		              JOIN teams t ON t.id = pt.team_id
		              WHERE pt.project_id = ? AND tm.user_id = u.id AND t.archived_at IS NULL)
		ORDER BY u.name COLLATE NOCASE, u.id`, p.Visibility, p.CreatedBy, p.ID, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var person Person
		var disabled int
		if err := rows.Scan(&person.ID, &person.Name, &person.Role, &person.GuestExpiresOn, &disabled); err != nil {
			return nil, err
		}
		person.Disabled = disabled == 1
		out = append(out, person)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(row scanner) (Project, error) {
	var p Project
	var archived sql.NullString
	err := row.Scan(
		&p.ID, &p.Name, &p.Description, &p.Visibility, &p.CreatedBy, &p.CreatedByName,
		&p.CreatedAt, &archived, &p.MemberCount, &p.TeamCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if archived.Valid {
		p.ArchivedAt = archived.String
	}
	return p, nil
}

// escapeLike makes user input literal inside a LIKE pattern with ESCAPE '\'.
func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
