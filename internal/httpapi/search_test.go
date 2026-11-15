package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type searchItemJSON struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Match     string `json:"match"`
}

type searchResponseJSON struct {
	Items []searchItemJSON `json:"items"`
	Total int              `json:"total"`
}

func decodeSearch(t *testing.T, w *httptest.ResponseRecorder) searchResponseJSON {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d %s", w.Code, w.Body.String())
	}
	var body searchResponseJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

type searchFixture struct {
	server       *Server
	adminCookie  *http.Cookie
	managerID    int64
	openItem     itemJSON
	privateItem  itemJSON
	outsiderID   int64
	outsiderAuth *http.Cookie
}

func newSearchFixture(t *testing.T) searchFixture {
	t.Helper()
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager-search@example.test", "manager", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider-search@example.test", "member", 0)
	outsiderCookie := login(t, s, outsider.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("open project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		`{"type":"action","title":"Caffè latte","description":"Release notes"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("open item: %d %s", w.Code, w.Body.String())
	}
	openItem := decodeItemResponse(t, w.Body.Bytes())

	w = apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Private","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("private project: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), managerCookie,
		`{"type":"action","title":"Secretneedle plan"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("private item: %d %s", w.Code, w.Body.String())
	}
	privateItem := decodeItemResponse(t, w.Body.Bytes())

	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", openItem.ID), adminCookie,
		`{"body":"A nuance lives only in this comment"}`); w.Code != http.StatusCreated {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	return searchFixture{
		server:       s,
		adminCookie:  adminCookie,
		managerID:    manager.ID,
		openItem:     openItem,
		privateItem:  privateItem,
		outsiderID:   outsider.ID,
		outsiderAuth: outsiderCookie,
	}
}

func TestItemsSearchFindsCommentAndReportsTotal(t *testing.T) {
	f := newSearchFixture(t)
	w := apiDo(t, f.server, http.MethodGet, "/api/items?q=nuance&total=1", f.adminCookie, "")
	body := decodeSearch(t, w)
	if len(body.Items) != 1 || body.Items[0].ID != f.openItem.ID || body.Total != 1 {
		t.Fatalf("comment search = %+v total %d", body.Items, body.Total)
	}
	if body.Items[0].Match != "comments" {
		t.Fatalf("match = %q", body.Items[0].Match)
	}
	if !strings.Contains(body.Items[0].Snippet, "«nuance»") {
		t.Fatalf("snippet = %q", body.Items[0].Snippet)
	}
}

func TestItemsSearchValidation(t *testing.T) {
	f := newSearchFixture(t)
	paths := []string{
		"/api/items?q=foo%20OR%20bar",
		"/api/items?q=" + strings.Repeat("a", 257),
		"/api/items?q=foo&sort=nope",
		"/api/items?q=foo&state_category=nope",
		"/api/items?q=foo&created_from=not-a-date",
		"/api/items?q=foo&updated_to=2026-13-01",
		"/api/items?q=foo&parent_id=abc",
	}
	for _, path := range paths {
		w := apiDo(t, f.server, http.MethodGet, path, f.adminCookie, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestItemsSearchDoesNotLeakPrivateProjects(t *testing.T) {
	f := newSearchFixture(t)
	w := apiDo(t, f.server, http.MethodGet, "/api/items?q=secretneedle&total=1", f.outsiderAuth, "")
	body := decodeSearch(t, w)
	if len(body.Items) != 0 || body.Total != 0 {
		t.Fatalf("private search leaked %+v total %d", body.Items, body.Total)
	}
	w = apiDo(t, f.server, http.MethodGet, "/api/items?q=secretneedle&total=1", f.adminCookie, "")
	body = decodeSearch(t, w)
	if len(body.Items) != 1 || body.Items[0].ID != f.privateItem.ID {
		t.Fatalf("admin private search = %+v", body.Items)
	}
}

func TestSearchRebuildRequiresAdminAndRepairsIndex(t *testing.T) {
	f := newSearchFixture(t)
	member := makeUser(t, f.server, f.managerID, "Member", "member-rebuild@example.test", "member", 0)
	memberCookie := login(t, f.server, member.Email, "password-strong")

	for name, cookie := range map[string]*http.Cookie{"member": memberCookie} {
		w := apiDo(t, f.server, http.MethodPost, "/api/admin/search/rebuild", cookie, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s rebuild = %d %s", name, w.Code, w.Body.String())
		}
	}
	w := apiDo(t, f.server, http.MethodPost, "/api/admin/search/rebuild", f.outsiderAuth, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("outsider rebuild = %d %s", w.Code, w.Body.String())
	}

	if _, err := f.server.db.Exec(`DELETE FROM items_fts`); err != nil {
		t.Fatal(err)
	}
	w = apiDo(t, f.server, http.MethodPost, "/api/admin/search/rebuild", f.adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin rebuild = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Indexed int `json:"indexed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Indexed < 1 {
		t.Fatalf("indexed = %d", body.Indexed)
	}
	w = apiDo(t, f.server, http.MethodGet, "/api/items?q=nuance", f.adminCookie, "")
	if result := decodeSearch(t, w); len(result.Items) != 1 {
		t.Fatalf("search after rebuild = %+v", result.Items)
	}
}

func TestSearchRebuildRejectsCrossSiteRequest(t *testing.T) {
	f := newSearchFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/search/rebuild", nil)
	request.AddCookie(f.adminCookie)
	request.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site rebuild = %d %s", w.Code, w.Body.String())
	}
}
