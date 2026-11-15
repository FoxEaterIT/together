package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/notifications"
	"jiralike/internal/realtime"
)

const streamHeartbeat = 25 * time.Second

// streamWriteDeadline bounds a single write to a client that stopped reading.
// Without it a stalled TCP connection could keep the handler and its goroutine
// alive past the shutdown drain.
const streamWriteDeadline = 10 * time.Second

// notificationsStream keeps one Server-Sent Events connection per open tab.
// It authenticates with the session cookie only: EventSource cannot send
// headers, and personal API tokens deliberately stay a single-factor
// credential that cannot open a long-lived stream.
func (s *Server) notificationsStream(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	user, err := s.auth.UserFromSession(cookie.Value)
	if errors.Is(err, auth.ErrGuestExpired) {
		writeError(w, http.StatusForbidden, "guest access ended")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}

	sub, err := s.hub.Subscribe(user.ID)
	if errors.Is(err, realtime.ErrTooManyStreams) {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too many notification streams")
		return
	}
	if errors.Is(err, realtime.ErrClosed) {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, "notification stream unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notification stream failed")
		return
	}
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)

	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return
	}
	count, err := s.notificationsStore.UnreadCount(user.ID, user.Role)
	if err != nil {
		count = 0
	}
	ready, err := json.Marshal(map[string]int{"count": count})
	if err != nil {
		return
	}
	_ = controller.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
	if err := writeSSE(w, "ready", ready); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			// Slow consumer or process shutdown. Ask the client to refetch; it
			// reconnects on its own and the full state comes from the API.
			_ = controller.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
			if err := writeSSE(w, "resync", []byte("{}")); err == nil {
				_ = controller.Flush()
			}
			return
		case ev := <-sub.Events():
			_ = controller.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
			if err := writeSSE(w, ev.Name, ev.Data); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case <-ticker.C:
			_ = controller.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func writeSSE(w io.Writer, name string, data []byte) error {
	if _, err := io.WriteString(w, "event: "+name+"\ndata: "); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

// publishUnread refreshes the bell after a read or read-all. Every frame carries
// the whole count, so a dropped frame cannot leave a stale badge.
func (s *Server) publishUnread(userID int64) {
	user, err := s.auth.GetUser(userID)
	if err != nil {
		return
	}
	count, err := s.notificationsStore.UnreadCount(userID, user.Role)
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]int{"count": count})
	if err != nil {
		return
	}
	s.hub.Publish(userID, realtime.Event{Name: "unread", Data: data})
}

// publishNotification pushes one newly committed notification. It checks
// visibility first: a row the recipient could not list is never pushed, so the
// stream cannot reveal a project the API would hide.
func (s *Server) publishNotification(userID int64, n notifications.Notification, projectID *int64, link string) {
	user, err := s.auth.GetUser(userID)
	if err != nil {
		return
	}
	visible, err := s.notificationsStore.Visible(userID, user.Role, n.ID)
	if err != nil || !visible {
		return
	}
	count, err := s.notificationsStore.UnreadCount(userID, user.Role)
	if err != nil {
		return
	}
	payload := struct {
		ID        int64  `json:"id"`
		Kind      string `json:"kind"`
		ItemID    *int64 `json:"item_id"`
		ProjectID *int64 `json:"project_id,omitempty"`
		Link      string `json:"link"`
		CreatedAt string `json:"created_at"`
		Count     int    `json:"count"`
	}{
		ID:        n.ID,
		Kind:      n.Kind,
		ItemID:    n.ItemID,
		ProjectID: projectID,
		Link:      link,
		CreatedAt: n.CreatedAt,
		Count:     count,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.hub.Publish(userID, realtime.Event{Name: "notification", Data: data})
}
