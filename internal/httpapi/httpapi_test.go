package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(dir + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewWithOptions(conn, nil, false, email.Config{}, nil, teams.New(conn))
	s.SetSecrets(box)
	return s
}

func login(t *testing.T, s *Server, email, password string) *http.Cookie {
	t.Helper()
	user, err := s.auth.Login(email, password)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	raw, _, err := s.auth.StartSession(user.ID)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	return &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
	}
}
func TestSecurityHeadersAndBodyLimit(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"email":"a","password":"`+strings.Repeat("x", 1<<20)+`"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected oversized/malformed body rejection, got %d", w.Code)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("security headers missing")
	}
}
func TestRequireUserRejectsMissingCredentials(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tokens", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}
func TestLoginRateLimit(t *testing.T) {
	s := testServer(t)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"email":"unknown@example.test","password":"badpassword"}`))
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"email":"unknown@example.test","password":"badpassword"}`))
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected throttling, got %d", w.Code)
	}
}

func TestTeamsCRUDAndMembers(t *testing.T) {
	s := testServer(t)
	// Setup admin user
	adminUser, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminID := adminUser.ID

	// Login to get session cookie
	cookie := login(t, s, "admin@example.test", "password-strong")

	// Create team
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/teams", strings.NewReader(`{"name":"Test Team"}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("team create: expected 201, got %d", w.Code)
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse team create: %v, body=%q", err, w.Body.String())
	}
	if v, ok := created["id"]; !ok || v == nil {
		t.Fatalf("team create response missing id: body=%q", w.Body.String())
	}
	teamID := int64(created["id"].(float64))

	// Add member
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/teams/%d/members", teamID), strings.NewReader(fmt.Sprintf(`{"user_id":%d}`, adminID)))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("add member: expected 204, got %d", w.Code)
	}

	// List members
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/teams/%d/members", teamID), nil)
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("team members: expected 200, got %d", w.Code)
	}
}

func TestUsersCRUD(t *testing.T) {
	s := testServer(t)
	// Setup admin user
	adminUser, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminID := adminUser.ID
	// Create a second user through an invite: the admin endpoint cannot edit the caller.
	inviteToken, err := s.auth.CreateInvite("member@example.test", "member", 0, adminID)
	if err != nil {
		t.Fatalf("create invite failed: %v", err)
	}
	memberUser, err := s.auth.AcceptInvite(inviteToken, "Member", "password-strong")
	if err != nil {
		t.Fatalf("accept invite failed: %v", err)
	}
	memberID := memberUser.ID

	// Login to get session cookie
	cookie := login(t, s, "admin@example.test", "password-strong")

	// Test users list (admin only)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("users list: expected 200, got %d", w.Code)
	}
	if len(w.Body.String()) == 0 {
		t.Fatalf("users list empty")
	}

	// Test get user (admin)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/users/%d", adminID), nil)
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("users get: expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "admin") {
		t.Fatalf("user response missing admin info")
	}

	// Test update user (admin, target must not be the caller)
	updateBody := `{"role": "manager", "email_notifications": 1}`
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d", memberID), strings.NewReader(updateBody))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("users update: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify update
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/users/%d", memberID), nil)
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("user get after update: expected 200, got %d", w.Code)
	}
	updated := w.Body.String()
	if !strings.Contains(updated, "manager") {
		t.Fatalf("role not updated to manager")
	}
	if !strings.Contains(updated, `"email_notifications":true`) {
		t.Fatalf("email_notifications not set to true: %s", updated)
	}
}

func TestUsersAdminSelfUpdateRejected(t *testing.T) {
	s := testServer(t)
	adminUser, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	cookie := login(t, s, "admin@example.test", "password-strong")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d", adminUser.ID), strings.NewReader(`{"email_notifications":1}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("self update via admin endpoint: expected 403, got %d", w.Code)
	}
}

