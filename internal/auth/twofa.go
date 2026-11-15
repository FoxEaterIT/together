package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"jiralike/internal/totp"
)

const (
	maxChallengeAttempts = 5
	verifyChallengeTTL   = 5 * time.Minute
	enrollChallengeTTL   = 15 * time.Minute
	recoveryCodeCount    = 10
)

// TOTPStatus is the safe view of a user's second factor: never the secret.
type TOTPStatus struct {
	Enabled                bool `json:"enabled"`
	Pending                bool `json:"pending"`
	RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
}

// Require2FA reports whether the administrator forces enrollment for every
// non-guest account on its next sign-in.
func (s *Store) Require2FA() (bool, error) {
	var value int
	if err := s.db.QueryRow(`SELECT require_2fa FROM settings WHERE id = 1`).Scan(&value); err != nil {
		return false, err
	}
	return value == 1, nil
}

func (s *Store) SetRequire2FA(value bool) error {
	n := 0
	if value {
		n = 1
	}
	_, err := s.db.Exec(`UPDATE settings SET require_2fa = ? WHERE id = 1`, n)
	return err
}

func (s *Store) TOTPStatus(userID int64) (TOTPStatus, error) {
	var status TOTPStatus
	var confirmed sql.NullString
	err := s.db.QueryRow(`SELECT confirmed_at FROM user_totp WHERE user_id = ?`, userID).Scan(&confirmed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status.Enabled = false
		status.Pending = false
	case err != nil:
		return TOTPStatus{}, err
	default:
		status.Enabled = confirmed.Valid
		status.Pending = !confirmed.Valid
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID,
	).Scan(&status.RecoveryCodesRemaining); err != nil {
		return TOTPStatus{}, err
	}
	return status, nil
}

