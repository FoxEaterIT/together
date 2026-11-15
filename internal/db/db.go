// Package db opens SQLite, applies the embedded migrations in version order,
// seeds the default workflow and produces consistent backups. Schema changes
// live only in internal/db/migrations; application code never alters tables.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", filepath.ToSlash(path))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	if err := seedWorkflow(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func migrate(conn *sql.DB) error {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var latest int
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&latest); err != nil {
		return fmt.Errorf("read latest migration: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	// Refuse a database written by a newer binary: its schema may contain
	// columns or tables this build does not understand, and silently running
	// old code against it would corrupt data. Downgrades require restoring a
	// backup made by the matching version.
	newest := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := versionOf(entry.Name())
		if err != nil {
			return err
		}
		if version > newest {
			newest = version
		}
	}
	if latest > newest {
		return fmt.Errorf("database schema version %d is newer than this binary (%d); restore a matching backup", latest, newest)
	}

	// A migration that rebuilds a table other tables reference (0007 does, to
	// widen the locale check) needs enforcement switched off. PRAGMA is a
	// no-op inside a transaction, so it has to run on the connection before the
	// per-migration transactions start. References are checked again below.
	pending := latest < newest
	if pending {
		if _, err := conn.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			return fmt.Errorf("disable foreign keys: %w", err)
		}
		defer conn.Exec(`PRAGMA foreign_keys = ON`)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := versionOf(entry.Name())
		if err != nil {
			return err
		}
		var seen int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&seen); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if seen > 0 {
			continue
		}
		if version <= latest {
			return fmt.Errorf("migration %s is older than applied version %d", entry.Name(), latest)
		}
		body, err := migrationFS.ReadFile(filepath.ToSlash(filepath.Join("migrations", entry.Name())))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := conn.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		latest = version
	}
	if pending {
		if _, err := conn.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			return fmt.Errorf("enable foreign keys: %w", err)
		}
		if err := checkForeignKeys(conn); err != nil {
			return err
		}
	}
	return nil
}

func checkForeignKeys(conn *sql.DB) error {
	rows, err := conn.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign key check reported a violation after migration")
	}
	return rows.Err()
}

func seedWorkflow(conn *sql.DB) error {
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM workflow_states`).Scan(&count); err != nil {
		return fmt.Errorf("count states: %w", err)
	}
	if count > 0 {
		return nil
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type seed struct {
		name     string
		category string
	}
	states := []seed{{"To do", "open"}, {"In progress", "open"}, {"Done", "done"}}
	var firstID int64
	for i, state := range states {
		res, err := tx.Exec(
			`INSERT INTO workflow_states (name, category, position, created_at) VALUES (?, ?, ?, ?)`,
			state.name, state.category, i, now(),
		)
		if err != nil {
			return fmt.Errorf("seed state %s: %w", state.name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if i == 0 {
			firstID = id
		}
	}
	if _, err := tx.Exec(`UPDATE settings SET initial_state_id = ? WHERE id = 1`, firstID); err != nil {
		return fmt.Errorf("set initial state: %w", err)
	}
	return tx.Commit()
}

func versionOf(name string) (int, error) {
	prefix := strings.SplitN(name, "_", 2)[0]
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration name %s: %w", name, err)
	}
	return version, nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