func TestGuestExtensionAndRoleChange(t *testing.T) {
	s := testServer(t)
	adminUser, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	cookie := login(t, s, "admin@example.test", "password-strong")
	token, err := s.auth.CreateInvite("guest@example.test", "guest", 30, adminUser.ID)
	if err != nil {
		t.Fatalf("create guest invite failed: %v", err)
	}
	guest, err := s.auth.AcceptInvite(token, "Guest", "password-strong")
	if err != nil {
		t.Fatalf("accept guest invite failed: %v", err)
	}

	// Extend the guest window to 7 days.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d", guest.ID), strings.NewReader(`{"guest_days":7}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("guest extension: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	updated, err := s.auth.GetUser(guest.ID)
	if err != nil {
		t.Fatalf("get guest failed: %v", err)
	}
	want := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02")
	if updated.GuestExpiresOn != want {
		t.Fatalf("guest expiry: expected %s, got %q", want, updated.GuestExpiresOn)
	}

	// guest_days on a non-guest role is rejected without applying anything.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d", guest.ID), strings.NewReader(`{"role":"member","guest_days":7}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("guest_days for member: expected 400, got %d", w.Code)
	}
	unchanged, err := s.auth.GetUser(guest.ID)
	if err != nil {
		t.Fatalf("get guest failed: %v", err)
	}
	if unchanged.Role != "guest" {
		t.Fatalf("rejected request changed the role to %q", unchanged.Role)
	}

	// Promoting a guest clears the guest-only expiry.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d", guest.ID), strings.NewReader(`{"role":"member"}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("guest promotion: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	promoted, err := s.auth.GetUser(guest.ID)
	if err != nil {
		t.Fatalf("get promoted user failed: %v", err)
	}
	if promoted.Role != "member" || promoted.GuestExpiresOn != "" {
		t.Fatalf("unexpected promoted user: role=%q expiry=%q", promoted.Role, promoted.GuestExpiresOn)
	}
}

func TestInviteEmailSentWhenConfigured(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var sentTo, sentSubject, sentBody string
	sent := 0
	sender := func(cfg email.Config, to, subject, body string) error {
		sent++
		sentTo, sentSubject, sentBody = to, subject, body
		return nil
	}
	emailCfg := email.Config{Host: "smtp.example.test", Port: 25, From: "noreply@example.test"}
	s := NewWithOptions(conn, nil, false, emailCfg, sender, teams.New(conn))
	if _, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong"); err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	cookie := login(t, s, "admin@example.test", "password-strong")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"new@example.test","role":"guest","guest_days":30}`))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		AcceptURL string `json:"accept_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse invite response: %v", err)
	}
	if sent != 1 {
		t.Fatalf("expected one invite email, got %d", sent)
	}
	if sentTo != "new@example.test" || sentSubject == "" || !strings.Contains(sentBody, resp.AcceptURL) {
		t.Fatalf("unexpected email: to=%q subject=%q body=%q", sentTo, sentSubject, sentBody)
	}
	if resp.AcceptURL != "http://example.com/invite/"+resp.Token {
		t.Fatalf("unexpected accept_url: %q", resp.AcceptURL)
	}
}

func TestMeUpdate(t *testing.T) {
	s := testServer(t)
	// Setup user
	_, _, err := s.auth.Setup("User", "user@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup user failed: %v", err)
	}

	// Login to get session cookie
	cookie := login(t, s, "user@example.test", "password-strong")

	// Update profile
	updateBody := `{"name":"Jane Doe","locale":"it","email_notifications":0}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/me", strings.NewReader(updateBody))
	r.AddCookie(cookie)
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("me update: expected 200, got %d", w.Code)
	}
}
func TestSetupStoresChosenLocale(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"name":"Ana Torres","email":"ana@example.test","password":"password-strong","locale":"es"}`))
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Locale string `json:"locale"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Locale != "es" {
		t.Fatalf("setup locale = %q, want es", created.Locale)
	}
	me := httptest.NewRecorder()
	mr := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	for _, cookie := range w.Result().Cookies() {
		mr.AddCookie(cookie)
	}
	s.Handler().ServeHTTP(me, mr)
	if me.Code != http.StatusOK {
		t.Fatalf("me after setup: %d %s", me.Code, me.Body.String())
	}
	var profile struct {
		Locale string `json:"locale"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Locale != "es" {
		t.Fatalf("stored locale = %q, want es", profile.Locale)
	}
}

