package db

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabaseAndEnablesForeignKeys(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "nested", "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var enabled int
	if err := conn.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign keys not enabled: %d", enabled)
	}
	if err := migrate(conn); err != nil {
		t.Fatalf("migrations should be idempotent: %v", err)
	}
}
