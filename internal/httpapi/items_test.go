package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type itemJSON struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Type        string `json:"type"`
	ParentID    *int64 `json:"parent_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	StateID     int64  `json:"state_id"`
	StateName   string `json:"state_name"`
	DueOn       string `json:"due_on"`
	ArchivedAt  string `json:"archived_at"`
	Assignees   []struct {
		UserID int64  `json:"user_id"`
		Name   string `json:"name"`
	} `json:"assignees"`
}

type commentJSON struct {
	ID       int64  `json:"id"`
	ItemID   int64  `json:"item_id"`
	AuthorID int64  `json:"author_id"`
	Body     string `json:"body"`
	EditedAt string `json:"edited_at"`
	Mentions []struct {
		UserID int64  `json:"user_id"`
		Name   string `json:"name"`
	} `json:"mentions"`
}

type eventJSON struct {
	Kind      string `json:"kind"`
	Field     string `json:"field"`
	FromValue string `json:"from_value"`
	ToValue   string `json:"to_value"`
	ActorID   int64  `json:"actor_id"`
}

func decodeItemResponse(t *testing.T, body []byte) itemJSON {
	t.Helper()
	var it itemJSON
	if err := json.Unmarshal(body, &it); err != nil {
		t.Fatalf("decode item: %v (%s)", err, string(body))
	}
	return it
}

func decodeItems(t *testing.T, body []byte) []itemJSON {
	t.Helper()
	var parsed struct {
		Items []itemJSON `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode items: %v (%s)", err, string(body))
	}
	return parsed.Items
}

func decodeComment(t *testing.T, body []byte) commentJSON {
	t.Helper()
	var c commentJSON
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("decode comment: %v (%s)", err, string(body))
	}
	return c
}

