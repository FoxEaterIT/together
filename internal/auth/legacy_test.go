package auth

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/secrets"
)

// TestSealLegacyRewritesOldRows covers the upgrade path for installations that
// stored bearer secrets and the TOTP secret in clear text. The conversion must
// keep every credential working and must be idempotent.
func TestSealLegacyRewritesOldRows(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := New(conn, nil)

	box, err := secrets.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	store.SetSecrets(box)

	user, _, err := store.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}

	// A personal token written by an older version: value in the column.
	legacyToken := "tgr_" + randomHex(t)
	if _, err := conn.Exec(
		`INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, created_at) VALUES (?, 'old', ?, '', ?)`,
		user.ID, legacyToken, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	// An invitation whose link is the only place the value should exist.
	legacyInvite := "inv_" + randomHex(t)
	now := time.Now().UTC()
	if _, err := conn.Exec(
		`INSERT INTO invites (email, role, token_hash, invited_by, created_at, expires_at) VALUES (?, 'member', ?, ?, ?, ?)`,
		"guest@example.test", legacyInvite, user.ID, now.Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	// A TOTP enrollment with the secret in clear text.
	const plainSecret = "JBSWY3DPEHPK3PXP"
	if _, err := conn.Exec(
		`INSERT INTO user_totp (user_id, secret_enc, confirmed_at, last_used_step, created_at) VALUES (?, ?, ?, 0, ?)`,
		user.ID, plainSecret, now.Format(time.RFC3339), now.Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	changed, err := store.SealLegacy(box)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 3 {
		t.Fatalf("expected three rewritten rows, got %d", changed)
	}

	// The token still authenticates, and the stored value is a digest.
	var storedToken, prefix string
	if err := conn.QueryRow(`SELECT token_hash, token_prefix FROM api_tokens WHERE name = 'old'`).Scan(&storedToken, &prefix); err != nil {
		t.Fatal(err)
	}
	if storedToken != hashSecret(legacyToken) || prefix != tokenPrefix(legacyToken) {
		t.Fatalf("token row after upgrade: %q %q", storedToken, prefix)
	}
	if got, err := store.AuthenticateToken(legacyToken); err != nil || got.ID != user.ID {
		t.Fatalf("legacy token stopped working: %v", err)
	}

	// The invitation link still resolves.
	if email, role, _, _, err := store.InvitePreview(legacyInvite); err != nil || email != "guest@example.test" || role != "member" {
		t.Fatalf("legacy invite stopped working: %q %q %v", email, role, err)
	}

	// The TOTP secret is sealed and opens to the original value.
	var sealed string
	if err := conn.QueryRow(`SELECT secret_enc FROM user_totp WHERE user_id = ?`, user.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsSealed(sealed) {
		t.Fatalf("TOTP secret after upgrade is not sealed: %q", sealed)
	}
	if opened, err := box.Open(sealed); err != nil || opened != plainSecret {
		t.Fatalf("sealed TOTP secret: %q %v", opened, err)
	}

	// Running again rewrites nothing.
	changed, err = store.SealLegacy(box)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("second pass rewrote %d rows", changed)
	}
}

func TestStartingTOTPWithoutKeyIsRefused(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := New(conn, nil)
	user, _, err := store.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StartTOTP(user.ID); err != secrets.ErrNoKey {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
	var rows int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM user_totp WHERE user_id = ?`, user.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a refused enrollment must not store a secret: %d rows", rows)
	}
}

func randomHex(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}
