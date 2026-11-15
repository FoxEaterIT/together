// Package teams implements reusable groups and their membership. Teams grant
// project access without changing anyone's global role.
package teams

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("team not found")
	ErrExists         = errors.New("team already exists")
	ErrNameEmpty      = errors.New("team name is required")
	ErrMemberAdded    = errors.New("member already in team")
	ErrMemberNotFound = errors.New("member not found")
)

type Team struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	ArchivedAt string `json:"archived_at"`
}

type Member struct {
	UserID  int64
	Name    string
	Email   string
	Role    string
	AddedAt string
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(name string) (Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Team{}, ErrNameEmpty
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`INSERT INTO teams (name, created_at) VALUES (?, ?)`, name, now)
	if err != nil {
		if isUnique(err) {
			return Team{}, ErrExists
		}
		return Team{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Team{}, err
	}
	return Team{ID: id, Name: name, CreatedAt: now}, nil
}

func (s *Store) List() ([]Team, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at, archived_at FROM teams WHERE archived_at IS NULL ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		var archived sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &archived); err != nil {
			return nil, err
		}
		if archived.Valid {
			t.ArchivedAt = archived.String
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Archive(id int64) error {
	res, err := s.db.Exec(`UPDATE teams SET archived_at = ? WHERE id = ? AND archived_at IS NULL`, time.Now().UTC().Format(time.RFC3339), id)
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

func (s *Store) AddMember(teamID, userID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO team_members (team_id, user_id, added_at) VALUES (?, ?, ?)`, teamID, userID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) RemoveMember(teamID, userID int64) error {
	res, err := s.db.Exec(`DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)
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

func (s *Store) Members(teamID int64) ([]Member, error) {
	rows, err := s.db.Query(`SELECT tm.user_id, u.name, u.email, u.role, tm.added_at FROM team_members tm JOIN users u ON u.id = tm.user_id WHERE tm.team_id = ? ORDER BY u.name COLLATE NOCASE`, teamID)
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

func (s *Store) TeamsForUser(userID int64) ([]Team, error) {
	rows, err := s.db.Query(`SELECT DISTINCT t.id, t.name, t.created_at, t.archived_at FROM teams t JOIN team_members tm ON tm.team_id = t.id WHERE tm.user_id = ? AND t.archived_at IS NULL ORDER BY t.name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		var archived sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &archived); err != nil {
			return nil, err
		}
		if archived.Valid {
			t.ArchivedAt = archived.String
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (s *Store) Exists(id int64) (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams WHERE id = ? AND archived_at IS NULL`, id).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
