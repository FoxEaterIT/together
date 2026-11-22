package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/httpapi"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
	"jiralike/internal/web"
)

type e2eClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newE2EClient(t *testing.T, base string) *e2eClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &e2eClient{t: t, base: base, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (c *e2eClient) do(method, path string, body any, headers map[string]string) (int, http.Header, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return response.StatusCode, response.Header, payload
}

func (c *e2eClient) expect(want int, method, path string, body any) []byte {
	c.t.Helper()
	status, _, payload := c.do(method, path, body, nil)
	if status != want {
		c.t.Fatalf("%s %s: expected %d, got %d: %s", method, path, want, status, payload)
	}
	return payload
}

func (c *e2eClient) decode(want int, method, path string, body any, dst any) {
	c.t.Helper()
	payload := c.expect(want, method, path, body)
	if err := json.Unmarshal(payload, dst); err != nil {
		c.t.Fatalf("%s %s: decode: %v (%s)", method, path, err, payload)
	}
}

type e2eUser struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
	SMTP  bool   `json:"smtp_configured"`
}

type e2eWorkflow struct {
	ID     int64 `json:"id"`
	States []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category"`
	} `json:"states"`
	InitialStateID int64 `json:"initial_state_id"`
}

type e2eItem struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	StateID   int64  `json:"state_id"`
	StateName string `json:"state_name"`
	Type      string `json:"type"`
}

type e2eNotification struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Read   bool   `json:"read"`
	ItemID *int64 `json:"item_id"`
	Link   string `json:"link"`
}

