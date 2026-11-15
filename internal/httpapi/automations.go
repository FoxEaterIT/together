package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/automations"
	"jiralike/internal/items"
	"jiralike/internal/projects"
)

type automationActionBody struct {
	Kind       string  `json:"kind"`
	StateID    int64   `json:"state_id"`
	UserID     int64   `json:"user_id"`
	Body       string  `json:"body"`
	MentionIDs []int64 `json:"mention_ids"`
}

type automationRuleBody struct {
	Name           string                 `json:"name"`
	Enabled        *bool                  `json:"enabled"`
	TriggerKind    string                 `json:"trigger_kind"`
	TriggerStateID int64                  `json:"trigger_state_id"`
	ProjectIDs     []int64                `json:"project_ids"`
	Actions        []automationActionBody `json:"actions"`
}

func (s *Server) automationsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	all, err := s.automationsStore.List()
	if err != nil {
		writeError(w, 500, "automation list failed")
		return
	}
	out := []automations.Rule{}
	for _, rule := range all {
		ok, err := s.canManageAutomationRule(rule, user)
		if err != nil {
			writeError(w, 500, "automation access check failed")
			return
		}
		if ok {
			out = append(out, rule)
		}
	}
	writeJSON(w, 200, map[string]any{"automations": out})
}

func (s *Server) automationsGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid automation id")
		return
	}
	rule, ok := s.manageableAutomation(w, id, user)
	if !ok {
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) automationsCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body automationRuleBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if !s.ensureAutomationProjects(w, body.ProjectIDs, user) {
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	rule, err := s.automationsStore.Create(automationInput(body, enabled), user.ID)
	if err != nil {
		writeAutomationError(w, err, "automation create failed")
		return
	}
	writeJSON(w, 201, rule)
}

