package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
	"jiralike/internal/totp"
	"jiralike/internal/web"
)

type e2eStream struct {
	resp   *http.Response
	reader *bufio.Reader
}

func openE2EStream(t *testing.T, client *e2eClient) *e2eStream {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, client.base+"/api/notifications/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.client.Do(request)
	if err != nil {
		t.Fatalf("open notification stream: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("notification stream status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		response.Body.Close()
		t.Fatalf("notification stream content type = %q", contentType)
	}
	return &e2eStream{resp: response, reader: bufio.NewReader(response.Body)}
}

func (s *e2eStream) next(t *testing.T) (string, string) {
	t.Helper()
	event, data := "", ""
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read notification stream: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event != "" {
				return event, data
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			event = rest
		} else if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = rest
		}
	}
}

func (s *e2eStream) close() {
	s.resp.Body.Close()
}

type e2eChallenge struct {
	TwoFactorRequired      bool   `json:"two_factor_required"`
	TwoFactorSetupRequired bool   `json:"two_factor_setup_required"`
	ChallengeToken         string `json:"challenge_token"`
	ExpiresAt              string `json:"expires_at"`
}

type e2eConfirm struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// TestEndToEndV2Flow drives the v2 surface over the same real HTTP server used
// for the v1 acceptance flow: setup, a comment-only search hit, the SSE stream,
// two-factor enrollment, a TOTP login, a recovery-code login, an administrator
// reset, and a backup that still carries both the FTS index and the 2FA tables.
func TestEndToEndV2Flow(t *testing.T) {
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
	reset := newE2EClient(t, base)

	var adminUser e2eUser
	admin.decode(http.StatusCreated, http.MethodPost, "/api/setup", map[string]any{
		"name": "V2 Admin", "email": "v2-admin@example.test", "password": "password-strong",
	}, &adminUser)

	// A project with one item whose searchable word exists only in a comment.
	var project struct {
		ID int64 `json:"id"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, "/api/projects", map[string]any{
		"name": "V2 Project", "visibility": "open",
	}, &project)
	var item e2eItem
	admin.decode(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), map[string]any{
		"type": "action", "title": "V2 search item",
	}, &item)
	admin.expect(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", item.ID), map[string]any{
		"body": "Please inspect the girasole notes",
	})

	var search struct {
		Items []struct {
			ID      int64  `json:"id"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Match   string `json:"match"`
		} `json:"items"`
		Total int `json:"total"`
	}
	admin.decode(http.StatusOK, http.MethodGet, "/api/items?q=girasole&total=1", nil, &search)
	if search.Total != 1 || len(search.Items) != 1 || search.Items[0].ID != item.ID {
		t.Fatalf("comment search: %+v", search)
	}
	if search.Items[0].Match != "comments" || !strings.Contains(search.Items[0].Snippet, "«") || strings.Contains(search.Items[0].Snippet, "<mark>") {
		t.Fatalf("comment search snippet: %+v", search.Items[0])
	}
	var rebuild struct {
		Indexed int `json:"indexed"`
	}
	admin.decode(http.StatusOK, http.MethodPost, "/api/admin/search/rebuild", nil, &rebuild)
	if rebuild.Indexed < 1 {
		t.Fatalf("search rebuild indexed %d rows", rebuild.Indexed)
	}

	// Enroll a TOTP factor on the administrator and keep the recovery codes.
	var start struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	admin.decode(http.StatusOK, http.MethodPost, "/api/me/2fa/start", nil, &start)
	if start.Secret == "" || !strings.HasPrefix(start.URI, "otpauth://totp/") {
		t.Fatalf("2FA start: %+v", start)
	}
	code, err := totp.Code(start.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var confirm e2eConfirm
	admin.decode(http.StatusOK, http.MethodPost, "/api/me/2fa/confirm", map[string]any{"code": code}, &confirm)
	if len(confirm.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes: %d", len(confirm.RecoveryCodes))
	}

	// The password alone no longer produces a session, then a fresh TOTP step
	// completes the sign-in. Resetting the stored step simulates the user
	// waiting for the next thirty-second window.
	admin.expect(http.StatusNoContent, http.MethodPost, "/api/logout", nil)
	status, headers, payload := admin.do(http.MethodPost, "/api/login", map[string]any{
		"email": adminUser.Email, "password": "password-strong",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("password login: %d %s", status, payload)
	}
	if len(headers.Values("Set-Cookie")) != 0 {
		t.Fatal("password-only login set a session cookie")
	}
	var challenge e2eChallenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		t.Fatalf("decode challenge: %v (%s)", err, payload)
	}
	if !challenge.TwoFactorRequired || challenge.ChallengeToken == "" {
		t.Fatalf("expected 2FA challenge: %+v", challenge)
	}
	if _, err := conn.Exec(`UPDATE user_totp SET last_used_step = 0 WHERE user_id = ?`, adminUser.ID); err != nil {
		t.Fatal(err)
	}
	code, err = totp.Code(start.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	status, headers, payload = admin.do(http.MethodPost, "/api/login/2fa", map[string]any{
		"challenge_token": challenge.ChallengeToken, "code": code,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("2FA login: %d %s", status, payload)
	}
	if len(headers.Values("Set-Cookie")) == 0 {
		t.Fatal("2FA login did not set a session cookie")
	}
	admin.expect(http.StatusOK, http.MethodGet, "/api/me", nil)

	// The stream starts with the current count, then carries the mention that a
	// member commits through the normal API.
	stream := openE2EStream(t, admin)
	defer stream.close()
	event, data := stream.next(t)
	if event != "ready" {
		t.Fatalf("first stream frame = %q %s", event, data)
	}
	var ready struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(data), &ready); err != nil {
		t.Fatalf("decode ready: %v (%s)", err, data)
	}

	var invite struct {
		Token string `json:"token"`
	}
	admin.decode(http.StatusCreated, http.MethodPost, "/api/invites", map[string]any{
		"email": "v2-member@example.test", "role": "member",
	}, &invite)
	var memberUser e2eUser
	member.decode(http.StatusCreated, http.MethodPost, "/api/invites/accept", map[string]any{
		"token": invite.Token, "name": "V2 Member", "password": "password-strong",
	}, &memberUser)
	member.expect(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", item.ID), map[string]any{
		"body": "Second note", "mentions": []int64{adminUser.ID},
	})

	event, data = stream.next(t)
	if event != "notification" {
		t.Fatalf("expected notification frame, got %q %s", event, data)
	}
	var pushed struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		ItemID    *int64 `json:"item_id"`
		ProjectID *int64 `json:"project_id"`
		Link      string `json:"link"`
		Count     int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(data), &pushed); err != nil {
		t.Fatalf("decode notification: %v (%s)", err, data)
	}
	if pushed.Kind != "mention" || pushed.Count != ready.Count+1 || pushed.ID == 0 {
		t.Fatalf("pushed notification: %+v (ready %d)", pushed, ready.Count)
	}
	if pushed.ItemID == nil || *pushed.ItemID != item.ID || pushed.ProjectID == nil || *pushed.ProjectID != project.ID {
		t.Fatalf("pushed notification ids: %+v", pushed)
	}
	if want := fmt.Sprintf("/projects/%d/items/%d", project.ID, item.ID); pushed.Link != want {
		t.Fatalf("notification link = %q, want %q", pushed.Link, want)
	}
	stream.close()

	// A recovery code is a one-shot second factor.
	admin.expect(http.StatusNoContent, http.MethodPost, "/api/logout", nil)
	status, _, payload = admin.do(http.MethodPost, "/api/login", map[string]any{
		"email": adminUser.Email, "password": "password-strong",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("password login before recovery: %d %s", status, payload)
	}
	if err := json.Unmarshal(payload, &challenge); err != nil {
		t.Fatal(err)
	}
	status, headers, payload = admin.do(http.MethodPost, "/api/login/2fa", map[string]any{
		"challenge_token": challenge.ChallengeToken, "code": confirm.RecoveryCodes[0],
	}, nil)
	if status != http.StatusOK || len(headers.Values("Set-Cookie")) == 0 {
		t.Fatalf("recovery login: %d %s", status, payload)
	}

	// An administrator can reset another account's second factor, which revokes
	// that account's open session.
	admin.decode(http.StatusCreated, http.MethodPost, "/api/invites", map[string]any{
		"email": "v2-reset@example.test", "role": "member",
	}, &invite)
	var resetUser e2eUser
	reset.decode(http.StatusCreated, http.MethodPost, "/api/invites/accept", map[string]any{
		"token": invite.Token, "name": "V2 Reset", "password": "password-strong",
	}, &resetUser)
	var resetStart struct {
		Secret string `json:"secret"`
	}
	reset.decode(http.StatusOK, http.MethodPost, "/api/me/2fa/start", nil, &resetStart)
	resetCode, err := totp.Code(resetStart.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reset.decode(http.StatusOK, http.MethodPost, "/api/me/2fa/confirm", map[string]any{"code": resetCode}, &e2eConfirm{})
	admin.expect(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", resetUser.ID), nil)
	reset.expect(http.StatusUnauthorized, http.MethodGet, "/api/me", nil)
	status, headers, payload = reset.do(http.MethodPost, "/api/login", map[string]any{
		"email": resetUser.Email, "password": "password-strong",
	}, nil)
	if status != http.StatusOK || len(headers.Values("Set-Cookie")) == 0 || strings.Contains(string(payload), "challenge_token") {
		t.Fatalf("login after admin reset: %d %s", status, payload)
	}
	if w := admin.doStatus(t, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", adminUser.ID), nil); w != http.StatusForbidden {
		t.Fatalf("admin self reset: expected 403, got %d", w)
	}

	// The backup is a self-contained SQLite file that keeps the FTS index and
	// every 2FA row. Reopening it and rebuilding the index must not lose the hit.
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
	backupPath := filepath.Join(dir, "v2-snapshot.db")
	if err := os.WriteFile(backupPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := db.Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	var indexed int64
	if err := restored.QueryRow(`SELECT item_id FROM items_fts WHERE items_fts MATCH '"girasole"'`).Scan(&indexed); err != nil || indexed != item.ID {
		t.Fatalf("search after restore: item %d, want %d (%v)", indexed, item.ID, err)
	}
	var totpRows, recoveryRows int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM user_totp WHERE confirmed_at IS NOT NULL`).Scan(&totpRows); err != nil || totpRows == 0 {
		t.Fatalf("2FA enrollment after restore: %d (%v)", totpRows, err)
	}
	if err := restored.QueryRow(`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ?`, adminUser.ID).Scan(&recoveryRows); err != nil || recoveryRows != 10 {
		t.Fatalf("recovery codes after restore: %d (%v)", recoveryRows, err)
	}
	if _, err := restored.Exec(`INSERT INTO items_fts(items_fts) VALUES('rebuild')`); err != nil {
		t.Fatalf("rebuild restored index: %v", err)
	}
	if err := restored.QueryRow(`SELECT item_id FROM items_fts WHERE items_fts MATCH '"girasole"'`).Scan(&indexed); err != nil || indexed != item.ID {
		t.Fatalf("search after rebuilt restore: item %d, want %d (%v)", indexed, item.ID, err)
	}

	// The embedded SPA still answers a deep link after the v2 flow.
	page, err := admin.client.Get(base + fmt.Sprintf("/projects/%d/items/%d", project.ID, item.ID))
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(html, []byte(`id="root"`)) {
		t.Fatalf("spa deep link: %d %.200s", page.StatusCode, html)
	}
	admin.expect(http.StatusOK, http.MethodGet, "/api/health", nil)
	admin.expect(http.StatusOK, http.MethodGet, "/api/ready", nil)
}

func (c *e2eClient) doStatus(t *testing.T, method, path string, body any) int {
	t.Helper()
	status, _, _ := c.do(method, path, body, nil)
	return status
}