func decodeEvents(t *testing.T, body []byte) []eventJSON {
	t.Helper()
	var parsed struct {
		Events []eventJSON `json:"events"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode events: %v (%s)", err, string(body))
	}
	return parsed.Events
}

func itemByID(list []itemJSON, id int64) bool {
	for _, it := range list {
		if it.ID == id {
			return true
		}
	}
	return false
}

func TestItemsCRUDHierarchyAndAccess(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	outsiderCookie := login(t, s, outsider.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create open project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Private","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private project: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie, fmt.Sprintf(`{"user_id":%d}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("grant member: %d %s", w.Code, w.Body.String())
	}

	// Observer, then an Action with two assignees and a Micro child.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie, `{"type":"observer","title":"Observer"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create observer: %d %s", w.Code, w.Body.String())
	}
	observer := decodeItemResponse(t, w.Body.Bytes())
	if observer.StateName != "To do" || observer.Type != "observer" {
		t.Fatalf("unexpected observer: %+v", observer)
	}

	body := fmt.Sprintf(`{"type":"action","parent_id":%d,"title":"Action","description":"desc","priority":"high","due_on":"2026-02-10","assignee_ids":[%d,%d]}`, observer.ID, member.ID, guest.ID)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create action: %d %s", w.Code, w.Body.String())
	}
	action := decodeItemResponse(t, w.Body.Bytes())
	if action.ParentID == nil || *action.ParentID != observer.ID || len(action.Assignees) != 2 {
		t.Fatalf("unexpected action: %+v", action)
	}

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		fmt.Sprintf(`{"type":"micro","parent_id":%d,"title":"Micro"}`, action.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create micro: %d %s", w.Code, w.Body.String())
	}
	micro := decodeItemResponse(t, w.Body.Bytes())

	// Hierarchy and validation failures.
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"micro without parent":    {`{"type":"micro","title":"No parent"}`, http.StatusBadRequest},
		"observation with parent": {fmt.Sprintf(`{"type":"observer","parent_id":%d,"title":"Child"}`, observer.ID), http.StatusBadRequest},
		"micro under observer":    {fmt.Sprintf(`{"type":"micro","parent_id":%d,"title":"Wrong"}`, observer.ID), http.StatusBadRequest},
		"empty title":             {`{"type":"action","title":"  "}`, http.StatusBadRequest},
		"bad priority":            {`{"type":"action","title":"P","priority":"bogus"}`, http.StatusBadRequest},
		"bad date":                {`{"type":"action","title":"D","due_on":"10/02/2026"}`, http.StatusBadRequest},
		"initial state not input": {`{"type":"action","title":"S","state_id":2}`, http.StatusBadRequest},
	} {
		w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie, tc.body)
		if w.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d: %s", name, tc.want, w.Code, w.Body.String())
		}
	}

	// Guests read but never write. Outsiders cannot even see the private project.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), guestCookie, `{"type":"action","title":"Guest"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest create: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), outsiderCookie, `{"type":"action","title":"Sneaky"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider private create: expected 404, got %d", w.Code)
	}
	// Assignees and mentions must be able to read the project.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), managerCookie,
		fmt.Sprintf(`{"type":"bug","title":"Private bug","assignee_ids":[%d]}`, outsider.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("outsider assignee on private: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), memberCookie,
		fmt.Sprintf(`{"type":"bug","title":"Private bug","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("member create private: %d %s", w.Code, w.Body.String())
	}
	privateItem := decodeItemResponse(t, w.Body.Bytes())

	// Reads follow project visibility.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", action.ID), guestCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("guest read open item: expected 200, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", privateItem.ID), outsiderCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider read private item: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", privateItem.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member read private item: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail struct {
		Item     itemJSON   `json:"item"`
		Parent   *itemJSON  `json:"parent"`
		Children []itemJSON `json:"children"`
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", action.ID), memberCookie, "")
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Parent == nil || detail.Parent.ID != observer.ID {
		t.Fatalf("action parent missing: %s", w.Body.String())
	}
	if len(detail.Children) != 1 || detail.Children[0].ID != micro.ID {
		t.Fatalf("action children missing: %s", w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", observer.ID), adminCookie, "")
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 1 || detail.Children[0].ID != action.ID {
		t.Fatalf("observer children missing: %s", w.Body.String())
	}
}

func TestItemsUpdateCommentsAndArchive(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	other := makeUser(t, s, admin.ID, "Other", "other@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	memberCookie := login(t, s, member.Email, "password-strong")
	otherCookie := login(t, s, other.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Task","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}
	action := decodeItemResponse(t, w.Body.Bytes())
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		fmt.Sprintf(`{"type":"micro","parent_id":%d,"title":"Micro"}`, action.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create micro: %d %s", w.Code, w.Body.String())
	}
	micro := decodeItemResponse(t, w.Body.Bytes())

	// Partial update, then a rejected type change while Micro children exist.
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", action.ID), memberCookie, `{"title":"Task renamed","priority":"urgent","due_on":"2026-03-01"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update item: %d %s", w.Code, w.Body.String())
	}
	updated := decodeItemResponse(t, w.Body.Bytes())
	if updated.Title != "Task renamed" || updated.Priority != "urgent" || updated.DueOn != "2026-03-01" {
		t.Fatalf("unexpected update: %+v", updated)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", action.ID), memberCookie, `{"type":"bug"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("type change with children: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", action.ID), guestCookie, `{"title":"Nope"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest update: expected 403, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/events", action.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("events: %d %s", w.Code, w.Body.String())
	}
	events := decodeEvents(t, w.Body.Bytes())
	fields := map[string]eventJSON{}
	for _, e := range events {
		fields[e.Kind+"/"+e.Field] = e
	}
	if e, ok := fields["created/"]; !ok || e.ActorID != admin.ID {
		t.Fatalf("created event missing: %+v", events)
	}
	if e, ok := fields["field_changed/title"]; !ok || e.FromValue != "Task" || e.ToValue != "Task renamed" {
		t.Fatalf("title event missing: %+v", events)
	}
	if _, ok := fields["field_changed/priority"]; !ok {
		t.Fatalf("priority event missing: %+v", events)
	}

	// Comments with mentions and the author-only edit rule.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", action.ID), memberCookie,
		fmt.Sprintf(`{"body":"Hello @guest","mentions":[%d]}`, guest.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("comment create: %d %s", w.Code, w.Body.String())
	}
	comment := decodeComment(t, w.Body.Bytes())
	if len(comment.Mentions) != 1 || comment.Mentions[0].UserID != guest.ID {
		t.Fatalf("mentions: %+v", comment.Mentions)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", action.ID), guestCookie, `{"body":"Guest"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest comment: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/comments/%d", comment.ID), otherCookie, `{"body":"Hijack"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-author edit: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/comments/%d", comment.ID), memberCookie,
		fmt.Sprintf(`{"body":"Edited @other","mentions":[%d]}`, other.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("author edit: %d %s", w.Code, w.Body.String())
	}
	edited := decodeComment(t, w.Body.Bytes())
	if edited.Body != "Edited @other" || edited.EditedAt == "" || len(edited.Mentions) != 1 || edited.Mentions[0].UserID != other.ID {
		t.Fatalf("unexpected edited comment: %+v", edited)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/comments/%d", comment.ID), adminCookie, `{"body":"Admin edit","mentions":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("admin edit: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", action.ID), guestCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("comment list: %d %s", w.Code, w.Body.String())
	}
	var comments struct {
		Comments []commentJSON `json:"comments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &comments); err != nil {
		t.Fatal(err)
	}
	if len(comments.Comments) != 1 || comments.Comments[0].Body != "Admin edit" {
		t.Fatalf("comments = %+v", comments.Comments)
	}

	// Filters on the list endpoint.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items?project_id=%d&type=action&assignee_id=me", open.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("filtered list: %d %s", w.Code, w.Body.String())
	}
	list := decodeItems(t, w.Body.Bytes())
	if len(list) != 1 || list[0].ID != action.ID {
		t.Fatalf("filtered list = %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, "/api/items?q=renamed", memberCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); len(list) != 1 || list[0].ID != action.ID {
		t.Fatalf("search list = %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, "/api/items?due_from=2026-03-01&due_to=2026-03-31", memberCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); len(list) != 1 || list[0].ID != action.ID {
		t.Fatalf("due filter = %+v", list)
	}

	// Archiving the Action cascades to Micro and hides both from the API.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/items/%d", action.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	var archived struct {
		IDs []int64 `json:"archived_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &archived); err != nil {
		t.Fatal(err)
	}
	if len(archived.IDs) != 2 {
		t.Fatalf("archived ids = %v", archived.IDs)
	}
	for _, id := range []int64{action.ID, micro.ID} {
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", id), adminCookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("archived item %d read: expected 404, got %d", id, w.Code)
		}
		// The row is still there: logical archive, no purge.
		it, err := s.itemsStore.Get(id)
		if err != nil {
			t.Fatalf("archived row %d: %v", id, err)
		}
		if it.ArchivedAt == "" {
			t.Fatalf("archived_at missing for %d", id)
		}
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items?project_id=%d", open.ID), adminCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); itemByID(list, action.ID) || itemByID(list, micro.ID) {
		t.Fatalf("archived items leaked in list: %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items?project_id=%d&archived=1", open.ID), adminCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); !itemByID(list, action.ID) || !itemByID(list, micro.ID) {
		t.Fatalf("admin archived list missing items: %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items?project_id=%d&archived=1", open.ID), memberCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); itemByID(list, action.ID) {
		t.Fatalf("member included archived item: %+v", list)
	}
}

func TestItemsPrivateProjectFilteringAndLeaks(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	outsiderCookie := login(t, s, outsider.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Secret","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie, fmt.Sprintf(`{"user_id":%d}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), managerCookie, `{"type":"bug","title":"Secret bug"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private item: %d %s", w.Code, w.Body.String())
	}
	secret := decodeItemResponse(t, w.Body.Bytes())

	// No list, search or direct read leaks the private item to outsiders.
	for name, cookie := range map[string]*http.Cookie{"outsider": outsiderCookie} {
		w = apiDo(t, s, http.MethodGet, "/api/items", cookie, "")
		if list := decodeItems(t, w.Body.Bytes()); itemByID(list, secret.ID) {
			t.Fatalf("%s list leaked secret: %+v", name, list)
		}
		w = apiDo(t, s, http.MethodGet, "/api/items?q=Secret", cookie, "")
		if list := decodeItems(t, w.Body.Bytes()); len(list) != 0 {
			t.Fatalf("%s search leaked secret: %+v", name, list)
		}
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items?project_id=%d", private.ID), cookie, "")
		if list := decodeItems(t, w.Body.Bytes()); len(list) != 0 {
			t.Fatalf("%s project filter leaked secret: %+v", name, list)
		}
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", secret.ID), cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s direct read: expected 404, got %d", name, w.Code)
		}
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", secret.ID), cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s comment read: expected 404, got %d", name, w.Code)
		}
		w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/events", secret.ID), cookie, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s events read: expected 404, got %d", name, w.Code)
		}
		w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", secret.ID), cookie, `{"title":"Hijacked"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s update: expected 404, got %d", name, w.Code)
		}
		w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", secret.ID), cookie, `{"body":"Hi"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s comment: expected 404, got %d", name, w.Code)
		}
	}

	// Members and admins do see it.
	w = apiDo(t, s, http.MethodGet, "/api/items", memberCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); !itemByID(list, secret.ID) {
		t.Fatalf("member list missing secret: %+v", list)
	}
	w = apiDo(t, s, http.MethodGet, "/api/items", adminCookie, "")
	if list := decodeItems(t, w.Body.Bytes()); !itemByID(list, secret.ID) {
		t.Fatalf("admin list missing secret: %+v", list)
	}

	// A mention of someone who cannot read the project is rejected, so the
	// comment does not become a notification leak.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", secret.ID), memberCookie,
		fmt.Sprintf(`{"body":"@outsider","mentions":[%d]}`, outsider.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("mention outsider: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", secret.ID), memberCookie,
		fmt.Sprintf(`{"body":"@manager","mentions":[%d]}`, manager.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("mention manager: %d %s", w.Code, w.Body.String())
	}

	// Strings that are not IDs fail cleanly.
	w = apiDo(t, s, http.MethodGet, "/api/items/not-a-number", adminCookie, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad item id: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodGet, "/api/items?project_id=abc", adminCookie, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad project filter: expected 400, got %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Fatal("error body should not be empty")
	}
}
