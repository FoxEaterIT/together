package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"
)

type automationActionJSON struct {
	Position   int     `json:"position"`
	Kind       string  `json:"kind"`
	Value      string  `json:"value"`
	StateID    int64   `json:"state_id"`
	UserID     int64   `json:"user_id"`
	Body       string  `json:"body"`
	MentionIDs []int64 `json:"mention_ids"`
}

type automationRuleJSON struct {
	ID               int64                  `json:"id"`
	Name             string                 `json:"name"`
	Enabled          bool                   `json:"enabled"`
	TriggerKind      string                 `json:"trigger_kind"`
	TriggerStateID   int64                  `json:"trigger_state_id"`
	TriggerStateName string                 `json:"trigger_state_name"`
	ProjectIDs       []int64                `json:"project_ids"`
	Actions          []automationActionJSON `json:"actions"`
	CreatedBy        int64                  `json:"created_by"`
}

func decodeAutomationRule(t *testing.T, body []byte) automationRuleJSON {
	t.Helper()
	var rule automationRuleJSON
	if err := json.Unmarshal(body, &rule); err != nil {
		t.Fatalf("decode automation: %v (%s)", err, string(body))
	}
	return rule
}

func decodeAutomationList(t *testing.T, body []byte) []automationRuleJSON {
	t.Helper()
	var parsed struct {
		Automations []automationRuleJSON `json:"automations"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode automations: %v (%s)", err, string(body))
	}
	return parsed.Automations
}

func decodeComments(t *testing.T, body []byte) []commentJSON {
	t.Helper()
	var parsed struct {
		Comments []commentJSON `json:"comments"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode comments: %v (%s)", err, string(body))
	}
	return parsed.Comments
}

func stateIDs(t *testing.T, s *Server, cookie *http.Cookie) map[string]int64 {
	t.Helper()
	w := apiDo(t, s, http.MethodGet, "/api/workflow", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("workflow: %d %s", w.Code, w.Body.String())
	}
	wf := decodeWorkflow(t, w.Body.Bytes())
	out := map[string]int64{}
	for _, state := range wf.States {
		out[state.Name] = state.ID
	}
	return out
}

func automationBody(name, trigger string, triggerState int64, projectIDs []int64, actions string) string {
	projects := "[]"
	if len(projectIDs) > 0 {
		projects = "["
		for i, id := range projectIDs {
			if i > 0 {
				projects += ","
			}
			projects += fmt.Sprintf("%d", id)
		}
		projects += "]"
	}
	return fmt.Sprintf(`{"name":%q,"trigger_kind":%q,"trigger_state_id":%d,"project_ids":%s,"actions":%s}`,
		name, trigger, triggerState, projects, actions)
}

