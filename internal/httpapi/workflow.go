package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"jiralike/internal/auth"
	"jiralike/internal/automations"
	"jiralike/internal/items"
	"jiralike/internal/projects"
	"jiralike/internal/workflow"
)

type workflowCreateBody struct {
	Name   string `json:"name"`
	Preset string `json:"preset"`
}

type workflowRenameBody struct {
	Name string `json:"name"`
}

type workflowStateCreateBody struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Position *int   `json:"position"`
}

type workflowStateUpdateBody struct {
	Name     *string `json:"name"`
	Category *string `json:"category"`
	Position *int    `json:"position"`
}

type workflowInitialBody struct {
	StateID int64 `json:"state_id"`
}

type workflowTransitionBody struct {
	FromStateID *int64                     `json:"from_state_id"`
	ToStateID   *int64                     `json:"to_state_id"`
	Name        *string                    `json:"name"`
	Position    *int                       `json:"position"`
	Conditions  *[]workflow.ConditionInput `json:"conditions"`
}

type workflowAssignBody struct {
	WorkflowID int64            `json:"workflow_id"`
	Mapping    map[string]int64 `json:"mapping"`
}

// projectForWorkflowRead resolves a project the caller is allowed to see. A
// private project they cannot see answers 404, like every other project lookup.
func (s *Server) projectForWorkflowRead(w http.ResponseWriter, r *http.Request, user auth.User) (projects.Project, bool) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid project id")
		return projects.Project{}, false
	}
	p, err := s.projectsStore.Get(id)
	if errors.Is(err, projects.ErrNotFound) {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	if err != nil {
		writeError(w, 500, "project get failed")
		return projects.Project{}, false
	}
	canSee, err := s.projectsStore.CanSee(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return projects.Project{}, false
	}
	if !canSee {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	return p, true
}

// workflowOwned loads a flow and verifies it belongs to the project in the path.
func (s *Server) workflowOwned(w http.ResponseWriter, r *http.Request, projectID int64) (workflow.Workflow, bool) {
	id, err := parseID(r.PathValue("workflow"))
	if err != nil {
		writeError(w, 400, "invalid workflow id")
		return workflow.Workflow{}, false
	}
	wf, err := s.workflowStore.Get(id)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		writeError(w, 404, "workflow not found")
		return workflow.Workflow{}, false
	}
	if err != nil {
		writeError(w, 500, "workflow read failed")
		return workflow.Workflow{}, false
	}
	if wf.ProjectID != projectID {
		writeError(w, 404, "workflow not found")
		return workflow.Workflow{}, false
	}
	return wf, true
}

func (s *Server) workflowPresetsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	writeJSON(w, 200, map[string]any{"presets": workflow.Presets()})
}

