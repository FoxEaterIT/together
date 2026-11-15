package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/teams"
)

type sentMail struct {
	To      string
	Subject string
	Body    string
}

type mailRecorder struct {
	mu    sync.Mutex
	mails []sentMail
}

func (r *mailRecorder) send(cfg email.Config, to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{To: to, Subject: subject, Body: body})
	return nil
}

func (r *mailRecorder) all() []sentMail {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sentMail, len(r.mails))
	copy(out, r.mails)
	return out
}

func testServerWithMail(t *testing.T, cfg email.Config, recorder *mailRecorder) *Server {
	t.Helper()
	return testServerWithSender(t, cfg, recorder.send)
}

func testServerWithSender(t *testing.T, cfg email.Config, sender func(cfg email.Config, to, subject, body string) error) *Server {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewWithOptions(conn, nil, false, cfg, sender, teams.New(conn))
}

type notificationJSON struct {
	ID             int64           `json:"id"`
	Kind           string          `json:"kind"`
	ItemID         *int64          `json:"item_id"`
	CommentID      *int64          `json:"comment_id"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      string          `json:"created_at"`
	Read           bool            `json:"read"`
	EmailedAt      string          `json:"emailed_at"`
	ItemTitle      string          `json:"item_title"`
	ProjectID      *int64          `json:"project_id"`
	ProjectName    string          `json:"project_name"`
	CommentExcerpt string          `json:"comment_excerpt"`
	Link           string          `json:"link"`
}

func decodeNotifications(t *testing.T, body []byte) []notificationJSON {
	t.Helper()
	var parsed struct {
		Notifications []notificationJSON `json:"notifications"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode notifications: %v (%s)", err, string(body))
	}
	return parsed.Notifications
}

func decodeUnread(t *testing.T, body []byte) int {
	t.Helper()
	var parsed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode unread: %v (%s)", err, string(body))
	}
	return parsed.Count
}

func unreadCount(t *testing.T, s *Server, cookie *http.Cookie) int {
	t.Helper()
	w := apiDo(t, s, http.MethodGet, "/api/notifications/unread-count", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("unread count: %d %s", w.Code, w.Body.String())
	}
	return decodeUnread(t, w.Body.Bytes())
}