func notificationKinds(t *testing.T, s *Server, cookie *http.Cookie) []string {
	t.Helper()
	w := apiDo(t, s, http.MethodGet, "/api/notifications", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("notifications: %d %s", w.Code, w.Body.String())
	}
	kinds := []string{}
	for _, n := range decodeNotifications(t, w.Body.Bytes()) {
		kinds = append(kinds, n.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

func TestAutomationsPermissionsAndManagerScope(t *testing.T) {
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

	w := apiDo(t, s, http.MethodPost, "/api/projects", managerCookie, `{"name":"Managed","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("manager project: %d %s", w.Code, w.Body.String())
	}
	managed := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Admin project","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin project: %d %s", w.Code, w.Body.String())
	}
	other := decodeProject(t, w)

	agents := `[{"kind":"notify"}]`
	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("Global reminder", "due_passed", 0, nil, agents))
	if w.Code != http.StatusCreated {
		t.Fatalf("admin global rule: %d %s", w.Code, w.Body.String())
	}
	global := decodeAutomationRule(t, w.Body.Bytes())

	if w = apiDo(t, s, http.MethodGet, "/api/automations", memberCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member list: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodGet, "/api/automations", managerCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("manager list: %d %s", w.Code, w.Body.String())
	} else if listed := decodeAutomationList(t, w.Body.Bytes()); len(listed) != 0 {
		t.Fatalf("manager must not see the global rule: %+v", listed)
	}
	if w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/automations/%d", global.ID), managerCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("manager get global: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/automations/%d", global.ID), managerCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("manager delete global: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodPost, "/api/automations", managerCookie,
		automationBody("No projects", "assigned", 0, nil, agents)); w.Code != http.StatusForbidden {
		t.Fatalf("manager global create: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPost, "/api/automations", managerCookie,
		automationBody("Foreign project", "assigned", 0, []int64{other.ID}, agents)); w.Code != http.StatusNotFound {
		t.Fatalf("manager foreign project: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, "/api/automations", managerCookie,
		automationBody("Managed rule", "assigned", 0, []int64{managed.ID}, agents))
	if w.Code != http.StatusCreated {
		t.Fatalf("manager own rule: %d %s", w.Code, w.Body.String())
	}
	managedRule := decodeAutomationRule(t, w.Body.Bytes())
	if managedRule.CreatedBy != manager.ID || len(managedRule.ProjectIDs) != 1 || managedRule.ProjectIDs[0] != managed.ID {
		t.Fatalf("managed rule = %+v", managedRule)
	}
	if w = apiDo(t, s, http.MethodGet, "/api/automations", managerCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("manager list: %d", w.Code)
	} else if listed := decodeAutomationList(t, w.Body.Bytes()); len(listed) != 1 || listed[0].ID != managedRule.ID {
		t.Fatalf("manager list = %+v", listed)
	}
	if w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/automations/%d", managedRule.ID), adminCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("admin get manager rule: %d", w.Code)
	}
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/automations/%d", managedRule.ID), managerCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("manager delete own rule: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/automations/%d", global.ID), adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("admin delete global rule: %d %s", w.Code, w.Body.String())
	}
}

func TestAutomationStateChangeActionsAndAntiLoop(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Alpha","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)
	states := stateIDs(t, s, adminCookie)
	todo, progress, done := states["To do"], states["In progress"], states["Done"]

	w = apiDo(t, s, http.MethodPost, "/api/workflow/transitions", adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Finish"}`, todo, done))
	if w.Code != http.StatusCreated {
		t.Fatalf("transition: %d %s", w.Code, w.Body.String())
	}
	finish := decodeWorkflowTransition(t, w.Body.Bytes())

	actions := fmt.Sprintf(
		`[{"kind":"set_state","state_id":%d},{"kind":"assign_user","user_id":%d},{"kind":"add_comment","body":"Please review","mention_ids":[%d]},{"kind":"notify"}]`,
		progress, member.ID, member.ID)
	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("On done", "state_changed", done, []int64{project.ID}, actions))
	if w.Code != http.StatusCreated {
		t.Fatalf("rule: %d %s", w.Code, w.Body.String())
	}
	rule := decodeAutomationRule(t, w.Body.Bytes())
	if rule.TriggerStateID != done || rule.TriggerStateName != "Done" {
		t.Fatalf("state trigger = %+v", rule)
	}
	if len(rule.Actions) != 4 || rule.Actions[2].Kind != "add_comment" || rule.Actions[2].Body != "Please review" {
		t.Fatalf("actions = %+v", rule.Actions)
	}
	if len(rule.Actions[2].MentionIDs) != 1 || rule.Actions[2].MentionIDs[0] != member.ID {
		t.Fatalf("mentions = %+v", rule.Actions[2].MentionIDs)
	}

	// A rule that would fire when the automation moves the item back to
	// In progress: it must not run, because automations never re-enter the
	// engine in the same round.
	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("Loop guard", "state_changed", progress, []int64{project.ID},
			`[{"kind":"add_comment","body":"should not run"}]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("loop guard rule: %d %s", w.Code, w.Body.String())
	}

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		`{"type":"action","title":"Ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, finish.ID), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("apply transition: %d %s", w.Code, w.Body.String())
	}
	after := decodeItemResponse(t, w.Body.Bytes())
	if after.StateID != progress {
		t.Fatalf("automation state = %+v", after)
	}
	if len(after.Assignees) != 1 || after.Assignees[0].UserID != member.ID {
		t.Fatalf("automation assignees = %+v", after.Assignees)
	}

	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", task.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("comments: %d %s", w.Code, w.Body.String())
	}
	comments := decodeComments(t, w.Body.Bytes())
	if len(comments) != 1 || comments[0].Body != "Please review" {
		t.Fatalf("automation comments = %+v", comments)
	}
	if comments[0].AuthorID != rule.CreatedBy {
		t.Fatalf("comment author = %d, want rule creator %d", comments[0].AuthorID, rule.CreatedBy)
	}
	if len(comments[0].Mentions) != 1 || comments[0].Mentions[0].UserID != member.ID {
		t.Fatalf("comment mentions = %+v", comments[0].Mentions)
	}

	kinds := notificationKinds(t, s, memberCookie)
	counts := map[string]int{}
	for _, kind := range kinds {
		counts[kind]++
	}
	if counts["assigned"] != 1 || counts["mention"] != 1 || counts["automation"] != 1 {
		t.Fatalf("notifications = %+v", kinds)
	}
}

func TestAutomationAssignedAndCommentTriggers(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Alpha","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)

	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("On assign", "assigned", 0, []int64{project.ID},
			`[{"kind":"add_comment","body":"Assignment noticed"}]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("assigned rule: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("On comment", "comment_added", 0, []int64{project.ID},
			fmt.Sprintf(`[{"kind":"assign_user","user_id":%d}]`, member.ID)))
	if w.Code != http.StatusCreated {
		t.Fatalf("comment rule: %d %s", w.Code, w.Body.String())
	}

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Task","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", task.ID), adminCookie, "")
	comments := decodeComments(t, w.Body.Bytes())
	if len(comments) != 1 || comments[0].Body != "Assignment noticed" {
		t.Fatalf("assigned comments = %+v", comments)
	}

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/comments", task.ID), adminCookie, `{"body":"Any update?"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("comment: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", task.ID), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("item get: %d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Item itemJSON `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode item detail: %v (%s)", err, w.Body.String())
	}
	after := detail.Item
	if after.ID != task.ID {
		t.Fatalf("item get body = %s (task=%+v)", w.Body.String(), task)
	}
	if len(after.Assignees) != 1 || after.Assignees[0].UserID != member.ID {
		t.Fatalf("comment automation item = %+v", after)
	}
	// The automation comment is not a new user comment, so the comment rule
	// must have run exactly once for the explicit comment.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", task.ID), adminCookie, "")
	comments = decodeComments(t, w.Body.Bytes())
	if len(comments) != 2 {
		t.Fatalf("comment count = %d: %+v", len(comments), comments)
	}
}

func TestAutomationDueIdempotencyAndProjectIsolation(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Alpha","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project A: %d %s", w.Code, w.Body.String())
	}
	alpha := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Beta","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project B: %d %s", w.Code, w.Body.String())
	}
	beta := decodeProject(t, w)

	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("Overdue in Alpha", "due_passed", 0, []int64{alpha.ID},
			`[{"kind":"add_comment","body":"Overdue"}]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("due rule: %d %s", w.Code, w.Body.String())
	}

	create := func(projectID int64) int64 {
		w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", projectID), adminCookie,
			`{"type":"action","title":"Late","due_on":"2026-11-01"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("item: %d %s", w.Code, w.Body.String())
		}
		return decodeItemResponse(t, w.Body.Bytes()).ID
	}
	inAlpha := create(alpha.ID)
	inBeta := create(beta.ID)

	when := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := s.RunDueAutomations(when); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	commentsOf := func(itemID int64) []commentJSON {
		w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/comments", itemID), adminCookie, "")
		if w.Code != http.StatusOK {
			t.Fatalf("comments: %d %s", w.Code, w.Body.String())
		}
		return decodeComments(t, w.Body.Bytes())
	}
	if got := commentsOf(inAlpha); len(got) != 1 || got[0].Body != "Overdue" {
		t.Fatalf("alpha comments = %+v", got)
	}
	if got := commentsOf(inBeta); len(got) != 0 {
		t.Fatalf("beta comments = %+v", got)
	}
}

func TestAutomationNotifyReachesOnlyAssignees(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	outsiderCookie := login(t, s, outsider.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Alpha","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)

	w = apiDo(t, s, http.MethodPost, "/api/automations", adminCookie,
		automationBody("Notify on assign", "assigned", 0, []int64{project.ID}, `[{"kind":"notify"}]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("rule: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Task","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("item: %d %s", w.Code, w.Body.String())
	}

	kinds := notificationKinds(t, s, memberCookie)
	counts := map[string]int{}
	for _, kind := range kinds {
		counts[kind]++
	}
	if counts["automation"] != 1 {
		t.Fatalf("member notifications = %+v", kinds)
	}
	if got := notificationKinds(t, s, outsiderCookie); len(got) != 0 {
		t.Fatalf("outsider notifications = %+v", got)
	}
	if got := notificationKinds(t, s, adminCookie); len(got) != 0 {
		t.Fatalf("admin notifications = %+v", got)
	}
	_ = outsider
}