// workflowStatesList returns the states of every project the caller can see.
// Global screens (search, my work, automations) use it to build their state
// filters without assuming a single installation workflow.
func (s *Server) workflowStatesList(w http.ResponseWriter, r *http.Request, user auth.User) {
	list, err := s.projectsStore.List(user.ID, user.Role, projects.ListFilter{})
	if err != nil {
		writeError(w, 500, "project list failed")
		return
	}
	states := []map[string]any{}
	if len(list) == 0 {
		writeJSON(w, 200, map[string]any{"states": states})
		return
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
	args := make([]any, len(list))
	for i, p := range list {
		args[i] = p.ID
	}
	rows, err := s.db.Query(`
		SELECT ws.id, ws.name, ws.category, ws.workflow_id, wf.name, p.id, p.name
		FROM workflow_states ws
		JOIN workflows wf ON wf.id = ws.workflow_id
		JOIN projects p ON p.id = wf.project_id
		WHERE p.archived_at IS NULL AND p.id IN (`+placeholders+`)
		ORDER BY p.name COLLATE NOCASE, wf.name COLLATE NOCASE, ws.position, ws.id`, args...)
	if err != nil {
		writeError(w, 500, "state list failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, workflowID, projectID int64
		var name, category, workflowName, projectName string
		if err := rows.Scan(&id, &name, &category, &workflowID, &workflowName, &projectID, &projectName); err != nil {
			writeError(w, 500, "state list failed")
			return
		}
		states = append(states, map[string]any{
			"id":            id,
			"name":          name,
			"category":      category,
			"workflow_id":   workflowID,
			"workflow_name": workflowName,
			"project_id":    projectID,
			"project_name":  projectName,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "state list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"states": states})
}

func (s *Server) projectWorkflowsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	list, err := s.workflowStore.ListByProject(p.ID)
	if err != nil {
		writeError(w, 500, "workflow list failed")
		return
	}
	bindings, err := s.workflowStore.Bindings(p.ID)
	if err != nil {
		writeError(w, 500, "workflow binding read failed")
		return
	}
	writeJSON(w, 200, map[string]any{"workflows": list, "bindings": bindings})
}

func (s *Server) projectWorkflowCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	var body workflowCreateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	wf, err := s.workflowStore.Create(p.ID, body.Name, body.Preset)
	if err != nil {
		writeWorkflowError(w, err, "workflow create failed")
		return
	}
	writeJSON(w, 201, wf)
}

func (s *Server) projectWorkflowRename(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	var body workflowRenameBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	updated, err := s.workflowStore.Rename(wf.ID, body.Name)
	if err != nil {
		writeWorkflowError(w, err, "workflow rename failed")
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) projectWorkflowDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	if err := s.workflowStore.Delete(wf.ID); err != nil {
		writeWorkflowError(w, err, "workflow delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectWorkflowDuplicate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	var body workflowRenameBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	created, err := s.workflowStore.Duplicate(wf.ID, p.ID, body.Name)
	if err != nil {
		writeWorkflowError(w, err, "workflow duplicate failed")
		return
	}
	writeJSON(w, 201, created)
}

// projectItemTypeAssign binds a flow to one item type. The mapping is required
// when the type already uses another flow and has items in it.
func (s *Server) projectItemTypeAssign(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	itemType := strings.ToLower(strings.TrimSpace(r.PathValue("type")))
	var body workflowAssignBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	mapping := map[int64]int64{}
	for key, value := range body.Mapping {
		from, err := strconv.ParseInt(strings.TrimSpace(key), 10, 64)
		if err != nil || from <= 0 {
			writeError(w, 400, "invalid mapping key")
			return
		}
		if value <= 0 {
			writeError(w, 400, "invalid mapping target")
			return
		}
		mapping[from] = value
	}
	if err := s.workflowStore.AssignType(p.ID, itemType, body.WorkflowID, mapping, user.ID); err != nil {
		writeWorkflowError(w, err, "workflow assignment failed")
		return
	}
	bindings, err := s.workflowStore.Bindings(p.ID)
	if err != nil {
		writeError(w, 500, "workflow binding read failed")
		return
	}
	writeJSON(w, 200, map[string]any{"bindings": bindings})
}

func (s *Server) workflowStateCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	var body workflowStateCreateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	state, err := s.workflowStore.CreateState(wf.ID, body.Name, body.Category, body.Position)
	if err != nil {
		writeWorkflowError(w, err, "state create failed")
		return
	}
	writeJSON(w, 201, state)
}

func (s *Server) workflowStateUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("state"))
	if err != nil {
		writeError(w, 400, "invalid state id")
		return
	}
	current, err := s.workflowStore.GetState(id)
	if errors.Is(err, workflow.ErrStateNotFound) {
		writeError(w, 404, "state not found")
		return
	}
	if err != nil {
		writeError(w, 500, "state read failed")
		return
	}
	if current.WorkflowID != wf.ID {
		writeError(w, 404, "state not found")
		return
	}
	var body workflowStateUpdateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	name, category := current.Name, current.Category
	if body.Name != nil {
		name = *body.Name
	}
	if body.Category != nil {
		category = *body.Category
	}
	state, err := s.workflowStore.UpdateState(id, name, category, body.Position)
	if err != nil {
		writeWorkflowError(w, err, "state update failed")
		return
	}
	writeJSON(w, 200, state)
}

func (s *Server) workflowStateDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("state"))
	if err != nil {
		writeError(w, 400, "invalid state id")
		return
	}
	current, err := s.workflowStore.GetState(id)
	if errors.Is(err, workflow.ErrStateNotFound) {
		writeError(w, 404, "state not found")
		return
	}
	if err != nil {
		writeError(w, 500, "state read failed")
		return
	}
	if current.WorkflowID != wf.ID {
		writeError(w, 404, "state not found")
		return
	}
	if err := s.workflowStore.DeleteState(id); err != nil {
		writeWorkflowError(w, err, "state delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workflowInitialSet(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	var body workflowInitialBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if err := s.workflowStore.SetInitialState(wf.ID, body.StateID); err != nil {
		writeWorkflowError(w, err, "initial state update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workflowTransitionCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	var body workflowTransitionBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if body.FromStateID == nil || body.ToStateID == nil {
		writeError(w, 400, "from_state_id and to_state_id are required")
		return
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	conditions := []workflow.ConditionInput{}
	if body.Conditions != nil {
		conditions = *body.Conditions
	}
	transition, err := s.workflowStore.CreateTransition(wf.ID, workflow.TransitionInput{
		FromStateID: *body.FromStateID,
		ToStateID:   *body.ToStateID,
		Name:        name,
		Position:    body.Position,
		Conditions:  conditions,
	})
	if err != nil {
		writeWorkflowError(w, err, "transition create failed")
		return
	}
	writeJSON(w, 201, transition)
}

func (s *Server) workflowTransitionUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("transition"))
	if err != nil {
		writeError(w, 400, "invalid transition id")
		return
	}
	current, err := s.workflowStore.GetTransition(id)
	if errors.Is(err, workflow.ErrTransitionNotFound) {
		writeError(w, 404, "transition not found")
		return
	}
	if err != nil {
		writeError(w, 500, "transition read failed")
		return
	}
	if current.WorkflowID != wf.ID {
		writeError(w, 404, "transition not found")
		return
	}
	var body workflowTransitionBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	in := workflow.TransitionInput{
		FromStateID: current.FromStateID,
		ToStateID:   current.ToStateID,
		Name:        current.Name,
		Position:    nil,
	}
	if body.FromStateID != nil {
		in.FromStateID = *body.FromStateID
	}
	if body.ToStateID != nil {
		in.ToStateID = *body.ToStateID
	}
	if body.Name != nil {
		in.Name = *body.Name
	}
	if body.Position != nil {
		in.Position = body.Position
	}
	if body.Conditions != nil {
		in.ReplaceConditions = true
		in.Conditions = *body.Conditions
	}
	transition, err := s.workflowStore.UpdateTransition(wf.ID, id, in)
	if err != nil {
		writeWorkflowError(w, err, "transition update failed")
		return
	}
	writeJSON(w, 200, transition)
}

func (s *Server) workflowTransitionDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForWorkflowRead(w, r, user)
	if !ok {
		return
	}
	wf, ok := s.workflowOwned(w, r, p.ID)
	if !ok {
		return
	}
	id, err := parseID(r.PathValue("transition"))
	if err != nil {
		writeError(w, 400, "invalid transition id")
		return
	}
	if err := s.workflowStore.DeleteTransition(wf.ID, id); err != nil {
		writeWorkflowError(w, err, "transition delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) itemTransitionsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	it, ok := s.itemVisibleByID(w, itemID, user)
	if !ok {
		return
	}
	list, err := s.workflowStore.Available(workflowSubject(it), workflowActor(user))
	if err != nil {
		writeError(w, 500, "transition list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"transitions": list})
}

func (s *Server) itemTransitionApply(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	transitionID, err := parseID(r.PathValue("transition_id"))
	if err != nil {
		writeError(w, 400, "invalid transition id")
		return
	}
	it, ok := s.itemWritableByID(w, itemID, user)
	if !ok {
		return
	}
	transition, err := s.workflowStore.Check(transitionID, workflowSubject(it), workflowActor(user))
	if errors.Is(err, workflow.ErrTransitionNotFound) {
		writeError(w, 404, "transition not found")
		return
	}
	if errors.Is(err, workflow.ErrTransitionNotApplicable) {
		writeError(w, 409, err.Error())
		return
	}
	if errors.Is(err, workflow.ErrTransitionForbidden) || errors.Is(err, workflow.ErrGuestTransition) {
		writeError(w, 403, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "transition check failed")
		return
	}
	if err := s.itemsStore.ChangeStateFrom(it.ID, transition.FromStateID, transition.ToStateID, user.ID); err != nil {
		if errors.Is(err, items.ErrStateConflict) {
			writeError(w, 409, err.Error())
			return
		}
		writeItemError(w, err, "transition failed")
		return
	}
	s.runAutomations(automations.Event{
		Kind:      "state_changed",
		ProjectID: it.ProjectID,
		ItemID:    it.ID,
		StateID:   transition.ToStateID,
		StateName: transition.ToStateName,
	})
	updated, err := s.itemsStore.Get(it.ID)
	if err != nil {
		writeError(w, 500, "item read failed")
		return
	}
	writeJSON(w, 200, updated)
}

func workflowSubject(it items.Item) workflow.Subject {
	ids := make([]int64, 0, len(it.Assignees))
	for _, a := range it.Assignees {
		ids = append(ids, a.UserID)
	}
	return workflow.Subject{
		StateID:     it.StateID,
		Type:        it.Type,
		Priority:    it.Priority,
		AssigneeIDs: ids,
	}
}

func workflowActor(user auth.User) workflow.Actor {
	return workflow.Actor{UserID: user.ID, Role: user.Role}
}

func writeWorkflowError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound), errors.Is(err, workflow.ErrStateNotFound), errors.Is(err, workflow.ErrTransitionNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, workflow.ErrTransitionNotApplicable):
		writeError(w, 409, err.Error())
	case errors.Is(err, workflow.ErrTransitionForbidden), errors.Is(err, workflow.ErrGuestTransition):
		writeError(w, 403, err.Error())
	case errors.Is(err, workflow.ErrWorkflowNameTaken),
		errors.Is(err, workflow.ErrWorkflowInUse),
		errors.Is(err, workflow.ErrStateNameTaken),
		errors.Is(err, workflow.ErrLastOpenState),
		errors.Is(err, workflow.ErrLastDoneState),
		errors.Is(err, workflow.ErrStateInUse),
		errors.Is(err, workflow.ErrInitialState),
		errors.Is(err, workflow.ErrTransitionExists),
		errors.Is(err, workflow.ErrMappingRequired):
		writeError(w, 409, err.Error())
	case errors.Is(err, workflow.ErrWorkflowNameEmpty),
		errors.Is(err, workflow.ErrWorkflowNameLong),
		errors.Is(err, workflow.ErrPresetNotFound),
		errors.Is(err, workflow.ErrInvalidItemType),
		errors.Is(err, workflow.ErrMappingInvalid),
		errors.Is(err, workflow.ErrStateNameEmpty),
		errors.Is(err, workflow.ErrStateNameTooLong),
		errors.Is(err, workflow.ErrInvalidCategory),
		errors.Is(err, workflow.ErrInvalidPosition),
		errors.Is(err, workflow.ErrTransitionNameLong),
		errors.Is(err, workflow.ErrTransitionSameState),
		errors.Is(err, workflow.ErrTransitionStateMismatch),
		errors.Is(err, workflow.ErrInvalidCondition):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 500, fallback)
	}
}