// StartTOTP creates (or replaces) an unconfirmed secret. Nothing is enforced
// until ConfirmTOTP succeeds. A confirmed enrollment must be disabled first.
func (s *Store) StartTOTP(userID int64) (secret, uri string, err error) {
	var confirmed sql.NullString
	err = s.db.QueryRow(`SELECT confirmed_at FROM user_totp WHERE user_id = ?`, userID).Scan(&confirmed)
	if err == nil && confirmed.Valid {
		return "", "", ErrTOTPEnabled
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	var email string
	if err := s.db.QueryRow(`SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		return "", "", err
	}
	secret, err = totp.GenerateSecret()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sealed, err := s.secrets.Seal(secret)
	if err != nil {
		return "", "", err
	}
	_, err = s.db.Exec(`
		INSERT INTO user_totp (user_id, secret_enc, confirmed_at, last_used_step, created_at)
		VALUES (?, ?, NULL, 0, ?)
		ON CONFLICT (user_id) DO UPDATE SET
			secret_enc = excluded.secret_enc,
			confirmed_at = NULL,
			last_used_step = 0,
			created_at = excluded.created_at`,
		userID, sealed, now)
	if err != nil {
		return "", "", err
	}
	return secret, totp.URI(email, secret), nil
}

// ConfirmTOTP verifies the first code and returns the recovery codes exactly
// once. Only their SHA-256 hashes are stored.
func (s *Store) ConfirmTOTP(userID int64, code string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := s.confirmPendingTx(tx, userID, code, now); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodesTx(tx, userID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

// ConfirmEnrollment does the same as ConfirmTOTP but is authorized by a
// short-lived scope=enroll challenge instead of a session. The challenge is
// consumed on success.
func (s *Store) ConfirmEnrollment(challengeToken, code string) (int64, []string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	challengeID, userID, err := challengeByTokenTx(tx, challengeToken, "enroll", now)
	if err != nil {
		return 0, nil, err
	}
	if role, err := userRoleTx(tx, userID); err != nil {
		return 0, nil, err
	} else if role == "guest" {
		return 0, nil, ErrInvalidChallenge
	}
	if _, err := s.confirmPendingTx(tx, userID, code, now); err != nil {
		if errors.Is(err, ErrInvalidCode) {
			if err := bumpChallengeTx(tx, challengeID); err != nil {
				return 0, nil, err
			}
			return 0, nil, err
		}
		return 0, nil, err
	}
	codes, err := replaceRecoveryCodesTx(tx, userID, now)
	if err != nil {
		return 0, nil, err
	}
	if _, err := tx.Exec(`UPDATE login_challenges SET consumed_at = ? WHERE id = ?`, now.Format(time.RFC3339), challengeID); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return userID, codes, nil
}

// EnrollChallengeUser validates a scope=enroll challenge read-only, so the
// client can start enrollment without burning an attempt.
func (s *Store) EnrollChallengeUser(challengeToken string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	_, userID, err := challengeByTokenTx(tx, challengeToken, "enroll", time.Now().UTC())
	if err != nil {
		return 0, err
	}
	role, err := userRoleTx(tx, userID)
	if err != nil {
		return 0, err
	}
	if role == "guest" {
		return 0, ErrInvalidChallenge
	}
	return userID, nil
}

// DisableTOTP requires the current password plus a TOTP or recovery code.
func (s *Store) DisableTOTP(userID int64, password, code string) error {
	if err := s.verifyPassword(userID, password); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	ok, err := s.consumeCodeTx(tx, userID, code, now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCode
	}
	if _, err := tx.Exec(`DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_totp WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// RegenerateRecoveryCodes invalidates every previous code and returns the new
// set once.
func (s *Store) RegenerateRecoveryCodes(userID int64, password, code string) ([]string, error) {
	if err := s.verifyPassword(userID, password); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	ok, err := s.consumeCodeTx(tx, userID, code, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCode
	}
	codes, err := replaceRecoveryCodesTx(tx, userID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

// ResetTOTP is the administrative escape hatch: it removes the second factor
// and revokes the user's open sessions, so a stolen session cannot outlive the
// reset.
func (s *Store) ResetTOTP(userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_totp WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM login_challenges WHERE user_id = ? AND consumed_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateChallenge issues a single-use login challenge. Only its hash is stored.
func (s *Store) CreateChallenge(userID int64, scope string, ttl time.Duration) (string, time.Time, error) {
	if scope != "verify" && scope != "enroll" {
		return "", time.Time{}, ErrInvalidChallenge
	}
	token, hash, err := newSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	_, err = s.db.Exec(`
		INSERT INTO login_challenges (user_id, token_hash, scope, created_at, expires_at, attempts)
		VALUES (?, ?, ?, ?, ?, 0)`,
		userID, hash, scope, now.Format(time.RFC3339), expires.Format(time.RFC3339))
	if err != nil {
		return "", time.Time{}, err
	}
	// Housekeeping: expired and consumed challenges have no further use.
	_, _ = s.db.Exec(
		`DELETE FROM login_challenges WHERE expires_at <= ? OR consumed_at IS NOT NULL`,
		now.Format(time.RFC3339))
	return token, expires, nil
}

// CreateVerifyChallenge is the second login step after a correct password.
func (s *Store) CreateVerifyChallenge(userID int64) (string, time.Time, error) {
	return s.CreateChallenge(userID, "verify", verifyChallengeTTL)
}

// CreateEnrollChallenge is used when an administrator forces enrollment or an
// invited account must set up 2FA before it gets a session.
func (s *Store) CreateEnrollChallenge(userID int64) (string, time.Time, error) {
	return s.CreateChallenge(userID, "enroll", enrollChallengeTTL)
}

// CompleteLogin consumes a scope=verify challenge and a TOTP or recovery code
// in one transaction. Any failure counts as one attempt on the challenge.
func (s *Store) CompleteLogin(challengeToken, code string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	challengeID, userID, err := challengeByTokenTx(tx, challengeToken, "verify", now)
	if err != nil {
		return 0, err
	}
	ok, err := s.consumeCodeTx(tx, userID, code, now)
	if err != nil {
		return 0, err
	}
	if !ok {
		if err := bumpChallengeTx(tx, challengeID); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, ErrInvalidCode
	}
	if _, err := tx.Exec(`UPDATE login_challenges SET consumed_at = ? WHERE id = ?`, now.Format(time.RFC3339), challengeID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) verifyPassword(userID int64, password string) error {
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrInvalidLogin
	}
	return nil
}

func challengeByTokenTx(tx *sql.Tx, token, scope string, now time.Time) (int64, int64, error) {
	if strings.TrimSpace(token) == "" {
		return 0, 0, ErrInvalidChallenge
	}
	var id, userID int64
	var attempts int
	var expires string
	var consumed sql.NullString
	err := tx.QueryRow(
		`SELECT id, user_id, expires_at, attempts, consumed_at FROM login_challenges WHERE token_hash = ? AND scope = ?`,
		hashSecret(token), scope,
	).Scan(&id, &userID, &expires, &attempts, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrInvalidChallenge
	}
	if err != nil {
		return 0, 0, err
	}
	if consumed.Valid || attempts >= maxChallengeAttempts || expires <= now.Format(time.RFC3339) {
		return 0, 0, ErrInvalidChallenge
	}
	return id, userID, nil
}

func bumpChallengeTx(tx *sql.Tx, challengeID int64) error {
	_, err := tx.Exec(
		`UPDATE login_challenges SET attempts = attempts + 1 WHERE id = ? AND consumed_at IS NULL`,
		challengeID)
	return err
}

func userRoleTx(tx *sql.Tx, userID int64) (string, error) {
	var role string
	if err := tx.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		return "", err
	}
	return role, nil
}

// confirmPendingTx accepts a code for an unconfirmed enrollment and marks it
// confirmed. It returns the accepted step for logging-free auditing.
func (s *Store) confirmPendingTx(tx *sql.Tx, userID int64, code string, now time.Time) (int64, error) {
	var stored string
	var lastStep int64
	var confirmed sql.NullString
	err := tx.QueryRow(
		`SELECT secret_enc, last_used_step, confirmed_at FROM user_totp WHERE user_id = ?`, userID,
	).Scan(&stored, &lastStep, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrTOTPNotStarted
	}
	if err != nil {
		return 0, err
	}
	if confirmed.Valid {
		return 0, ErrTOTPEnabled
	}
	secret, err := s.secrets.Open(stored)
	if err != nil {
		return 0, err
	}
	step, ok := totp.Validate(secret, code, now, lastStep)
	if !ok {
		return 0, ErrInvalidCode
	}
	if _, err := tx.Exec(
		`UPDATE user_totp SET confirmed_at = ?, last_used_step = ? WHERE user_id = ?`,
		now.Format(time.RFC3339), step, userID,
	); err != nil {
		return 0, err
	}
	return step, nil
}

// consumeCodeTx accepts either a six-digit TOTP or a recovery code. TOTP steps
// are persisted to block replay; recovery codes are marked used.
func (s *Store) consumeCodeTx(tx *sql.Tx, userID int64, code string, now time.Time) (bool, error) {
	code = strings.TrimSpace(code)
	if len(code) == totp.Digits && allDigits(code) {
		var stored string
		var lastStep int64
		var confirmed sql.NullString
		err := tx.QueryRow(
			`SELECT secret_enc, last_used_step, confirmed_at FROM user_totp WHERE user_id = ?`, userID,
		).Scan(&stored, &lastStep, &confirmed)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !confirmed.Valid {
			return false, nil
		}
		secret, err := s.secrets.Open(stored)
		if err != nil {
			return false, err
		}
		step, ok := totp.Validate(secret, code, now, lastStep)
		if !ok {
			return false, nil
		}
		if _, err := tx.Exec(`UPDATE user_totp SET last_used_step = ? WHERE user_id = ?`, step, userID); err != nil {
			return false, err
		}
		return true, nil
	}
	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return false, nil
	}
	var id int64
	err := tx.QueryRow(
		`SELECT id FROM user_recovery_codes WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		userID, hashSecret(normalized),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(
		`UPDATE user_recovery_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`,
		now.Format(time.RFC3339), id,
	); err != nil {
		return false, err
	}
	return true, nil
}

func replaceRecoveryCodesTx(tx *sql.Tx, userID int64, now time.Time) ([]string, error) {
	if _, err := tx.Exec(`DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	for _, hash := range hashes {
		if _, err := tx.Exec(
			`INSERT INTO user_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)`,
			userID, hash, now.Format(time.RFC3339),
		); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// newRecoveryCodes returns ten display codes and their hashes. Each code is
// 80 bits of randomness: high enough entropy that SHA-256 is sufficient and
// bcrypt would only slow recovery down.
func newRecoveryCodes() ([]string, []string, error) {
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		buf := make([]byte, 10)
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, err
		}
		raw := encoder.EncodeToString(buf)
		codes = append(codes, raw[0:4]+"-"+raw[4:8]+"-"+raw[8:12]+"-"+raw[12:16])
		hashes = append(hashes, hashSecret(raw))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	replacer := strings.NewReplacer("-", "", " ", "", "\t", "")
	code = replacer.Replace(code)
	if len(code) != 16 {
		return ""
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z') && !(r >= '2' && r <= '7') {
			return ""
		}
	}
	return code
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
