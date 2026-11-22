package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type workflowStateJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Position  int    `json:"position"`
	Initial   bool   `json:"initial"`
	ItemCount int    `json:"item_count"`
}

type workflowConditionJSON struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type workflowTransitionJSON struct {
	ID            int64                   `json:"id"`
	FromStateID   int64                   `json:"from_state_id"`
	FromStateName string                  `json:"from_state_name"`
	ToStateID     int64                   `json:"to_state_id"`
	ToStateName   string                  `json:"to_state_name"`
	Name          string                  `json:"name"`
	Conditions    []workflowConditionJSON `json:"conditions"`
}

type workflowJSON struct {
	ID             int64                    `json:"id"`
	ProjectID      int64                    `json:"project_id"`
	Name           string                   `json:"name"`
	Preset         string                   `json:"preset"`
	States         []workflowStateJSON      `json:"states"`
	Transitions    []workflowTransitionJSON `json:"transitions"`
	InitialStateID int64                    `json:"initial_state_id"`
	ItemTypes      []string                 `json:"item_types"`
}

type workflowListJSON struct {
	Workflows []workflowJSON   `json:"workflows"`
	Bindings  map[string]int64 `json:"bindings"`
}

func decodeWorkflow(t *testing.T, body []byte) workflowJSON {
	t.Helper()
	var wf workflowJSON
	if err := json.Unmarshal(body, &wf); err != nil {
		t.Fatalf("decode workflow: %v (%s)", err, string(body))
	}
	return wf
}

func decodeWorkflowList(t *testing.T, body []byte) workflowListJSON {
	t.Helper()
	var parsed workflowListJSON
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode workflow list: %v (%s)", err, string(body))
	}
	return parsed
}

func decodeWorkflowState(t *testing.T, body []byte) workflowStateJSON {
	t.Helper()
	var st workflowStateJSON
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode state: %v (%s)", err, string(body))
	}
	return st
}

func decodeWorkflowTransition(t *testing.T, body []byte) workflowTransitionJSON {
	t.Helper()
	var tr workflowTransitionJSON
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("decode transition: %v (%s)", err, string(body))
	}
	return tr
}

