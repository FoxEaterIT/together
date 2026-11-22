// Package workflow owns project workflows: states, transitions, transition
// conditions and the initial state of each flow. A project has one or more
// flows and binds one flow to each item type, so two projects can run the same
// item type through completely different states.
package workflow

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrWorkflowNotFound  = errors.New("workflow not found")
	ErrWorkflowNameEmpty = errors.New("workflow name is required")
	ErrWorkflowNameLong  = errors.New("workflow name is too long")
	ErrWorkflowNameTaken = errors.New("a workflow with this name already exists")
	ErrWorkflowInUse     = errors.New("workflow is assigned to an item type or used by items")
	ErrPresetNotFound    = errors.New("unknown workflow preset")
	ErrInvalidItemType   = errors.New("invalid item type")
	ErrMappingRequired   = errors.New("every state of the current workflow must be mapped")
	ErrMappingInvalid    = errors.New("state mapping is invalid")

	ErrStateNotFound           = errors.New("state not found")
	ErrStateNameEmpty          = errors.New("state name is required")
	ErrStateNameTooLong        = errors.New("state name is too long")
	ErrStateNameTaken          = errors.New("state name already exists")
	ErrInvalidCategory         = errors.New("state category must be open or done")
	ErrLastOpenState           = errors.New("at least one open state is required")
	ErrLastDoneState           = errors.New("at least one done state is required")
	ErrStateInUse              = errors.New("state is used by items")
	ErrInitialState            = errors.New("the initial state cannot be deleted")
	ErrTransitionNotFound      = errors.New("transition not found")
	ErrTransitionNameLong      = errors.New("transition name is too long")
	ErrTransitionSameState     = errors.New("a transition must connect two different states")
	ErrTransitionExists        = errors.New("a transition between these states already exists")
	ErrTransitionStateMismatch = errors.New("both states must belong to the workflow")
	ErrInvalidCondition        = errors.New("invalid transition condition")
	ErrInvalidPosition         = errors.New("position cannot be negative")
	ErrTransitionNotApplicable = errors.New("transition does not start from the current state")
	ErrTransitionForbidden     = errors.New("transition conditions are not met")
	ErrGuestTransition         = errors.New("guests cannot change item state")
)

const (
	maxWorkflowNameLength   = 80
	maxStateNameLength      = 80
	maxTransitionNameLength = 120
	maxConditionsPerRoute   = 50
)

// ItemTypes are the four task kinds a flow can be bound to.
var ItemTypes = []string{"observer", "action", "micro", "bug"}

type State struct {
	ID              int64  `json:"id"`
	WorkflowID      int64  `json:"workflow_id"`
	Name            string `json:"name"`
	Category        string `json:"category"`
	Position        int    `json:"position"`
	CreatedAt       string `json:"created_at"`
	Initial         bool   `json:"initial"`
	ItemCount       int    `json:"item_count"`
	ActiveItemCount int    `json:"active_item_count"`
}

type Condition struct {
	ID           int64  `json:"id"`
	TransitionID int64  `json:"transition_id"`
	Kind         string `json:"kind"`
	Value        string `json:"value"`
}

type Transition struct {
	ID            int64       `json:"id"`
	WorkflowID    int64       `json:"workflow_id"`
	FromStateID   int64       `json:"from_state_id"`
	FromStateName string      `json:"from_state_name"`
	ToStateID     int64       `json:"to_state_id"`
	ToStateName   string      `json:"to_state_name"`
	Name          string      `json:"name"`
	Position      int         `json:"position"`
	Conditions    []Condition `json:"conditions"`
}

type Workflow struct {
	ID             int64        `json:"id"`
	ProjectID      int64        `json:"project_id"`
	Name           string       `json:"name"`
	Preset         string       `json:"preset"`
	CreatedAt      string       `json:"created_at"`
	States         []State      `json:"states"`
	Transitions    []Transition `json:"transitions"`
	InitialStateID int64        `json:"initial_state_id"`
	ItemTypes      []string     `json:"item_types"`
}

// Binding says which flow a project uses for one item type.
type Binding struct {
	ItemType   string `json:"item_type"`
	WorkflowID int64  `json:"workflow_id"`
}

type ConditionInput struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type TransitionInput struct {
	FromStateID       int64
	ToStateID         int64
	Name              string
	Position          *int
	Conditions        []ConditionInput
	ReplaceConditions bool
}

// Subject is the item data the transition engine needs. It deliberately does
// not import the items package, so the rules stay small and testable.
type Subject struct {
	StateID     int64
	Type        string
	Priority    string
	AssigneeIDs []int64
}