// TestEndToEndV1Flow drives the shipped binary surface over real HTTP: setup,
// invite, private project grant, item creation, comment mention, notification,
// workflow transition, report, backup and the embedded React shell.
func TestEndToEndV1Flow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	assets, err := web.FS()
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewWithOptions(conn, assets, false, email.Config{}, nil, teams.New(conn))
	box, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetSecrets(box)
	if _, err := handler.SealLegacy(); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve(ctx, listener, server, nil, handler.Close, 5*time.Second)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	base := "http://" + listener.Addr().String()

	admin := newE2EClient(t, base)
	member := newE2EClient(t, base)

	// 1. First visit: setup is open.
	var setup struct {
		Completed bool `json:"setup_completed"`
	}
	admin.decode(http.StatusOK, http.MethodGet, "/api/setup", nil, &setup)
	if setup.Completed {
		t.Fatal("fresh database reports setup completed")
	}

	// 2. Create the administrator, which signs them in.
	var adminUser e2eUser
	admin.decode(http.StatusCreated, http.MethodPost, "/api/setup", map[string]any{
		"name": "E2E Admin", "email": "e2e-admin@example.test", "password": "password-strong",
	}, &adminUser)
	if adminUser.Role != "admin" || adminUser.ID == 0 {
		t.Fatalf("setup user: %+v", adminUser)
	}

	// 3. Define a project and its workflow.
	var project struct {
		ID         int64  `json:"id"`
		Visibility string `json:"visibility"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, "/api/projects", map[string]any{
		"name": "E2E Project", "description": "end to end", "visibility": "private",
	}, &project)
	if project.ID == 0 {
		t.Fatalf("project creation: %+v", project)
	}

	var wfList struct {
		Workflows []e2eWorkflow `json:"workflows"`
	}
	admin.decode(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/projects/%d/workflows", project.ID), nil, &wfList)
	if len(wfList.Workflows) == 0 {
		t.Fatal("project has no seeded workflow")
	}
	wf := wfList.Workflows[0]
	var todoID, doneID int64
	for _, state := range wf.States {
		switch state.Name {
		case "To do":
			todoID = state.ID
		case "Done":
			doneID = state.ID
		}
	}
	if todoID == 0 || doneID == 0 {
		t.Fatalf("seeded workflow: %+v", wf.States)
	}
	var transition struct {
		ID int64 `json:"id"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/projects/%d/workflows/%d/transitions", project.ID, wf.ID), map[string]any{
		"from_state_id": todoID, "to_state_id": doneID, "name": "Finish",
	}, &transition)
	if transition.ID == 0 {
		t.Fatal("transition was not created")
	}

	// 4. Invite a member and add them to the project.
	var invite struct {
		Token string `json:"token"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, "/api/invites", map[string]any{
		"email": "e2e-member@example.test", "role": "member",
	}, &invite)
	if !strings.HasPrefix(invite.Token, "inv_") {
		t.Fatalf("invite token shape: %q", invite.Token)
	}

	// 5. The invitee accepts on a separate cookie jar.
	var memberUser e2eUser
	member.decode(http.StatusCreated, http.MethodPost, "/api/invites/accept", map[string]any{
		"token": invite.Token, "name": "E2E Member", "password": "password-strong",
	}, &memberUser)
	if memberUser.Role != "member" {
		t.Fatalf("member role: %+v", memberUser)
	}

	// 6. The private project is invisible until the grant exists.
	member.expect(http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/projects/%d", project.ID), nil)
	admin.expect(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", project.ID), map[string]any{
		"user_id": memberUser.ID,
	})
	member.expect(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/projects/%d", project.ID), nil)

	// 7. The member creates an item assigned to the admin, then mentions them.
	var item e2eItem
	member.decode(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), map[string]any{
		"type": "action", "title": "E2E item", "priority": "high", "assignee_ids": []int64{adminUser.ID},
	}, &item)
	if item.StateID != todoID || item.StateName != "To do" {
		t.Fatalf("new item should start in the initial state: %+v", item)
	}
	member.expect(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", item.ID), map[string]any{
		"body": "Please review @E2E Admin", "mentions": []int64{adminUser.ID},
	})

	// 8. The admin sees the assignment and the mention, then marks them read.
	var notifications struct {
		Notifications []e2eNotification `json:"notifications"`
	}
	admin.decode(http.StatusOK, http.MethodGet, "/api/notifications", nil, &notifications)
	kinds := map[string]bool{}
	for _, notification := range notifications.Notifications {
		kinds[notification.Kind] = true
		if notification.ItemID == nil || *notification.ItemID != item.ID {
			t.Fatalf("notification for wrong item: %+v", notification)
		}
		if !strings.HasPrefix(notification.Link, "/projects/") {
			t.Fatalf("notification link: %q", notification.Link)
		}
	}
	if !kinds["assigned"] || !kinds["mention"] {
		t.Fatalf("expected assignment and mention notifications, got %v", kinds)
	}
	var unread struct {
		Count int `json:"count"`
	}
	admin.decode(http.StatusOK, http.MethodGet, "/api/notifications/unread-count", nil, &unread)
	if unread.Count < 2 {
		t.Fatalf("unread count: %d", unread.Count)
	}
	for _, notification := range notifications.Notifications {
		if notification.Read {
			continue
		}
		admin.expect(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", notification.ID), nil)
	}

	// 9. Apply the transition and verify the stored state.
	member.expect(http.StatusOK, http.MethodPost,
		fmt.Sprintf("/api/items/%d/transitions/%d", item.ID, transition.ID), nil)
	var moved struct {
		Item e2eItem `json:"item"`
	}
	member.decode(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/items/%d", item.ID), nil, &moved)
	if moved.Item.StateID != doneID || moved.Item.StateName != "Done" {
		t.Fatalf("item did not move: %+v", moved.Item)
	}

	// 10. Reports count the closed item for the filtered project.
	var report struct {
		States []struct {
			Name  string `json:"state_name"`
			Count int    `json:"count"`
		} `json:"states"`
		Totals struct {
			Open int `json:"open"`
			Done int `json:"done"`
			All  int `json:"all"`
		} `json:"totals"`
	}
	admin.decode(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/reports?project_id=%d", project.ID), nil, &report)
	if report.Totals.All != 1 || report.Totals.Done != 1 || report.Totals.Open != 0 {
		t.Fatalf("report totals: %+v", report.Totals)
	}

	// 11. The backup is a self-contained SQLite file with the work inside.
	status, headers, backup := admin.do(http.MethodGet, "/api/admin/backup", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("backup: %d %s", status, backup)
	}
	if got := headers.Get("Content-Type"); got != "application/vnd.sqlite3" {
		t.Fatalf("backup content type: %q", got)
	}
	if !bytes.HasPrefix(backup, []byte("SQLite format 3\x00")) {
		t.Fatalf("backup is not a SQLite file: %q", backup[:16])
	}
	backupPath := filepath.Join(dir, "snapshot.db")
	if err := os.WriteFile(backupPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := db.Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	var title string
	if err := restored.QueryRow(`SELECT title FROM items WHERE id = ?`, item.ID).Scan(&title); err != nil {
		restored.Close()
		t.Fatalf("read backup item: %v", err)
	}
	restored.Close()
	if title != "E2E item" {
		t.Fatalf("backup item title: %q", title)
	}

	// 12. The SPA fallback serves the embedded React shell for deep links.
	page, err := admin.client.Get(base + fmt.Sprintf("/projects/%d/items/%d", project.ID, item.ID))
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(page.Header.Get("Content-Type")), "text/html") {
		t.Fatalf("spa deep link: %d %s", page.StatusCode, page.Header.Get("Content-Type"))
	}
	if !bytes.Contains(html, []byte(`id="root"`)) {
		t.Fatalf("spa shell missing root element: %.200s", html)
	}

	// 13. A personal API token authenticates without the session cookie.
	var token struct {
		Token string `json:"token"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, "/api/tokens", map[string]any{"name": "cli"}, &token)
	api := newE2EClient(t, base)
	status, _, payload := api.do(http.MethodGet, "/api/me", nil, map[string]string{
		"Authorization": "Bearer " + token.Token,
	})
	if status != http.StatusOK {
		t.Fatalf("bearer auth: %d %s", status, payload)
	}
	status, _, payload = api.do(http.MethodGet, "/api/me", nil, map[string]string{
		"Authorization": "Bearer " + token.Token + "x",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("tampered bearer token: %d %s", status, payload)
	}

	// 14. Health, readiness and sign-out.
	admin.expect(http.StatusOK, http.MethodGet, "/api/health", nil)
	admin.expect(http.StatusOK, http.MethodGet, "/api/ready", nil)
	admin.expect(http.StatusNoContent, http.MethodPost, "/api/logout", nil)
	admin.expect(http.StatusUnauthorized, http.MethodGet, "/api/me", nil)
}
