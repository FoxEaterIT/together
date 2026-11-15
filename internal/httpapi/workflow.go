package httpapi

import (
	"errors"
	"net/http"

	"jiralike/internal/auth"
	"jiralike/internal/automations"
	"jiralike/internal/items"
	"jiralike/internal/workflow"
)

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

func (s *Server) workflowGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	wf, err := s.workflowStore.Get()
	if err != nil {
		writeError(w, 500, "workflow read failed")
		return
	}
	writeJSON(w, 200, wf)
}

func (s *Server) workflowStateCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body workflowStateCreateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	state, err := s.workflowStore.CreateState(body.Name, body.Category, body.Position)
	if err != nil {
		writeWorkflowError(w, err, "state create failed")
		return
	}
	writeJSON(w, 201, state)
}

func (s *Server) workflowStateUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
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
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid state id")
		return
	}
	if err := s.workflowStore.DeleteState(id); err != nil {
		writeWorkflowError(w, err, "state delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workflowInitialSet(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body workflowInitialBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if err := s.workflowStore.SetInitialState(body.StateID); err != nil {
		writeWorkflowError(w, err, "initial state update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workflowTransitionCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
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
	transition, err := s.workflowStore.CreateTransition(workflow.TransitionInput{
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
	id, err := parseID(r.PathValue("id"))
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
	transition, err := s.workflowStore.UpdateTransition(id, in)
	if err != nil {
		writeWorkflowError(w, err, "transition update failed")
		return
	}
	writeJSON(w, 200, transition)
}

func (s *Server) workflowTransitionDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid transition id")
		return
	}
	if err := s.workflowStore.DeleteTransition(id); err != nil {
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
	case errors.Is(err, workflow.ErrStateNotFound):
		writeError(w, 404, "state not found")
	case errors.Is(err, workflow.ErrTransitionNotFound):
		writeError(w, 404, "transition not found")
	case errors.Is(err, workflow.ErrTransitionNotApplicable):
		writeError(w, 409, err.Error())
	case errors.Is(err, workflow.ErrTransitionForbidden):
		writeError(w, 403, err.Error())
	case errors.Is(err, workflow.ErrGuestTransition):
		writeError(w, 403, err.Error())
	case errors.Is(err, workflow.ErrStateNameTaken),
		errors.Is(err, workflow.ErrLastOpenState),
		errors.Is(err, workflow.ErrLastDoneState),
		errors.Is(err, workflow.ErrStateInUse),
		errors.Is(err, workflow.ErrInitialState),
		errors.Is(err, workflow.ErrTransitionExists):
		writeError(w, 409, err.Error())
	case errors.Is(err, workflow.ErrStateNameEmpty),
		errors.Is(err, workflow.ErrStateNameTooLong),
		errors.Is(err, workflow.ErrInvalidCategory),
		errors.Is(err, workflow.ErrInvalidPosition),
		errors.Is(err, workflow.ErrTransitionNameLong),
		errors.Is(err, workflow.ErrTransitionSameState),
		errors.Is(err, workflow.ErrInvalidCondition):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 500, fallback)
	}
}
