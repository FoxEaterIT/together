package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"jiralike/internal/auth"
	"jiralike/internal/secrets"
	"jiralike/internal/workflow"
)

func openForMigration(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Ping(); err != nil {
		t.Fatal(err)
	}
	return conn
}

// TestOpenMigratesEmptyDatabase checks the first start: every migration is
// recorded once and no workflow is seeded, because a flow belongs to a project
// and a fresh installation has none.
func TestOpenMigratesEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var versions []int
	rows, err := conn.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	rows.Close()
	if len(versions) != 9 || versions[0] != 1 || versions[8] != 9 {
		t.Fatalf("migration versions: %v", versions)
	}

	var states, workflows int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM workflow_states`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM workflows`).Scan(&workflows); err != nil {
		t.Fatal(err)
	}
	if states != 0 || workflows != 0 {
		t.Fatalf("seeded workflows: %d flows, %d states", workflows, states)
	}
	var initialColumns int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('settings') WHERE name = 'initial_state_id'`).Scan(&initialColumns); err != nil {
		t.Fatal(err)
	}
	if initialColumns != 0 {
		t.Fatal("settings.initial_state_id survived 0009")
	}

	// Reopening must not duplicate rows or reapply migrations.
	conn.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if err := again.QueryRow(`SELECT COUNT(*) FROM workflow_states`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 0 {
		t.Fatalf("states after reopen: %d", states)
	}
}

// TestOpenUpgradesVersionOneWithData simulates a database created by the first
// release, fills it with real rows, and checks that the later migrations apply
// without losing data.
func TestOpenUpgradesVersionOneWithData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	conn := openForMigration(t, path)

	body, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(body)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	stamp := "2026-01-01T00:00:00Z"
	if _, err := conn.Exec(`INSERT INTO users (id, name, email, password_hash, role, created_at) VALUES (1, 'Old Admin', 'old@example.test', 'hash', 'admin', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO workflow_states (id, name, category, position, created_at) VALUES (1, 'To do', 'open', 0, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO workflow_states (id, name, category, position, created_at) VALUES (2, 'Done', 'done', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE settings SET initial_state_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id, name, visibility, created_by, created_at) VALUES (7, 'Legacy', 'open', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, project_id, type, title, state_id, created_by, created_at, updated_at) VALUES (9, 7, 'action', 'Survives the upgrade', 1, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO notifications (user_id, kind, item_id, created_at) VALUES (1, 'assigned', 9, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer upgraded.Close()

	var title string
	if err := upgraded.QueryRow(`SELECT title FROM items WHERE id = 9`).Scan(&title); err != nil {
		t.Fatalf("legacy item lost: %v", err)
	}
	if title != "Survives the upgrade" {
		t.Fatalf("legacy item title: %q", title)
	}
	var notifications int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'assigned'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("legacy notification lost: %d", notifications)
	}

	// 0002 adds the profile timestamp, out of range for the legacy rows.
	var updatedAt sql.NullString
	if err := upgraded.QueryRow(`SELECT updated_at FROM users WHERE id = 1`).Scan(&updatedAt); err != nil {
		t.Fatalf("0002 column missing: %v", err)
	}
	// 0004 adds the automation mentions column and the run marker table.
	if _, err := upgraded.Exec(`SELECT mentions FROM automation_actions LIMIT 1`); err != nil {
		t.Fatalf("0004 column missing: %v", err)
	}
	var runs int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM automation_rule_runs`).Scan(&runs); err != nil {
		t.Fatalf("0004 table missing: %v", err)
	}
	// 0003 creates the deduplication indexes.
	for _, index := range []string{"notifications_assigned_unread", "notifications_mention_once", "notifications_due_day"} {
		var found int
		if err := upgraded.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Fatalf("index %s missing after upgrade", index)
		}
	}
	// 0005 creates the full-text index and backfills rows that existed before
	// the migration.
	var indexed int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH '"survives"'`).Scan(&indexed); err != nil {
		t.Fatalf("0005 index missing: %v", err)
	}
	if indexed != 1 {
		t.Fatalf("legacy item not backfilled: %d", indexed)
	}
	// 0006 adds the 2FA tables and the administrator switch, off by default.
	var require2FA int
	if err := upgraded.QueryRow(`SELECT require_2fa FROM settings WHERE id = 1`).Scan(&require2FA); err != nil {
		t.Fatalf("0006 column missing: %v", err)
	}
	if require2FA != 0 {
		t.Fatalf("require_2fa default = %d, want 0", require2FA)
	}
	for _, table := range []string{"user_totp", "user_recovery_codes", "login_challenges"} {
		var found int
		if err := upgraded.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Fatalf("table %s missing after upgrade", table)
		}
	}

	// 0007 rebuilds users so Spanish fits the locale check while keeping rows.
	var legacyName, legacyLocale string
	if err := upgraded.QueryRow(`SELECT name, locale FROM users WHERE id = 1`).Scan(&legacyName, &legacyLocale); err != nil {
		t.Fatalf("legacy user lost after 0007: %v", err)
	}
	if legacyName != "Old Admin" || legacyLocale != "en" {
		t.Fatalf("legacy user after 0007: %q %q", legacyName, legacyLocale)
	}
	if _, err := upgraded.Exec(`UPDATE users SET locale = 'es' WHERE id = 1`); err != nil {
		t.Fatalf("0007 does not accept es: %v", err)
	}
	if err := upgraded.QueryRow(`SELECT locale FROM users WHERE id = 1`).Scan(&legacyLocale); err != nil || legacyLocale != "es" {
		t.Fatalf("spanish locale after update: %q %v", legacyLocale, err)
	}
	var usersSQL string
	if err := upgraded.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&usersSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(usersSQL, "COLLATE NOCASE UNIQUE") || !strings.Contains(usersSQL, "CHECK (locale IN ('en', 'it', 'es'))") {
		t.Fatalf("users table after 0007 lost a constraint: %s", usersSQL)
	}

	var versions int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 9 {
		t.Fatalf("migration rows after upgrade: %d", versions)
	}

	// 0009 copies the legacy flow into every project and binds all four item
	// types, then moves existing items onto the copy.
	var workflowID, workflowProjectID int64
	if err := upgraded.QueryRow(`SELECT id, project_id FROM workflows WHERE project_id = 7`).Scan(&workflowID, &workflowProjectID); err != nil {
		t.Fatalf("0009 workflow missing: %v", err)
	}
	if workflowProjectID != 7 {
		t.Fatalf("0009 workflow project = %d", workflowProjectID)
	}
	var stateID int64
	if err := upgraded.QueryRow(`SELECT ws.id FROM items i JOIN workflow_states ws ON ws.id = i.state_id WHERE i.id = 9 AND ws.workflow_id = ?`, workflowID).Scan(&stateID); err != nil {
		t.Fatalf("0009 did not remap the item: %v", err)
	}
	if stateID == 0 {
		t.Fatal("0009 remapped the item to state 0")
	}
	var legacyStates int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ?`, workflowID).Scan(&legacyStates); err != nil || legacyStates != 2 {
		t.Fatalf("0009 states = %d (%v)", legacyStates, err)
	}
	var bindings int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM project_workflows WHERE project_id = 7 AND workflow_id = ?`, workflowID).Scan(&bindings); err != nil || bindings != 4 {
		t.Fatalf("0009 bindings = %d (%v)", bindings, err)
	}
	var initial int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ? AND is_initial = 1`, workflowID).Scan(&initial); err != nil || initial != 1 {
		t.Fatalf("0009 initial states = %d (%v)", initial, err)
	}
}

// TestOpenUpgradesVersionFiveWithData starts from a populated v1 installation
// already on the search migration and checks that 0006 applies without losing
// rows and leaves every existing account without a second factor.
func TestOpenUpgradesVersionFiveWithData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	conn := openForMigration(t, path)
	for _, name := range []string{
		"0001_init.sql",
		"0002_user_profile.sql",
		"0003_notifications_unique.sql",
		"0004_automation_runs.sql",
		"0005_search_fts.sql",
	} {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 5; version++ {
		if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, version); err != nil {
			t.Fatal(err)
		}
	}
	stamp := "2026-01-01T00:00:00Z"
	if _, err := conn.Exec(`INSERT INTO users (id, name, email, password_hash, role, created_at) VALUES (1, 'Admin', 'admin@example.test', 'hash', 'admin', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO workflow_states (id, name, category, position, created_at) VALUES (1, 'To do', 'open', 0, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE settings SET initial_state_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id, name, visibility, created_by, created_at) VALUES (7, 'Kept', 'open', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, project_id, type, title, state_id, created_by, created_at, updated_at) VALUES (9, 7, 'action', 'Survives 0006', 1, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade from 0005: %v", err)
	}
	defer upgraded.Close()
	var title string
	if err := upgraded.QueryRow(`SELECT title FROM items WHERE id = 9`).Scan(&title); err != nil || title != "Survives 0006" {
		t.Fatalf("legacy item after 0006: %q %v", title, err)
	}
	var totpRows int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM user_totp`).Scan(&totpRows); err != nil || totpRows != 0 {
		t.Fatalf("user_totp after upgrade: %d %v", totpRows, err)
	}
	var maxVersion int
	if err := upgraded.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil || maxVersion != 9 {
		t.Fatalf("schema version after upgrade: %d %v", maxVersion, err)
	}
}

// TestOpenUpgradesVersionFourWithData starts from a populated installation that
// already has notifications, automation metadata and comments but no search
// index, then checks that 0005 backfills title and comment text and that 0006
// adds the 2FA tables without touching existing rows.
func TestOpenUpgradesVersionFourWithData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	conn := openForMigration(t, path)
	for _, name := range []string{
		"0001_init.sql",
		"0002_user_profile.sql",
		"0003_notifications_unique.sql",
		"0004_automation_runs.sql",
	} {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 4; version++ {
		if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, version); err != nil {
			t.Fatal(err)
		}
	}
	stamp := "2026-01-01T00:00:00Z"
	if _, err := conn.Exec(`INSERT INTO users (id, name, email, password_hash, role, created_at) VALUES (1, 'Admin', 'admin@example.test', 'hash', 'admin', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO workflow_states (id, name, category, position, created_at) VALUES (1, 'To do', 'open', 0, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`UPDATE settings SET initial_state_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id, name, visibility, created_by, created_at) VALUES (7, 'Legacy', 'open', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, project_id, type, title, description, state_id, created_by, created_at, updated_at) VALUES (9, 7, 'action', 'Quarterly roadmap', 'Search backfill target', 1, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO comments (id, item_id, author_id, body, created_at) VALUES (5, 9, 1, 'Remember the girasole word in this comment', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO notifications (user_id, kind, item_id, created_at) VALUES (1, 'assigned', 9, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade from 0004: %v", err)
	}
	defer upgraded.Close()

	var title, body string
	if err := upgraded.QueryRow(`SELECT title FROM items WHERE id = 9`).Scan(&title); err != nil || title != "Quarterly roadmap" {
		t.Fatalf("legacy item after upgrade: %q %v", title, err)
	}
	if err := upgraded.QueryRow(`SELECT body FROM comments WHERE id = 5`).Scan(&body); err != nil || !strings.Contains(body, "girasole") {
		t.Fatalf("legacy comment after upgrade: %q %v", body, err)
	}
	for _, term := range []string{"roadmap", "girasole"} {
		var itemID int64
		if err := upgraded.QueryRow(`SELECT item_id FROM items_fts WHERE items_fts MATCH ?`, `"`+term+`"`).Scan(&itemID); err != nil {
			t.Fatalf("0005 did not backfill %q: %v", term, err)
		}
		if itemID != 9 {
			t.Fatalf("0005 backfilled %q as item %d, want 9", term, itemID)
		}
	}
	var notifications int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'assigned'`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notification lost after upgrade: %d %v", notifications, err)
	}
	var maxVersion int
	if err := upgraded.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&maxVersion); err != nil || maxVersion != 9 {
		t.Fatalf("schema version after upgrade: %d %v", maxVersion, err)
	}
	var require2FA int
	if err := upgraded.QueryRow(`SELECT require_2fa FROM settings WHERE id = 1`).Scan(&require2FA); err != nil || require2FA != 0 {
		t.Fatalf("require_2fa after upgrade: %d %v", require2FA, err)
	}
	for _, table := range []string{"user_totp", "user_recovery_codes", "login_challenges"} {
		var found int
		if err := upgraded.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil || found != 1 {
			t.Fatalf("table %s after upgrade: %d %v", table, found, err)
		}
	}
}

