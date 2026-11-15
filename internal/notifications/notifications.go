// Package notifications creates deduplicated in-app notifications (assignment,
// mention, daily due summary, automation) and tracks whether email was sent.
package notifications

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("notification not found")

const (
	maxListLimit = 200
	defaultLimit = 50
)

// visibleClause hides notifications whose item or project is no longer
// readable by the recipient. It mirrors the project access rules: admins see
// everything; archived projects stay visible only to managers who manage them;
// private projects require a direct or team grant. Due summaries have no item
// and are always visible.
const visibleClause = `
	(n.item_id IS NULL OR EXISTS (
		SELECT 1 FROM items i
		JOIN projects p ON p.id = i.project_id
		WHERE i.id = n.item_id
		  AND i.archived_at IS NULL
		  AND (
			? = 'admin'
			OR (p.archived_at IS NULL AND (
				p.visibility = 'open'
				OR p.created_by = ?
				OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = ?)
				OR EXISTS (
					SELECT 1 FROM project_teams pt
					JOIN team_members tm ON tm.team_id = pt.team_id
					WHERE pt.project_id = p.id AND tm.user_id = ?
				)
			))
			OR (p.archived_at IS NOT NULL AND ? = 'manager' AND (
				p.created_by = ?
				OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = ?)
				OR EXISTS (
					SELECT 1 FROM project_teams pt
					JOIN team_members tm ON tm.team_id = pt.team_id
					WHERE pt.project_id = p.id AND tm.user_id = ?
				)
			))
		  )
	))`

