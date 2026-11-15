package items_test

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/items"
	"jiralike/internal/projects"
)

type fixture struct {
	db       *sql.DB
	items    *items.Store
	projects *projects.Store
	adminID  int64
	openID   int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	store := items.New(conn)
	projectStore := projects.New(conn)
	adminID := mkUser(t, conn, "Admin", "admin@example.test", "admin")
	open, err := projectStore.Create("Open", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: conn, items: store, projects: projectStore, adminID: adminID, openID: open.ID}
}

func mkUser(t *testing.T, conn *sql.DB, name, email, role string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
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

func createItem(t *testing.T, store *items.Store, projectID, actorID int64, typ string, parent *int64, title string) items.Item {
	t.Helper()
	it, err := store.Create(items.CreateInput{
		ProjectID: projectID,
		Type:      typ,
		ParentID:  parent,
		Title:     title,
		ActorID:   actorID,
	})
	if err != nil {
		t.Fatalf("create %s %q: %v", typ, title, err)
	}
	return it
}

func TestHierarchyValidation(t *testing.T) {
	f := setup(t)
	other, err := f.projects.Create("Other", "", "open", f.adminID)
	if err != nil {
		t.Fatal(err)
	}

	observer := createItem(t, f.items, f.openID, f.adminID, "observer", nil, "Observer")
	action := createItem(t, f.items, f.openID, f.adminID, "action", &observer.ID, "Action")
	createItem(t, f.items, f.openID, f.adminID, "micro", &action.ID, "Micro")
	otherObserver := createItem(t, f.items, other.ID, f.adminID, "observer", nil, "Other observer")

	cases := []struct {
		name   string
		typ    string
		parent *int64
		want   error
	}{
		{"observer cannot have a parent", "observer", &observer.ID, items.ErrParentInvalid},
		{"micro requires a parent", "micro", nil, items.ErrParentRequired},
		{"micro parent must be an action", "micro", &observer.ID, items.ErrParentInvalid},
		{"action parent must be an observer", "action", &action.ID, items.ErrParentInvalid},
		{"bug parent must be an observer", "bug", &action.ID, items.ErrParentInvalid},
		{"parent must share the project", "bug", &otherObserver.ID, items.ErrParentProject},
		{"unknown parent", "action", int64Ptr(9999), items.ErrParentNotFound},
		{"invalid type", "epic", nil, items.ErrInvalidType},
		{"title required", "action", nil, items.ErrTitleEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title := "Valid title"
			if tc.want == items.ErrTitleEmpty {
				title = "  "
			}
			_, err := f.items.Create(items.CreateInput{
				ProjectID: f.openID,
				Type:      tc.typ,
				ParentID:  tc.parent,
				Title:     title,
				ActorID:   f.adminID,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	// Valid shapes: action and bug under an observer, micro under an action.
	createItem(t, f.items, f.openID, f.adminID, "bug", &observer.ID, "Bug")
	createItem(t, f.items, f.openID, f.adminID, "action", nil, "Loose action")

	// An observer cannot become a child on update.
	_, err = f.items.Update(observer.ID, items.UpdateInput{
		Type: "observer", ParentID: &action.ID, Title: "Observer", Priority: "none", ActorID: f.adminID,
	})
	if !errors.Is(err, items.ErrParentInvalid) {
		t.Fatalf("observer to child: %v", err)
	}
	// An action with micro children cannot stop being an action.
	_, err = f.items.Update(action.ID, items.UpdateInput{
		Type: "bug", Title: "Action", Priority: "none", ActorID: f.adminID,
	})
	if !errors.Is(err, items.ErrChildrenRequireAction) {
		t.Fatalf("action with children type change: %v", err)
	}
	// Detaching the action clears the parent and records the parent change.
	detached, err := f.items.Update(action.ID, items.UpdateInput{
		Type: "action", Title: "Action", Priority: "none", ActorID: f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detached.ParentID != nil {
		t.Fatalf("parent should be nil, got %v", *detached.ParentID)
	}
	events, err := f.items.Events(action.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "field_changed" && e.Field == "parent" && e.FromValue == int64String(observer.ID) && e.ToValue == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("parent event missing: %+v", events)
	}
	// The archived observer cannot receive new children.
	if _, err := f.items.Archive(observer.ID, f.adminID); err != nil {
		t.Fatal(err)
	}
	_, err = f.items.Create(items.CreateInput{
		ProjectID: f.openID, Type: "bug", ParentID: &observer.ID, Title: "Late bug", ActorID: f.adminID,
	})
	if !errors.Is(err, items.ErrParentArchived) {
		t.Fatalf("archived parent: %v", err)
	}

	// Archiving an Observer does not archive its Actions, and those Actions
	// must remain editable even though their parent link is now archived.
	parentObserver := createItem(t, f.items, f.openID, f.adminID, "observer", nil, "Parent observer")
	childAction := createItem(t, f.items, f.openID, f.adminID, "action", &parentObserver.ID, "Child action")
	if _, err := f.items.Archive(parentObserver.ID, f.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Update(childAction.ID, items.UpdateInput{
		Type: "action", ParentID: &parentObserver.ID, Title: "Child renamed", Priority: "none", ActorID: f.adminID,
	}); err != nil {
		t.Fatalf("editing an action after its observer is archived: %v", err)
	}
}

func TestCreateUpdateLifecycleAndEvents(t *testing.T) {
	f := setup(t)
	bob := mkUser(t, f.db, "Bob", "bob@example.test", "member")
	alice := mkUser(t, f.db, "Alice", "alice@example.test", "member")

	it, err := f.items.Create(items.CreateInput{
		ProjectID:   f.openID,
		Type:        "action",
		Title:       "First task",
		Description: "Initial",
		Priority:    "low",
		DueOn:       "2026-01-02",
		AssigneeIDs: []int64{bob, alice, bob},
		ActorID:     f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if it.StateName != "To do" || it.StateCategory != "open" {
		t.Fatalf("initial state not applied: %+v", it)
	}
	if len(it.Assignees) != 2 || it.Assignees[0].Name != "Alice" || it.Assignees[1].Name != "Bob" {
		t.Fatalf("assignees not deduplicated/sorted: %+v", it.Assignees)
	}
	events, err := f.items.Events(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "created" || events[0].ActorID != f.adminID {
		t.Fatalf("created event: %+v", events)
	}

	updated, err := f.items.Update(it.ID, items.UpdateInput{
		Type:        "action",
		Title:       "Renamed",
		Description: "Changed",
		Priority:    "urgent",
		DueOn:       "",
		AssigneeIDs: []int64{bob},
		ActorID:     alice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Renamed" || updated.Priority != "urgent" || updated.DueOn != "" || len(updated.Assignees) != 1 {
		t.Fatalf("update failed: %+v", updated)
	}
	events, err = f.items.Events(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range events {
		if e.Kind == "field_changed" || e.Kind == "assignees_changed" {
			got[e.Field] = e.FromValue + "->" + e.ToValue
			if e.ActorID != alice {
				t.Fatalf("event actor = %d, want %d", e.ActorID, alice)
			}
		}
	}
	want := map[string]string{
		"title":       "First task->Renamed",
		"description": "Initial->Changed",
		"priority":    "low->urgent",
		"due_on":      "2026-01-02->",
		"assignees":   int64String(bob) + "," + int64String(alice) + "->" + int64String(bob),
	}
	for field, value := range want {
		if got[field] != value {
			t.Fatalf("event %s = %q, want %q (all: %+v)", field, got[field], value, got)
		}
	}
	if _, ok := got["type"]; ok {
		t.Fatalf("unchanged type should not emit an event: %+v", got)
	}

	var doneID int64
	if err := f.db.QueryRow(`SELECT id FROM workflow_states WHERE name = 'Done'`).Scan(&doneID); err != nil {
		t.Fatal(err)
	}
	if err := f.items.ChangeState(it.ID, doneID, f.adminID); err != nil {
		t.Fatal(err)
	}
	updated, err = f.items.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StateName != "Done" || updated.StateCategory != "done" {
		t.Fatalf("state not changed: %+v", updated)
	}
	events, err = f.items.Events(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Kind != "state_changed" || events[len(events)-1].Field != "state" {
		t.Fatalf("state event missing: %+v", events[len(events)-1])
	}

	// Invalid values never reach the database.
	if _, err := f.items.Update(it.ID, items.UpdateInput{Type: "action", Title: " ", Priority: "none", ActorID: f.adminID}); !errors.Is(err, items.ErrTitleEmpty) {
		t.Fatalf("empty title: %v", err)
	}
	if _, err := f.items.Update(it.ID, items.UpdateInput{Type: "action", Title: "x", Priority: "bogus", ActorID: f.adminID}); !errors.Is(err, items.ErrInvalidPriority) {
		t.Fatalf("bad priority: %v", err)
	}
	if _, err := f.items.Update(it.ID, items.UpdateInput{Type: "action", Title: "x", Priority: "none", DueOn: "2026-13-40", ActorID: f.adminID}); !errors.Is(err, items.ErrDueInvalid) {
		t.Fatalf("bad date: %v", err)
	}
}

func TestChangeStateFromIsAtomic(t *testing.T) {
	f := setup(t)
	it := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Task")
	var progressID, doneID int64
	if err := f.db.QueryRow(`SELECT id FROM workflow_states WHERE name = 'In progress'`).Scan(&progressID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT id FROM workflow_states WHERE name = 'Done'`).Scan(&doneID); err != nil {
		t.Fatal(err)
	}
	// The expected source state does not match, so nothing is written.
	if err := f.items.ChangeStateFrom(it.ID, progressID, doneID, f.adminID); !errors.Is(err, items.ErrStateConflict) {
		t.Fatalf("wrong source state: %v", err)
	}
	unchanged, err := f.items.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.StateID != it.StateID {
		t.Fatalf("state changed despite conflict: %+v", unchanged)
	}
	if err := f.items.ChangeStateFrom(it.ID, it.StateID, doneID, f.adminID); err != nil {
		t.Fatal(err)
	}
	changed, err := f.items.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed.StateID != doneID {
		t.Fatalf("state = %d, want %d", changed.StateID, doneID)
	}
}

func TestArchiveCascadesMicroAndKeepsRows(t *testing.T) {
	f := setup(t)
	action := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Action")
	microOne := createItem(t, f.items, f.openID, f.adminID, "micro", &action.ID, "Micro one")
	microTwo := createItem(t, f.items, f.openID, f.adminID, "micro", &action.ID, "Micro two")
	unrelated := createItem(t, f.items, f.openID, f.adminID, "bug", nil, "Bug")

	archived, err := f.items.Archive(action.ID, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 3 {
		t.Fatalf("archived ids = %v, want action plus two micro", archived)
	}
	for _, id := range []int64{action.ID, microOne.ID, microTwo.ID} {
		it, err := f.items.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if it.ArchivedAt == "" {
			t.Fatalf("item %d not archived", id)
		}
		events, err := f.items.Events(id)
		if err != nil {
			t.Fatal(err)
		}
		if events[len(events)-1].Kind != "archived" {
			t.Fatalf("archived event missing for %d: %+v", id, events)
		}
	}
	if _, err := f.items.Archive(action.ID, f.adminID); !errors.Is(err, items.ErrArchived) {
		t.Fatalf("double archive: %v", err)
	}
	list, err := f.items.List(f.adminID, "admin", items.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list {
		if it.ID == action.ID || it.ID == microOne.ID || it.ID == microTwo.ID {
			t.Fatalf("archived item %d leaked into the list", it.ID)
		}
	}
	if len(list) != 1 || list[0].ID != unrelated.ID {
		t.Fatalf("unexpected list: %+v", list)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("rows = %d, archive must not delete", count)
	}
}

func TestCommentsMentionsAndEditing(t *testing.T) {
	f := setup(t)
	bob := mkUser(t, f.db, "Bob", "bob@example.test", "member")
	alice := mkUser(t, f.db, "Alice", "alice@example.test", "member")
	other := mkUser(t, f.db, "Other", "other@example.test", "member")
	it := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Task")

	comment, err := f.items.CreateComment(it.ID, bob, "Hello @alice", []int64{alice, alice})
	if err != nil {
		t.Fatal(err)
	}
	if len(comment.Mentions) != 1 || comment.Mentions[0].UserID != alice {
		t.Fatalf("mentions = %+v", comment.Mentions)
	}
	if comment.AuthorName != "Bob" || comment.EditedAt != "" {
		t.Fatalf("comment = %+v", comment)
	}

	if _, err := f.items.UpdateComment(comment.ID, other, "member", "hijack", nil); !errors.Is(err, items.ErrCommentAuthor) {
		t.Fatalf("non-author edit: %v", err)
	}
	edited, err := f.items.UpdateComment(comment.ID, bob, "member", "Edited @alice", []int64{alice})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Body != "Edited @alice" || edited.EditedAt == "" {
		t.Fatalf("edited comment = %+v", edited)
	}
	cleared, err := f.items.UpdateComment(comment.ID, bob, "member", "No mentions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Mentions) != 0 {
		t.Fatalf("mentions should be rewritten: %+v", cleared.Mentions)
	}
	list, err := f.items.ListComments(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Body != "No mentions" {
		t.Fatalf("comments = %+v", list)
	}
	events, err := f.items.Events(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, e := range events {
		if e.Kind == "comment_added" && e.ActorID == bob {
			added++
		}
	}
	if added != 1 {
		t.Fatalf("comment_added events = %d: %+v", added, events)
	}
	if _, err := f.items.CreateComment(it.ID, bob, "  ", nil); !errors.Is(err, items.ErrCommentEmpty) {
		t.Fatalf("empty comment: %v", err)
	}
	if _, err := f.items.CreateComment(it.ID, bob, "Hi", []int64{9999}); !errors.Is(err, items.ErrCommentNotFound) {
		t.Fatalf("unknown mention: %v", err)
	}
	if _, err := f.items.CreateComment(9999, bob, "Hi", nil); !errors.Is(err, items.ErrNotFound) {
		t.Fatalf("unknown item: %v", err)
	}
}

func TestListFiltersAndVisibility(t *testing.T) {
	f := setup(t)
	manager := mkUser(t, f.db, "Manager", "manager@example.test", "manager")
	member := mkUser(t, f.db, "Member", "member@example.test", "member")
	outsider := mkUser(t, f.db, "Outsider", "outsider@example.test", "member")
	guest := mkUser(t, f.db, "Guest", "guest@example.test", "guest")
	private, err := f.projects.Create("Private", "", "private", manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.projects.AddMember(private.ID, member); err != nil {
		t.Fatal(err)
	}

	openItem, err := f.items.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Open task", Description: "searchable text",
		Priority: "high", DueOn: "2026-02-10", AssigneeIDs: []int64{guest}, ActorID: f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateItem := createItem(t, f.items, private.ID, manager, "bug", nil, "Private bug")
	action := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Container")
	archivedItem := createItem(t, f.items, f.openID, f.adminID, "micro", &action.ID, "Archived micro")
	if _, err := f.items.Archive(archivedItem.ID, f.adminID); err != nil {
		t.Fatal(err)
	}

	check := func(name string, userID int64, role string, filter items.Filter, want ...int64) {
		t.Helper()
		list, err := f.items.List(userID, role, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(list) != len(want) {
			t.Fatalf("%s: got %d items %+v, want %d", name, len(list), list, len(want))
		}
		for _, id := range want {
			if !containsID(list, id) {
				t.Fatalf("%s: missing item %d in %+v", name, id, list)
			}
		}
	}

	// Outsider sees only the open item; the guest sees it too even though it is
	// assigned to them, and neither sees the private project item.
	check("outsider list", outsider, "member", items.Filter{}, openItem.ID, action.ID)
	check("guest list", guest, "guest", items.Filter{}, openItem.ID, action.ID)
	check("member private", member, "member", items.Filter{ProjectID: private.ID}, privateItem.ID)
	check("admin sees all", f.adminID, "admin", items.Filter{}, openItem.ID, privateItem.ID, action.ID)

	// Query, type, assignee, priority and due filters combine.
	check("q title", f.adminID, "admin", items.Filter{Query: "open task"}, openItem.ID)
	check("q description", f.adminID, "admin", items.Filter{Query: "searchable"}, openItem.ID)
	check("type", f.adminID, "admin", items.Filter{Type: "bug"}, privateItem.ID)
	check("assignee", f.adminID, "admin", items.Filter{AssigneeID: guest}, openItem.ID)
	check("priority", f.adminID, "admin", items.Filter{Priority: "high"}, openItem.ID)
	check("due window", f.adminID, "admin", items.Filter{DueFrom: "2026-02-01", DueTo: "2026-02-28"}, openItem.ID)
	check("due window excludes", f.adminID, "admin", items.Filter{DueFrom: "2026-03-01"})
	check("no due", f.adminID, "admin", items.Filter{NoDue: true}, privateItem.ID, action.ID)

	// Archived rows stay hidden unless an admin explicitly asks for them.
	list, err := f.items.List(f.adminID, "admin", items.Filter{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(list, archivedItem.ID) {
		t.Fatalf("admin include archived missing item: %+v", list)
	}
	list, err = f.items.List(member, "member", items.Filter{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if containsID(list, archivedItem.ID) {
		t.Fatalf("non-admin must never include archived: %+v", list)
	}

	// Search must not leak private projects.
	list, err = f.items.List(outsider, "member", items.Filter{Query: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("private item leaked through search: %+v", list)
	}
}

func containsID(list []items.Item, id int64) bool {
	for _, it := range list {
		if it.ID == id {
			return true
		}
	}
	return false
}

func int64Ptr(v int64) *int64 {
	return &v
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}