func decodeItemTransitions(t *testing.T, body []byte) []workflowTransitionJSON {
	t.Helper()
	var parsed struct {
		Transitions []workflowTransitionJSON `json:"transitions"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode transitions: %v (%s)", err, string(body))
	}
	return parsed.Transitions
}

func stateByName(list []workflowStateJSON, name string) workflowStateJSON {
	for _, st := range list {
		if st.Name == name {
			return st
		}
	}
	return workflowStateJSON{}
}

// defaultWorkflow reads the workflows of a project and returns the flow every
// sealed item type is bound to.
func defaultWorkflow(t *testing.T, s *Server, cookie *http.Cookie, projectID int64) workflowListJSON {
	t.Helper()
	w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/workflows", projectID), cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("workflow list: %d %s", w.Code, w.Body.String())
	}
	list := decodeWorkflowList(t, w.Body.Bytes())
	if len(list.Workflows) == 0 {
		t.Fatalf("no workflows seeded for project %d", projectID)
	}
	return list
}

func TestWorkflowAdminCRUDAndConstraints(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	project := decodeProject(t, w)

	// The preset catalog is readable and offers the standard kits.
	w = apiDo(t, s, http.MethodGet, "/api/workflow/presets", memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("presets: %d %s", w.Code, w.Body.String())
	}
	var catalog struct {
		Presets []struct {
			Slug string `json:"slug"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode presets: %v (%s)", err, w.Body.String())
	}
	if len(catalog.Presets) < 5 {
		t.Fatalf("preset catalog = %+v", catalog.Presets)
	}

	// Everyone who can see the project reads its flows; only the admin writes.
	list := defaultWorkflow(t, s, memberCookie, project.ID)
	if len(list.Bindings) != 4 {
		t.Fatalf("bindings = %+v", list.Bindings)
	}
	wf := list.Workflows[0]
	for _, itemType := range []string{"observer", "action", "micro", "bug"} {
		if list.Bindings[itemType] != wf.ID {
			t.Fatalf("binding %s = %d (want %d)", itemType, list.Bindings[itemType], wf.ID)
		}
	}
	if len(wf.States) != 3 {
		t.Fatalf("seeded states = %+v", wf.States)
	}
	todo := stateByName(wf.States, "To do")
	progress := stateByName(wf.States, "In progress")
	done := stateByName(wf.States, "Done")
	if todo.ID == 0 || progress.ID == 0 || done.ID == 0 || wf.InitialStateID != todo.ID {
		t.Fatalf("workflow = %+v", wf)
	}
	if !todo.Initial {
		t.Fatalf("initial flag missing: %+v", todo)
	}
	defaultWorkflow(t, s, guestCookie, project.ID)

	statesURL := fmt.Sprintf("/api/projects/%d/workflows/%d/states", project.ID, wf.ID)
	transitionsURL := fmt.Sprintf("/api/projects/%d/workflows/%d/transitions", project.ID, wf.ID)
	initialURL := fmt.Sprintf("/api/projects/%d/workflows/%d/initial-state", project.ID, wf.ID)

	w = apiDo(t, s, http.MethodPost, statesURL, managerCookie, `{"name":"Backlog","category":"open"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("manager state create: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, statesURL, memberCookie, `{"name":"Backlog","category":"open"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member state create: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, statesURL, adminCookie, `{"name":"Backlog","category":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin state create: %d %s", w.Code, w.Body.String())
	}
	backlog := decodeWorkflowState(t, w.Body.Bytes())
	if backlog.Name != "Backlog" || backlog.Category != "open" {
		t.Fatalf("created state = %+v", backlog)
	}
	w = apiDo(t, s, http.MethodPost, statesURL, adminCookie, `{"name":"backlog","category":"done"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate state: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, statesURL, adminCookie, `{"name":"  ","category":"open"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty state: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, statesURL, adminCookie, `{"name":"Odd","category":"paused"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad category: expected 400, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("%s/%d", statesURL, backlog.ID), adminCookie, `{"name":"Icebox","position":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("state update: %d %s", w.Code, w.Body.String())
	}
	renamed := decodeWorkflowState(t, w.Body.Bytes())
	if renamed.Name != "Icebox" || renamed.Position != 0 {
		t.Fatalf("updated state = %+v", renamed)
	}
	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("%s/%d", statesURL, done.ID), adminCookie, `{"category":"open"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("last done category: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	if w = apiDo(t, s, http.MethodPut, initialURL, adminCookie, fmt.Sprintf(`{"state_id":%d}`, backlog.ID)); w.Code != http.StatusNoContent {
		t.Fatalf("set initial: %d %s", w.Code, w.Body.String())
	}
	list = defaultWorkflow(t, s, memberCookie, project.ID)
	if list.Workflows[0].InitialStateID != backlog.ID || !stateByName(list.Workflows[0].States, "Icebox").Initial {
		t.Fatalf("initial not updated: %+v", list.Workflows[0])
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("%s/%d", statesURL, backlog.ID), adminCookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete initial: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPut, initialURL, adminCookie, fmt.Sprintf(`{"state_id":%d}`, todo.ID)); w.Code != http.StatusNoContent {
		t.Fatalf("reset initial: %d", w.Code)
	}

	// Transitions: admin only, unique pair, same-state rejected, invalid
	// conditions rejected.
	w = apiDo(t, s, http.MethodPost, transitionsURL, managerCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d}`, todo.ID, progress.ID))
	if w.Code != http.StatusForbidden {
		t.Fatalf("manager transition create: expected 403, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, transitionsURL, adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"conditions":[{"kind":"role","value":"admin"}]}`, todo.ID, progress.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("transition create: %d %s", w.Code, w.Body.String())
	}
	start := decodeWorkflowTransition(t, w.Body.Bytes())
	if start.Name == "" || start.FromStateID != todo.ID || len(start.Conditions) != 1 {
		t.Fatalf("created transition = %+v", start)
	}
	w = apiDo(t, s, http.MethodPost, transitionsURL, adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d}`, todo.ID, progress.ID))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate transition: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, transitionsURL, adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d}`, todo.ID, todo.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("same-state transition: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, transitionsURL, adminCookie,
		fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"conditions":[{"kind":"script","value":"x"}]}`, todo.ID, done.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid condition: expected 400, got %d", w.Code)
	}

	w = apiDo(t, s, http.MethodPut, fmt.Sprintf("%s/%d", transitionsURL, start.ID), adminCookie,
		`{"name":"Start work","conditions":[{"kind":"role","value":"manager"},{"kind":"priority","value":"high"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("transition update: %d %s", w.Code, w.Body.String())
	}
	updated := decodeWorkflowTransition(t, w.Body.Bytes())
	if updated.Name != "Start work" || len(updated.Conditions) != 2 {
		t.Fatalf("updated transition = %+v", updated)
	}

	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("%s/%d", transitionsURL, start.ID), adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("transition delete: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("%s/%d", transitionsURL, start.ID), adminCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("double transition delete: expected 404, got %d", w.Code)
	}

	// A state still referenced by an item cannot be deleted.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", project.ID), adminCookie, `{"type":"action","title":"Task"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPut, initialURL, adminCookie, fmt.Sprintf(`{"state_id":%d}`, progress.ID)); w.Code != http.StatusNoContent {
		t.Fatalf("initial to progress: %d", w.Code)
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("%s/%d", statesURL, todo.ID), adminCookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete state in use: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("%s/%d", statesURL, backlog.ID), adminCookie, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete unused state: %d %s", w.Code, w.Body.String())
	}

	// A flow bound to items cannot be deleted; a duplicate can.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/workflows/%d", project.ID, wf.ID), adminCookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete bound workflow: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/workflows/%d/duplicate", project.ID, wf.ID), adminCookie, `{"name":"Copy"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("duplicate workflow: %d %s", w.Code, w.Body.String())
	}
	copied := decodeWorkflow(t, w.Body.Bytes())
	if copied.Name != "Copy" || len(copied.States) != len(wf.States) {
		t.Fatalf("copied workflow = %+v", copied)
	}
	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/projects/%d/workflows/%d", project.ID, copied.ID), adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete unbound workflow: %d %s", w.Code, w.Body.String())
	}
}

func TestItemTransitionsConditionsAndAccess(t *testing.T) {
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
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	list := defaultWorkflow(t, s, adminCookie, open.ID)
	wf := list.Workflows[0]
	todo := stateByName(wf.States, "To do")
	progress := stateByName(wf.States, "In progress")
	done := stateByName(wf.States, "Done")

	statesURL := fmt.Sprintf("/api/projects/%d/workflows/%d/states", open.ID, wf.ID)
	transitionsURL := fmt.Sprintf("/api/projects/%d/workflows/%d/transitions", open.ID, wf.ID)

	w = apiDo(t, s, http.MethodPost, statesURL, adminCookie, `{"name":"Blocked","category":"done"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("blocked state: %d %s", w.Code, w.Body.String())
	}
	blocked := decodeWorkflowState(t, w.Body.Bytes())

	// Unconditional arrow from To do, a manager-only arrow, an assignee-only
	// arrow and a typed arrow from In progress.
	createTransition := func(body string) workflowTransitionJSON {
		t.Helper()
		w := apiDo(t, s, http.MethodPost, transitionsURL, adminCookie, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create transition: %d %s", w.Code, w.Body.String())
		}
		return decodeWorkflowTransition(t, w.Body.Bytes())
	}
	start := createTransition(fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Start"}`, todo.ID, progress.ID))
	managerClose := createTransition(fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Manager close","conditions":[{"kind":"role","value":"manager"}]}`, progress.ID, done.ID))
	blockAssigned := createTransition(fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Block","conditions":[{"kind":"assignee","value":"yes"}]}`, todo.ID, blocked.ID))
	fixBug := createTransition(fmt.Sprintf(`{"from_state_id":%d,"to_state_id":%d,"name":"Fix bug","conditions":[{"kind":"priority","value":"urgent"},{"kind":"item_type","value":"bug"}]}`, progress.ID, blocked.ID))

	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		fmt.Sprintf(`{"type":"action","title":"Task","priority":"high","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create item: %d %s", w.Code, w.Body.String())
	}
	task := decodeItemResponse(t, w.Body.Bytes())

	// Available arrows are filtered by role, assignee and current state.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", task.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member transitions: %d %s", w.Code, w.Body.String())
	}
	list2 := decodeItemTransitions(t, w.Body.Bytes())
	if len(list2) != 2 || !hasTransition(list2, start.ID) || !hasTransition(list2, blockAssigned.ID) {
		t.Fatalf("member transitions = %+v", list2)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", task.ID), managerCookie, "")
	if list2 = decodeItemTransitions(t, w.Body.Bytes()); len(list2) != 1 || !hasTransition(list2, start.ID) {
		t.Fatalf("manager transitions = %+v", list2)
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", task.ID), guestCookie, "")
	if list2 = decodeItemTransitions(t, w.Body.Bytes()); len(list2) != 0 {
		t.Fatalf("guest transitions = %+v", list2)
	}

	// The item detail carries the same available list.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", task.ID), memberCookie, "")
	var detail struct {
		Item        itemJSON                 `json:"item"`
		Transitions []workflowTransitionJSON `json:"transitions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Transitions) != 2 {
		t.Fatalf("detail transitions = %+v", detail.Transitions)
	}

	// Guests cannot apply an arrow.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, start.ID), guestCookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("guest apply: expected 403, got %d", w.Code)
	}
	// A member may use the unconditional arrow.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, start.ID), memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member start: %d %s", w.Code, w.Body.String())
	}
	changed := decodeItemResponse(t, w.Body.Bytes())
	if changed.StateID != progress.ID || changed.StateName != "In progress" {
		t.Fatalf("state after start = %+v", changed)
	}
	// The manager-only arrow is hidden from members and refused.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", task.ID), memberCookie, "")
	if list2 = decodeItemTransitions(t, w.Body.Bytes()); len(list2) != 0 {
		t.Fatalf("member transitions after start = %+v", list2)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, managerClose.ID), memberCookie, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("member manager-close: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, managerClose.ID), managerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("manager close: %d %s", w.Code, w.Body.String())
	}
	if changed = decodeItemResponse(t, w.Body.Bytes()); changed.StateID != done.ID {
		t.Fatalf("state after close = %+v", changed)
	}
	// The arrow no longer starts from the current state.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", task.ID, managerClose.ID), managerCookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("stale arrow: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	// The state change is visible in the history.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/events", task.ID), memberCookie, "")
	if events := decodeEvents(t, w.Body.Bytes()); len(events) == 0 || events[len(events)-1].Kind != "state_changed" {
		t.Fatalf("state events = %+v", events)
	}

	// A typed arrow: only an urgent Bug shows it and may use it.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie,
		fmt.Sprintf(`{"type":"bug","title":"Crash","priority":"urgent","assignee_ids":[%d]}`, member.ID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create bug: %d %s", w.Code, w.Body.String())
	}
	bug := decodeItemResponse(t, w.Body.Bytes())
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", bug.ID, start.ID), memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("bug start: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", bug.ID), memberCookie, "")
	list2 = decodeItemTransitions(t, w.Body.Bytes())
	if len(list2) != 1 || !hasTransition(list2, fixBug.ID) {
		t.Fatalf("bug transitions = %+v", list2)
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", bug.ID, fixBug.ID), memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("bug fix: %d %s", w.Code, w.Body.String())
	}

	// A non-urgent Action in In progress does not satisfy the typed arrow.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", open.ID), adminCookie, `{"type":"action","title":"Slow","priority":"low"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create slow: %d %s", w.Code, w.Body.String())
	}
	slow := decodeItemResponse(t, w.Body.Bytes())
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", slow.ID, start.ID), memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("slow start: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", slow.ID, fixBug.ID), memberCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("slow fix: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// A private project seeds its own independent flow; outsiders cannot read it
	// or use a transition that belongs to another project.
	private := createPrivateProject(t, s, managerCookie)
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/projects/%d/workflows", private.ID), outsiderCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider workflow list: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", private.ID), managerCookie, `{"type":"action","title":"Secret"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("private item: %d %s", w.Code, w.Body.String())
	}
	secret := decodeItemResponse(t, w.Body.Bytes())
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", secret.ID), outsiderCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider transitions: expected 404, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", secret.ID, start.ID), outsiderCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("outsider apply: expected 404, got %d", w.Code)
	}
	// Even an insider of the private project cannot apply an arrow of another
	// project's flow.
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/%d", secret.ID, start.ID), managerCookie, "")
	if w.Code == http.StatusOK {
		t.Fatalf("cross-project transition accepted")
	}

	// Archived items disappear from the transition API too.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/items/%d", bug.ID), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("archive bug: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d/transitions", bug.ID), adminCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("archived transitions: expected 404, got %d", w.Code)
	}

	// Bad identifiers fail cleanly.
	w = apiDo(t, s, http.MethodGet, "/api/items/abc/transitions", adminCookie, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad item id: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/abc", task.ID), adminCookie, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad transition id: expected 400, got %d", w.Code)
	}
	w = apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/items/%d/transitions/9999", bug.ID), adminCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("archived item wins over missing arrow: expected 404, got %d", w.Code)
	}
}

func hasTransition(list []workflowTransitionJSON, id int64) bool {
	for _, tr := range list {
		if tr.ID == id {
			return true
		}
	}
	return false
}

func createPrivateProject(t *testing.T, s *Server, cookie *http.Cookie) projectJSON {
	t.Helper()
	w := apiDo(t, s, http.MethodPost, "/api/projects", cookie, `{"name":"Secret project","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private project: %d %s", w.Code, w.Body.String())
	}
	return decodeProject(t, w)
}