// Notification is one inbox row. ItemTitle, ProjectID, ProjectName and
// CommentExcerpt are filled by the list query so the UI can render a row
// without extra requests. Link points at the destination screen.
type Notification struct {
	ID             int64           `json:"id"`
	UserID         int64           `json:"user_id,omitempty"`
	Kind           string          `json:"kind"`
	ItemID         *int64          `json:"item_id"`
	CommentID      *int64          `json:"comment_id"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	CreatedAt      string          `json:"created_at"`
	ReadAt         string          `json:"read_at,omitempty"`
	EmailedAt      string          `json:"emailed_at,omitempty"`
	Read           bool            `json:"read"`
	ItemTitle      string          `json:"item_title,omitempty"`
	ProjectID      *int64          `json:"project_id,omitempty"`
	ProjectName    string          `json:"project_name,omitempty"`
	CommentExcerpt string          `json:"comment_excerpt,omitempty"`
	Link           string          `json:"link"`
}

// DueRecipient is one user with at least one open, assigned item whose due
// date is today or in the past.
type DueRecipient struct {
	UserID             int64
	Count              int
	Name               string
	Email              string
	Locale             string
	EmailNotifications bool
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateAssigned inserts an assignment notification unless the same user
// already has an unread one for the item. Once it is read, a later assignment
// may create a fresh notification.
func (s *Store) CreateAssigned(userID, itemID int64) (Notification, bool, error) {
	return s.insert(userID, "assigned", &itemID, nil, "", timestamp())
}

// CreateMention inserts one mention notification per comment and user. Editing
// a comment can therefore re-run the call without spamming the same person.
func (s *Store) CreateMention(userID, itemID, commentID int64) (Notification, bool, error) {
	return s.insert(userID, "mention", &itemID, &commentID, "", timestamp())
}

// CreateAutomation is the explicit notify action. It reaches only the people
// who are assigned at that moment; the caller decides who those are.
func (s *Store) CreateAutomation(userID, itemID int64) (Notification, bool, error) {
	return s.insert(userID, "automation", &itemID, nil, "", timestamp())
}

// CreateDue inserts the daily summary for a user. createdAt fixes both the
// day used by the payload and the UTC day used for deduplication.
func (s *Store) CreateDue(userID int64, count int, createdAt string) (Notification, bool, error) {
	createdAt = normalizeTimestamp(createdAt)
	if len(createdAt) < 10 {
		return Notification{}, false, fmt.Errorf("created at must start with YYYY-MM-DD")
	}
	day := createdAt[:10]
	payload, err := json.Marshal(map[string]any{"day": day, "count": count})
	if err != nil {
		return Notification{}, false, err
	}
	return s.insert(userID, "due", nil, nil, string(payload), createdAt)
}

func (s *Store) insert(userID int64, kind string, itemID, commentID *int64, payload, createdAt string) (Notification, bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO notifications (user_id, kind, item_id, comment_id, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, kind, itemID, commentID, payload, createdAt,
	)
	if err != nil {
		return Notification{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Notification{}, false, err
	}
	n := Notification{
		UserID:    userID,
		Kind:      kind,
		ItemID:    itemID,
		CommentID: commentID,
		CreatedAt: createdAt,
	}
	if affected == 0 {
		return n, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Notification{}, false, err
	}
	n.ID = id
	if payload != "" {
		n.Payload = json.RawMessage(payload)
	}
	return n, true, nil
}

// List returns the newest notifications first. beforeID pages backwards.
// Rows whose project or item is no longer readable are dropped in SQL.
func (s *Store) List(userID int64, role string, unreadOnly bool, limit int, beforeID int64) ([]Notification, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	query := `
		SELECT n.id, n.user_id, n.kind, n.item_id, n.comment_id, n.payload,
		       n.created_at, COALESCE(n.read_at, ''), COALESCE(n.emailed_at, ''),
		       COALESCE(i.title, ''), i.project_id, COALESCE(p.name, ''),
		       COALESCE(c.body, '')
		FROM notifications n
		LEFT JOIN items i ON i.id = n.item_id
		LEFT JOIN projects p ON p.id = i.project_id
		LEFT JOIN comments c ON c.id = n.comment_id
		WHERE n.user_id = ? AND ` + visibleClause
	args := append([]any{userID}, visibilityArgs(role, userID)...)
	if unreadOnly {
		query += ` AND n.read_at IS NULL`
	}
	if beforeID > 0 {
		query += ` AND n.id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY n.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Notification{}
	for rows.Next() {
		var n Notification
		var itemID, commentID, projectID sql.NullInt64
		var payload, readAt, emailedAt string
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.Kind, &itemID, &commentID, &payload,
			&n.CreatedAt, &readAt, &emailedAt,
			&n.ItemTitle, &projectID, &n.ProjectName, &n.CommentExcerpt,
		); err != nil {
			return nil, err
		}
		if itemID.Valid {
			id := itemID.Int64
			n.ItemID = &id
		}
		if commentID.Valid {
			id := commentID.Int64
			n.CommentID = &id
		}
		if projectID.Valid {
			id := projectID.Int64
			n.ProjectID = &id
		}
		if payload != "" {
			n.Payload = json.RawMessage(payload)
		}
		n.ReadAt = readAt
		n.Read = readAt != ""
		n.EmailedAt = emailedAt
		n.CommentExcerpt = excerpt(n.CommentExcerpt)
		n.Link = destination(n)
		out = append(out, n)
	}
	return out, rows.Err()
}