func TestSetupPersistsInitialSMTPOnly(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(t.TempDir(), "config.ini")
	s.SetConfigPath(path)
	body := `{"name":"Admin","email":"admin@example.test","password":"password-strong","locale":"it","smtp":{"host":"mail.example.test","port":587,"user":"mailer","password":"smtp-secret","from":"noreply@example.test","tls":true}}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"smtp_host=mail.example.test", "smtp_port=587", "smtp_from=noreply@example.test", "smtp_tls=true", "smtp_pass_enc="} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "smtp-secret") {
		t.Fatal("SMTP password was written in clear text")
	}
}

func TestCookieSecureAndCSRFOrigin(t *testing.T) {
	s := testServer(t)
	s.secureCookies = true
	if _, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"email":"admin@example.test","password":"password-strong"}`))
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("cookie flags missing: %+v", cookie)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://attacker.example")
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d", w.Code)
	}
}

type projectJSON struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	CreatedBy   int64  `json:"created_by"`
	ArchivedAt  string `json:"archived_at"`
	MemberCount int    `json:"member_count"`
	TeamCount   int    `json:"team_count"`
}

func makeUser(t *testing.T, s *Server, adminID int64, name, email, role string, guestDays int) auth.User {
	t.Helper()
	token, err := s.auth.CreateInvite(email, role, guestDays, adminID)
	if err != nil {
		t.Fatalf("create %s invite: %v", role, err)
	}
	user, err := s.auth.AcceptInvite(token, name, "password-strong")
	if err != nil {
		t.Fatalf("accept %s invite: %v", role, err)
	}
	return user
}

func apiDo(t *testing.T, s *Server, method, path string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func decodeProject(t *testing.T, w *httptest.ResponseRecorder) projectJSON {
	t.Helper()
	var p projectJSON
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode project: %v (%s)", err, w.Body.String())
	}
	return p
}

