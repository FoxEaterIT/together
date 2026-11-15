// Package auth owns accounts, sessions, API tokens, invitations, TOTP and
// password hashing. Passwords are stored only as bcrypt hashes, session
// tokens, personal tokens and invitation links only as SHA-256 hashes, and TOTP
// secrets sealed with the installation key. A bearer secret exists in clear
// text only in the HTTP response that creates it.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"jiralike/internal/email"
	"jiralike/internal/secrets"
)

const (
	SessionCookie = "session"
	sessionDays   = 14
)

var (
	ErrInvalidLogin        = errors.New("invalid login")
	ErrInvalidToken        = errors.New("invalid token")
	ErrLastToken           = errors.New("at least one active token must remain")
	ErrNotFound            = errors.New("not found")
	ErrInviteInvalid       = errors.New("invite invalid")
	ErrEmailTaken          = errors.New("email taken")
	ErrDisabled            = errors.New("disabled")
	ErrGuestExpired        = errors.New("guest expired")
	ErrAlreadySetup        = errors.New("already setup")
	ErrLastActiveAdmin     = errors.New("cannot disable the last active admin")
	ErrInvalidGuestDays    = errors.New("guest days must be 7, 30, or 90")
	ErrAdminCannotChange   = errors.New("admin cannot change their own role")
	ErrInvalidRoleChange   = errors.New("invalid role change")
	ErrCannotUpdateSelf    = errors.New("cannot update your own profile via admin endpoint")
	ErrSelfDisable         = errors.New("cannot disable your own account")
	ErrExceedingGuestLimit = errors.New("cannot add more guests than the configured limit")
	ErrTOTPEnabled         = errors.New("two-factor authentication is already enabled")
	ErrTOTPNotStarted      = errors.New("two-factor setup was not started")
	ErrTOTPNotEnabled      = errors.New("two-factor authentication is not enabled")
	ErrTOTPGuest           = errors.New("two-factor authentication is not available for guest accounts")
	ErrInvalidCode         = errors.New("invalid code")
	ErrInvalidChallenge    = errors.New("invalid challenge")
)

type User struct {
	ID                 int64
	Name               string
	Email              string
	Role               string
	Locale             string
	EmailNotifications bool
	GuestExpiresOn     string
	DisabledAt         string
	CreatedAt          string
	TwoFactorEnabled   bool
}

type Store struct {
	db          *sql.DB
	emailSender func(cfg email.Config, to, subject, body string) error
	secrets     *secrets.Box
}

func New(db *sql.DB, emailSender func(cfg email.Config, to, subject, body string) error) *Store {
	return &Store{db: db, emailSender: emailSender}
}

// SetSecrets gives the store the installation key used for values that must be
// recoverable, currently the TOTP secret. Without it, enrollment is refused
// instead of writing a plain-text secret.
func (s *Store) SetSecrets(box *secrets.Box) {
	s.secrets = box
}

func (s *Store) SetupCompleted() (bool, error) {
	var done int
	err := s.db.QueryRow(`SELECT setup_completed FROM settings WHERE id = 1`).Scan(&done)
	if err != nil {
		return false, err
	}
	return done == 1, nil
}