func TestAssignmentAndMentionNotifications(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Fix the roof","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("member unread after assignment = %d", got)
	}
	if got := unreadCount(t, s, managerCookie); got != 0 {
		t.Fatalf("manager unread after assignment = %d", got)
	}

	// Self assignment does not notify.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), memberCookie,
		fmt.Sprintf(`{"type":"action","title":"Mine","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("self assigned item: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("member unread after self assignment = %d", got)
	}

	// Adding an assignee notifies once; repeating the same list does not.
	assignBoth := fmt.Sprintf(`{"assignee_ids":[%d,%d]}`, member.ID, manager.ID)
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", task.ID), adminCookie, assignBoth); w.Code != http.StatusOK {
		t.Fatalf("add manager: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, managerCookie); got != 1 {
		t.Fatalf("manager unread after assignment = %d", got)
	}
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", task.ID), adminCookie, assignBoth); w.Code != http.StatusOK {
		t.Fatalf("repeat assignees: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, managerCookie); got != 1 {
		t.Fatalf("manager unread after repeated assignment = %d", got)
	}
	// The member already had an unread assignment, so no duplicate either.
	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("member unread after repeated assignment = %d", got)
	}

	// Mentioning notifies each new target; self mention is ignored.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), managerCookie,
		fmt.Sprintf(`{"body":"Please review this","mentions":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("mention member: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, memberCookie); got != 2 {
		t.Fatalf("member unread after mention = %d", got)
	}
	if got := unreadCount(t, s, managerCookie); got != 1 {
		t.Fatalf("manager unread after self mention = %d", got)
	}

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), memberCookie,
		fmt.Sprintf(`{"body":"Looping you in","mentions":[%d,%d]}`, member.ID, manager.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("mention manager: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, managerCookie); got != 2 {
		t.Fatalf("manager unread after mention = %d", got)
	}

	// Editing a comment notifies only newly added mentions.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), memberCookie,
		fmt.Sprintf(`{"body":"Admin please check","mentions":[%d]}`, admin.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("mention admin: %d %s", w.Code, w.Body.String())
	}
	adminComment := decodeComment(t, w.Body.Bytes())
	if got := unreadCount(t, s, adminCookie); got != 1 {
		t.Fatalf("admin unread after mention = %d", got)
	}
	// Re-saving the same mentions creates nothing new.
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/comments/%d", adminComment.ID), memberCookie,
		fmt.Sprintf(`{"body":"Admin please check now","mentions":[%d]}`, admin.ID)); w.Code != http.StatusOK {
		t.Fatalf("edit mention: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, adminCookie); got != 1 {
		t.Fatalf("admin unread after same mention = %d", got)
	}
	// Adding a new mention on edit notifies the new person only.
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/comments/%d", adminComment.ID), memberCookie,
		fmt.Sprintf(`{"body":"Both please check","mentions":[%d,%d]}`, admin.ID, manager.ID)); w.Code != http.StatusOK {
		t.Fatalf("edit add mention: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, managerCookie); got != 3 {
		t.Fatalf("manager unread after edit mention = %d", got)
	}
	if got := unreadCount(t, s, adminCookie); got != 1 {
		t.Fatalf("admin unread repeated on edit = %d", got)
	}

	// Inbox contents and destination.
	w = apiDo(t, s, http.MethodGet, "/api/notifications", memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	list := decodeNotifications(t, w.Body.Bytes())
	if len(list) != 2 {
		t.Fatalf("member notifications = %+v", list)
	}
	if list[0].Kind != "mention" || list[0].ItemTitle != "Fix the roof" || list[0].Link != fmt.Sprintf("/projects/%d/items/%d", project.ID, task.ID) {
		t.Fatalf("mention row = %+v", list[0])
	}
	if list[1].Kind != "assigned" || list[1].Read {
		t.Fatalf("assigned row = %+v", list[1])
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications?unread=1", memberCookie, "")
	if unread := decodeNotifications(t, w.Body.Bytes()); len(unread) != 2 {
		t.Fatalf("unread list = %+v", unread)
	}

	// Reading: own rows only, idempotent, then read-all.
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", list[1].ID), memberCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", list[1].ID), memberCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("second read: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", list[1].ID), managerCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign read: expected 404, got %d", w.Code)
	}
	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("unread after read = %d", got)
	}
	if w = apiDo(t, s, http.MethodPost, "/api/notifications/read-all", memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("read all: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, memberCookie); got != 0 {
		t.Fatalf("unread after read all = %d", got)
	}
	// Reading is per user: the manager still has unread rows.
	if got := unreadCount(t, s, managerCookie); got != 3 {
		t.Fatalf("manager unread after member read all = %d", got)
	}
}

func TestNotificationAccessIsRevalidated(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")

	private := createPrivateProject(t, s, managerCookie)
	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie,
		fmt.Sprintf(`{"user_id":%d}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), managerCookie,
		fmt.Sprintf(`{"type":"action","title":"Private task","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())
	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("unread while granted = %d", got)
	}

	// Removing the grant hides the old notification without deleting it.
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/members/%d", private.ID, member.ID), managerCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications", memberCookie, "")
	if list := decodeNotifications(t, w.Body.Bytes()); len(list) != 0 {
		t.Fatalf("notifications after revocation = %+v", list)
	}
	if got := unreadCount(t, s, memberCookie); got != 0 {
		t.Fatalf("unread after revocation = %d", got)
	}

	// Granting access again reveals the same row.
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", private.ID), managerCookie,
		fmt.Sprintf(`{"user_id":%d}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("re-add member: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, memberCookie); got != 1 {
		t.Fatalf("unread after re-grant = %d", got)
	}

	// Archiving the item hides it, matching the item API.
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/items/%d", task.ID), managerCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("archive item: %d %s", w.Code, w.Body.String())
	}
	if got := unreadCount(t, s, memberCookie); got != 0 {
		t.Fatalf("unread after item archive = %d", got)
	}
}

func TestEmailFailureKeepsInAppNotification(t *testing.T) {
	attempts := 0
	s := testServerWithSender(t, email.Config{Host: "smtp.example.test", Port: 25, From: "work@example.test"},
		func(cfg email.Config, to, subject, body string) error {
			attempts++
			return fmt.Errorf("smtp unavailable")
		})
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	if err := s.auth.SetEmailNotifications(member.ID, 1); err != nil {
		t.Fatal(err)
	}
	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Retry later","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	if attempts != 1 {
		t.Fatalf("email attempts = %d", attempts)
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications", memberCookie, "")
	list := decodeNotifications(t, w.Body.Bytes())
	if len(list) != 1 || list[0].Kind != "assigned" {
		t.Fatalf("in-app notification after SMTP error = %+v", list)
	}
	if list[0].EmailedAt != "" {
		t.Fatalf("emailed_at set after failure: %+v", list[0])
	}
}

func TestNotificationEmailPreferenceAndDedupe(t *testing.T) {
	recorder := &mailRecorder{}
	cfg := email.Config{Host: "smtp.example.test", Port: 25, From: "work@example.test"}
	s := testServerWithMail(t, cfg, recorder)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	if err := s.auth.UpdateProfile(member.ID, "Member", "it", 1); err != nil {
		t.Fatal(err)
	}

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Scarico","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	mails := recorder.all()
	if len(mails) != 1 {
		t.Fatalf("mails = %+v", mails)
	}
	if mails[0].To != member.Email || !strings.Contains(mails[0].Subject, "Assegnata") || !strings.Contains(mails[0].Body, "Scarico") {
		t.Fatalf("assignment mail = %+v", mails[0])
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications", memberCookie, "")
	rows := decodeNotifications(t, w.Body.Bytes())
	if len(rows) != 1 || rows[0].EmailedAt == "" {
		t.Fatalf("emailed_at not recorded: %+v", rows)
	}

	// Repeating the same assignment is suppressed.
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", task.ID), adminCookie,
		fmt.Sprintf(`{"assignee_ids":[%d]}`, member.ID)); w.Code != http.StatusOK {
		t.Fatalf("repeat assignment: %d %s", w.Code, w.Body.String())
	}
	if len(recorder.all()) != 1 {
		t.Fatalf("duplicate assignment sent mail: %+v", recorder.all())
	}

	// Read the notification, then reassign: a fresh notification and mail.
	w = apiDo(t, s, http.MethodGet, "/api/notifications", memberCookie, "")
	list := decodeNotifications(t, w.Body.Bytes())
	if len(list) != 1 {
		t.Fatalf("notifications = %+v", list)
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", list[0].ID), memberCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("read: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", task.ID), adminCookie, `{"assignee_ids":[]}`); w.Code != http.StatusOK {
		t.Fatalf("unassign: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/items/%d", task.ID), adminCookie,
		fmt.Sprintf(`{"assignee_ids":[%d]}`, member.ID)); w.Code != http.StatusOK {
		t.Fatalf("reassign: %d %s", w.Code, w.Body.String())
	}
	if len(recorder.all()) != 2 {
		t.Fatalf("reassignment mails = %+v", recorder.all())
	}

	// Preference off: the in-app mention is still created, no email.
	if err := s.auth.SetEmailNotifications(member.ID, 0); err != nil {
		t.Fatal(err)
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), adminCookie,
		fmt.Sprintf(`{"body":"Guarda qui","mentions":[%d]}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("mention: %d %s", w.Code, w.Body.String())
	}
	if len(recorder.all()) != 2 {
		t.Fatalf("email sent with preference off: %+v", recorder.all())
	}
	if got := unreadCount(t, s, memberCookie); got != 2 {
		t.Fatalf("in-app mention missing: unread=%d", got)
	}
}

func TestDueDigestSchedulerAndEmail(t *testing.T) {
	recorder := &mailRecorder{}
	cfg := email.Config{Host: "smtp.example.test", Port: 25, From: "work@example.test"}
	s := testServerWithMail(t, cfg, recorder)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	if err := s.auth.UpdateProfile(member.ID, "Member", "en", 1); err != nil {
		t.Fatal(err)
	}
	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	project := decodeProject(t, w)

	now := time.Date(2026, 11, 10, 23, 30, 0, 0, time.UTC)
	create := func(title, due string) int64 {
		t.Helper()
		w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
			fmt.Sprintf(`{"type":"action","title":%q,"due_on":%q,"assignee_ids":[%d]}`, title, due, member.ID))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, w.Code, w.Body.String())
		}
		return decodeItemResponse(t, w.Body.Bytes()).ID
	}
	create("Today", "2026-11-10")
	create("Overdue", "2026-11-01")
	done := create("Done but late", "2026-11-02")
	archived := create("Archived", "2026-11-03")
	w = apiDo(t, s, http.MethodGet, "/api/workflow", adminCookie, "")
	states := decodeWorkflow(t, w.Body.Bytes()).States
	var doneID int64
	for _, st := range states {
		if st.Name == "Done" {
			doneID = st.ID
		}
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/items/%d", archived), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	if err := s.itemsStore.ChangeState(done, doneID, admin.ID); err != nil {
		t.Fatal(err)
	}

	// The assignment mails are not part of this check.
	baseline := len(recorder.all())
	if err := s.RunDueDigest(now); err != nil {
		t.Fatal(err)
	}
	mails := recorder.all()[baseline:]
	if len(mails) != 1 || !strings.Contains(mails[0].Subject, "Deadlines today: 2") {
		t.Fatalf("due mails = %+v", mails)
	}
	// Running again the same day changes nothing.
	if err := s.RunDueDigest(now.Add(20 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.all()) != baseline+1 {
		t.Fatalf("same-day rerun sent mail: %+v", recorder.all()[baseline:])
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications?unread=1", memberCookie, "")
	list := decodeNotifications(t, w.Body.Bytes())
	due := 0
	for _, n := range list {
		if n.Kind == "due" {
			due++
			if n.ItemID != nil || n.Link != "/calendar" {
				t.Fatalf("due row = %+v", n)
			}
		}
	}
	if due != 1 {
		t.Fatalf("due notifications = %+v", list)
	}

	// Just after midnight UTC a new summary is due. Email is off now, so the
	// in-app row appears without another message.
	if err := s.auth.SetEmailNotifications(member.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RunDueDigest(time.Date(2026, 11, 11, 0, 10, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(recorder.all()) != baseline+1 {
		t.Fatalf("email sent with preference off: %+v", recorder.all()[baseline:])
	}
	w = apiDo(t, s, http.MethodGet, "/api/notifications?unread=1", memberCookie, "")
	due = 0
	for _, n := range decodeNotifications(t, w.Body.Bytes()) {
		if n.Kind == "due" {
			due++
		}
	}
	if due != 2 {
		t.Fatalf("due notifications after two days = %d", due)
	}
}

func TestSMTPAdminSettings(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.ini")
	if err := os.WriteFile(path, []byte("# keep me\naddr=:9000\nsmtp_pass=old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.SetConfigPath(path)

	if w := apiDo(t, s, http.MethodGet, "/api/admin/smtp", memberCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member smtp read: expected 403, got %d", w.Code)
	}
	w := apiDo(t, s, http.MethodGet, "/api/admin/smtp", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("smtp read: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"password":`) {
		t.Fatalf("password field leaked: %s", w.Body.String())
	}
	var view smtpView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Configured || view.HasPassword {
		t.Fatalf("initial smtp view = %+v", view)
	}

	body := `{"host":"smtp.example.test","port":587,"user":"mailer","password":"new-secret","from":"work@example.test","tls":true}`
	if w = apiDo(t, s, http.MethodPut, "/api/admin/smtp", memberCookie, body); w.Code != http.StatusForbidden {
		t.Fatalf("member smtp update: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPut, "/api/admin/smtp", adminCookie, body)
	if w.Code != http.StatusOK {
		t.Fatalf("smtp update: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "new-secret") {
		t.Fatalf("password returned: %s", w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Configured || !view.HasPassword || view.Host != "smtp.example.test" || view.Port != 587 || !view.TLS {
		t.Fatalf("smtp view = %+v", view)
	}
	cfg, _ := s.mailConfig()
	if cfg.Pass != "new-secret" || !cfg.Configured() || !s.mailConfigured() {
		t.Fatalf("live config = %+v", cfg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# keep me", "addr=:9000", "smtp_host=smtp.example.test", "smtp_port=587", "smtp_pass_enc=v1:", "smtp_from=work@example.test", "smtp_tls=true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
	// The ini file is the one place the SMTP password used to leak: it must
	// never hold the clear value.
	if strings.Contains(text, "new-secret") {
		t.Fatalf("config.ini still holds the clear-text SMTP password:\n%s", text)
	}
	if strings.Contains(text, "smtp_pass=") {
		t.Fatalf("config.ini still has a clear-text smtp_pass key:\n%s", text)
	}

	// An empty password keeps the stored one and never returns it.
	w = apiDo(t, s, http.MethodPut, "/api/admin/smtp", adminCookie,
		`{"host":"smtp2.example.test","port":2525,"user":"mailer","password":"","from":"work@example.test","tls":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("smtp update keep password: %d %s", w.Code, w.Body.String())
	}
	if cfg, _ = s.mailConfig(); cfg.Pass != "new-secret" || cfg.Host != "smtp2.example.test" || cfg.TLS {
		t.Fatalf("password not kept: %+v", cfg)
	}
	if strings.Contains(w.Body.String(), "new-secret") {
		t.Fatalf("password returned on keep: %s", w.Body.String())
	}

	// Validation.
	for _, bad := range []string{
		`{"host":"smtp.example.test","port":0,"from":"work@example.test"}`,
		`{"host":"smtp.example.test","port":70000,"from":"work@example.test"}`,
		`{"host":"","port":25,"from":"work@example.test"}`,
		`{"host":"smtp.example.test","port":25,"from":""}`,
		`{"host":"smtp.example.test","port":25,"from":"not-an-address"}`,
	} {
		if w = apiDo(t, s, http.MethodPut, "/api/admin/smtp", adminCookie, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("bad smtp %s: expected 400, got %d: %s", bad, w.Code, w.Body.String())
		}
	}

	// Test email uses the current settings and goes to the admin only.
	recorder := &mailRecorder{}
	s.emailMu.Lock()
	s.emailSender = recorder.send
	s.emailMu.Unlock()
	if w = apiDo(t, s, http.MethodPost, "/api/admin/smtp/test", adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("test email: %d %s", w.Code, w.Body.String())
	}
	mails := recorder.all()
	if len(mails) != 1 || mails[0].To != admin.Email || mails[0].Subject != "SMTP test" {
		t.Fatalf("test mails = %+v", mails)
	}

	// Disabling SMTP removes it from the file but keeps an unrelated line.
	w = apiDo(t, s, http.MethodPut, "/api/admin/smtp", adminCookie, `{"host":"","port":0,"from":"","tls":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("disable smtp: %d %s", w.Code, w.Body.String())
	}
	if cfg, _ = s.mailConfig(); cfg.Configured() || s.mailConfigured() {
		t.Fatalf("smtp still configured: %+v", cfg)
	}
	if w = apiDo(t, s, http.MethodPost, "/api/admin/smtp/test", adminCookie, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("test without smtp: expected 400, got %d", w.Code)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "# keep me") || !strings.Contains(string(data), "addr=:9000") {
		t.Fatalf("config lost unrelated content:\n%s", string(data))
	}
}
