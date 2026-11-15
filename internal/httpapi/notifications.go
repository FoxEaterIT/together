package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/items"
	"jiralike/internal/notifications"
)

func (s *Server) notificationsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	query := r.URL.Query()
	limit := 0
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			writeError(w, 400, "invalid limit")
			return
		}
		limit = parsed
	}
	beforeID := int64(0)
	if value := strings.TrimSpace(query.Get("before_id")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 {
			writeError(w, 400, "invalid before_id")
			return
		}
		beforeID = parsed
	}

	list, err := s.notificationsStore.List(user.ID, user.Role, query.Get("unread") == "1", limit, beforeID)
	if err != nil {
		writeError(w, 500, "notification list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": list})
}

func (s *Server) notificationsUnreadCount(w http.ResponseWriter, r *http.Request, user auth.User) {
	count, err := s.notificationsStore.UnreadCount(user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "notification count failed")
		return
	}
	writeJSON(w, 200, map[string]any{"count": count})
}

func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid notification id")
		return
	}
	if err := s.notificationsStore.MarkRead(user.ID, id); err != nil {
		if errors.Is(err, notifications.ErrNotFound) {
			writeError(w, 404, "notification not found")
			return
		}
		writeError(w, 500, "notification update failed")
		return
	}
	s.publishUnread(user.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notificationsReadAll(w http.ResponseWriter, r *http.Request, user auth.User) {
	count, err := s.notificationsStore.MarkAllRead(user.ID)
	if err != nil {
		writeError(w, 500, "notification update failed")
		return
	}
	s.publishUnread(user.ID)
	writeJSON(w, 200, map[string]any{"marked": count})
}

// notifyAssigned creates the in-app notification first and then tries the
// email. Self-assignment never notifies.
func (s *Server) notifyAssigned(it items.Item, assigneeID int64, actor auth.User) {
	if assigneeID == actor.ID {
		return
	}
	n, created, err := s.notificationsStore.CreateAssigned(assigneeID, it.ID)
	if err != nil {
		log.Printf("assigned notification for item %d user %d failed: %v", it.ID, assigneeID, err)
		return
	}
	if !created {
		return
	}
	s.publishNotification(assigneeID, n, &it.ProjectID, itemLink(it))
	s.emailNotification(assigneeID, n, it, "", actor)
}

func (s *Server) notifyNewAssignees(it items.Item, ids []int64, actor auth.User) {
	for _, id := range ids {
		s.notifyAssigned(it, id, actor)
	}
}

// notifyMention notifies each newly mentioned user. The store also deduplicates
// per comment, so an edit that keeps the same mention sends nothing new.
func (s *Server) notifyMention(it items.Item, comment items.Comment, mentionedID int64, actor auth.User) {
	if mentionedID == actor.ID {
		return
	}
	n, created, err := s.notificationsStore.CreateMention(mentionedID, it.ID, comment.ID)
	if err != nil {
		log.Printf("mention notification for comment %d user %d failed: %v", comment.ID, mentionedID, err)
		return
	}
	if !created {
		return
	}
	s.publishNotification(mentionedID, n, &it.ProjectID, itemLink(it))
	s.emailNotification(mentionedID, n, it, comment.Body, actor)
}

func (s *Server) notifyNewMentions(it items.Item, comment items.Comment, ids []int64, actor auth.User) {
	for _, id := range ids {
		s.notifyMention(it, comment, id, actor)
	}
}

// emailNotification sends only the three allowed events, only when SMTP is
// configured and the recipient enabled email in the profile. emailed_at is
// written only after the mail server accepted the message.
func (s *Server) emailNotification(userID int64, n notifications.Notification, it items.Item, commentBody string, actor auth.User) {
	recipient, err := s.auth.GetUser(userID)
	if err != nil {
		log.Printf("notification email user %d lookup failed: %v", userID, err)
		return
	}
	if !s.canReceiveEmail(recipient) {
		return
	}
	if !s.mailConfigured() {
		return
	}
	subject, body := notificationEmail(recipient, n.Kind, it, commentBody, actor)
	if err := s.sendMail(recipient.Email, subject, body); err != nil {
		log.Printf("notification email to %s failed: %v", recipient.Email, err)
		return
	}
	if err := s.notificationsStore.MarkEmailed(n.ID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("mark notification %d emailed failed: %v", n.ID, err)
	}
}

func (s *Server) canReceiveEmail(user auth.User) bool {
	if !user.EmailNotifications || user.DisabledAt != "" {
		return false
	}
	if user.Role == "guest" && user.GuestExpiresOn != "" && user.GuestExpiresOn < todayUTC() {
		return false
	}
	return true
}

func notificationEmail(user auth.User, kind string, it items.Item, commentBody string, actor auth.User) (string, string) {
	link := fmt.Sprintf("/projects/%d/items/%d", it.ProjectID, it.ID)
	if user.Locale == "it" {
		switch kind {
		case "mention":
			return "Menzione in: " + it.Title, fmt.Sprintf(
				"Ciao %s,\n\n%s ti ha menzionato in \"%s\" (%s).\n\n%s\n\nApri: %s\n",
				user.Name, actor.Name, it.Title, it.ProjectName, excerptEmail(commentBody), link,
			)
		case "automation":
			return "Automazione: " + it.Title, fmt.Sprintf(
				"Ciao %s,\n\nUn'automazione ha aggiornato \"%s\" (%s).\n\nApri: %s\n",
				user.Name, it.Title, it.ProjectName, link,
			)
		default:
			return "Assegnata: " + it.Title, fmt.Sprintf(
				"Ciao %s,\n\n%s ti ha assegnato \"%s\" (%s).\n\nApri: %s\n",
				user.Name, actor.Name, it.Title, it.ProjectName, link,
			)
		}
	}
	if user.Locale == "es" {
		switch kind {
		case "mention":
			return "Mención en: " + it.Title, fmt.Sprintf(
				"Hola %s,\n\n%s te mencionó en \"%s\" (%s).\n\n%s\n\nAbrir: %s\n",
				user.Name, actor.Name, it.Title, it.ProjectName, excerptEmail(commentBody), link,
			)
		case "automation":
			return "Automatización: " + it.Title, fmt.Sprintf(
				"Hola %s,\n\nUna automatización actualizó \"%s\" (%s).\n\nAbrir: %s\n",
				user.Name, it.Title, it.ProjectName, link,
			)
		default:
			return "Asignada: " + it.Title, fmt.Sprintf(
				"Hola %s,\n\n%s te asignó \"%s\" (%s).\n\nAbrir: %s\n",
				user.Name, actor.Name, it.Title, it.ProjectName, link,
			)
		}
	}
	switch kind {
	case "mention":
		return "Mention in: " + it.Title, fmt.Sprintf(
			"Hello %s,\n\n%s mentioned you in \"%s\" (%s).\n\n%s\n\nOpen: %s\n",
			user.Name, actor.Name, it.Title, it.ProjectName, excerptEmail(commentBody), link,
		)
	case "automation":
		return "Automation: " + it.Title, fmt.Sprintf(
			"Hello %s,\n\nAn automation updated \"%s\" (%s).\n\nOpen: %s\n",
			user.Name, it.Title, it.ProjectName, link,
		)
	default:
		return "Assigned: " + it.Title, fmt.Sprintf(
			"Hello %s,\n\n%s assigned you \"%s\" (%s).\n\nOpen: %s\n",
			user.Name, actor.Name, it.Title, it.ProjectName, link,
		)
	}
}

func itemLink(it items.Item) string {
	return fmt.Sprintf("/projects/%d/items/%d", it.ProjectID, it.ID)
}

func excerptEmail(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	runes := []rune(body)
	if len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return body
}

// RunDueDigest creates at most one summary per user and UTC day and emails it
// when the profile allows it. It is safe to run repeatedly: existing rows are
// reused and already emailed rows are skipped.
func (s *Server) RunDueDigest(now time.Time) error {
	now = now.UTC()
	day := now.Format("2006-01-02")
	createdAt := now.Format(time.RFC3339)
	recipients, err := s.notificationsStore.DueRecipients(day)
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		n, created, err := s.notificationsStore.CreateDue(recipient.UserID, recipient.Count, createdAt)
		if err != nil {
			return err
		}
		if !created {
			existing, ok, err := s.notificationsStore.DayDue(recipient.UserID, day)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			n = existing
		} else {
			s.publishNotification(recipient.UserID, n, nil, "/calendar")
		}
		if n.EmailedAt != "" || !recipient.EmailNotifications || !s.mailConfigured() {
			continue
		}
		subject, body := dueEmail(recipient)
		if err := s.sendMail(recipient.Email, subject, body); err != nil {
			log.Printf("due digest email to %s failed: %v", recipient.Email, err)
			continue
		}
		if err := s.notificationsStore.MarkEmailed(n.ID, createdAt); err != nil {
			log.Printf("mark due notification %d emailed failed: %v", n.ID, err)
		}
	}
	return nil
}