type Actor struct {
	UserID int64
	Role   string
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Get returns one flow with its states and arrows.
func (s *Store) Get(id int64) (Workflow, error) {
	var wf Workflow
	err := s.db.QueryRow(
		`SELECT id, project_id, name, preset, created_at FROM workflows WHERE id = ?`, id,
	).Scan(&wf.ID, &wf.ProjectID, &wf.Name, &wf.Preset, &wf.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrWorkflowNotFound
	}
	if err != nil {
		return Workflow{}, err
	}
	if err := s.hydrate(&wf); err != nil {
		return Workflow{}, err
	}
	return wf, nil
}

// ListByProject returns every flow of a project, ordered by name.
func (s *Store) ListByProject(projectID int64) ([]Workflow, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, preset, created_at FROM workflows WHERE project_id = ? ORDER BY name COLLATE NOCASE, id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	out := []Workflow{}
	for rows.Next() {
		var wf Workflow
		if err := rows.Scan(&wf.ID, &wf.ProjectID, &wf.Name, &wf.Preset, &wf.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, wf)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := s.hydrate(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Bindings returns the item-type binding of a project.
func (s *Store) Bindings(projectID int64) (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT item_type, workflow_id FROM project_workflows WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var itemType string
		var id int64
		if err := rows.Scan(&itemType, &id); err != nil {
			return nil, err
		}
		out[itemType] = id
	}
	return out, rows.Err()
}

// WorkflowFor returns the flow a project uses for an item type.
func (s *Store) WorkflowFor(projectID int64, itemType string) (Workflow, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT workflow_id FROM project_workflows WHERE project_id = ? AND item_type = ?`, projectID, itemType,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrWorkflowNotFound
	}
	if err != nil {
		return Workflow{}, err
	}
	return s.Get(id)
}

// InitialStateFor returns the state a new item of the type starts in. It is the
// single source of truth used by the items store.
func (s *Store) InitialStateFor(projectID int64, itemType string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		SELECT ws.id
		FROM project_workflows pw
		JOIN workflow_states ws ON ws.workflow_id = pw.workflow_id AND ws.is_initial = 1
		WHERE pw.project_id = ? AND pw.item_type = ?`, projectID, itemType).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrWorkflowNotFound
	}
	return id, err
}

// Create adds a flow to a project, built from a standard preset.
func (s *Store) Create(projectID int64, name, presetSlug string) (Workflow, error) {
	preset, ok := PresetBySlug(presetSlug)
	if !ok {
		return Workflow{}, ErrPresetNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = preset.Name
	}
	if len(name) > maxWorkflowNameLength {
		return Workflow{}, ErrWorkflowNameLong
	}
	if err := s.ensureProject(projectID); err != nil {
		return Workflow{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Workflow{}, err
	}
	defer tx.Rollback()
	if err := ensureWorkflowNameFree(tx, projectID, name, 0); err != nil {
		return Workflow{}, err
	}
	res, err := tx.Exec(
		`INSERT INTO workflows (project_id, name, preset, created_at) VALUES (?, ?, ?, ?)`,
		projectID, name, preset.Slug, timestamp(),
	)
	if err != nil {
		return Workflow{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Workflow{}, err
	}
	if err := insertPreset(tx, id, preset); err != nil {
		return Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, err
	}
	return s.Get(id)
}

// Rename changes the label of a flow. Bindings and states are untouched.
func (s *Store) Rename(id int64, name string) (Workflow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workflow{}, ErrWorkflowNameEmpty
	}
	if len(name) > maxWorkflowNameLength {
		return Workflow{}, ErrWorkflowNameLong
	}
	wf, err := s.Get(id)
	if err != nil {
		return Workflow{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Workflow{}, err
	}
	defer tx.Rollback()
	if err := ensureWorkflowNameFree(tx, wf.ProjectID, name, id); err != nil {
		return Workflow{}, err
	}
	if _, err := tx.Exec(`UPDATE workflows SET name = ? WHERE id = ?`, name, id); err != nil {
		return Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, err
	}
	return s.Get(id)
}

// Duplicate copies a flow into a project (possibly the same one). States,
// arrows and conditions are copied; bindings are not.
func (s *Store) Duplicate(sourceID, targetProjectID int64, name string) (Workflow, error) {
	source, err := s.Get(sourceID)
	if err != nil {
		return Workflow{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = source.Name
	}
	if len(name) > maxWorkflowNameLength {
		return Workflow{}, ErrWorkflowNameLong
	}
	if err := s.ensureProject(targetProjectID); err != nil {
		return Workflow{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Workflow{}, err
	}
	defer tx.Rollback()
	if err := ensureWorkflowNameFree(tx, targetProjectID, name, 0); err != nil {
		return Workflow{}, err
	}
	res, err := tx.Exec(
		`INSERT INTO workflows (project_id, name, preset, created_at) VALUES (?, ?, ?, ?)`,
		targetProjectID, name, source.Preset, timestamp(),
	)
	if err != nil {
		return Workflow{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Workflow{}, err
	}
	stateIDs := map[int64]int64{}
	for _, state := range source.States {
		res, err := tx.Exec(`
			INSERT INTO workflow_states (workflow_id, name, category, position, is_initial, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, state.Name, state.Category, state.Position, boolToInt(state.Initial), timestamp(),
		)
		if err != nil {
			return Workflow{}, err
		}
		stateIDs[state.ID], err = res.LastInsertId()
		if err != nil {
			return Workflow{}, err
		}
	}
	for _, transition := range source.Transitions {
		res, err := tx.Exec(`
			INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, position)
			VALUES (?, ?, ?, ?, ?)`,
			id, stateIDs[transition.FromStateID], stateIDs[transition.ToStateID], transition.Name, transition.Position,
		)
		if err != nil {
			return Workflow{}, err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return Workflow{}, err
		}
		conditions := make([]ConditionInput, 0, len(transition.Conditions))
		for _, condition := range transition.Conditions {
			conditions = append(conditions, ConditionInput{Kind: condition.Kind, Value: condition.Value})
		}
		if err := replaceConditions(tx, newID, conditions); err != nil {
			return Workflow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, err
	}
	return s.Get(id)
}

// Delete removes an unbound flow that no item points at.
func (s *Store) Delete(id int64) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	var bound int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM project_workflows WHERE workflow_id = ?`, id).Scan(&bound); err != nil {
		return err
	}
	if bound > 0 {
		return ErrWorkflowInUse
	}
	var used int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM items i
		JOIN workflow_states ws ON ws.id = i.state_id
		WHERE ws.workflow_id = ?`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrWorkflowInUse
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		DELETE FROM transition_conditions
		WHERE transition_id IN (SELECT id FROM workflow_transitions WHERE workflow_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflow_transitions WHERE workflow_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflow_states WHERE workflow_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflows WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// AssignType binds a flow to an item type. When the type already uses another
// flow, every state of the old flow must map to a state of the new one and the
// items move in the same transaction, with a history entry each.
func (s *Store) AssignType(projectID int64, itemType string, workflowID int64, mapping map[int64]int64, actorID int64) error {
	itemType = strings.ToLower(strings.TrimSpace(itemType))
	if !validItemType(itemType) {
		return ErrInvalidItemType
	}
	wf, err := s.Get(workflowID)
	if err != nil {
		return err
	}
	if wf.ProjectID != projectID {
		return ErrWorkflowNotFound
	}
	if len(wf.States) == 0 {
		return ErrMappingInvalid
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentID sql.NullInt64
	err = tx.QueryRow(
		`SELECT workflow_id FROM project_workflows WHERE project_id = ? AND item_type = ?`, projectID, itemType,
	).Scan(&currentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if currentID.Valid && currentID.Int64 == workflowID {
		return tx.Commit()
	}

	now := timestamp()
	if currentID.Valid {
		oldStates, err := statesTx(tx, currentID.Int64)
		if err != nil {
			return err
		}
		targets := map[int64]int64{}
		for _, state := range oldStates {
			target := mapping[state.ID]
			if target == 0 {
				return ErrMappingRequired
			}
			targets[state.ID] = target
		}
		// Every target must be a state of the new flow.
		newTargets := map[int64]bool{}
		for _, state := range wf.States {
			newTargets[state.ID] = true
		}
		for _, target := range targets {
			if !newTargets[target] {
				return ErrMappingInvalid
			}
		}
		for fromID, toID := range targets {
			if fromID == toID {
				continue
			}
			rows, err := tx.Query(
				`SELECT id FROM items WHERE project_id = ? AND type = ? AND state_id = ?`, projectID, itemType, fromID,
			)
			if err != nil {
				return err
			}
			ids := []int64{}
			for rows.Next() {
				var itemID int64
				if err := rows.Scan(&itemID); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, itemID)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			for _, itemID := range ids {
				if _, err := tx.Exec(`UPDATE items SET state_id = ? WHERE id = ?`, toID, itemID); err != nil {
					return err
				}
				if err := insertStateEvent(tx, itemID, actorID, fromID, toID, now); err != nil {
					return err
				}
			}
		}
		// No item of this type may stay outside the new flow.
		var stray int
		if err := tx.QueryRow(`
			SELECT COUNT(*)
			FROM items i
			JOIN workflow_states ws ON ws.id = i.state_id
			WHERE i.project_id = ? AND i.type = ? AND ws.workflow_id <> ?`,
			projectID, itemType, workflowID,
		).Scan(&stray); err != nil {
			return err
		}
		if stray > 0 {
			return ErrMappingInvalid
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO project_workflows (project_id, item_type, workflow_id) VALUES (?, ?, ?)
		ON CONFLICT (project_id, item_type) DO UPDATE SET workflow_id = excluded.workflow_id`,
		projectID, itemType, workflowID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateState adds a state to a flow. The first state of an empty flow becomes
// the initial one.
func (s *Store) CreateState(workflowID int64, name, category string, position *int) (State, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return State{}, ErrStateNameEmpty
	}
	if len(name) > maxStateNameLength {
		return State{}, ErrStateNameTooLong
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if category != "open" && category != "done" {
		return State{}, ErrInvalidCategory
	}

	tx, err := s.db.Begin()
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()

	if err := ensureWorkflowExists(tx, workflowID); err != nil {
		return State{}, err
	}
	if err := ensureStateNameFree(tx, workflowID, name, 0); err != nil {
		return State{}, err
	}
	pos := 0
	if position != nil {
		if *position < 0 {
			return State{}, ErrInvalidPosition
		}
		pos = *position
	} else {
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(position), -1) + 1 FROM workflow_states WHERE workflow_id = ?`, workflowID,
		).Scan(&pos); err != nil {
			return State{}, err
		}
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ?`, workflowID).Scan(&count); err != nil {
		return State{}, err
	}
	res, err := tx.Exec(`
		INSERT INTO workflow_states (workflow_id, name, category, position, is_initial, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		workflowID, name, category, pos, boolToInt(count == 0), timestamp(),
	)
	if err != nil {
		return State{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return s.GetState(id)
}

// UpdateState changes the label, category or order of a state.
func (s *Store) UpdateState(id int64, name, category string, position *int) (State, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return State{}, ErrStateNameEmpty
	}
	if len(name) > maxStateNameLength {
		return State{}, ErrStateNameTooLong
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if category != "open" && category != "done" {
		return State{}, ErrInvalidCategory
	}

	tx, err := s.db.Begin()
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()

	current, err := stateTx(tx, id)
	if err != nil {
		return State{}, err
	}
	if err := ensureStateNameFree(tx, current.WorkflowID, name, id); err != nil {
		return State{}, err
	}
	if current.Category != category {
		remaining, err := countStates(tx, current.WorkflowID, current.Category, id)
		if err != nil {
			return State{}, err
		}
		if remaining == 0 {
			if current.Category == "open" {
				return State{}, ErrLastOpenState
			}
			return State{}, ErrLastDoneState
		}
	}
	pos := current.Position
	if position != nil {
		if *position < 0 {
			return State{}, ErrInvalidPosition
		}
		pos = *position
	}
	if _, err := tx.Exec(
		`UPDATE workflow_states SET name = ?, category = ?, position = ? WHERE id = ?`, name, category, pos, id,
	); err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return s.GetState(id)
}

// DeleteState removes a state and the arrows that touch it. The initial state,
// the last state of a category and any state referenced by an item are
// protected.
func (s *Store) DeleteState(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	current, err := stateTx(tx, id)
	if err != nil {
		return err
	}
	if current.Initial {
		return ErrInitialState
	}
	remaining, err := countStates(tx, current.WorkflowID, current.Category, id)
	if err != nil {
		return err
	}
	if remaining == 0 {
		if current.Category == "open" {
			return ErrLastOpenState
		}
		return ErrLastDoneState
	}
	var used int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM items WHERE state_id = ?`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrStateInUse
	}

	if _, err := tx.Exec(`
		DELETE FROM transition_conditions
		WHERE transition_id IN (
			SELECT id FROM workflow_transitions WHERE from_state_id = ? OR to_state_id = ?
		)`, id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflow_transitions WHERE from_state_id = ? OR to_state_id = ?`, id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflow_states WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetInitialState marks one state of a flow as the entry point for new items.
func (s *Store) SetInitialState(workflowID, stateID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := stateTx(tx, stateID)
	if err != nil {
		return err
	}
	if current.WorkflowID != workflowID {
		return ErrStateNotFound
	}
	if _, err := tx.Exec(`UPDATE workflow_states SET is_initial = 0 WHERE workflow_id = ?`, workflowID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE workflow_states SET is_initial = 1 WHERE id = ?`, stateID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetState(id int64) (State, error) {
	var st State
	var initial int
	err := s.db.QueryRow(`
		SELECT ws.id, ws.workflow_id, ws.name, ws.category, ws.position, ws.is_initial, ws.created_at,
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id),
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id AND i.archived_at IS NULL)
		FROM workflow_states ws WHERE ws.id = ?`, id).
		Scan(&st.ID, &st.WorkflowID, &st.Name, &st.Category, &st.Position, &initial, &st.CreatedAt, &st.ItemCount, &st.ActiveItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrStateNotFound
	}
	if err != nil {
		return State{}, err
	}
	st.Initial = initial == 1
	return st, nil
}

func (s *Store) GetTransition(id int64) (Transition, error) {
	var t Transition
	err := s.db.QueryRow(`
		SELECT t.id, t.workflow_id, t.from_state_id, fs.name, t.to_state_id, ts.name, t.name, t.position
		FROM workflow_transitions t
		JOIN workflow_states fs ON fs.id = t.from_state_id
		JOIN workflow_states ts ON ts.id = t.to_state_id
		WHERE t.id = ?`, id).
		Scan(&t.ID, &t.WorkflowID, &t.FromStateID, &t.FromStateName, &t.ToStateID, &t.ToStateName, &t.Name, &t.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return Transition{}, ErrTransitionNotFound
	}
	if err != nil {
		return Transition{}, err
	}
	byTransition, err := s.conditionsFor([]int64{id})
	if err != nil {
		return Transition{}, err
	}
	t.Conditions = byTransition[id]
	if t.Conditions == nil {
		t.Conditions = []Condition{}
	}
	return t, nil
}

func (s *Store) CreateTransition(workflowID int64, in TransitionInput) (Transition, error) {
	if in.FromStateID == in.ToStateID {
		return Transition{}, ErrTransitionSameState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Transition{}, err
	}
	defer tx.Rollback()

	from, err := stateTx(tx, in.FromStateID)
	if err != nil {
		return Transition{}, err
	}
	to, err := stateTx(tx, in.ToStateID)
	if err != nil {
		return Transition{}, err
	}
	if from.WorkflowID != workflowID || to.WorkflowID != workflowID {
		return Transition{}, ErrTransitionStateMismatch
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = from.Name + " → " + to.Name
	}
	if len(name) > maxTransitionNameLength {
		return Transition{}, ErrTransitionNameLong
	}
	var seen int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_transitions WHERE workflow_id = ? AND from_state_id = ? AND to_state_id = ?`,
		workflowID, in.FromStateID, in.ToStateID,
	).Scan(&seen); err != nil {
		return Transition{}, err
	}
	if seen > 0 {
		return Transition{}, ErrTransitionExists
	}
	conditions, err := normalizeConditions(in.Conditions)
	if err != nil {
		return Transition{}, err
	}
	pos := 0
	if in.Position != nil {
		if *in.Position < 0 {
			return Transition{}, ErrInvalidPosition
		}
		pos = *in.Position
	} else {
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(position), -1) + 1 FROM workflow_transitions WHERE workflow_id = ?`, workflowID,
		).Scan(&pos); err != nil {
			return Transition{}, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, position) VALUES (?, ?, ?, ?, ?)`,
		workflowID, in.FromStateID, in.ToStateID, name, pos,
	)
	if err != nil {
		return Transition{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Transition{}, err
	}
	if err := replaceConditions(tx, id, conditions); err != nil {
		return Transition{}, err
	}
	if err := tx.Commit(); err != nil {
		return Transition{}, err
	}
	return s.GetTransition(id)
}

func (s *Store) UpdateTransition(workflowID, id int64, in TransitionInput) (Transition, error) {
	if in.FromStateID == in.ToStateID {
		return Transition{}, ErrTransitionSameState
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Transition{}, err
	}
	defer tx.Rollback()

	current, err := transitionTx(tx, id)
	if err != nil {
		return Transition{}, err
	}
	if current.WorkflowID != workflowID {
		return Transition{}, ErrTransitionNotFound
	}
	from, err := stateTx(tx, in.FromStateID)
	if err != nil {
		return Transition{}, err
	}
	to, err := stateTx(tx, in.ToStateID)
	if err != nil {
		return Transition{}, err
	}
	if from.WorkflowID != workflowID || to.WorkflowID != workflowID {
		return Transition{}, ErrTransitionStateMismatch
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = from.Name + " → " + to.Name
	}
	if len(name) > maxTransitionNameLength {
		return Transition{}, ErrTransitionNameLong
	}
	var seen int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_transitions WHERE workflow_id = ? AND from_state_id = ? AND to_state_id = ? AND id <> ?`,
		workflowID, in.FromStateID, in.ToStateID, id,
	).Scan(&seen); err != nil {
		return Transition{}, err
	}
	if seen > 0 {
		return Transition{}, ErrTransitionExists
	}
	var conditions []ConditionInput
	if in.ReplaceConditions {
		conditions, err = normalizeConditions(in.Conditions)
		if err != nil {
			return Transition{}, err
		}
	}
	pos := current.Position
	if in.Position != nil {
		if *in.Position < 0 {
			return Transition{}, ErrInvalidPosition
		}
		pos = *in.Position
	}
	if _, err := tx.Exec(
		`UPDATE workflow_transitions SET from_state_id = ?, to_state_id = ?, name = ?, position = ? WHERE id = ?`,
		in.FromStateID, in.ToStateID, name, pos, id,
	); err != nil {
		return Transition{}, err
	}
	if in.ReplaceConditions {
		if err := replaceConditions(tx, id, conditions); err != nil {
			return Transition{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Transition{}, err
	}
	return s.GetTransition(id)
}

func (s *Store) DeleteTransition(workflowID, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := transitionTx(tx, id)
	if err != nil {
		return err
	}
	if current.WorkflowID != workflowID {
		return ErrTransitionNotFound
	}
	if _, err := tx.Exec(`DELETE FROM transition_conditions WHERE transition_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workflow_transitions WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Available returns the arrows the actor can use from the item's current
// state. Guests never receive any arrow: the role matrix says they only read.
func (s *Store) Available(subject Subject, actor Actor) ([]Transition, error) {
	if actor.Role == "guest" {
		return []Transition{}, nil
	}
	rows, err := s.db.Query(`
		SELECT t.id, t.workflow_id, t.from_state_id, fs.name, t.to_state_id, ts.name, t.name, t.position
		FROM workflow_transitions t
		JOIN workflow_states fs ON fs.id = t.from_state_id
		JOIN workflow_states ts ON ts.id = t.to_state_id
		WHERE t.from_state_id = ?
		ORDER BY t.position, t.id`, subject.StateID)
	if err != nil {
		return nil, err
	}
	out := []Transition{}
	ids := []int64{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.WorkflowID, &t.FromStateID, &t.FromStateName, &t.ToStateID, &t.ToStateName, &t.Name, &t.Position); err != nil {
			rows.Close()
			return nil, err
		}
		t.Conditions = []Condition{}
		out = append(out, t)
		ids = append(ids, t.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	byTransition, err := s.conditionsFor(ids)
	if err != nil {
		return nil, err
	}
	filtered := make([]Transition, 0, len(out))
	for _, t := range out {
		if list, ok := byTransition[t.ID]; ok {
			t.Conditions = list
		}
		if conditionsMet(t.Conditions, subject, actor) {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// Check resolves one arrow and verifies both the source state and every
// condition. It is the single gate used by the HTTP layer and by automations.
func (s *Store) Check(transitionID int64, subject Subject, actor Actor) (Transition, error) {
	if actor.Role == "guest" {
		return Transition{}, ErrGuestTransition
	}
	t, err := s.GetTransition(transitionID)
	if err != nil {
		return Transition{}, err
	}
	if t.FromStateID != subject.StateID {
		return Transition{}, ErrTransitionNotApplicable
	}
	if !conditionsMet(t.Conditions, subject, actor) {
		return Transition{}, ErrTransitionForbidden
	}
	return t, nil
}

// Presets describes the built-in flows. The catalog is data, like the seeded
// state names; administrators can rename every label after creating a flow.
type Preset struct {
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	TeamKind    string             `json:"team_kind"`
	Description string             `json:"description"`
	States      []PresetState      `json:"states"`
	Transitions []PresetTransition `json:"transitions"`
}

type PresetState struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type PresetTransition struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	Name       string           `json:"name"`
	Conditions []ConditionInput `json:"conditions"`
}

func conditionsMet(conditions []Condition, subject Subject, actor Actor) bool {
	roles := []string{}
	priorities := []string{}
	types := []string{}
	assigneeRequired := false
	for _, c := range conditions {
		switch c.Kind {
		case "role":
			roles = append(roles, c.Value)
		case "assignee":
			assigneeRequired = assigneeRequired || c.Value == "yes"
		case "priority":
			priorities = append(priorities, c.Value)
		case "item_type":
			types = append(types, c.Value)
		}
	}
	if len(roles) > 0 && !containsString(roles, actor.Role) {
		return false
	}
	if assigneeRequired && !containsID(subject.AssigneeIDs, actor.UserID) {
		return false
	}
	if len(priorities) > 0 && !containsString(priorities, subject.Priority) {
		return false
	}
	if len(types) > 0 && !containsString(types, subject.Type) {
		return false
	}
	return true
}

func normalizeConditions(in []ConditionInput) ([]ConditionInput, error) {
	if len(in) > maxConditionsPerRoute {
		return nil, ErrInvalidCondition
	}
	seen := map[string]bool{}
	out := []ConditionInput{}
	for _, c := range in {
		kind := strings.ToLower(strings.TrimSpace(c.Kind))
		value := strings.ToLower(strings.TrimSpace(c.Value))
		switch kind {
		case "role":
			if value != "admin" && value != "manager" && value != "member" {
				return nil, ErrInvalidCondition
			}
		case "assignee":
			if value != "yes" {
				return nil, ErrInvalidCondition
			}
		case "priority":
			if !validPriority(value) {
				return nil, ErrInvalidCondition
			}
		case "item_type":
			if !validType(value) {
				return nil, ErrInvalidCondition
			}
		default:
			return nil, ErrInvalidCondition
		}
		key := kind + "\x00" + value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ConditionInput{Kind: kind, Value: value})
	}
	return out, nil
}

func replaceConditions(tx *sql.Tx, transitionID int64, conditions []ConditionInput) error {
	if _, err := tx.Exec(`DELETE FROM transition_conditions WHERE transition_id = ?`, transitionID); err != nil {
		return err
	}
	for _, c := range conditions {
		if _, err := tx.Exec(
			`INSERT INTO transition_conditions (transition_id, kind, value) VALUES (?, ?, ?)`,
			transitionID, c.Kind, c.Value,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) conditionsFor(ids []int64) (map[int64][]Condition, error) {
	out := map[int64][]Condition{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT id, transition_id, kind, value FROM transition_conditions WHERE transition_id IN (`+placeholders+`) ORDER BY id`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Condition
		if err := rows.Scan(&c.ID, &c.TransitionID, &c.Kind, &c.Value); err != nil {
			return nil, err
		}
		out[c.TransitionID] = append(out[c.TransitionID], c)
	}
	return out, rows.Err()
}

func (s *Store) hydrate(wf *Workflow) error {
	states, err := s.statesFor(wf.ID)
	if err != nil {
		return err
	}
	wf.States = states
	for _, state := range states {
		if state.Initial {
			wf.InitialStateID = state.ID
			break
		}
	}
	transitions, err := s.transitionsFor(wf.ID)
	if err != nil {
		return err
	}
	wf.Transitions = transitions
	rows, err := s.db.Query(`SELECT item_type FROM project_workflows WHERE workflow_id = ? ORDER BY item_type`, wf.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	wf.ItemTypes = []string{}
	for rows.Next() {
		var itemType string
		if err := rows.Scan(&itemType); err != nil {
			return err
		}
		wf.ItemTypes = append(wf.ItemTypes, itemType)
	}
	return rows.Err()
}

func (s *Store) statesFor(workflowID int64) ([]State, error) {
	rows, err := s.db.Query(`
		SELECT ws.id, ws.workflow_id, ws.name, ws.category, ws.position, ws.is_initial, ws.created_at,
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id),
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id AND i.archived_at IS NULL)
		FROM workflow_states ws
		WHERE ws.workflow_id = ?
		ORDER BY ws.position, ws.id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		var st State
		var initial int
		if err := rows.Scan(&st.ID, &st.WorkflowID, &st.Name, &st.Category, &st.Position, &initial, &st.CreatedAt, &st.ItemCount, &st.ActiveItemCount); err != nil {
			return nil, err
		}
		st.Initial = initial == 1
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) transitionsFor(workflowID int64) ([]Transition, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.workflow_id, t.from_state_id, fs.name, t.to_state_id, ts.name, t.name, t.position
		FROM workflow_transitions t
		JOIN workflow_states fs ON fs.id = t.from_state_id
		JOIN workflow_states ts ON ts.id = t.to_state_id
		WHERE t.workflow_id = ?
		ORDER BY t.position, t.id`, workflowID)
	if err != nil {
		return nil, err
	}
	out := []Transition{}
	ids := []int64{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.WorkflowID, &t.FromStateID, &t.FromStateName, &t.ToStateID, &t.ToStateName, &t.Name, &t.Position); err != nil {
			rows.Close()
			return nil, err
		}
		t.Conditions = []Condition{}
		out = append(out, t)
		ids = append(ids, t.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	byTransition, err := s.conditionsFor(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if list, ok := byTransition[out[i].ID]; ok {
			out[i].Conditions = list
		}
	}
	return out, nil
}

func (s *Store) ensureProject(projectID int64) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrWorkflowNotFound
	}
	return nil
}

func statesTx(tx *sql.Tx, workflowID int64) ([]State, error) {
	rows, err := tx.Query(`SELECT id, workflow_id, name, category, position, is_initial, created_at FROM workflow_states WHERE workflow_id = ? ORDER BY position, id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		var st State
		var initial int
		if err := rows.Scan(&st.ID, &st.WorkflowID, &st.Name, &st.Category, &st.Position, &initial, &st.CreatedAt); err != nil {
			return nil, err
		}
		st.Initial = initial == 1
		out = append(out, st)
	}
	return out, rows.Err()
}

func stateTx(tx *sql.Tx, id int64) (State, error) {
	var st State
	var initial int
	err := tx.QueryRow(
		`SELECT id, workflow_id, name, category, position, is_initial, created_at FROM workflow_states WHERE id = ?`, id,
	).Scan(&st.ID, &st.WorkflowID, &st.Name, &st.Category, &st.Position, &initial, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrStateNotFound
	}
	st.Initial = initial == 1
	return st, err
}

func transitionTx(tx *sql.Tx, id int64) (Transition, error) {
	var t Transition
	err := tx.QueryRow(
		`SELECT id, workflow_id, from_state_id, to_state_id, name, position FROM workflow_transitions WHERE id = ?`, id,
	).Scan(&t.ID, &t.WorkflowID, &t.FromStateID, &t.ToStateID, &t.Name, &t.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return Transition{}, ErrTransitionNotFound
	}
	return t, err
}

func ensureWorkflowExists(tx *sql.Tx, id int64) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrWorkflowNotFound
	}
	return nil
}

func ensureWorkflowNameFree(tx *sql.Tx, projectID int64, name string, excludeID int64) error {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflows WHERE project_id = ? AND name = ? COLLATE NOCASE AND id <> ?`,
		projectID, name, excludeID,
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrWorkflowNameTaken
	}
	return nil
}

func ensureStateNameFree(tx *sql.Tx, workflowID int64, name string, excludeID int64) error {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ? AND name = ? COLLATE NOCASE AND id <> ?`,
		workflowID, name, excludeID,
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrStateNameTaken
	}
	return nil
}

func countStates(tx *sql.Tx, workflowID int64, category string, excludeID int64) (int, error) {
	var count int
	err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ? AND category = ? AND id <> ?`,
		workflowID, category, excludeID,
	).Scan(&count)
	return count, err
}

func insertPreset(tx *sql.Tx, workflowID int64, preset Preset) error {
	stateIDs := map[string]int64{}
	for i, state := range preset.States {
		initial := false
		if i == 0 {
			initial = true
		}
		res, err := tx.Exec(`
			INSERT INTO workflow_states (workflow_id, name, category, position, is_initial, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			workflowID, state.Name, state.Category, i, boolToInt(initial), timestamp(),
		)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		stateIDs[state.Key] = id
	}
	for i, transition := range preset.Transitions {
		from, ok := stateIDs[transition.From]
		if !ok {
			return fmt.Errorf("preset %s: unknown state %s", preset.Slug, transition.From)
		}
		to, ok := stateIDs[transition.To]
		if !ok {
			return fmt.Errorf("preset %s: unknown state %s", preset.Slug, transition.To)
		}
		name := strings.TrimSpace(transition.Name)
		if name == "" {
			name = preset.StateName(transition.From) + " → " + preset.StateName(transition.To)
		}
		res, err := tx.Exec(`
			INSERT INTO workflow_transitions (workflow_id, from_state_id, to_state_id, name, position)
			VALUES (?, ?, ?, ?, ?)`, workflowID, from, to, name, i)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		conditions, err := normalizeConditions(transition.Conditions)
		if err != nil {
			return err
		}
		if err := replaceConditions(tx, id, conditions); err != nil {
			return err
		}
	}
	return nil
}

// StateName resolves a preset state key to its label, for default arrow names.
func (p Preset) StateName(key string) string {
	for _, state := range p.States {
		if state.Key == key {
			return state.Name
		}
	}
	return key
}

// SeedDefault creates the default flow of a brand-new project and binds it to
// all four item types. It runs inside the project creation transaction.
func SeedDefault(tx *sql.Tx, projectID int64) error {
	preset, ok := PresetBySlug("default")
	if !ok {
		return ErrPresetNotFound
	}
	res, err := tx.Exec(
		`INSERT INTO workflows (project_id, name, preset, created_at) VALUES (?, ?, ?, ?)`,
		projectID, preset.Name, preset.Slug, timestamp(),
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := insertPreset(tx, id, preset); err != nil {
		return err
	}
	for _, itemType := range ItemTypes {
		if _, err := tx.Exec(
			`INSERT INTO project_workflows (project_id, item_type, workflow_id) VALUES (?, ?, ?)`,
			projectID, itemType, id,
		); err != nil {
			return err
		}
	}
	return nil
}

func insertStateEvent(tx *sql.Tx, itemID, actorID, fromID, toID int64, at string) error {
	_, err := tx.Exec(`
		INSERT INTO item_events (item_id, actor_id, kind, field, from_value, to_value, created_at)
		VALUES (?, ?, 'state_changed', 'state', ?, ?, ?)`,
		itemID, actorID, fmt.Sprintf("%d", fromID), fmt.Sprintf("%d", toID), at,
	)
	return err
}

func validItemType(value string) bool {
	for _, itemType := range ItemTypes {
		if itemType == value {
			return true
		}
	}
	return false
}

func validPriority(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "urgent":
		return true
	}
	return false
}

func validType(value string) bool {
	return validItemType(value)
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func containsID(list []int64, value int64) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