// Visible reports whether one notification would be listed for this user. The
// realtime publisher uses it before pushing, so a stream can never announce a
// row that List would hide.
func (s *Store) Visible(userID int64, role string, id int64) (bool, error) {
	count := 0
	query := `SELECT COUNT(*) FROM notifications n WHERE n.id = ? AND n.user_id = ? AND ` + visibleClause
	args := append([]any{id, userID}, visibilityArgs(role, userID)...)
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// UnreadCount is what the bell shows. It applies the same visibility filter as
// List, so a revoked project cannot leave a phantom badge.
func (s *Store) UnreadCount(userID int64, role string) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM notifications n WHERE n.user_id = ? AND n.read_at IS NULL AND ` + visibleClause
	args := append([]any{userID}, visibilityArgs(role, userID)...)
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func visibilityArgs(role string, userID int64) []any {
	return []any{role, userID, userID, userID, role, userID, userID, userID}
}

// MarkRead is idempotent: marking an already read notification succeeds.
func (s *Store) MarkRead(userID, id int64) error {
	res, err := s.db.Exec(`UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at IS NULL`, timestamp(), id, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id = ? AND user_id = ?`, id, userID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead clears the whole inbox for one user and reports the count.
func (s *Store) MarkAllRead(userID int64) (int, error) {
	res, err := s.db.Exec(`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, timestamp(), userID)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	return int(affected), err
}

// MarkEmailed records that SMTP accepted the message.
func (s *Store) MarkEmailed(id int64, at string) error {
	_, err := s.db.Exec(`UPDATE notifications SET emailed_at = ? WHERE id = ?`, normalizeTimestamp(at), id)
	return err
}

// Due recipients are active users with at least one open assigned item that
// is due today or overdue. Archived items and archived projects never appear.
func (s *Store) DueRecipients(day string) ([]DueRecipient, error) {
	rows, err := s.db.Query(`
		SELECT u.id, u.name, u.email, u.locale, u.email_notifications, COUNT(DISTINCT i.id)
		FROM item_assignees ia
		JOIN items i ON i.id = ia.item_id
		JOIN projects p ON p.id = i.project_id
		JOIN workflow_states ws ON ws.id = i.state_id
		JOIN users u ON u.id = ia.user_id
		WHERE i.archived_at IS NULL
		  AND p.archived_at IS NULL
		  AND ws.category = 'open'
		  AND i.due_on IS NOT NULL AND i.due_on <> '' AND i.due_on <= ?
		  AND u.disabled_at IS NULL
		  AND (u.role <> 'guest' OR u.guest_expires_on IS NULL OR u.guest_expires_on >= ?)
		  AND (
			u.role = 'admin'
			OR p.visibility = 'open'
			OR p.created_by = u.id
			OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = u.id)
			OR EXISTS (
				SELECT 1 FROM project_teams pt
				JOIN team_members tm ON tm.team_id = pt.team_id
				WHERE pt.project_id = p.id AND tm.user_id = u.id
			)
		  )
		GROUP BY u.id, u.name, u.email, u.locale, u.email_notifications
		ORDER BY u.id`, day, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DueRecipient{}
	for rows.Next() {
		var r DueRecipient
		var emailNotifications int
		if err := rows.Scan(&r.UserID, &r.Name, &r.Email, &r.Locale, &emailNotifications, &r.Count); err != nil {
			return nil, err
		}
		r.EmailNotifications = emailNotifications == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// DayDue returns the summary already created for a user on the given UTC day.
func (s *Store) DayDue(userID int64, day string) (Notification, bool, error) {
	var n Notification
	var payload, readAt, emailedAt string
	err := s.db.QueryRow(`
		SELECT id, user_id, kind, payload, created_at, COALESCE(read_at, ''), COALESCE(emailed_at, '')
		FROM notifications
		WHERE user_id = ? AND kind = 'due' AND substr(created_at, 1, 10) = ?
		ORDER BY id DESC LIMIT 1`, userID, day).
		Scan(&n.ID, &n.UserID, &n.Kind, &payload, &n.CreatedAt, &readAt, &emailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Notification{}, false, nil
	}
	if err != nil {
		return Notification{}, false, err
	}
	if payload != "" {
		n.Payload = json.RawMessage(payload)
	}
	n.ReadAt = readAt
	n.Read = readAt != ""
	n.EmailedAt = emailedAt
	n.Link = "/calendar"
	return n, true, nil
}

func destination(n Notification) string {
	if n.ItemID == nil {
		return "/calendar"
	}
	if n.ProjectID != nil {
		return fmt.Sprintf("/projects/%d/items/%d", *n.ProjectID, *n.ItemID)
	}
	return fmt.Sprintf("/items/%d", *n.ItemID)
}

func excerpt(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	runes := []rune(body)
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return body
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func normalizeTimestamp(value string) string {
	if value == "" {
		return timestamp()
	}
	return value
}
