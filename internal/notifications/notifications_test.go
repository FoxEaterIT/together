package notifications_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/items"
	"jiralike/internal/notifications"
	"jiralike/internal/projects"
)

type fixture struct {
	db           *sql.DB
	store        *notifications.Store
	items        *items.Store
	adminID      int64
	memberID     int64
	managerID    int64
	projectID    int64
	itemID       int64
	secondItemID int64
	doneStateID  int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	adminID := mkUser(t, conn, "Admin", "admin@example.test", "admin")
	memberID := mkUser(t, conn, "Member", "member@example.test", "member")
	managerID := mkUser(t, conn, "Manager", "manager@example.test", "manager")
	project, err := projects.New(conn).Create("Open", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	itemStore := items.New(conn)
	first, err := itemStore.Create(items.CreateInput{
		ProjectID:   project.ID,
		Type:        "action",
		Title:       "First task",
		DueOn:       "2026-01-01",
		AssigneeIDs: []int64{memberID},
		ActorID:     adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := itemStore.Create(items.CreateInput{
		ProjectID:   project.ID,
		Type:        "bug",
		Title:       "Second task",
		DueOn:       "2026-02-01",
		AssigneeIDs: []int64{memberID},
		ActorID:     adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var doneID int64
	if err := conn.QueryRow(`SELECT id FROM workflow_states WHERE name = 'Done'`).Scan(&doneID); err != nil {
		t.Fatal(err)
	}
	return fixture{
		db:           conn,
		store:        notifications.New(conn),
		items:        itemStore,
		adminID:      adminID,
		memberID:     memberID,
		managerID:    managerID,
		projectID:    project.ID,
		itemID:       first.ID,
		secondItemID: second.ID,
		doneStateID:  doneID,
	}
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

func TestAssignedDeduplicationReadAndList(t *testing.T) {
	f := setup(t)

	first, created, err := f.store.CreateAssigned(f.memberID, f.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first assignment should create a notification")
	}
	// A second unread assignment for the same item is suppressed.
	if _, created, err := f.store.CreateAssigned(f.memberID, f.itemID); err != nil || created {
		t.Fatalf("duplicate assignment: created=%v err=%v", created, err)
	}
	// The admin sees every notification; the member is filtered by project
	// access. The fixture project is open, so both see this row.
	if _, created, err := f.store.CreateAssigned(f.managerID, f.itemID); err != nil || !created {
		t.Fatalf("other user assignment: created=%v err=%v", created, err)
	}

	count, err := f.store.UnreadCount(f.memberID, "member")
	if err != nil || count != 1 {
		t.Fatalf("unread count = %d, %v", count, err)
	}
	list, err := f.store.List(f.memberID, "member", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	n := list[0]
	if n.Kind != "assigned" || n.Read || n.ItemTitle != "First task" || n.ProjectName != "Open" {
		t.Fatalf("notification = %+v", n)
	}
	if n.ItemID == nil || *n.ItemID != f.itemID || n.Link != "/projects/1/items/1" {
		t.Fatalf("destination = %+v", n)
	}

	if err := f.store.MarkRead(f.memberID, first.ID); err != nil {
		t.Fatal(err)
	}
	if count, _ = f.store.UnreadCount(f.memberID, "member"); count != 0 {
		t.Fatalf("unread after read = %d", count)
	}
	// Idempotent.
	if err := f.store.MarkRead(f.memberID, first.ID); err != nil {
		t.Fatalf("second read: %v", err)
	}
	// A read assignment may be followed by a fresh one.
	if _, created, err := f.store.CreateAssigned(f.memberID, f.itemID); err != nil || !created {
		t.Fatalf("assignment after read: created=%v err=%v", created, err)
	}
	if count, _ = f.store.UnreadCount(f.memberID, "member"); count != 1 {
		t.Fatalf("unread after reassign = %d", count)
	}

	// Another user cannot mark it read.
	if err := f.store.MarkRead(f.managerID, first.ID); !errors.Is(err, notifications.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}

	marked, err := f.store.MarkAllRead(f.memberID)
	if err != nil || marked != 1 {
		t.Fatalf("mark all = %d, %v", marked, err)
	}
	if marked, err = f.store.MarkAllRead(f.memberID); err != nil || marked != 0 {
		t.Fatalf("second mark all = %d, %v", marked, err)
	}
	if err := f.store.MarkEmailed(first.ID, "2026-03-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	list, err = f.store.List(f.memberID, "member", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range list {
		if n.ID == first.ID {
			found = true
			if n.EmailedAt != "2026-03-01T10:00:00Z" || !n.Read {
				t.Fatalf("emailed/read row = %+v", n)
			}
		}
	}
	if !found {
		t.Fatal("first notification missing from list")
	}
}

func TestMentionUniquePerComment(t *testing.T) {
	f := setup(t)
	comment, err := f.items.CreateComment(f.itemID, f.managerID, "Please look at this\nnow", []int64{f.memberID})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.store.CreateMention(f.memberID, f.itemID, comment.ID); err != nil || !created {
		t.Fatalf("first mention: created=%v err=%v", created, err)
	}
	if _, created, err := f.store.CreateMention(f.memberID, f.itemID, comment.ID); err != nil || created {
		t.Fatalf("duplicate mention: created=%v err=%v", created, err)
	}
	second, err := f.items.CreateComment(f.itemID, f.managerID, "One more", []int64{f.memberID})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.store.CreateMention(f.memberID, f.itemID, second.ID); err != nil || !created {
		t.Fatalf("second comment mention: created=%v err=%v", created, err)
	}

	list, err := f.store.List(f.memberID, "member", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("mentions = %+v", list)
	}
	// Newest first, excerpt collapsed on one line.
	if list[0].Kind != "mention" || list[0].CommentExcerpt != "One more" || list[1].CommentExcerpt != "Please look at this now" {
		t.Fatalf("mention rows = %+v", list)
	}
	if list[0].CommentID == nil || *list[0].CommentID != second.ID {
		t.Fatalf("comment id = %+v", list[0])
	}
}

func TestDueDigestOncePerUserAndDay(t *testing.T) {
	f := setup(t)
	dayOne := "2026-11-10T09:00:00Z"
	first, created, err := f.store.CreateDue(f.memberID, 2, dayOne)
	if err != nil || !created {
		t.Fatalf("first due: created=%v err=%v", created, err)
	}
	if _, created, err := f.store.CreateDue(f.memberID, 2, "2026-11-10T18:00:00Z"); err != nil || created {
		t.Fatalf("same day due: created=%v err=%v", created, err)
	}
	next, created, err := f.store.CreateDue(f.memberID, 1, "2026-11-11T00:05:00Z")
	if err != nil || !created {
		t.Fatalf("next day due: created=%v err=%v", created, err)
	}
	if next.ID == first.ID {
		t.Fatal("next day reused the previous notification")
	}

	var payload struct {
		Day   string `json:"day"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Day != "2026-11-10" || payload.Count != 2 {
		t.Fatalf("payload = %+v", payload)
	}
	found, ok, err := f.store.DayDue(f.memberID, "2026-11-10")
	if err != nil || !ok || found.ID != first.ID {
		t.Fatalf("day due = %+v %v %v", found, ok, err)
	}
	if _, ok, err := f.store.DayDue(f.memberID, "2026-11-12"); err != nil || ok {
		t.Fatalf("missing day = %v %v", ok, err)
	}
	list, err := f.store.List(f.memberID, "member", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Link != "/calendar" || list[0].ItemID != nil {
		t.Fatalf("due rows = %+v", list)
	}
}

func TestDueRecipientsOnlyOpenActiveVisibleItems(t *testing.T) {
	f := setup(t)
	recipients, err := f.store.DueRecipients("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].UserID != f.memberID || recipients[0].Count != 2 {
		t.Fatalf("recipients = %+v", recipients)
	}

	// A done item stops being a deadline.
	if err := f.items.ChangeState(f.secondItemID, f.doneStateID, f.adminID); err != nil {
		t.Fatal(err)
	}
	recipients, err = f.store.DueRecipients("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].Count != 1 {
		t.Fatalf("after done = %+v", recipients)
	}

	// A future due date does not count.
	future, err := f.items.Create(items.CreateInput{
		ProjectID:   f.projectID,
		Type:        "action",
		Title:       "Future",
		DueOn:       "2027-01-01",
		AssigneeIDs: []int64{f.memberID},
		ActorID:     f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	recipients, err = f.store.DueRecipients("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].Count != 1 {
		t.Fatalf("with future = %+v", recipients)
	}

	// Archiving removes the remaining overdue item.
	if _, err := f.items.Archive(f.itemID, f.adminID); err != nil {
		t.Fatal(err)
	}
	recipients, err = f.store.DueRecipients("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 0 {
		t.Fatalf("after archive = %+v", recipients)
	}
	_ = future
}

func TestListHidesNotificationsAfterAccessLoss(t *testing.T) {
	f := setup(t)
	projectsStore := projects.New(f.db)
	private, err := projectsStore.Create("Secret", "", "private", f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectsStore.AddMember(private.ID, f.memberID); err != nil {
		t.Fatal(err)
	}
	item, err := f.items.Create(items.CreateInput{
		ProjectID:   private.ID,
		Type:        "action",
		Title:       "Private task",
		AssigneeIDs: []int64{f.memberID},
		ActorID:     f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.store.CreateAssigned(f.memberID, item.ID); err != nil || !created {
		t.Fatalf("assign private: created=%v err=%v", created, err)
	}
	if list, err := f.store.List(f.memberID, "member", false, 0, 0); err != nil || len(list) != 1 {
		t.Fatalf("member list while granted = %+v %v", list, err)
	}
	if err := projectsStore.RemoveMember(private.ID, f.memberID); err != nil {
		t.Fatal(err)
	}
	if list, err := f.store.List(f.memberID, "member", false, 0, 0); err != nil || len(list) != 0 {
		t.Fatalf("member list after revocation = %+v %v", list, err)
	}
	if count, err := f.store.UnreadCount(f.memberID, "member"); err != nil || count != 0 {
		t.Fatalf("unread after revocation = %d %v", count, err)
	}
	// The admin still sees it.
	if list, err := f.store.List(f.memberID, "admin", false, 0, 0); err != nil || len(list) != 1 {
		t.Fatalf("admin list = %+v %v", list, err)
	}

	// Archiving the item hides the row for everyone, matching the item API.
	if err := projectsStore.AddMember(private.ID, f.memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Archive(item.ID, f.adminID); err != nil {
		t.Fatal(err)
	}
	if list, err := f.store.List(f.memberID, "member", false, 0, 0); err != nil || len(list) != 0 {
		t.Fatalf("list after item archive = %+v %v", list, err)
	}
	if count, err := f.store.UnreadCount(f.memberID, "member"); err != nil || count != 0 {
		t.Fatalf("unread after item archive = %d %v", count, err)
	}
}