func (s *Server) automationsUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid automation id")
		return
	}
	current, ok := s.manageableAutomation(w, id, user)
	if !ok {
		return
	}
	var body automationRuleBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if !s.ensureAutomationProjects(w, body.ProjectIDs, user) {
		return
	}
	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	rule, err := s.automationsStore.Update(id, automationInput(body, enabled))
	if err != nil {
		writeAutomationError(w, err, "automation update failed")
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) automationsDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid automation id")
		return
	}
	if _, ok := s.manageableAutomation(w, id, user); !ok {
		return
	}
	if err := s.automationsStore.Delete(id); err != nil {
		if errors.Is(err, automations.ErrNotFound) {
			writeError(w, 404, "automation not found")
			return
		}
		writeError(w, 500, "automation delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func automationInput(body automationRuleBody, enabled bool) automations.RuleInput {
	in := automations.RuleInput{
		Name:           strings.TrimSpace(body.Name),
		Enabled:        enabled,
		TriggerKind:    strings.TrimSpace(body.TriggerKind),
		TriggerStateID: body.TriggerStateID,
		ProjectIDs:     body.ProjectIDs,
		Actions:        make([]automations.ActionInput, 0, len(body.Actions)),
	}
	for _, action := range body.Actions {
		in.Actions = append(in.Actions, automations.ActionInput{
			Kind:       strings.TrimSpace(action.Kind),
			StateID:    action.StateID,
			UserID:     action.UserID,
			Body:       action.Body,
			MentionIDs: action.MentionIDs,
		})
	}
	return in
}

// manageableAutomation returns a rule only if the caller may edit it. A manager
// sees rules bound to projects they manage; anyone else gets 404 so an id
// cannot be probed.
func (s *Server) manageableAutomation(w http.ResponseWriter, id int64, user auth.User) (automations.Rule, bool) {
	rule, err := s.automationsStore.Get(id)
	if errors.Is(err, automations.ErrNotFound) {
		writeError(w, 404, "automation not found")
		return automations.Rule{}, false
	}
	if err != nil {
		writeError(w, 500, "automation read failed")
		return automations.Rule{}, false
	}
	ok, err := s.canManageAutomationRule(rule, user)
	if err != nil {
		writeError(w, 500, "automation access check failed")
		return automations.Rule{}, false
	}
	if !ok {
		writeError(w, 404, "automation not found")
		return automations.Rule{}, false
	}
	return rule, true
}

func (s *Server) canManageAutomationRule(rule automations.Rule, user auth.User) (bool, error) {
	if user.Role == "admin" {
		return true, nil
	}
	if user.Role != "manager" || len(rule.ProjectIDs) == 0 {
		return false, nil
	}
	for _, id := range rule.ProjectIDs {
		p, err := s.projectsStore.Get(id)
		if errors.Is(err, projects.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		ok, err := s.projectsStore.CanManage(p, user.ID, user.Role)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// ensureAutomationProjects enforces the manager scope before the store sees the
// input. A manager must name at least one project and every project must be one
// they manage; a global rule belongs to the admin.
func (s *Server) ensureAutomationProjects(w http.ResponseWriter, ids []int64, user auth.User) bool {
	if user.Role == "admin" {
		return true
	}
	if user.Role != "manager" || len(ids) == 0 {
		writeError(w, 403, "managers must choose at least one managed project")
		return false
	}
	for _, id := range ids {
		p, err := s.projectsStore.Get(id)
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, 404, "project not found")
			return false
		}
		if err != nil {
			writeError(w, 500, "project access check failed")
			return false
		}
		ok, err := s.projectsStore.CanManage(p, user.ID, user.Role)
		if err != nil {
			writeError(w, 500, "project access check failed")
			return false
		}
		if !ok {
			writeError(w, 404, "project not found")
			return false
		}
	}
	return true
}

func writeAutomationError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, automations.ErrNotFound):
		writeError(w, 404, "automation not found")
	case errors.Is(err, automations.ErrNameRequired),
		errors.Is(err, automations.ErrNameTooLong),
		errors.Is(err, automations.ErrInvalidTrigger),
		errors.Is(err, automations.ErrTriggerState),
		errors.Is(err, automations.ErrTriggerValue),
		errors.Is(err, automations.ErrActionsRequired),
		errors.Is(err, automations.ErrInvalidAction),
		errors.Is(err, automations.ErrActionValue),
		errors.Is(err, automations.ErrCommentEmpty),
		errors.Is(err, automations.ErrCommentTooLong),
		errors.Is(err, automations.ErrProjectNotFound),
		errors.Is(err, automations.ErrProjectArchived),
		errors.Is(err, automations.ErrStateNotFound),
		errors.Is(err, automations.ErrUserNotFound),
		errors.Is(err, automations.ErrTooManyActions),
		errors.Is(err, automations.ErrTooManyProjects),
		errors.Is(err, automations.ErrTooManyMentions):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 500, fallback)
	}
}

// runAutomations applies every enabled rule matching the event. Each rule gets
// its own transaction; a broken rule is logged and does not block the user
// action that triggered it.
func (s *Server) runAutomations(event automations.Event) {
	rules, err := s.automationsStore.Matching(event)
	if err != nil {
		log.Printf("automation matching for item %d failed: %v", event.ItemID, err)
		return
	}
	for _, rule := range rules {
		s.applyAutomation(rule, event)
	}
}

