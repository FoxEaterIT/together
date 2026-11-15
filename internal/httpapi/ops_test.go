package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jiralike/internal/db"
)

func TestBackupDownloadIsConsistent(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Backup","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/projects/"+fmt.Sprint(project.ID)+"/items", adminCookie, `{"type":"action","title":"Snapshot me"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}

	if w = apiDo(t, s, http.MethodGet, "/api/admin/backup", memberCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member backup: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodGet, "/api/admin/backup", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous backup: %d", w.Code)
	}

	w = apiDo(t, s, http.MethodGet, "/api/admin/backup", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin backup: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/vnd.sqlite3" {
		t.Fatalf("backup content type: %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, ".db") {
		t.Fatalf("backup disposition: %q", got)
	}
	if w.Body.Len() == 0 {
		t.Fatal("backup body is empty")
	}

	// The snapshot includes uncheckpointed WAL content and needs no sidecar.
	copyPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(copyPath, w.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copyPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("backup unexpectedly needs a -wal sidecar: %v", err)
	}
	copyConn, err := db.Open(copyPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copyConn.Close()
	var title string
	if err := copyConn.QueryRow(`SELECT title FROM items WHERE title = 'Snapshot me'`).Scan(&title); err != nil {
		t.Fatalf("read backup item: %v", err)
	}
	if title != "Snapshot me" {
		t.Fatalf("backup item title: %q", title)
	}
	var migrations int
	if err := copyConn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("read backup migrations: %v", err)
	}
	if migrations == 0 {
		t.Fatal("backup lost migration history")
	}
}

func TestHealthAndReadiness(t *testing.T) {
	s := testServer(t)
	w := apiDo(t, s, http.MethodGet, "/api/health", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("health: %d", w.Code)
	}
	var health struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil || !health.OK {
		t.Fatalf("health body: %s", w.Body.String())
	}

	w = apiDo(t, s, http.MethodGet, "/api/ready", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("ready: %d", w.Code)
	}
	var ready struct {
		OK    bool `json:"ok"`
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ready); err != nil || !ready.OK || !ready.Ready {
		t.Fatalf("ready body: %s", w.Body.String())
	}

	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	w = apiDo(t, s, http.MethodGet, "/api/ready", nil, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed database readiness: %d %s", w.Code, w.Body.String())
	}
}

func TestRequestLogRedactsSecrets(t *testing.T) {
	s := testServer(t)
	previous := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	apiDo(t, s, http.MethodPost, "/api/login", nil, `{"email":"admin@example.test","password":"super-secret-password"}`)
	apiDo(t, s, http.MethodGet, "/api/invites/inv_secret_token", nil, "")
	apiDo(t, s, http.MethodGet, "/invite/inv_secret_token", nil, "")

	logged := buf.String()
	if strings.Contains(logged, "super-secret-password") {
		t.Fatal("request log contains a password")
	}
	if strings.Contains(logged, "inv_secret_token") {
		t.Fatal("request log contains an invite token")
	}
	if !strings.Contains(logged, "POST /api/login") {
		t.Fatalf("request log missing method and path: %s", logged)
	}
	if !strings.Contains(logged, "/api/invites/[redacted]") || !strings.Contains(logged, "/invite/[redacted]") {
		t.Fatalf("request log did not redact invite paths: %s", logged)
	}
}
