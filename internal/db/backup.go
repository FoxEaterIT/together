package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup writes a consistent snapshot of the live database to destination.
// VACUUM INTO reads through the open connection, so uncheckpointed WAL content
// is included and the result is one self-contained file that does not need the
// -wal or -shm sidecars. The destination must not exist; the caller owns it.
func Backup(conn *sql.DB, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear backup destination: %w", err)
	}
	path := strings.ReplaceAll(filepath.ToSlash(destination), "'", "''")
	if _, err := conn.Exec(`VACUUM INTO '` + path + `'`); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}