func (s *Store) Setup(name, email, password string) (User, string, error) {
	done, err := s.SetupCompleted()
	if err != nil {
		return User{}, "", err
	}
	if done {
		return User{}, "", ErrAlreadySetup
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO users (name, email, password_hash, role, locale, created_at) VALUES (?, ?, ?, 'admin', 'en', ?)`,
		strings.TrimSpace(name), strings.TrimSpace(email), string(hash), now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, "", ErrEmailTaken
		}
		return User{}, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, "", err
	}
	token, err := insertToken(tx, id, "default", now)
	if err != nil {
		return User{}, "", err
	}
	if _, err := tx.Exec(`UPDATE settings SET setup_completed = 1 WHERE id = 1`); err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", err
	}
	user := User{ID: id, Name: strings.TrimSpace(name), Email: strings.TrimSpace(email), Role: "admin", Locale: "en"}
	return user, token, nil
}

func (s *Store) Login(email, password string) (User, error) {
	var user User
	var hash string
	var disabled sql.NullString
	var notifications int
	var guestExpires sql.NullString
	err := s.db.QueryRow(
		`SELECT id, name, email, password_hash, role, locale, email_notifications, guest_expires_on, disabled_at,
		        EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = users.id AND t.confirmed_at IS NOT NULL)
		 FROM users WHERE email = ?`,
		strings.TrimSpace(email),
	).Scan(&user.ID, &user.Name, &user.Email, &hash, &user.Role, &user.Locale, &notifications, &guestExpires, &disabled, &user.TwoFactorEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrInvalidLogin
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidLogin
	}
	if disabled.Valid {
		return User{}, ErrDisabled
	}
	if user.Role == "guest" && guestExpires.Valid && guestExpires.String < time.Now().UTC().Format("2006-01-02") {
		user.GuestExpiresOn = guestExpires.String
		return user, ErrGuestExpired
	}
	user.EmailNotifications = notifications == 1
	if guestExpires.Valid {
		user.GuestExpiresOn = guestExpires.String
	}
	return user, nil
}

func (s *Store) StartSession(userID int64) (string, time.Time, error) {
	raw, hash, err := newSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(sessionDays * 24 * time.Hour)
	_, err = s.db.Exec(
		`INSERT INTO sessions (user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		userID, hash, now.Format(time.RFC3339), expires.Format(time.RFC3339),
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expires, nil
}

func (s *Store) UserFromSession(raw string) (User, error) {
	if raw == "" {
		return User{}, ErrInvalidLogin
	}
	hash := hashSecret(raw)
	var user User
	var notifications int
	var guestExpires, disabled, revoked sql.NullString
	var sessionExpires string
	err := s.db.QueryRow(`
		SELECT u.id, u.name, u.email, u.role, u.locale, u.email_notifications, u.guest_expires_on, u.disabled_at, s.expires_at, s.revoked_at,
		       EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hash,
	).Scan(&user.ID, &user.Name, &user.Email, &user.Role, &user.Locale, &notifications, &guestExpires, &disabled, &sessionExpires, &revoked, &user.TwoFactorEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrInvalidLogin
	}
	if err != nil {
		return User{}, err
	}
	if revoked.Valid || disabled.Valid || sessionExpires < time.Now().UTC().Format(time.RFC3339) {
		return User{}, ErrInvalidLogin
	}
	if user.Role == "guest" && guestExpires.Valid && guestExpires.String < time.Now().UTC().Format("2006-01-02") {
		return User{}, ErrGuestExpired
	}
	user.EmailNotifications = notifications == 1
	if guestExpires.Valid {
		user.GuestExpiresOn = guestExpires.String
	}
	return user, nil
}

func (s *Store) RevokeSession(raw string) error {
	if raw == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339), hashSecret(raw))
	return err
}

// Token is the profile view of a personal token. Token carries the value and is
// set only in the response that creates it; every later read returns Prefix,
// because only the hash is stored.
type Token struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Token     string `json:"token,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) ListTokens(userID int64) ([]Token, error) {
	rows, err := s.db.Query(`SELECT id, name, token_prefix, created_at FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateToken(userID int64, name string) (Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return Token{}, errors.New("token name must be 1-80 characters")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback()
	raw, _, err := newSecret()
	if err != nil {
		return Token{}, err
	}
	token := "tgr_" + raw
	res, err := tx.Exec(`INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, hashSecret(token), tokenPrefix(token), now)
	if err != nil {
		return Token{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Token{}, err
	}
	if err := tx.Commit(); err != nil {
		return Token{}, err
	}
	return Token{ID: id, Name: name, Prefix: tokenPrefix(token), Token: token, CreatedAt: now}, nil
}

func (s *Store) RevokeToken(userID, tokenID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&active); err != nil {
		return err
	}
	if active <= 1 {
		return ErrLastToken
	}
	res, err := tx.Exec(`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339), tokenID, userID)
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
	return tx.Commit()
}

func (s *Store) AuthenticateToken(raw string) (User, error) {
	if len(raw) < 4 || len(raw) > 256 {
		return User{}, ErrInvalidToken
	}
	var user User
	var disabled sql.NullString
	var expires sql.NullString
	err := s.db.QueryRow(`SELECT u.id, u.name, u.email, u.role, u.locale, u.disabled_at, u.guest_expires_on,
		EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
		FROM api_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=? AND t.revoked_at IS NULL`, hashSecret(raw)).Scan(&user.ID, &user.Name, &user.Email, &user.Role, &user.Locale, &disabled, &expires, &user.TwoFactorEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrInvalidToken
	}
	if err != nil {
		return User{}, err
	}
	if disabled.Valid {
		return User{}, ErrInvalidToken
	}
	if user.Role == "guest" && expires.Valid && expires.String < time.Now().UTC().Format("2006-01-02") {
		return User{}, ErrInvalidToken
	}
	return user, nil
}

func (s *Store) CreateInvite(email, role string, guestDays int, invitedBy int64) (string, error) {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") || (role != "admin" && role != "manager" && role != "member" && role != "guest") {
		return "", errors.New("invalid invite")
	}
	if role == "guest" && guestDays != 7 && guestDays != 30 && guestDays != 90 {
		return "", ErrInvalidGuestDays
	}
	if role != "guest" {
		guestDays = 0
	}
	raw, _, err := newSecret()
	if err != nil {
		return "", err
	}
	token := "inv_" + raw
	now := time.Now().UTC()
	_, err = s.db.Exec(`INSERT INTO invites (email, role, guest_days, token_hash, invited_by, created_at, expires_at) VALUES (?, ?, NULLIF(?, 0), ?, ?, ?, ?)`, email, role, guestDays, hashSecret(token), invitedBy, now.Format(time.RFC3339), now.Add(7*24*time.Hour).Format(time.RFC3339))
	// Email delivery is handled by the HTTP layer, which owns SMTP config and
	// the public base URL. The invite token is always returned for copying; only
	// its hash is stored, so the link cannot be recovered later.
	return token, err
}

// InvitePreview returns the decisions already stored on a pending invitation.
// The token is the bearer secret, so holding it is enough to read these fields.
func (s *Store) InvitePreview(token string) (email, role string, guestDays int, expiresAt string, err error) {
	var days sql.NullInt64
	err = s.db.QueryRow(
		`SELECT email, role, guest_days, expires_at FROM invites
		 WHERE token_hash = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
		hashSecret(token),
	).Scan(&email, &role, &days, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, "", ErrInviteInvalid
	}
	if err != nil {
		return "", "", 0, "", err
	}
	if expiresAt < time.Now().UTC().Format(time.RFC3339) {
		return "", "", 0, "", ErrInviteInvalid
	}
	if days.Valid {
		guestDays = int(days.Int64)
	}
	return email, role, guestDays, expiresAt, nil
}

func (s *Store) ListInvites() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT id, email, role, guest_days, created_at, expires_at FROM invites WHERE accepted_at IS NULL AND revoked_at IS NULL ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var email, role, created, expires string
		var days sql.NullInt64
		if err := rows.Scan(&id, &email, &role, &days, &created, &expires); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "email": email, "role": role, "guest_days": guestDaysValue(days), "created_at": created, "expires_at": expires})
	}
	return out, rows.Err()
}

