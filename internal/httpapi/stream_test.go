package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jiralike/internal/items"
	"jiralike/internal/notifications"
)

type streamFrame struct {
	event string
	data  string
}

// streamClient reads SSE frames in one background goroutine, so a test that
// asserts the absence of a frame never leaves a competing reader behind.
type streamClient struct {
	resp   *http.Response
	frames chan streamFrame
	done   chan struct{}
}

func openStream(t *testing.T, baseURL string, cookie *http.Cookie) *streamClient {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/notifications/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("open stream status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		response.Body.Close()
		t.Fatalf("content type = %q", contentType)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
		response.Body.Close()
		t.Fatalf("stream headers = %v", response.Header)
	}

	client := &streamClient{resp: response, frames: make(chan streamFrame, 16), done: make(chan struct{})}
	go func() {
		defer close(client.frames)
		reader := bufio.NewReader(response.Body)
		frame := streamFrame{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if frame.event != "" {
					select {
					case client.frames <- frame:
					case <-client.done:
						return
					}
				}
				frame = streamFrame{}
				continue
			}
			if rest, ok := strings.CutPrefix(line, "event: "); ok {
				frame.event = rest
			} else if rest, ok := strings.CutPrefix(line, "data: "); ok {
				frame.data = rest
			}
		}
	}()
	t.Cleanup(func() {
		close(client.done)
		response.Body.Close()
	})
	return client
}

func (c *streamClient) next(t *testing.T) streamFrame {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		if !ok {
			t.Fatal("stream closed before the expected frame")
		}
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a stream frame")
		return streamFrame{}
	}
}

func (c *streamClient) expectNoFrame(t *testing.T) {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		if ok {
			t.Fatalf("unexpected stream frame %+v", frame)
		}
		t.Fatal("stream closed unexpectedly")
	case <-time.After(300 * time.Millisecond):
	}
}

func (c *streamClient) expectClosed(t *testing.T) {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		if ok {
			// Drain any frame written just before the hub closed the stream.
			t.Logf("frame before close: %+v", frame)
			c.expectClosed(t)
			return
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream was not closed")
	}
}

func streamServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := testServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func decodeUnreadFrame(t *testing.T, frame streamFrame) int {
	t.Helper()
	if frame.event != "ready" && frame.event != "unread" {
		t.Fatalf("expected unread frame, got %+v", frame)
	}
	var payload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatalf("decode frame: %v (%s)", err, frame.data)
	}
	return payload.Count
}

func TestNotificationsStreamRequiresSessionCookie(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(baseURL + "/api/notifications/stream")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream = %d", response.StatusCode)
	}

	token, err := s.auth.CreateToken(admin.ID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/notifications/stream", nil)
	request.Header.Set("Authorization", "Bearer "+token.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token stream = %d", response.StatusCode)
	}
}

func TestNotificationsStreamDeliversMentionAndRead(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie, `{"type":"action","title":"Review"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	stream := openStream(t, baseURL, memberCookie)
	if count := decodeUnreadFrame(t, stream.next(t)); count != 0 {
		t.Fatalf("initial count = %d", count)
	}

	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), adminCookie,
		fmt.Sprintf(`{"body":"Please review","mentions":[%d]}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}

	frame := stream.next(t)
	if frame.event != "notification" {
		t.Fatalf("expected notification frame, got %+v", frame)
	}
	var payload struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		ItemID    *int64 `json:"item_id"`
		ProjectID *int64 `json:"project_id"`
		Link      string `json:"link"`
		Count     int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatalf("decode notification: %v (%s)", err, frame.data)
	}
	if payload.Kind != "mention" || payload.Count != 1 || payload.ID == 0 {
		t.Fatalf("notification payload = %+v", payload)
	}
	if payload.ItemID == nil || *payload.ItemID != task.ID || payload.ProjectID == nil || *payload.ProjectID != project.ID {
		t.Fatalf("notification ids = %+v", payload)
	}
	if want := fmt.Sprintf("/projects/%d/items/%d", project.ID, task.ID); payload.Link != want {
		t.Fatalf("link = %q, want %q", payload.Link, want)
	}

	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/notifications/%d/read", payload.ID), memberCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("mark read: %d %s", w.Code, w.Body.String())
	}
	if count := decodeUnreadFrame(t, stream.next(t)); count != 0 {
		t.Fatalf("count after read = %d", count)
	}

	if w = apiDo(t, s, http.MethodPost, "/api/notifications/read-all", memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("mark all: %d %s", w.Code, w.Body.String())
	}
	if count := decodeUnreadFrame(t, stream.next(t)); count != 0 {
		t.Fatalf("count after read-all = %d", count)
	}
}

func TestNotificationsStreamLimitsPerUser(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	for i := 0; i < 5; i++ {
		openStream(t, baseURL, cookie)
	}
	if users, total := s.hub.Counters(); users != 1 || total != 5 {
		t.Fatalf("hub counters = %d/%d", users, total)
	}

	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/notifications/stream", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sixth stream = %d", response.StatusCode)
	}
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestNotificationsStreamClosesOnServerClose(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	stream := openStream(t, baseURL, cookie)
	stream.next(t)

	s.Close()
	stream.expectClosed(t)

	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/notifications/stream", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream after close = %d", response.StatusCode)
	}
}

func TestNotificationsStreamDeliversDueDigest(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Overdue","due_on":"2026-11-10","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}

	stream := openStream(t, baseURL, memberCookie)
	if count := decodeUnreadFrame(t, stream.next(t)); count != 1 {
		t.Fatalf("initial count = %d", count)
	}
	if err := s.RunDueDigest(time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	frame := stream.next(t)
	if frame.event != "notification" {
		t.Fatalf("expected notification frame, got %+v", frame)
	}
	var payload struct {
		Kind   string `json:"kind"`
		ItemID *int64 `json:"item_id"`
		Link   string `json:"link"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Kind != "due" || payload.ItemID != nil || payload.Link != "/calendar" || payload.Count != 2 {
		t.Fatalf("due payload = %+v", payload)
	}
}

func TestPublishSkipsNotificationHiddenByVisibility(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Private","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/members", project.ID), managerCookie,
		fmt.Sprintf(`{"user_id":%d}`, member.ID)); w.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}

	stream := openStream(t, baseURL, memberCookie)
	stream.next(t)

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), managerCookie,
		fmt.Sprintf(`{"type":"action","title":"Secret","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())
	frame := stream.next(t)
	if frame.event != "notification" {
		t.Fatalf("expected notification frame, got %+v", frame)
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatal(err)
	}

	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/members/%d", project.ID, member.ID), managerCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	created := time.Now().UTC().Format(time.RFC3339)
	n := notifications.Notification{ID: payload.ID, Kind: "assigned", CreatedAt: created}
	itemID := task.ID
	n.ItemID = &itemID
	s.publishNotification(member.ID, n, &project.ID, itemLink(items.Item{ID: task.ID, ProjectID: project.ID}))

	stream.expectNoFrame(t)
}