func dueEmail(recipient notifications.DueRecipient) (string, string) {
	if recipient.Locale == "it" {
		return fmt.Sprintf("Scadenze di oggi: %d", recipient.Count), fmt.Sprintf(
			"Ciao %s,\n\nHai %d scadenze oggi o gia' passate.\n\nApri /calendar per rivederle.\n",
			recipient.Name, recipient.Count,
		)
	}
	return fmt.Sprintf("Deadlines today: %d", recipient.Count), fmt.Sprintf(
		"Hello %s,\n\nYou have %d deadlines today or overdue.\n\nOpen /calendar to review them.\n",
		recipient.Name, recipient.Count,
	)
}

// StartDueDigest runs the daily summary immediately and then hourly. The
// deduplication makes hourly checks harmless; the first check after midnight
// UTC creates the new day's summary. The returned stop function cancels the
// loop and waits for an in-flight run, so shutdown can close the database after
// it returns.
func (s *Server) StartDueDigest(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Hour
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.RunDueDigest(time.Now().UTC()); err != nil {
			log.Printf("due digest failed: %v", err)
		}
		if err := s.RunDueAutomations(time.Now().UTC()); err != nil {
			log.Printf("due automations failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.RunDueDigest(time.Now().UTC()); err != nil {
					log.Printf("due digest failed: %v", err)
				}
				if err := s.RunDueAutomations(time.Now().UTC()); err != nil {
					log.Printf("due automations failed: %v", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func todayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// differenceIDs returns the values in want that are not in have.
func differenceIDs(want, have []int64) []int64 {
	seen := make(map[int64]bool, len(have))
	for _, id := range have {
		seen[id] = true
	}
	out := make([]int64, 0, len(want))
	for _, id := range want {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}