// guestDaysValue keeps the JSON contract stable: the client expects a number
// or null, never the driver's NullInt64 wrapper.
func guestDaysValue(days sql.NullInt64) any {
	if !days.Valid {
		return nil
	}
	return days.Int64
}

func (s *Store) RevokeInvite(id int64) error {
	res, err := s.db.Exec(`UPDATE invites SET revoked_at=? WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) AcceptInvite(token, name, password string) (User, error) {
	if len(password) < 8 || strings.TrimSpace(name) == "" {
		return User{}, errors.New("name and password of at least 8 characters are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var inviteID, invitedBy int64
	var email, role, expires string
	var days sql.NullInt64
	err = tx.QueryRow(`SELECT id,email,role,guest_days,invited_by,expires_at FROM invites WHERE token_hash=? AND accepted_at IS NULL AND revoked_at IS NULL`, hashSecret(token)).Scan(&inviteID, &email, &role, &days, &invitedBy, &expires)
	if err != nil || expires < time.Now().UTC().Format(time.RFC3339) {
		return User{}, ErrInviteInvalid
	}
	now := time.Now().UTC()
	var guestExpiry any
	if role == "guest" && days.Valid {
		guestExpiry = now.AddDate(0, 0, int(days.Int64)).Format("2006-01-02")
	}
	res, err := tx.Exec(`INSERT INTO users (name,email,password_hash,role,locale,guest_expires_on,invited_by,created_at) VALUES (?,?,?,?,'en',?,?,?)`, strings.TrimSpace(name), email, string(hash), role, guestExpiry, invitedBy, now.Format(time.RFC3339))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if _, err := insertToken(tx, id, "default", now.Format(time.RFC3339)); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(`UPDATE invites SET accepted_at=? WHERE id=?`, now.Format(time.RFC3339), inviteID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Name: strings.TrimSpace(name), Email: email, Role: role, Locale: "en"}, nil
}

func (s *Store) EnsureDefaultToken(userID int64) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := insertToken(s.db, userID, "default", time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetUser(id int64) (User, error) {
	var user User
	var hash string
	var disabled sql.NullString
	var guestExpires sql.NullString
	err := s.db.QueryRow(`
		SELECT u.id, u.name, u.email, u.password_hash, u.role, u.locale, u.email_notifications, u.guest_expires_on, u.disabled_at,
		       EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
		FROM users u
		WHERE u.id = ?
	`, id).Scan(&user.ID, &user.Name, &user.Email, &hash, &user.Role, &user.Locale, &user.EmailNotifications, &guestExpires, &disabled, &user.TwoFactorEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if disabled.Valid {
		user.DisabledAt = disabled.String
	}
	if guestExpires.Valid {
		user.GuestExpiresOn = guestExpires.String
	}
	return user, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, u.role, u.locale, u.email_notifications, u.guest_expires_on, u.disabled_at, u.created_at,
		EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
		FROM users u ORDER BY u.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var guestExpires sql.NullString
		var disabled sql.NullString
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Locale, &u.EmailNotifications, &guestExpires, &disabled, &u.CreatedAt, &u.TwoFactorEnabled); err != nil {
			return nil, err
		}
		if guestExpires.Valid {
			u.GuestExpiresOn = guestExpires.String
		}
		if disabled.Valid {
			u.DisabledAt = disabled.String
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProfile(id int64, name, locale string, emailNotifications int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE users SET name = ?, locale = ?, email_notifications = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(name), locale, emailNotifications, now, id)
	return err
}

// AdminUpdateUser writes every admin-editable field in a single statement so a
// request either applies completely or not at all. guestDays is only honoured
// for guests and extends the guest access window from today. Demoting a guest
// clears guest_expires_on, since that column is only meaningful for guests.
func (s *Store) AdminUpdateUser(id int64, role string, emailNotifications int, disabled sql.NullString, guestDays int) error {
	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339)
	if role != "guest" {
		_, err := s.db.Exec(
			`UPDATE users SET role = ?, email_notifications = ?, disabled_at = ?, guest_expires_on = NULL, updated_at = ? WHERE id = ?`,
			role, emailNotifications, disabled, updatedAt, id,
		)
		return err
	}
	if guestDays > 0 {
		expiry := now.AddDate(0, 0, guestDays).Format("2006-01-02")
		_, err := s.db.Exec(
			`UPDATE users SET role = ?, email_notifications = ?, disabled_at = ?, guest_expires_on = ?, updated_at = ? WHERE id = ?`,
			role, emailNotifications, disabled, expiry, updatedAt, id,
		)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE users SET role = ?, email_notifications = ?, disabled_at = ?, updated_at = ? WHERE id = ?`,
		role, emailNotifications, disabled, updatedAt, id,
	)
	return err
}

