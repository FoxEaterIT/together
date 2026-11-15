// Package workflow owns the installation-wide workflow: states, transitions,
// transition conditions and the initial state for new items. There is one
// workflow for every project and every item type.
package workflow

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
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
	ErrInvalidCondition        = errors.New("invalid transition condition")
	ErrInvalidPosition         = errors.New("position cannot be negative")
	ErrTransitionNotApplicable = errors.New("transition does not start from the current state")
	ErrTransitionForbidden     = errors.New("transition conditions are not met")
	ErrGuestTransition         = errors.New("guests cannot change item state")
)

const (
	maxStateNameLength      = 80
	maxTransitionNameLength = 120
	maxConditionsPerRoute   = 50
)

type State struct {
	ID              int64  `json:"id"`
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
	FromStateID   int64       `json:"from_state_id"`
	FromStateName string      `json:"from_state_name"`
	ToStateID     int64       `json:"to_state_id"`
	ToStateName   string      `json:"to_state_name"`
	Name          string      `json:"name"`
	Position      int         `json:"position"`
	Conditions    []Condition `json:"conditions"`
}

type Workflow struct {
	States         []State      `json:"states"`
	Transitions    []Transition `json:"transitions"`
	InitialStateID int64        `json:"initial_state_id"`
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

func (s *Store) Get() (Workflow, error) {
	states, err := s.ListStates()
	if err != nil {
		return Workflow{}, err
	}
	transitions, err := s.ListTransitions()
	if err != nil {
		return Workflow{}, err
	}
	var initial sql.NullInt64
	if err := s.db.QueryRow(`SELECT initial_state_id FROM settings WHERE id = 1`).Scan(&initial); err != nil {
		return Workflow{}, err
	}
	wf := Workflow{States: states, Transitions: transitions}
	if initial.Valid {
		wf.InitialStateID = initial.Int64
	}
	return wf, nil
}

func (s *Store) ListStates() ([]State, error) {
	rows, err := s.db.Query(`
		SELECT ws.id, ws.name, ws.category, ws.position, ws.created_at,
		       CASE WHEN ws.id = COALESCE((SELECT initial_state_id FROM settings WHERE id = 1), 0) THEN 1 ELSE 0 END,
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id),
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id AND i.archived_at IS NULL)
		FROM workflow_states ws
		ORDER BY ws.position, ws.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		var st State
		var initial int
		if err := rows.Scan(&st.ID, &st.Name, &st.Category, &st.Position, &st.CreatedAt, &initial, &st.ItemCount, &st.ActiveItemCount); err != nil {
			return nil, err
		}
		st.Initial = initial == 1
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetState(id int64) (State, error) {
	var st State
	var initial int
	err := s.db.QueryRow(`
		SELECT ws.id, ws.name, ws.category, ws.position, ws.created_at,
		       CASE WHEN ws.id = COALESCE((SELECT initial_state_id FROM settings WHERE id = 1), 0) THEN 1 ELSE 0 END,
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id),
		       (SELECT COUNT(*) FROM items i WHERE i.state_id = ws.id AND i.archived_at IS NULL)
		FROM workflow_states ws WHERE ws.id = ?`, id).
		Scan(&st.ID, &st.Name, &st.Category, &st.Position, &st.CreatedAt, &initial, &st.ItemCount, &st.ActiveItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrStateNotFound
	}
	if err != nil {
		return State{}, err
	}
	st.Initial = initial == 1
	return st, nil
}

func (s *Store) CreateState(name, category string, position *int) (State, error) {
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

	if err := ensureStateNameFree(tx, name, 0); err != nil {
		return State{}, err
	}
	pos := 0
	if position != nil {
		if *position < 0 {
			return State{}, ErrInvalidPosition
		}
		pos = *position
	} else {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM workflow_states`).Scan(&pos); err != nil {
			return State{}, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO workflow_states (name, category, position, created_at) VALUES (?, ?, ?, ?)`,
		name, category, pos, timestamp(),
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

// UpdateState changes the label, category or order. Moving the last open or
// done state to the other category would break the workflow invariant, so the
// change is rejected instead.
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
	if err := ensureStateNameFree(tx, name, id); err != nil {
		return State{}, err
	}
	if current.Category != category {
		remaining, err := countStates(tx, current.Category, id)
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
	if _, err := tx.Exec(`UPDATE workflow_states SET name = ?, category = ?, position = ? WHERE id = ?`, name, category, pos, id); err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return s.GetState(id)
}

// DeleteState removes a state and any arrows that touch it. It refuses to
// delete the initial state, the last state of a category, or a state still
// referenced by an item. Archived items count: deleting their state would make
// their history unreadable.
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
	var initial int64
	if err := tx.QueryRow(`SELECT COALESCE(initial_state_id, 0) FROM settings WHERE id = 1`).Scan(&initial); err != nil {
		return err
	}
	if initial == id {
		return ErrInitialState
	}
	remaining, err := countStates(tx, current.Category, id)
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

func (s *Store) SetInitialState(id int64) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrStateNotFound
	}
	_, err := s.db.Exec(`UPDATE settings SET initial_state_id = ? WHERE id = 1`, id)
	return err
}

func (s *Store) ListTransitions() ([]Transition, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.from_state_id, fs.name, t.to_state_id, ts.name, t.name, t.position
		FROM workflow_transitions t
		JOIN workflow_states fs ON fs.id = t.from_state_id
		JOIN workflow_states ts ON ts.id = t.to_state_id
		ORDER BY t.position, t.id`)
	if err != nil {
		return nil, err
	}
	out := []Transition{}
	ids := []int64{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.FromStateID, &t.FromStateName, &t.ToStateID, &t.ToStateName, &t.Name, &t.Position); err != nil {
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

func (s *Store) GetTransition(id int64) (Transition, error) {
	var t Transition
	err := s.db.QueryRow(`
		SELECT t.id, t.from_state_id, fs.name, t.to_state_id, ts.name, t.name, t.position
		FROM workflow_transitions t
		JOIN workflow_states fs ON fs.id = t.from_state_id
		JOIN workflow_states ts ON ts.id = t.to_state_id
		WHERE t.id = ?`, id).
		Scan(&t.ID, &t.FromStateID, &t.FromStateName, &t.ToStateID, &t.ToStateName, &t.Name, &t.Position)
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

func (s *Store) CreateTransition(in TransitionInput) (Transition, error) {
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
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = from.Name + " → " + to.Name
	}
	if len(name) > maxTransitionNameLength {
		return Transition{}, ErrTransitionNameLong
	}
	var seen int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_transitions WHERE from_state_id = ? AND to_state_id = ?`, in.FromStateID, in.ToStateID).Scan(&seen); err != nil {
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
		if err := tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM workflow_transitions`).Scan(&pos); err != nil {
			return Transition{}, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO workflow_transitions (from_state_id, to_state_id, name, position) VALUES (?, ?, ?, ?)`,
		in.FromStateID, in.ToStateID, name, pos,
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

func (s *Store) UpdateTransition(id int64, in TransitionInput) (Transition, error) {
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
	from, err := stateTx(tx, in.FromStateID)
	if err != nil {
		return Transition{}, err
	}
	to, err := stateTx(tx, in.ToStateID)
	if err != nil {
		return Transition{}, err
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
		`SELECT COUNT(*) FROM workflow_transitions WHERE from_state_id = ? AND to_state_id = ? AND id <> ?`,
		in.FromStateID, in.ToStateID, id,
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

func (s *Store) DeleteTransition(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_transitions WHERE id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
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
	all, err := s.ListTransitions()
	if err != nil {
		return nil, err
	}
	out := []Transition{}
	for _, t := range all {
		if t.FromStateID != subject.StateID {
			continue
		}
		if conditionsMet(t.Conditions, subject, actor) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Check resolves one arrow and verifies both the source state and every
// condition. It is the single gate used by the HTTP layer and, later, by the
// automation engine.
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

func stateTx(tx *sql.Tx, id int64) (State, error) {
	var st State
	err := tx.QueryRow(`SELECT id, name, category, position, created_at FROM workflow_states WHERE id = ?`, id).
		Scan(&st.ID, &st.Name, &st.Category, &st.Position, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrStateNotFound
	}
	return st, err
}

func transitionTx(tx *sql.Tx, id int64) (Transition, error) {
	var t Transition
	err := tx.QueryRow(`SELECT id, from_state_id, to_state_id, name, position FROM workflow_transitions WHERE id = ?`, id).
		Scan(&t.ID, &t.FromStateID, &t.ToStateID, &t.Name, &t.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return Transition{}, ErrTransitionNotFound
	}
	return t, err
}

func ensureStateNameFree(tx *sql.Tx, name string, excludeID int64) error {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM workflow_states WHERE name = ? COLLATE NOCASE AND id <> ?`,
		name, excludeID,
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrStateNameTaken
	}
	return nil
}

func countStates(tx *sql.Tx, category string, excludeID int64) (int, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE category = ? AND id <> ?`, category, excludeID).Scan(&count)
	return count, err
}

func validPriority(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "urgent":
		return true
	}
	return false
}

func validType(value string) bool {
	switch value {
	case "observer", "action", "micro", "bug":
		return true
	}
	return false
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

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