func decodeProjectList(t *testing.T, w *httptest.ResponseRecorder) []projectJSON {
	t.Helper()
	var body struct {
		Projects []projectJSON `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode project list: %v (%s)", err, w.Body.String())
	}
	return body.Projects
}

func TestProjectsCRUDArchiveAndFilters(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	cookie := login(t, s, "admin@example.test", "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", cookie, `{"name":"Alpha","description":"First project","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	alpha := decodeProject(t, w)
	if alpha.Name != "Alpha" || alpha.Visibility != "open" || alpha.CreatedBy != admin.ID {
		t.Fatalf("unexpected created project: %+v", alpha)
	}
	if alpha.MemberCount != 1 {
		t.Fatalf("creator should be a member, count = %d", alpha.MemberCount)
	}

	w = apiDo(t, s, http.MethodPost, "/api/projects", cookie, `{"name":"Beta","description":"Secret work","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private project: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	beta := decodeProject(t, w)

	w = apiDo(t, s, http.MethodPost, "/api/projects", cookie, `{"name":"","visibility":"open"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty name: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, "/api/projects", cookie, `{"name":"Gamma","visibility":"weird"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid visibility: expected 400, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodGet, "/api/projects", cookie, "")
	if w.Code != http.StatusOK || len(decodeProjectList(t, w)) != 2 {
		t.Fatalf("list projects: expected 2, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects?q=alp", cookie, "")
	if list := decodeProjectList(t, w); len(list) != 1 || list[0].ID != alpha.ID {
		t.Fatalf("q filter: unexpected list %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects?visibility=private", cookie, "")
	if list := decodeProjectList(t, w); len(list) != 1 || list[0].ID != beta.ID {
		t.Fatalf("visibility filter: unexpected list %+v", list)
	}

	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", alpha.ID), cookie, `{"name":"Alpha 2","description":"Updated","visibility":"private"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update project: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated := decodeProject(t, w)
	if updated.Name != "Alpha 2" || updated.Description != "Updated" || updated.Visibility != "private" {
		t.Fatalf("unexpected updated project: %+v", updated)
	}

	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d", alpha.ID), cookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive project: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects", cookie, "")
	if list := decodeProjectList(t, w); len(list) != 1 || list[0].ID != beta.ID {
		t.Fatalf("archived project should be hidden: %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects?archived=1", cookie, "")
	if list := decodeProjectList(t, w); len(list) != 2 {
		t.Fatalf("archived=1 should include archived projects: %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", alpha.ID), cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin should read archived project: %d", w.Code)
	}
	if archived := decodeProject(t, w); archived.ArchivedAt == "" {
		t.Fatalf("archived_at missing: %+v", archived)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", alpha.ID), cookie, `{"name":"Nope"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("update archived: expected 409, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d", alpha.ID), cookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("archive twice: expected 409, got %d", w.Code)
	}

	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	memberCookie := login(t, s, member.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")
	for _, c := range []*http.Cookie{memberCookie, guestCookie} {
		w = apiDo(t, s, http.MethodPost, "/api/projects", c, `{"name":"Forbidden","visibility":"open"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-manager create: expected 403, got %d", w.Code)
		}
	}
}

func TestProjectAccessMatrixAndAntiLeak(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	otherManager := makeUser(t, s, admin.ID, "Other Manager", "manager2@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	managerCookie := login(t, s, manager.Email, "password-strong")
	otherManagerCookie := login(t, s, otherManager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	outsiderCookie := login(t, s, outsider.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")

	// The manager creates a private project and is its implicit member.
	w := apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Private One","description":"secret","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("manager create private: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)

	// Direct member grant.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie, fmt.Sprintf(`{"user_id":%d}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("add project member: %d %s", w.Code, w.Body.String())
	}
	// Team grant that reaches the guest.
	team, err := s.teamsStore.Create("Delivery")
	if err != nil {
		t.Fatalf("create team failed: %v", err)
	}
	if err := s.teamsStore.AddMember(team.ID, guest.ID); err != nil {
		t.Fatalf("add team member failed: %v", err)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/teams", private.ID), managerCookie, fmt.Sprintf(`{"team_id":%d}`, team.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("add project team: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "member@example.test") {
		t.Fatalf("manager member list: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/teams", private.ID), managerCookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Delivery") {
		t.Fatalf("manager team list: %d %s", w.Code, w.Body.String())
	}

	// Direct member and team member can read the private project.
	for name, cookie := range map[string]*http.Cookie{"direct member": memberCookie, "team guest": guestCookie} {
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", private.ID), cookie, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s read private: expected 200, got %d: %s", name, w.Code, w.Body.String())
		}
		w = apiDo(t, s, http.MethodGet, "/api/projects", cookie, "")
		if list := decodeProjectList(t, w); len(list) != 1 || list[0].ID != private.ID {
			t.Fatalf("%s private project missing from list: %+v", name, list)
		}
	}

	// Unrelated users get the same 404 as a missing project and never see it
	// in lists or searches. Role-gated management routes answer 403 to plain
	// members, which still does not confirm that the project exists.
	for _, tc := range []struct {
		name       string
		cookie     *http.Cookie
		roleDenied int
	}{
		{"outsider", outsiderCookie, http.StatusForbidden},
		{"other manager", otherManagerCookie, http.StatusNotFound},
	} {
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", private.ID), tc.cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s private read: expected 404, got %d", tc.name, w.Code)
		}
		w = apiDo(t, s, http.MethodGet, "/api/projects", tc.cookie, "")
		if list := decodeProjectList(t, w); len(list) != 0 {
			t.Fatalf("%s saw private project in list: %+v", tc.name, list)
		}
		w = apiDo(t, s, http.MethodGet, "/api/projects?q=Private", tc.cookie, "")
		if list := decodeProjectList(t, w); len(list) != 0 {
			t.Fatalf("%s saw private project in search: %+v", tc.name, list)
		}
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/members", private.ID), tc.cookie, "")
		if w.Code != tc.roleDenied {
			t.Fatalf("%s member list: expected %d, got %d", tc.name, tc.roleDenied, w.Code)
		}
		w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", private.ID), tc.cookie, `{"name":"Hijacked"}`)
		if w.Code != tc.roleDenied {
			t.Fatalf("%s update private: expected %d, got %d", tc.name, tc.roleDenied, w.Code)
		}
	}

	// Admin sees everything without grants.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", private.ID), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin read private: %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects", adminCookie, "")
	if list := decodeProjectList(t, w); len(list) != 1 || list[0].ID != private.ID {
		t.Fatalf("admin list missing private project: %+v", list)
	}

	// Guest reads but never writes.
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", private.ID), guestCookie, `{"name":"Guest Edit"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest project update: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), guestCookie, fmt.Sprintf(`{"user_id":%d}`, guest.ID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest grant management: expected 403, got %d", w.Code)
	}
	// Members are not project managers either.
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", private.ID), memberCookie, `{"name":"Member Edit"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member project update: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/members", private.ID), memberCookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("member grant list: expected 403, got %d", w.Code)
	}

	// An open project is readable by everyone, but only its manager edits it.
	w = apiDo(t, s, http.MethodPost, "/api/projects", otherManagerCookie, `{"name":"Open Work","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("other manager create open: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", open.ID), guestCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("guest read open project: expected 200, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects", guestCookie, "")
	if list := decodeProjectList(t, w); len(list) != 2 {
		t.Fatalf("guest should see open plus granted private: %+v", list)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", open.ID), managerCookie, `{"name":"Not Mine"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("manager editing another open project: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", open.ID), otherManagerCookie, `{"name":"Open Work 2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("creator manager update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/projects/%d", open.ID), memberCookie, `{"name":"Member Edit"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member editing open project: expected 403, got %d", w.Code)
	}

	// Revoking both grants removes access again.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/members/%d", private.ID, member.ID), managerCookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove project member: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/teams/%d", private.ID, team.ID), managerCookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove project team: %d %s", w.Code, w.Body.String())
	}
	for name, cookie := range map[string]*http.Cookie{"removed member": memberCookie, "removed guest": guestCookie} {
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", private.ID), cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s after revoke: expected 404, got %d", name, w.Code)
		}
	}
	// Removing a grant that is not there reports a missing link, not a crash.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/members/%d", private.ID, member.ID), managerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("remove missing member: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/teams/%d", private.ID, team.ID), managerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("remove missing team: expected 404, got %d", w.Code)
	}
}

func TestProjectArchiveVisibilityAndGrantGuards(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Archived Private","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	p := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", p.ID), managerCookie, fmt.Sprintf(`{"user_id":%d}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	// Adding unknown users fails cleanly.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", p.ID), managerCookie, `{"user_id":9999}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("grant unknown user: expected 404, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d", p.ID), managerCookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	// The manager who owns the archived project can still read it; ordinary
	// members cannot.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", p.ID), managerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("manager read archived: expected 200, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", p.ID), memberCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("member read archived: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects", memberCookie, "")
	if list := decodeProjectList(t, w); len(list) != 0 {
		t.Fatalf("archived project leaked in member list: %+v", list)
	}
	// Archived projects accept no grant changes.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", p.ID), managerCookie, fmt.Sprintf(`{"user_id":%d}`, admin.ID))
	if w.Code != http.StatusConflict {
		t.Fatalf("grant on archived: expected 409, got %d", w.Code)
	}
	// Invalid IDs are rejected before any lookup.
	w = apiDo(t, s, http.MethodGet, "/api/projects/not-a-number", adminCookie, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid project id: expected 400, got %d", w.Code)
	}
}