func (s *Store) ChangeUserRole(userID int64, newRole string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, newRole, now, userID)
	return err
}

func (s *Store) DisableUser(userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE users SET disabled_at = ?, updated_at = ? WHERE id = ?`, now, now, userID)
	return err
}

func (s *Store) EnableUser(userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE users SET disabled_at = NULL, updated_at = ? WHERE id = ?`, now, userID)
	return err
}

func (s *Store) ExtendGuest(userID int64, guestDays int) error {
	if guestDays != 7 && guestDays != 30 && guestDays != 90 {
		return ErrInvalidGuestDays
	}
	now := time.Now().UTC()
	expiry := now.AddDate(0, 0, guestDays).Format("2006-01-02")
	_, err := s.db.Exec(`UPDATE users SET guest_expires_on = ?, updated_at = ? WHERE id = ?`, expiry, now.Format(time.RFC3339), userID)
	return err
}

func (s *Store) CountActiveAdmins() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&count)
	return count, err
}

func (s *Store) SetEmailNotifications(id int64, value int) error {
	_, err := s.db.Exec(`UPDATE users SET email_notifications = ? WHERE id = ?`, value, id)
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertToken(conn execer, userID int64, name, now string) (string, error) {
	raw, _, err := newSecret()
	if err != nil {
		return "", err
	}
	token := "tgr_" + raw
	_, err = conn.Exec(`INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, hashSecret(token), tokenPrefix(token), now)
	if err != nil {
		return "", fmt.Errorf("insert token: %w", err)
	}
	return token, nil
}

// tokenPrefix keeps the first characters of a personal token so the profile can
// distinguish two entries without storing enough to authenticate.
func tokenPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}

func newSecret() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw := hex.EncodeToString(buf)
	return raw, hashSecret(raw), nil
}

func hashSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SealLegacy rewrites rows written before migration 0008, when personal tokens,
// invitation links and TOTP secrets were stored in clear text. Bearer secrets
// become their SHA-256 hash, TOTP secrets become ciphertext. It is idempotent,
// so it runs on every start and does nothing once the rows are converted.
func (s *Store) SealLegacy(box *secrets.Box) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	changed := 0
	tokens, err := collectValues(tx, `SELECT id, token_hash FROM api_tokens WHERE token_hash GLOB 'tgr_*'`)
	if err != nil {
		return 0, err
	}
	for _, row := range tokens {
		if _, err := tx.Exec(
			`UPDATE api_tokens SET token_hash = ?, token_prefix = ? WHERE id = ?`,
			hashSecret(row.value), tokenPrefix(row.value), row.id,
		); err != nil {
			return 0, err
		}
		changed++
	}

	invites, err := collectValues(tx, `SELECT id, token_hash FROM invites WHERE token_hash GLOB 'inv_*'`)
	if err != nil {
		return 0, err
	}
	for _, row := range invites {
		if _, err := tx.Exec(`UPDATE invites SET token_hash = ? WHERE id = ?`, hashSecret(row.value), row.id); err != nil {
			return 0, err
		}
		changed++
	}

	pending, err := collectValues(tx, `SELECT user_id, secret_enc FROM user_totp WHERE secret_enc <> '' AND secret_enc NOT LIKE ?`, secrets.SealPrefix+"%")
	if err != nil {
		return 0, err
	}
	for _, row := range pending {
		sealed, err := box.Seal(row.value)
		if err != nil {
			return 0, fmt.Errorf("seal TOTP secret: %w", err)
		}
		if _, err := tx.Exec(`UPDATE user_totp SET secret_enc = ? WHERE user_id = ?`, sealed, row.id); err != nil {
			return 0, err
		}
		changed++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changed, nil
}

type storedValue struct {
	id    int64
	value string
}

func collectValues(tx *sql.Tx, query string, args ...any) ([]storedValue, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storedValue{}
	for rows.Next() {
		var row storedValue
		if err := rows.Scan(&row.id, &row.value); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
