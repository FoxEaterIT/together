package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoleAccessMatrixV1 is the release acceptance matrix: every role against
// an open project, a private project reached directly, through a team, not at
// all, and an archived project. It asserts the status code and the absence of
// leaked titles in list, search and report responses.
func TestRoleAccessMatrixV1(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	expiredGuest := makeUser(t, s, admin.ID, "Expired Guest", "expired@example.test", "guest", 30)

	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")
	outsiderCookie := login(t, s, outsider.Email, "password-strong")
	expiredCookie := login(t, s, expiredGuest.Email, "password-strong")
	if _, err := s.db.Exec(`UPDATE users SET guest_expires_on = '2020-01-01' WHERE id = ?`, expiredGuest.ID); err != nil {
		t.Fatal(err)
	}

	// Projects: open, private with direct and team grants, archived.
	openProject := createProjectAs(t, s, adminCookie, "Open Matrix", "open")
	privateProject := createProjectAs(t, s, managerCookie, "Private Matrix", "private")
	archivedProject := createProjectAs(t, s, managerCookie, "Archived Matrix", "private")
	for _, grant := range []struct {
		project int64
		user    int64
	}{{privateProject.ID, member.ID}, {archivedProject.ID, member.ID}} {
		w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", grant.project), managerCookie, fmt.Sprintf(`{"user_id":%d}`, grant.user))
		if w.Code != http.StatusCreated {
			t.Fatalf("grant %d on %d: %d %s", grant.user, grant.project, w.Code, w.Body.String())
		}
	}
	team, err := s.teamsStore.Create("Matrix Team")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.teamsStore.AddMember(team.ID, guest.ID); err != nil {
		t.Fatal(err)
	}
	w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/teams", privateProject.ID), managerCookie, fmt.Sprintf(`{"team_id":%d}`, team.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("grant team: %d %s", w.Code, w.Body.String())
	}

	openItem := createItemAs(t, s, adminCookie, openProject.ID, "Open item")
	privateItem := createItemAs(t, s, adminCookie, privateProject.ID, "Private secret item")
	archivedItem := createItemAs(t, s, adminCookie, archivedProject.ID, "Archived secret item")
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d", archivedProject.ID), managerCookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("archive project: %d %s", w.Code, w.Body.String())
	}

	actors := []struct {
		name      string
		cookie    *http.Cookie
		expired   bool
		role      string
		expect404 bool
	}{
		{"admin", adminCookie, false, "admin", false},
		{"manager", managerCookie, false, "manager", false},
		{"member", memberCookie, false, "member", false},
		{"guest", guestCookie, false, "guest", false},
		{"outsider", outsiderCookie, false, "member", true},
		{"expired guest", expiredCookie, true, "guest", true},
	}

	// Read matrix for project detail.
	wantProject := map[string]map[string]int{
		"admin":         {"open": 200, "private": 200, "archived": 200},
		"manager":       {"open": 200, "private": 200, "archived": 200},
		"member":        {"open": 200, "private": 200, "archived": 404},
		"guest":         {"open": 200, "private": 200, "archived": 404},
		"outsider":      {"open": 200, "private": 404, "archived": 404},
		"expired guest": {"open": 401, "private": 401, "archived": 401},
	}
	projects := map[string]int64{"open": openProject.ID, "private": privateProject.ID, "archived": archivedProject.ID}
	for _, actor := range actors {
		for label, id := range projects {
			w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d", id), actor.cookie, "")
			if w.Code != wantProject[actor.name][label] {
				t.Fatalf("project %s %s: expected %d, got %d", actor.name, label, wantProject[actor.name][label], w.Code)
			}
			if w.Code >= 400 && strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("project %s %s leaked a title: %s", actor.name, label, w.Body.String())
			}
		}
	}

	// Read matrix for item detail, including the archived project's item.
	wantItem := map[string]map[string]int{
		"admin":         {"open": 200, "private": 200, "archived": 200},
		"manager":       {"open": 200, "private": 200, "archived": 200},
		"member":        {"open": 200, "private": 200, "archived": 404},
		"guest":         {"open": 200, "private": 200, "archived": 404},
		"outsider":      {"open": 200, "private": 404, "archived": 404},
		"expired guest": {"open": 401, "private": 401, "archived": 401},
	}
	items := map[string]int64{"open": openItem.ID, "private": privateItem.ID, "archived": archivedItem.ID}
	for _, actor := range actors {
		for label, id := range items {
			w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", id), actor.cookie, "")
			if w.Code != wantItem[actor.name][label] {
				t.Fatalf("item %s %s: expected %d, got %d", actor.name, label, wantItem[actor.name][label], w.Code)
			}
			if w.Code >= 400 && (strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "Private secret")) {
				t.Fatalf("item %s %s leaked a title: %s", actor.name, label, w.Body.String())
			}
		}
	}

	// Lists and search never include the private project or its item for the
	// outsider; the archived project is absent even for its manager without the
	// archived filter.
	for _, path := range []string{"/api/projects?q=Private", "/api/projects?visibility=private"} {
		w := apiDo(t, s, http.MethodGet, path, outsiderCookie, "")
		if w.Code != http.StatusOK {
			t.Fatalf("outsider %s: %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "Private Matrix") {
			t.Fatalf("outsider saw private project in %s: %s", path, w.Body.String())
		}
	}
	w = apiDo(t, s, http.MethodGet, "/api/items?q=secret", outsiderCookie, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("outsider search leaked private items: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/items", memberCookie, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Archived secret item") {
		t.Fatalf("archived item leaked into the member list: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects?archived=1", managerCookie, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Archived Matrix") {
		t.Fatalf("manager archived filter: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/projects?archived=1", memberCookie, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Archived Matrix") {
		t.Fatalf("member archived filter: %d %s", w.Code, w.Body.String())
	}

	// Reports follow the same visibility, and an archived project filter is a
	// 404 even for the manager who can still read the project.
	for _, tc := range []struct {
		actor  string
		cookie *http.Cookie
		want   int
	}{
		{"admin", adminCookie, 200},
		{"manager", managerCookie, 200},
		{"member", memberCookie, 200},
		{"guest", guestCookie, 200},
		{"outsider", outsiderCookie, 404},
		{"expired guest", expiredCookie, 401},
	} {
		w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/reports?project_id=%d", privateProject.ID), tc.cookie, "")
		if w.Code != tc.want {
			t.Fatalf("report %s private: expected %d, got %d", tc.actor, tc.want, w.Code)
		}
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/reports?project_id=%d", archivedProject.ID), managerCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("archived report filter: expected 404, got %d", w.Code)
	}

	// Write matrix: guests read but never write; the outsider cannot write
	// through an invisible project, and archived projects reject writes.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", openProject.ID), guestCookie, `{"type":"action","title":"Guest attempt"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest item create: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", openItem.ID), guestCookie, `{"title":"Guest edit"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest item edit: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", openItem.ID), guestCookie, `{"body":"Guest comment"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest comment: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", privateItem.ID), outsiderCookie, `{"body":"Outsider comment"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider comment on private item: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", privateItem.ID), outsiderCookie, `{"title":"Hijacked"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider edit on private item: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", archivedItem.ID), managerCookie, `{"title":"Archived edit"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("archived item edit: expected 409, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", privateItem.ID), memberCookie, `{"body":"Member comment"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("member comment on private item: %d %s", w.Code, w.Body.String())
	}

	// The guest can read the private project through the team and still cannot
	// transition; the member can.
	var wf workflowJSON
	w = apiDo(t, s, http.MethodGet, "/api/workflow", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("workflow: %d", w.Code)
	}
	wf = decodeWorkflow(t, w.Body.Bytes())
	var todoID, doneID int64
	for _, state := range wf.States {
		switch state.Name {
		case "To do":
			todoID = state.ID
		case "Done":
			doneID = state.ID
		}
	}
	w = apiDo(t, s, http.MethodPost, "/api/workflow/transitions", adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Finish"}`, todoID, doneID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create transition: %d %s", w.Code, w.Body.String())
	}
	transition := decodeWorkflowTransition(t, w.Body.Bytes())
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", privateItem.ID, transition.ID), guestCookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest transition: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", privateItem.ID, transition.ID), outsiderCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider transition on private item: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", privateItem.ID, transition.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member transition: %d %s", w.Code, w.Body.String())
	}

	// Expired guests lose both the session and personal tokens, and cannot log
	// in again. Data endpoints answer 401 so the UI signs out; /api/me keeps
	// its explicit 403 for the expired session.
	w = apiDo(t, s, http.MethodGet, "/api/me", expiredCookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired guest session: expected 403, got %d", w.Code)
	}
	token, err := s.auth.CreateToken(expiredGuest.ID, "expired")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Authorization", "Bearer "+token.Token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired guest token: expected 401, got %d", rec.Code)
	}
	w = apiDo(t, s, http.MethodPost, "/api/login", nil, `{"email":"expired@example.test","password":"password-strong"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired guest login: expected 403, got %d", w.Code)
	}

	// Role-gated routes: the answer is 403, not a hidden button.
	roleCases := []struct {
		method string
		path   string
		body   string
		want   map[string]int
	}{
		{http.MethodPost, "/api/projects", `{"name":"Role case","visibility":"open"}`, map[string]int{"admin": 201, "manager": 201, "member": 403, "guest": 403}},
		{http.MethodGet, "/api/users", "", map[string]int{"admin": 200, "manager": 403, "member": 403, "guest": 403}},
		{http.MethodGet, "/api/automations", "", map[string]int{"admin": 200, "manager": 200, "member": 403, "guest": 403}},
		{http.MethodPost, "/api/workflow/states", `{"name":"Blocked","category":"open"}`, map[string]int{"admin": 201, "manager": 403, "member": 403, "guest": 403}},
		{http.MethodGet, "/api/admin/backup", "", map[string]int{"admin": 200, "manager": 403, "member": 403, "guest": 403}},
	}
	cookies := map[string]*http.Cookie{"admin": adminCookie, "manager": managerCookie, "member": memberCookie, "guest": guestCookie}
	for _, tc := range roleCases {
		for role, want := range tc.want {
			w := apiDo(t, s, tc.method, tc.path, cookies[role], tc.body)
			if w.Code != want {
				t.Fatalf("%s %s as %s: expected %d, got %d (%s)", tc.method, tc.path, role, want, w.Code, w.Body.String())
			}
		}
	}
}

func createProjectAs(t *testing.T, s *Server, cookie *http.Cookie, name, visibility string) projectJSON {
	t.Helper()
	body, err := json.Marshal(map[string]string{"name": name, "description": name, "visibility": visibility})
	if err != nil {
		t.Fatal(err)
	}
	w := apiDo(t, s, http.MethodPost, "/api/projects", cookie, string(body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create project %s: %d %s", name, w.Code, w.Body.String())
	}
	return decodeProject(t, w)
}

func createItemAs(t *testing.T, s *Server, cookie *http.Cookie, projectID int64, title string) itemJSON {
	t.Helper()
	w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", projectID), cookie, fmt.Sprintf(`{"type":"action","title":%q}`, title))
	if w.Code != http.StatusCreated {
		t.Fatalf("create item %s: %d %s", title, w.Code, w.Body.String())
	}
	return decodeItemResponse(t, w.Body.Bytes())
}
