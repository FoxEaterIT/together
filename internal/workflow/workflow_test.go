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
	db      *sql.DB
	store   *workflow.Store
	states  map[string]workflow.State
	adminID int64
	openID  int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	store := workflow.New(conn)
	wf, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]workflow.State{}
	for _, st := range wf.States {
		states[st.Name] = st
	}
	adminID := mkUser(t, conn, "Admin", "admin@example.test", "admin")
	open, err := projects.New(conn).Create("Open", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: conn, store: store, states: states, adminID: adminID, openID: open.ID}
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

func mustTransition(t *testing.T, store *workflow.Store, from, to int64, name string, conditions ...workflow.ConditionInput) workflow.Transition {
	t.Helper()
	tr, err := store.CreateTransition(workflow.TransitionInput{
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

	wf, err := f.store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if wf.InitialStateID != todo.ID {
		t.Fatalf("initial state = %d, want %d", wf.InitialStateID, todo.ID)
	}
	if !todo.Initial {
		t.Fatalf("To do should be marked initial: %+v", todo)
	}

	// Position defaults to the end, and names are unique case-insensitively.
	created, err := f.store.CreateState("Backlog", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Position <= todo.Position {
		t.Fatalf("created position = %d, todo = %d", created.Position, todo.Position)
	}
	if _, err := f.store.CreateState("backlog", "open", nil); !errors.Is(err, workflow.ErrStateNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := f.store.CreateState("  ", "open", nil); !errors.Is(err, workflow.ErrStateNameEmpty) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := f.store.CreateState("Weird", "paused", nil); !errors.Is(err, workflow.ErrInvalidCategory) {
		t.Fatalf("bad category: %v", err)
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
	if err := f.store.SetInitialState(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(created.ID); !errors.Is(err, workflow.ErrInitialState) {
		t.Fatalf("delete initial: %v", err)
	}
	if err := f.store.SetInitialState(9999); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing initial: %v", err)
	}
	if err := f.store.SetInitialState(todo.ID); err != nil {
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
	if err := f.store.SetInitialState(f.states["In progress"].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteState(todo.ID); !errors.Is(err, workflow.ErrStateInUse) {
		t.Fatalf("delete used state: %v", err)
	}

	// Deleting the last open state is refused even if it is unused.
	other, err := f.store.CreateState("Temp", "open", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateState(other.ID, "Temp", "done", nil); err != nil {
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
	tr := mustTransition(t, f.store, todo.ID, progress.ID, "")
	if tr.Name == "" || tr.FromStateName != "To do" || tr.ToStateName != "In progress" || len(tr.Conditions) != 0 {
		t.Fatalf("created transition = %+v", tr)
	}
	if _, err := f.store.CreateTransition(workflow.TransitionInput{FromStateID: todo.ID, ToStateID: progress.ID}); !errors.Is(err, workflow.ErrTransitionExists) {
		t.Fatalf("duplicate transition: %v", err)
	}
	if _, err := f.store.CreateTransition(workflow.TransitionInput{FromStateID: todo.ID, ToStateID: todo.ID}); !errors.Is(err, workflow.ErrTransitionSameState) {
		t.Fatalf("same-state transition: %v", err)
	}
	if _, err := f.store.CreateTransition(workflow.TransitionInput{FromStateID: 9999, ToStateID: done.ID}); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("missing source state: %v", err)
	}
	if _, err := f.store.CreateTransition(workflow.TransitionInput{
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
		if _, err := f.store.CreateTransition(workflow.TransitionInput{
			FromStateID: todo.ID, ToStateID: done.ID, Conditions: []workflow.ConditionInput{bad},
		}); !errors.Is(err, workflow.ErrInvalidCondition) {
			t.Fatalf("condition %+v: %v", bad, err)
		}
	}

	// Duplicate conditions collapse, while different conditions of the same
	// kind are kept to express OR.
	withConds := mustTransition(t, f.store, todo.ID, done.ID, "Finish", []workflow.ConditionInput{
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
	updated, err := f.store.UpdateTransition(withConds.ID, workflow.TransitionInput{
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
	third := mustTransition(t, f.store, progress.ID, done.ID, "Close")
	if _, err := f.store.UpdateTransition(third.ID, workflow.TransitionInput{FromStateID: todo.ID, ToStateID: progress.ID, Name: "Close"}); !errors.Is(err, workflow.ErrTransitionExists) {
		t.Fatalf("move onto existing pair: %v", err)
	}

	// Deleting a state removes its arrows and their conditions.
	if _, err := f.db.Exec(`UPDATE settings SET initial_state_id = ? WHERE id = 1`, progress.ID); err != nil {
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
	if err := f.store.DeleteTransition(third.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteTransition(third.ID); !errors.Is(err, workflow.ErrTransitionNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestTransitionEngineConditions(t *testing.T) {
	f := setup(t)
	todo := f.states["To do"]
	progress := f.states["In progress"]
	done := f.states["Done"]
	blocked, err := f.store.CreateState("Blocked", "done", nil)
	if err != nil {
		t.Fatal(err)
	}

	none := mustTransition(t, f.store, todo.ID, progress.ID, "Start")
	role := mustTransition(t, f.store, todo.ID, done.ID, "Manager close",
		workflow.ConditionInput{Kind: "role", Value: "manager"},
		workflow.ConditionInput{Kind: "role", Value: "admin"},
	)
	assignee := mustTransition(t, f.store, todo.ID, blocked.ID, "Block myself",
		workflow.ConditionInput{Kind: "assignee", Value: "yes"},
	)
	typed := mustTransition(t, f.store, progress.ID, done.ID, "Fix bug",
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