// TestBackupPreservesSearchAndTwoFactor checks the release backup contract at
// the storage layer: VACUUM INTO carries the FTS index and the 2FA tables, the
// restored copy still answers a full-text query, and the index can be rebuilt
// after a restore without losing the result.
func TestBackupPreservesSearchAndTwoFactor(t *testing.T) {
	dir := t.TempDir()
	source, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	res, err := source.Exec(`INSERT INTO users (name, email, password_hash, role, created_at) VALUES ('Admin', 'backup@example.test', 'x', 'admin', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err = tx.Exec(`INSERT INTO projects (name, visibility, created_by, created_at) VALUES ('Backup', 'open', ?, '2026-01-01T00:00:00Z')`, userID)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	projectID, _ := res.LastInsertId()
	if err := workflow.SeedDefault(tx, projectID); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	var stateID int64
	if err := tx.QueryRow(`
		SELECT ws.id
		FROM project_workflows pw
		JOIN workflow_states ws ON ws.workflow_id = pw.workflow_id AND ws.is_initial = 1
		WHERE pw.project_id = ? AND pw.item_type = 'action'`, projectID).Scan(&stateID); err != nil {
		t.Fatalf("initial state: %v", err)
	}
	res, err = tx.Exec(`INSERT INTO items (project_id, type, title, state_id, created_by, created_at, updated_at) VALUES (?, 'action', 'Restore target', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID, stateID, userID)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
	itemID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO comments (item_id, author_id, body, created_at) VALUES (?, ?, 'The restoreneedle lives in this comment', '2026-01-01T00:00:00Z')`, itemID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO user_totp (user_id, secret_enc, confirmed_at, last_used_step, created_at) VALUES (?, 'JBSWY3DPEHPK3PXP', '2026-01-01T00:00:00Z', 5, '2026-01-01T00:00:00Z')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO user_recovery_codes (user_id, code_hash, created_at) VALUES (?, 'hash-one', '2026-01-01T00:00:00Z')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE settings SET require_2fa = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(dir, "backup", "snapshot.db")
	if err := Backup(source, backupPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()

	var indexed int64
	if err := restored.QueryRow(`SELECT item_id FROM items_fts WHERE items_fts MATCH '"restoreneedle"'`).Scan(&indexed); err != nil || indexed != itemID {
		t.Fatalf("search after restore: item %d, want %d (%v)", indexed, itemID, err)
	}
	// The restored copy carries whatever the backup held; the startup backfill in
	// auth is what guarantees a sealed secret, so exercise that step here too.
	box, err := secrets.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	store := auth.New(restored, nil)
	store.SetSecrets(box)
	if sealed, err := store.SealLegacy(box); err != nil || sealed != 1 {
		t.Fatalf("seal legacy secrets after restore: %d %v", sealed, err)
	}
	var stored string
	var step int64
	if err := restored.QueryRow(`SELECT secret_enc, last_used_step FROM user_totp WHERE user_id = ?`, userID).Scan(&stored, &step); err != nil || step != 5 {
		t.Fatalf("2FA enrollment after restore: step %d (%v)", step, err)
	}
	if !secrets.IsSealed(stored) {
		t.Fatalf("TOTP secret after restore is not sealed: %q", stored)
	}
	if opened, err := box.Open(stored); err != nil || opened != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("sealed TOTP secret does not open: %q %v", opened, err)
	}
	var recovery int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&recovery); err != nil || recovery != 1 {
		t.Fatalf("recovery codes after restore: %d (%v)", recovery, err)
	}
	var require2FA int
	if err := restored.QueryRow(`SELECT require_2fa FROM settings WHERE id = 1`).Scan(&require2FA); err != nil || require2FA != 1 {
		t.Fatalf("policy after restore: %d (%v)", require2FA, err)
	}

	if _, err := restored.Exec(`INSERT INTO items_fts(items_fts) VALUES('rebuild')`); err != nil {
		t.Fatalf("rebuild restored index: %v", err)
	}
	if err := restored.QueryRow(`SELECT item_id FROM items_fts WHERE items_fts MATCH '"restoreneedle"'`).Scan(&indexed); err != nil || indexed != itemID {
		t.Fatalf("search after rebuild: item %d, want %d (%v)", indexed, itemID, err)
	}
}

// TestOpenRefusesNewerDatabase protects against running an old binary against a
// schema it does not understand.
func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (99, '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("opening a newer schema should fail")
	}
	if !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBackupRoundTrip checks the snapshot helper at the storage layer: the copy
// opens, carries the schema history and keeps the rows.
func TestBackupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	source, err := Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`INSERT INTO users (name, email, password_hash, role, created_at) VALUES ('A', 'a@example.test', 'x', 'admin', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup", "snapshot.db")
	if err := Backup(source, backupPath); err != nil {
		t.Fatal(err)
	}
	copyConn, err := Open(backupPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer copyConn.Close()
	var name string
	if err := copyConn.QueryRow(`SELECT name FROM users WHERE email = 'a@example.test'`).Scan(&name); err != nil {
		t.Fatalf("snapshot row missing: %v", err)
	}
	if name != "A" {
		t.Fatalf("snapshot name: %q", name)
	}
	if _, err := copyConn.Exec(`DELETE FROM schema_migrations WHERE version = 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := copyConn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (99, '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	copyConn.Close()
	if _, err := Open(backupPath); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("mutated snapshot should be refused, got %v", err)
	}
}