func (s *Server) applyAutomation(rule automations.Rule, event automations.Event) {
	it, err := s.itemsStore.Get(event.ItemID)
	if errors.Is(err, items.ErrNotFound) || (err == nil && it.ArchivedAt != "") {
		return
	}
	if err != nil {
		log.Printf("automation %d item %d read failed: %v", rule.ID, event.ItemID, err)
		return
	}
	project, err := s.projectsStore.Get(it.ProjectID)
	if err != nil {
		log.Printf("automation %d project %d read failed: %v", rule.ID, it.ProjectID, err)
		return
	}
	if project.ArchivedAt != "" {
		return
	}

	actor, err := s.auth.GetUser(rule.CreatedBy)
	if err != nil {
		actor = auth.User{ID: rule.CreatedBy, Name: "Automation"}
	}

	actions := make([]items.Action, 0, len(rule.Actions))
	notify := false
	for _, action := range rule.Actions {
		switch action.Kind {
		case "set_state":
			actions = append(actions, items.Action{Kind: "set_state", StateID: action.StateID})
		case "assign_user":
			ok, err := s.automationUserCanRead(project, action.UserID)
			if err != nil {
				log.Printf("automation %d assignee %d check failed: %v", rule.ID, action.UserID, err)
				continue
			}
			if !ok {
				log.Printf("automation %d skipped assignee %d: no access to project %d", rule.ID, action.UserID, project.ID)
				continue
			}
			actions = append(actions, items.Action{Kind: "assign_user", UserID: action.UserID})
		case "add_comment":
			mentions := make([]int64, 0, len(action.MentionIDs))
			for _, userID := range action.MentionIDs {
				ok, err := s.automationUserCanRead(project, userID)
				if err != nil {
					log.Printf("automation %d mention %d check failed: %v", rule.ID, userID, err)
					continue
				}
				if !ok {
					log.Printf("automation %d skipped mention %d: no access to project %d", rule.ID, userID, project.ID)
					continue
				}
				mentions = append(mentions, userID)
			}
			actions = append(actions, items.Action{Kind: "add_comment", Body: action.Body, MentionIDs: mentions})
		case "notify":
			notify = true
		}
	}

	var marker *items.RunMarker
	if rule.TriggerKind == "due_passed" {
		marker = &items.RunMarker{RuleID: rule.ID, Day: event.Day}
	}
	outcome, err := s.itemsStore.ApplyActions(it.ID, actor.ID, actions, marker)
	if errors.Is(err, items.ErrAlreadyRun) {
		return
	}
	if err != nil {
		log.Printf("automation %d on item %d failed: %v", rule.ID, it.ID, err)
		return
	}

	s.notifyNewAssignees(outcome.Item, outcome.NewAssigneeIDs, actor)
	for _, comment := range outcome.Comments {
		s.notifyNewMentions(outcome.Item, items.Comment{ID: comment.ID, ItemID: outcome.Item.ID, Body: comment.Body}, comment.MentionIDs, actor)
	}
	if notify {
		seen := map[int64]bool{}
		for _, assignee := range outcome.Item.Assignees {
			if seen[assignee.UserID] {
				continue
			}
			seen[assignee.UserID] = true
			s.notifyAutomation(outcome.Item, assignee.UserID, actor)
		}
	}
}

func (s *Server) automationUserCanRead(project projects.Project, userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	u, err := s.auth.GetUser(userID)
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if u.DisabledAt != "" {
		return false, nil
	}
	if u.Role == "guest" && u.GuestExpiresOn != "" && u.GuestExpiresOn < todayUTC() {
		return false, nil
	}
	return s.projectsStore.CanSee(project, u.ID, u.Role)
}

func (s *Server) notifyAutomation(it items.Item, userID int64, actor auth.User) {
	if userID <= 0 {
		return
	}
	n, created, err := s.notificationsStore.CreateAutomation(userID, it.ID)
	if err != nil {
		log.Printf("automation notification for item %d user %d failed: %v", it.ID, userID, err)
		return
	}
	if !created {
		return
	}
	s.publishNotification(userID, n, &it.ProjectID, itemLink(it))
	s.emailNotification(userID, n, it, "", actor)
}

// RunDueAutomations checks open items whose due date passed and runs the
// due_passed rules. The per-rule marker makes repeated runs and restarts safe.
func (s *Server) RunDueAutomations(now time.Time) error {
	day := now.UTC().Format("2006-01-02")
	dueItems, err := s.automationsStore.DueItems(day)
	if err != nil {
		return err
	}
	for _, due := range dueItems {
		s.runAutomations(automations.Event{
			Kind:      "due_passed",
			ProjectID: due.ProjectID,
			ItemID:    due.ID,
			Day:       day,
		})
	}
	return nil
}
