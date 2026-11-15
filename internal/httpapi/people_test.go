package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"jiralike/internal/email"
)

type personJSON struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	GuestExpiresOn string `json:"guest_expires_on"`
	Disabled       bool   `json:"disabled"`
}

func decodePeople(t *testing.T, body []byte) []personJSON {
	t.Helper()
	var resp struct {
		People []personJSON `json:"people"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode people: %v (%s)", err, string(body))
	}
	return resp.People
}

func personByName(t *testing.T, list []personJSON, name string) personJSON {
	t.Helper()
	for _, person := range list {
		if person.Name == name {
			return person
		}
	}
	t.Fatalf("person %q not found in %+v", name, list)
	return personJSON{}
}

func TestPeopleDirectoryAndProjectScoping(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	guestCookie := login(t, s, guest.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	temporary := makeUser(t, s, admin.ID, "Temporary", "temporary@example.test", "member", 0)

	if w := apiDo(t, s, http.MethodGet, "/api/people", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous people: expected 401, got %d", w.Code)
	}

	w := apiDo(t, s, http.MethodGet, "/api/people", memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("people list: %d %s", w.Code, w.Body.String())
	}
	people := decodePeople(t, w.Body.Bytes())
	if len(people) != 5 {
		t.Fatalf("people list length = %d, want 5: %+v", len(people), people)
	}
	if got := personByName(t, people, "Guest"); got.Role != "guest" || got.GuestExpiresOn == "" {
		t.Fatalf("guest directory entry = %+v", got)
	}
	if got := personByName(t, people, "Admin"); got.Role != "admin" || got.Disabled {
		t.Fatalf("admin directory entry = %+v", got)
	}

	// Disabling a user is visible in the directory.
	if w := apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/users/%d", temporary.ID), adminCookie, `{"disabled":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("disable temporary: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/people", memberCookie, "")
	if got := personByName(t, decodePeople(t, w.Body.Bytes()), "Temporary"); !got.Disabled {
		t.Fatalf("disabled user not flagged: %+v", got)
	}

	// A private project is visible to the admin, its direct members and the
	// guest only when explicitly added. Everyone else gets 404, not 403.
	w = apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Secret","description":"","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private project: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)
	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), adminCookie, fmt.Sprintf(`{"user_id":%d}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}

	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/people", private.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member project people: %d %s", w.Code, w.Body.String())
	}
	memberView := decodePeople(t, w.Body.Bytes())
	if len(memberView) != 2 {
		t.Fatalf("private project people = %+v, want admin and member", memberView)
	}
	for _, name := range []string{"Admin", "Member"} {
		if got := personByName(t, memberView, name); got.ID <= 0 {
			t.Fatalf("missing %s in project people", name)
		}
	}
	for _, outsider := range []struct {
		name   string
		cookie *http.Cookie
	}{{"manager", managerCookie}, {"guest", guestCookie}} {
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/people", private.ID), outsider.cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s private project people: expected 404, got %d: %s", outsider.name, w.Code, w.Body.String())
		}
	}

	// Open projects expose everyone who can be assigned, guests included when
	// their access is still active.
	w = apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","description":"","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create open project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/people", open.ID), guestCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("guest open project people: %d %s", w.Code, w.Body.String())
	}
	openPeople := decodePeople(t, w.Body.Bytes())
	if len(openPeople) != 5 {
		t.Fatalf("open project people = %+v, want the 5 users", openPeople)
	}

	if w := apiDo(t, s, http.MethodGet, "/api/projects/999999/people", adminCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing project people: expected 404, got %d", w.Code)
	}
}

func TestInvitePreviewAndSmtpFlag(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")

	// /api/me exposes the SMTP state the UI uses to disable the preference.
	w := apiDo(t, s, http.MethodGet, "/api/me", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	var me map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if configured, ok := me["smtp_configured"].(bool); !ok || configured {
		t.Fatalf("smtp_configured = %v, want false", me["smtp_configured"])
	}

	w = apiDo(t, s, http.MethodPost, "/api/invites", adminCookie, `{"email":"new@example.test","role":"guest","guest_days":30}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invite: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode invite: %v", err)
	}

	// The token is the bearer credential: no session is needed to preview it.
	w = apiDo(t, s, http.MethodGet, "/api/invites/"+created.Token, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("invite preview: %d %s", w.Code, w.Body.String())
	}
	var preview struct {
		Email     string `json:"email"`
		Role      string `json:"role"`
		GuestDays int    `json:"guest_days"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.Email != "new@example.test" || preview.Role != "guest" || preview.GuestDays != 30 || preview.ExpiresAt == "" {
		t.Fatalf("preview = %+v", preview)
	}

	w = apiDo(t, s, http.MethodGet, "/api/invites", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list invites: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Invites []map[string]any `json:"invites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode invite list: %v", err)
	}
	if len(listed.Invites) != 1 {
		t.Fatalf("invite list length = %d, want 1", len(listed.Invites))
	}
	if days, ok := listed.Invites[0]["guest_days"].(float64); !ok || days != 30 {
		t.Fatalf("listed guest_days = %#v, want the number 30", listed.Invites[0]["guest_days"])
	}

	w = apiDo(t, s, http.MethodPost, "/api/invites/accept", nil,
		fmt.Sprintf(`{"token":%q,"name":"New Person","password":"password-strong"}`, created.Token))
	if w.Code != http.StatusCreated {
		t.Fatalf("accept invite: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/invites/"+created.Token, nil, ""); w.Code != http.StatusGone {
		t.Fatalf("accepted invite preview: expected 410, got %d", w.Code)
	}
	if w := apiDo(t, s, http.MethodGet, "/api/invites/inv_does_not_exist", nil, ""); w.Code != http.StatusGone {
		t.Fatalf("unknown invite preview: expected 410, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodPost, "/api/invites", adminCookie, `{"email":"revoked@example.test","role":"member"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create revocable invite: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode revocable invite: %v", err)
	}
	w = apiDo(t, s, http.MethodGet, "/api/invites", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list invites: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Invites []struct {
			ID int64 `json:"id"`
		} `json:"invites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode invites: %v", err)
	}
	if len(list.Invites) == 0 {
		t.Fatal("expected a pending invite")
	}
	if w := apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/invites/%d", list.Invites[0].ID), adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke invite: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/invites/"+created.Token, nil, ""); w.Code != http.StatusGone {
		t.Fatalf("revoked invite preview: expected 410, got %d", w.Code)
	}

	// A second server with SMTP already configured reports the flag as true.
	withMail := testServerWithMail(t, email.Config{Host: "smtp.example.test", Port: 25, From: "work@example.test"}, &mailRecorder{})
	mailAdmin, _, err := withMail.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatalf("setup mail admin: %v", err)
	}
	mailCookie := login(t, withMail, mailAdmin.Email, "password-strong")
	w = apiDo(t, withMail, http.MethodGet, "/api/me", mailCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("mail me: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"smtp_configured":true`) {
		t.Fatalf("smtp_configured not true: %s", w.Body.String())
	}
}
