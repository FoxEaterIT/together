package workflow_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/items"
	"jiralike/internal/projects"
	"jiralike/internal/workflow"
)

type fixture struct {
	db         *sql.DB
	store      *workflow.Store
	workflowID int64
	states     map[string]workflow.State
	adminID    int64
	openID     int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	store := workflow.New(conn)
	adminID := mkUser(t, conn, "Admin", "admin@example.test", "admin")
	open, err := projects.New(conn).Create("Open", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := store.WorkflowFor(open.ID, "action")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]workflow.State{}
	for _, st := range wf.States {
		states[st.Name] = st
	}
	return fixture{db: conn, store: store, workflowID: wf.ID, states: states, adminID: adminID, openID: open.ID}
}

func mkUser(t *testing.T, conn *sql.DB, name, email, role string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := conn.Exec(
		`INSERT INTO users (name, email, password_hash, role, locale, email_notifications, created_at) VALUES (?, ?, 'x', ?, 'en', 0, ?)`,
		name, email, role, now,
	)
	if err != nil {
		t.Fatalf("insert %s: %v", role, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTransition(t *testing.T, store *workflow.Store, workflowID, from, to int64, name string, conditions ...workflow.ConditionInput) workflow.Transition {
	t.Helper()
	tr, err := store.CreateTransition(workflowID, workflow.TransitionInput{
		FromStateID: from,
		ToStateID:   to,
		Name:        name,
		Conditions:  conditions,
	})
	if err != nil {
		t.Fatalf("create transition %q: %v", name, err)
	}
	return tr
}

func TestSeedStatesAndStateCRUD(t *testing.T) {
	f := setup(t)
	if len(f.states) != 3 {
		t.Fatalf("seeded states = %+v", f.states)
	}
	todo := f.states["To do"]
	done := f.states["Done"]
	if todo.Category != "open" || done.Category != "done" {
		t.Fatalf("seed categories: %+v %+v", todo, done)
	}

	wf, err := f.store.Get(f.workflowID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.InitialStateID != todo.ID {
		t.Fatalf("initial state = %d, want %d", wf.InitialStateID, todo.ID)
	}
	if !todo.Initial {
		t.Fatalf("To do should be marked initial: %+v", todo)
	}
	if len(wf.ItemTypes) != 4 {
		t.Fatalf("default flow item types = %+v", wf.ItemTypes)
	}

	// Position defaults to the end, and names are unique case-insensitively
	// inside the flow only.
	created, err := f.store.CreateState(f.workflowID, "Backlog", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Position <= todo.Position {
		t.Fatalf("created position = %d, todo = %d", created.Position, todo.Position)
	}
	if _, err := f.store.CreateState(f.workflowID, "backlog", "open", nil); !errors.Is(err, workflow.ErrStateNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := f.store.CreateState(f.workflowID, "  ", "open", nil); !errors.Is(err, workflow.ErrStateNameEmpty) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := f.store.CreateState(f.workflowID, "Weird", "paused", nil); !errors.Is(err, workflow.ErrInvalidCategory) {
		t.Fatalf("bad category: %v", err)
	}

	// The same state name is free in another flow of the same project.
	other, err := f.store.Create(f.openID, "Other", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateState(other.ID, "Backlog", "open", nil); err != nil {
		t.Fatalf("same name in another flow: %v", err)
	}

	// Rename and reorder.
	pos := 0
	renamed, err := f.store.UpdateState(created.ID, "Icebox", "open", &pos)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Icebox" || renamed.Position != 0 {
		t.Fatalf("renamed = %+v", renamed)
	}
	if _, err := f.store.UpdateState(created.ID, "To do", "open", nil); !errors.Is(err, workflow.ErrStateNameTaken) {
		t.Fatalf("rename collision: %v", err)
	}

	// The last open or done state cannot switch category.
	if _, err := f.store.UpdateState(done.ID, "Done", "open", nil); !errors.Is(err, workflow.ErrLastDoneState) {
		t.Fatalf("last done change: %v", err)
	}
	if _, err := f.store.UpdateState(done.ID, "Done", "done", nil); err != nil {
		t.Fatalf("same category: %v", err)
	}

	// Initial state can be moved, but the state it points to cannot be deleted.
	if err := f.store.SetInitialState(f.workflowID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(created.ID); !errors.Is(err, workflow.ErrInitialState) {
		t.Fatalf("delete initial: %v", err)
	}
	if err := f.store.SetInitialState(f.workflowID, 9999); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing initial: %v", err)
	}
	// A state of another flow is not a valid initial state.
	if err := f.store.SetInitialState(f.workflowID, other.States[0].ID); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("foreign initial: %v", err)
	}
	if err := f.store.SetInitialState(f.workflowID, todo.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(created.ID); err != nil {
		t.Fatalf("delete unused state: %v", err)
	}

	// A state used by an item cannot be deleted.
	itemStore := items.New(f.db)
	if _, err := itemStore.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Used", ActorID: f.adminID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetInitialState(f.workflowID, f.states["In progress"].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(todo.ID); !errors.Is(err, workflow.ErrStateInUse) {
		t.Fatalf("delete used state: %v", err)
	}

	// Deleting the last open state is refused even if it is unused.
	extra, err := f.store.CreateState(f.workflowID, "Temp", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateState(extra.ID, "Temp", "done", nil); err != nil {
		t.Fatalf("second open to done: %v", err)
	}
	// Two open states remain (To do and In progress); move one to done, then
	// the final open state cannot follow.
	if _, err := f.store.UpdateState(f.states["In progress"].ID, "In progress", "done", nil); err != nil {
		t.Fatalf("one of two open states to done: %v", err)
	}
	if _, err := f.store.UpdateState(todo.ID, "To do", "done", nil); !errors.Is(err, workflow.ErrLastOpenState) {
		t.Fatalf("last open change: %v", err)
	}
}

func TestTransitionCRUDAndConditions(t *testing.T) {
	f := setup(t)
	todo := f.states["To do"]
	progress := f.states["In progress"]
	done := f.states["Done"]

	// A blank name is derived from the two states.
	tr := mustTransition(t, f.store, f.workflowID, todo.ID, progress.ID, "")
	if tr.Name == "" || tr.FromStateName != "To do" || tr.ToStateName != "In progress" || len(tr.Conditions) != 0 {
		t.Fatalf("created transition = %+v", tr)
	}
	if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{FromStateID: todo.ID, ToStateID: progress.ID}); !errors.Is(err, workflow.ErrTransitionExists) {
		t.Fatalf("duplicate transition: %v", err)
	}
	if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{FromStateID: todo.ID, ToStateID: todo.ID}); !errors.Is(err, workflow.ErrTransitionSameState) {
		t.Fatalf("same-state transition: %v", err)
	}
	if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{FromStateID: 9999, ToStateID: done.ID}); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing source state: %v", err)
	}
	other, err := f.store.Create(f.openID, "Other", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{FromStateID: todo.ID, ToStateID: other.States[0].ID}); !errors.Is(err, workflow.ErrTransitionStateMismatch) {
		t.Fatalf("cross-flow transition: %v", err)
	}
	if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{
		FromStateID: todo.ID, ToStateID: done.ID,
		Conditions: []workflow.ConditionInput{{Kind: "script", Value: "return true"}},
	}); !errors.Is(err, workflow.ErrInvalidCondition) {
		t.Fatalf("invalid condition kind: %v", err)
	}
	for _, bad := range []workflow.ConditionInput{
		{Kind: "role", Value: "guest"},
		{Kind: "assignee", Value: "no"},
		{Kind: "priority", Value: "blocker"},
		{Kind: "item_type", Value: "epic"},
	} {
		if _, err := f.store.CreateTransition(f.workflowID, workflow.TransitionInput{
			FromStateID: todo.ID, ToStateID: done.ID, Conditions: []workflow.ConditionInput{bad},
		}); !errors.Is(err, workflow.ErrInvalidCondition) {
			t.Fatalf("condition %+v: %v", bad, err)
		}
	}

	// Duplicate conditions collapse, while different conditions of the same
	// kind are kept to express OR.
	withConds := mustTransition(t, f.store, f.workflowID, todo.ID, done.ID, "Finish", []workflow.ConditionInput{
		{Kind: "role", Value: "manager"},
		{Kind: "ROLE", Value: " ADMIN "},
		{Kind: "role", Value: "manager"},
		{Kind: "priority", Value: "HIGH"},
		{Kind: "item_type", Value: "bug"},
		{Kind: "assignee", Value: "yes"},
	}...)
	if len(withConds.Conditions) != 5 {
		t.Fatalf("conditions = %+v", withConds.Conditions)
	}

	// Replacing conditions keeps the rest of the arrow.
	name := "Finish now"
	updated, err := f.store.UpdateTransition(f.workflowID, withConds.ID, workflow.TransitionInput{
		FromStateID:       todo.ID,
		ToStateID:         done.ID,
		Name:              name,
		Conditions:        []workflow.ConditionInput{{Kind: "role", Value: "admin"}},
		ReplaceConditions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || len(updated.Conditions) != 1 || updated.Conditions[0].Value != "admin" {
		t.Fatalf("updated transition = %+v", updated)
	}

	// Moving onto an existing pair is refused.
	third := mustTransition(t, f.store, f.workflowID, progress.ID, done.ID, "Close")
	if _, err := f.store.UpdateTransition(f.workflowID, third.ID, workflow.TransitionInput{FromStateID: todo.ID, ToStateID: progress.ID, Name: "Close"}); !errors.Is(err, workflow.ErrTransitionExists) {
		t.Fatalf("move onto existing pair: %v", err)
	}

	// Deleting a state removes its arrows and their conditions.
	if err := f.store.SetInitialState(f.workflowID, progress.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(todo.ID); err != nil {
		t.Fatalf("delete state with arrows: %v", err)
	}
	if _, err := f.store.GetTransition(withConds.ID); !errors.Is(err, workflow.ErrTransitionNotFound) {
		t.Fatalf("arrow survived state delete: %v", err)
	}
	var conds int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM transition_conditions WHERE transition_id = ?`, withConds.ID).Scan(&conds); err != nil {
		t.Fatal(err)
	}
	if conds != 0 {
		t.Fatalf("orphan conditions = %d", conds)
	}

	// Explicit arrow deletion.
	if err := f.store.DeleteTransition(f.workflowID, third.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteTransition(f.workflowID, third.ID); !errors.Is(err, workflow.ErrTransitionNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	// An arrow of another flow cannot be deleted through this one.
	foreignFrom, err := f.store.CreateState(other.ID, "Foreign from", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignTo, err := f.store.CreateState(other.ID, "Foreign to", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := mustTransition(t, f.store, other.ID, foreignFrom.ID, foreignTo.ID, "Foreign")
	if err := f.store.DeleteTransition(f.workflowID, foreign.ID); !errors.Is(err, workflow.ErrTransitionNotFound) {
		t.Fatalf("foreign arrow delete: %v", err)
	}
}

func TestTransitionEngineConditions(t *testing.T) {
	f := setup(t)
	todo := f.states["To do"]
	progress := f.states["In progress"]
	done := f.states["Done"]
	blocked, err := f.store.CreateState(f.workflowID, "Blocked", "done", nil)
	if err != nil {
		t.Fatal(err)
	}

	none := mustTransition(t, f.store, f.workflowID, todo.ID, progress.ID, "Start")
	role := mustTransition(t, f.store, f.workflowID, todo.ID, done.ID, "Manager close",
		workflow.ConditionInput{Kind: "role", Value: "manager"},
		workflow.ConditionInput{Kind: "role", Value: "admin"},
	)
	assignee := mustTransition(t, f.store, f.workflowID, todo.ID, blocked.ID, "Block myself",
		workflow.ConditionInput{Kind: "assignee", Value: "yes"},
	)
	typed := mustTransition(t, f.store, f.workflowID, progress.ID, done.ID, "Fix bug",
		workflow.ConditionInput{Kind: "priority", Value: "urgent"},
		workflow.ConditionInput{Kind: "item_type", Value: "bug"},
	)

	member := workflow.Actor{UserID: 10, Role: "member"}
	manager := workflow.Actor{UserID: 11, Role: "manager"}
	admin := workflow.Actor{UserID: 12, Role: "admin"}
	subject := workflow.Subject{StateID: todo.ID, Type: "action", Priority: "high", AssigneeIDs: []int64{manager.UserID}}

	available, err := f.store.Available(subject, member)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].ID != none.ID {
		t.Fatalf("member available = %+v", available)
	}
	available, err = f.store.Available(subject, manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 3 {
		t.Fatalf("manager available = %+v", available)
	}
	available, err = f.store.Available(subject, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 2 {
		t.Fatalf("admin available = %+v", available)
	}
	available, err = f.store.Available(subject, workflow.Actor{UserID: 99, Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 0 {
		t.Fatalf("guest available = %+v", available)
	}

	// Conditions of different kinds are ANDed.
	bugUrgent := workflow.Subject{StateID: progress.ID, Type: "bug", Priority: "urgent"}
	if _, err := f.store.Check(typed.ID, bugUrgent, member); err != nil {
		t.Fatalf("bug urgent: %v", err)
	}
	if _, err := f.store.Check(typed.ID, workflow.Subject{StateID: progress.ID, Type: "action", Priority: "urgent"}, member); !errors.Is(err, workflow.ErrTransitionForbidden) {
		t.Fatalf("wrong type: %v", err)
	}
	if _, err := f.store.Check(typed.ID, workflow.Subject{StateID: progress.ID, Type: "bug", Priority: "low"}, member); !errors.Is(err, workflow.ErrTransitionForbidden) {
		t.Fatalf("wrong priority: %v", err)
	}

	// The source state must match.
	if _, err := f.store.Check(none.ID, workflow.Subject{StateID: progress.ID}, member); !errors.Is(err, workflow.ErrTransitionNotApplicable) {
		t.Fatalf("wrong source: %v", err)
	}
	// Role conditions are OR inside the role group.
	if _, err := f.store.Check(role.ID, subject, member); !errors.Is(err, workflow.ErrTransitionForbidden) {
		t.Fatalf("member role: %v", err)
	}
	if _, err := f.store.Check(role.ID, subject, manager); err != nil {
		t.Fatalf("manager role: %v", err)
	}
	// Assignee compares the actor with the item's assignees.
	if _, err := f.store.Check(assignee.ID, subject, member); !errors.Is(err, workflow.ErrTransitionForbidden) {
		t.Fatalf("not assigned: %v", err)
	}
	if _, err := f.store.Check(assignee.ID, subject, manager); err != nil {
		t.Fatalf("assigned: %v", err)
	}
	// Guests never transition, even on an unconditional arrow.
	if _, err := f.store.Check(none.ID, subject, workflow.Actor{UserID: 99, Role: "guest"}); !errors.Is(err, workflow.ErrGuestTransition) {
		t.Fatalf("guest check: %v", err)
	}
	if _, err := f.store.Check(9999, subject, admin); !errors.Is(err, workflow.ErrTransitionNotFound) {
		t.Fatalf("missing transition: %v", err)
	}
}

func TestProjectFlowsAreIndependent(t *testing.T) {
	f := setup(t)
	second, err := projects.New(f.db).Create("Second", "", "open", f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.WorkflowFor(second.ID, "action")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == f.workflowID {
		t.Fatalf("projects share workflow %d", other.ID)
	}
	// Renaming a state in one project does not touch the other.
	dev, err := f.store.Create(f.openID, "Dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateState(dev.States[0].ID, "Ready", "open", nil); err != nil {
		t.Fatal(err)
	}
	unchanged, err := f.store.Get(f.workflowID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range unchanged.States {
		if st.Name == "Ready" {
			t.Fatalf("rename leaked into another flow: %+v", unchanged.States)
		}
	}
	if _, err := f.store.WorkflowFor(f.openID, "bug"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.WorkflowFor(f.openID, "epic"); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Fatalf("unknown item type: %v", err)
	}
}

func TestAssignTypeMovesItemsWithHistory(t *testing.T) {
	f := setup(t)
	itemStore := items.New(f.db)
	created, err := itemStore.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Move me", ActorID: f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.StateID != f.states["To do"].ID {
		t.Fatalf("initial state = %d, want %d", created.StateID, f.states["To do"].ID)
	}

	dev, err := f.store.Create(f.openID, "Dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	devStates := map[string]workflow.State{}
	for _, st := range dev.States {
		devStates[st.Name] = st
	}

	// Switching without a mapping is refused when the type already has a flow.
	if err := f.store.AssignType(f.openID, "action", dev.ID, nil, f.adminID); !errors.Is(err, workflow.ErrMappingRequired) {
		t.Fatalf("missing mapping: %v", err)
	}
	// Every old state needs a target.
	mapping := map[int64]int64{
		f.states["To do"].ID:       devStates["To do"].ID,
		f.states["In progress"].ID: devStates["In progress"].ID,
	}
	if err := f.store.AssignType(f.openID, "action", dev.ID, mapping, f.adminID); !errors.Is(err, workflow.ErrMappingRequired) {
		t.Fatalf("partial mapping: %v", err)
	}
	// Targets must belong to the new flow.
	mapping[f.states["Done"].ID] = f.states["Done"].ID
	if err := f.store.AssignType(f.openID, "action", dev.ID, mapping, f.adminID); !errors.Is(err, workflow.ErrMappingInvalid) {
		t.Fatalf("foreign target: %v", err)
	}
	mapping[f.states["Done"].ID] = devStates["Done"].ID
	if err := f.store.AssignType(f.openID, "action", dev.ID, mapping, f.adminID); err != nil {
		t.Fatal(err)
	}

	bindings, err := f.store.Bindings(f.openID)
	if err != nil {
		t.Fatal(err)
	}
	if bindings["action"] != dev.ID || bindings["bug"] != f.workflowID {
		t.Fatalf("bindings = %+v", bindings)
	}
	moved, err := itemStore.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.StateID != devStates["To do"].ID {
		t.Fatalf("moved state = %d, want %d", moved.StateID, devStates["To do"].ID)
	}
	events, err := itemStore.Events(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Kind != "state_changed" {
		t.Fatalf("mapping history = %+v", events)
	}

	// A bound flow cannot be deleted, and the old one is now free of items but
	// still bound to the other three types.
	if err := f.store.Delete(dev.ID); !errors.Is(err, workflow.ErrWorkflowInUse) {
		t.Fatalf("delete bound flow: %v", err)
	}
	if err := f.store.Delete(f.workflowID); !errors.Is(err, workflow.ErrWorkflowInUse) {
		t.Fatalf("delete default flow: %v", err)
	}

	// The new item type flow is the source of the initial state.
	initial, err := f.store.InitialStateFor(f.openID, "action")
	if err != nil {
		t.Fatal(err)
	}
	if initial != devStates["To do"].ID {
		t.Fatalf("initial for action = %d, want %d", initial, devStates["To do"].ID)
	}
	if createdAfter, err := itemStore.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Second", ActorID: f.adminID,
	}); err != nil {
		t.Fatal(err)
	} else if createdAfter.StateID != devStates["To do"].ID {
		t.Fatalf("second item state = %d, want %d", createdAfter.StateID, devStates["To do"].ID)
	}

	// An explicit state from another flow is rejected at creation.
	if _, err := itemStore.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Wrong flow", StateID: f.states["Done"].ID, ActorID: f.adminID,
	}); !errors.Is(err, items.ErrStateNotFound) {
		t.Fatalf("cross-flow create: %v", err)
	}
}

func TestPresetsAndDuplicate(t *testing.T) {
	f := setup(t)
	presets := workflow.Presets()
	if len(presets) < 5 {
		t.Fatalf("presets = %d", len(presets))
	}
	kinds := map[string]bool{}
	for _, preset := range presets {
		kinds[preset.TeamKind] = true
		if len(preset.States) == 0 {
			t.Fatalf("preset %s has no states", preset.Slug)
		}
		wf, err := f.store.Create(f.openID, "Preset "+preset.Slug, preset.Slug)
		if err != nil {
			t.Fatalf("create %s: %v", preset.Slug, err)
		}
		if wf.InitialStateID == 0 || len(wf.States) != len(preset.States) {
			t.Fatalf("preset %s flow = %+v", preset.Slug, wf)
		}
	}
	for _, kind := range []string{"generic", "pm", "dev", "design", "support", "sales"} {
		if !kinds[kind] {
			t.Fatalf("missing team kind %s: %+v", kind, kinds)
		}
	}
	if _, err := f.store.Create(f.openID, "Nope", "missing"); !errors.Is(err, workflow.ErrPresetNotFound) {
		t.Fatalf("unknown preset: %v", err)
	}

	source, err := f.store.Create(f.openID, "Source", "design")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(f.openID, "source", "dev"); !errors.Is(err, workflow.ErrWorkflowNameTaken) {
		t.Fatalf("duplicate flow name: %v", err)
	}
	copied, err := f.store.Duplicate(source.ID, f.openID, "Copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == source.ID || len(copied.States) != len(source.States) || len(copied.Transitions) != len(source.Transitions) {
		t.Fatalf("copy = %+v", copied)
	}
	if len(copied.ItemTypes) != 0 {
		t.Fatalf("copy kept bindings: %+v", copied.ItemTypes)
	}
	// Renaming and deleting an unbound copy works.
	renamed, err := f.store.Rename(copied.ID, "Copy 2")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Copy 2" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if err := f.store.Delete(copied.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Get(copied.ID); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Fatalf("deleted flow still readable: %v", err)
	}
}
