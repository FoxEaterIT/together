package projects_test

import (
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/projects"
)

func TestAccessRules(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	store := projects.New(conn)

	now := time.Now().UTC().Format(time.RFC3339)
	mkUser := func(name, email, role string) int64 {
		t.Helper()
		res, err := conn.Exec(
			`INSERT INTO users (name, email, password_hash, role, locale, email_notifications, created_at) VALUES (?, ?, 'x', ?, 'en', 0, ?)`,
			name, email, role, now,
		)
		if err != nil {
			t.Fatalf("insert %s: %v", role, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	adminID := mkUser("Admin", "admin@example.test", "admin")
	managerID := mkUser("Manager", "manager@example.test", "manager")
	otherManagerID := mkUser("Other Manager", "manager2@example.test", "manager")
	memberID := mkUser("Member", "member@example.test", "member")
	outsiderID := mkUser("Outsider", "outsider@example.test", "member")
	guestID := mkUser("Guest", "guest@example.test", "guest")

	open, err := store.Create("Open", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	private, err := store.Create("Private", "", "private", managerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(private.ID, memberID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		project projects.Project
		userID  int64
		role    string
		view    bool
		write   bool
		manage  bool
	}{
		{"guest reads open but cannot write", open, guestID, "guest", true, false, false},
		{"member reads and writes open", open, memberID, "member", true, true, false},
		{"manager cannot manage another open project", open, otherManagerID, "manager", true, true, false},
		{"admin manages everything", open, adminID, "admin", true, true, true},
		{"outsider cannot see private", private, outsiderID, "member", false, false, false},
		{"outsider manager cannot manage private", private, otherManagerID, "manager", false, false, false},
		{"creator manager manages private", private, managerID, "manager", true, true, true},
		{"direct member reads and writes private", private, memberID, "member", true, true, false},
		{"admin sees private without grant", private, adminID, "admin", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := store.CanView(tc.project, tc.userID, tc.role)
			if err != nil {
				t.Fatal(err)
			}
			if view != tc.view {
				t.Fatalf("CanView = %v, want %v", view, tc.view)
			}
			manage, err := store.CanManage(tc.project, tc.userID, tc.role)
			if err != nil {
				t.Fatal(err)
			}
			if manage != tc.manage {
				t.Fatalf("CanManage = %v, want %v", manage, tc.manage)
			}
			if view {
				write, err := store.CanWrite(tc.project, tc.userID, tc.role)
				if err != nil {
					t.Fatal(err)
				}
				if write != tc.write {
					t.Fatalf("CanWrite = %v, want %v", write, tc.write)
				}
			}
		})
	}
}
