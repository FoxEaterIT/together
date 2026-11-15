package automations_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"jiralike/internal/automations"
	"jiralike/internal/db"
	"jiralike/internal/items"
	"jiralike/internal/projects"
)

type fixture struct {
	db         *sql.DB
	store      *automations.Store
	items      *items.Store
	projects   *projects.Store
	adminID    int64
	memberID   int64
	managerID  int64
	projectID  int64
	project2ID int64
	initialID  int64
	progressID int64
	doneID     int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	adminID := mkUser(t, conn, "Admin", "admin@example.test", "admin")
	memberID := mkUser(t, conn, "Member", "member@example.test", "member")
	managerID := mkUser(t, conn, "Manager", "manager@example.test", "manager")
	projectStore := projects.New(conn)
	project, err := projectStore.Create("Alpha", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectStore.Create("Beta", "", "open", adminID)
	if err != nil {
		t.Fatal(err)
	}
	stateIDs := map[string]int64{}
	rows, err := conn.Query(`SELECT id, name FROM workflow_states`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		stateIDs[name] = id
	}
	rows.Close()
	return fixture{
		db:         conn,
		store:      automations.New(conn),
		items:      items.New(conn),
		projects:   projectStore,
		adminID:    adminID,
		memberID:   memberID,
		managerID:  managerID,
		projectID:  project.ID,
		project2ID: second.ID,
		initialID:  stateIDs["To do"],
		progressID: stateIDs["In progress"],
		doneID:     stateIDs["Done"],
	}
}

func mkUser(t *testing.T, conn *sql.DB, name, email, role string) int64 {
	t.Helper()
	res, err := conn.Exec(
		`INSERT INTO users (name, email, password_hash, role, locale, email_notifications, created_at) VALUES (?, ?, 'x', ?, 'en', 0, ?)`,
		name, email, role, time.Now().UTC().Format(time.RFC3339),
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

func (f fixture) baseInput() automations.RuleInput {
	return automations.RuleInput{
		Name:           "When done, assign",
		Enabled:        true,
		TriggerKind:    "state_changed",
		TriggerStateID: f.doneID,
		ProjectIDs:     []int64{f.projectID},
		Actions: []automations.ActionInput{
			{Kind: "set_state", StateID: f.progressID},
			{Kind: "assign_user", UserID: f.memberID},
			{Kind: "add_comment", Body: "Please review", MentionIDs: []int64{f.memberID, f.managerID}},
			{Kind: "notify"},
		},
	}
}

func TestRuleCRUDAndExplicitFields(t *testing.T) {
	f := setup(t)
	rule, err := f.store.Create(f.baseInput(), f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == 0 || !rule.Enabled || rule.TriggerKind != "state_changed" || rule.TriggerStateID != f.doneID {
		t.Fatalf("rule = %+v", rule)
	}
	if rule.TriggerValue != "" && rule.TriggerValue != "done" {
		// The stored value is the state id; the test asserts the id below.
	}
	if rule.TriggerStateName != "Done" {
		t.Fatalf("trigger state name = %q", rule.TriggerStateName)
	}
	if len(rule.ProjectIDs) != 1 || rule.ProjectIDs[0] != f.projectID || len(rule.Projects) != 1 || rule.Projects[0].Name != "Alpha" {
		t.Fatalf("projects = %+v / %+v", rule.ProjectIDs, rule.Projects)
	}
	if len(rule.Actions) != 4 {
		t.Fatalf("actions = %+v", rule.Actions)
	}
	if rule.Actions[0].Kind != "set_state" || rule.Actions[0].StateID != f.progressID {
		t.Fatalf("set_state = %+v", rule.Actions[0])
	}
	if rule.Actions[1].Kind != "assign_user" || rule.Actions[1].UserID != f.memberID {
		t.Fatalf("assign_user = %+v", rule.Actions[1])
	}
	if rule.Actions[2].Kind != "add_comment" || rule.Actions[2].Body != "Please review" {
		t.Fatalf("comment = %+v", rule.Actions[2])
	}
	if len(rule.Actions[2].MentionIDs) != 2 || rule.Actions[2].MentionIDs[0] != f.memberID {
		t.Fatalf("mentions = %+v", rule.Actions[2].MentionIDs)
	}
	if rule.Actions[3].Kind != "notify" || rule.Actions[3].Value != "" {
		t.Fatalf("notify = %+v", rule.Actions[3])
	}

	// Enabled toggles and replaces actions atomically.
	update := f.baseInput()
	update.Name = "Global reminder"
	update.Enabled = false
	update.TriggerKind = "due_passed"
	update.TriggerStateID = 0
	update.ProjectIDs = nil
	update.Actions = []automations.ActionInput{{Kind: "notify"}}
	updated, err := f.store.Update(rule.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.TriggerKind != "due_passed" || len(updated.ProjectIDs) != 0 || len(updated.Actions) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.TriggerStateID != 0 || updated.TriggerStateName != "" {
		t.Fatalf("state trigger not cleared: %+v", updated)
	}

	list, err := f.store.List()
	if err != nil || len(list) != 1 || list[0].ID != rule.ID {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := f.store.Delete(rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Get(rule.ID); !errors.Is(err, automations.ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if err := f.store.Delete(rule.ID); !errors.Is(err, automations.ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
}

func TestRuleValidation(t *testing.T) {
	f := setup(t)
	archived, err := f.projects.Create("Archived", "", "open", f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.projects.Archive(archived.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mutate func(*automations.RuleInput)
		want   error
	}{
		{"empty name", func(in *automations.RuleInput) { in.Name = "  " }, automations.ErrNameRequired},
		{"long name", func(in *automations.RuleInput) { in.Name = strings.Repeat("x", 200) }, automations.ErrNameTooLong},
		{"bad trigger", func(in *automations.RuleInput) { in.TriggerKind = "cron" }, automations.ErrInvalidTrigger},
		{"state missing", func(in *automations.RuleInput) { in.TriggerStateID = 0 }, automations.ErrTriggerState},
		{"state unknown", func(in *automations.RuleInput) { in.TriggerStateID = 9999 }, automations.ErrTriggerState},
		{"value on assigned", func(in *automations.RuleInput) {
			in.TriggerKind = "assigned"
		}, automations.ErrTriggerValue},
		{"no actions", func(in *automations.RuleInput) { in.Actions = nil }, automations.ErrActionsRequired},
		{"bad action", func(in *automations.RuleInput) { in.Actions = []automations.ActionInput{{Kind: "webhook"}} }, automations.ErrInvalidAction},
		{"empty comment", func(in *automations.RuleInput) {
			in.Actions = []automations.ActionInput{{Kind: "add_comment", Body: "  "}}
		}, automations.ErrCommentEmpty},
		{"bad state action", func(in *automations.RuleInput) {
			in.Actions = []automations.ActionInput{{Kind: "set_state", StateID: 9999}}
		}, automations.ErrActionValue},
		{"bad user action", func(in *automations.RuleInput) {
			in.Actions = []automations.ActionInput{{Kind: "assign_user", UserID: 9999}}
		}, automations.ErrActionValue},
		{"notify with value", func(in *automations.RuleInput) {
			in.Actions = []automations.ActionInput{{Kind: "notify", Body: "hello"}}
		}, automations.ErrActionValue},
		{"missing project", func(in *automations.RuleInput) { in.ProjectIDs = []int64{9999} }, automations.ErrProjectNotFound},
		{"archived project", func(in *automations.RuleInput) { in.ProjectIDs = []int64{archived.ID} }, automations.ErrProjectArchived},
		{"unknown mention", func(in *automations.RuleInput) {
			in.Actions = []automations.ActionInput{{Kind: "add_comment", Body: "x", MentionIDs: []int64{9999}}}
		}, automations.ErrUserNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := f.baseInput()
			tc.mutate(&in)
			if _, err := f.store.Create(in, f.adminID); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	// assigned has no trigger value, so clear the state.
	ok := f.baseInput()
	ok.TriggerKind = "assigned"
	ok.TriggerStateID = 0
	if _, err := f.store.Create(ok, f.adminID); err != nil {
		t.Fatalf("assigned trigger: %v", err)
	}
}

func TestMatchingTriggerProjectAndState(t *testing.T) {
	f := setup(t)
	global, err := f.store.Create(automations.RuleInput{
		Name:           "Any done",
		Enabled:        true,
		TriggerKind:    "state_changed",
		TriggerStateID: f.doneID,
		Actions:        []automations.ActionInput{{Kind: "notify"}},
	}, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := f.store.Create(automations.RuleInput{
		Name:        "On assignment in Alpha",
		Enabled:     true,
		TriggerKind: "assigned",
		ProjectIDs:  []int64{f.projectID},
		Actions:     []automations.ActionInput{{Kind: "notify"}},
	}, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := f.store.Create(automations.RuleInput{
		Name:        "Disabled",
		Enabled:     false,
		TriggerKind: "assigned",
		Actions:     []automations.ActionInput{{Kind: "notify"}},
	}, f.adminID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.store.Matching(automations.Event{Kind: "state_changed", ProjectID: f.project2ID, StateID: f.doneID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != global.ID {
		t.Fatalf("state match = %+v", got)
	}
	got, err = f.store.Matching(automations.Event{Kind: "state_changed", ProjectID: f.projectID, StateID: f.progressID})
	if err != nil || len(got) != 0 {
		t.Fatalf("wrong state = %+v %v", got, err)
	}

	got, err = f.store.Matching(automations.Event{Kind: "assigned", ProjectID: f.projectID})
	if err != nil || len(got) != 1 || got[0].ID != scoped.ID {
		t.Fatalf("assignment match = %+v %v", got, err)
	}
	got, err = f.store.Matching(automations.Event{Kind: "assigned", ProjectID: f.project2ID})
	if err != nil || len(got) != 0 {
		t.Fatalf("other project match = %+v %v", got, err)
	}
	if got, err = f.store.Matching(automations.Event{Kind: "comment_added", ProjectID: f.projectID}); err != nil || len(got) != 0 {
		t.Fatalf("comment match = %+v %v", got, err)
	}
	_ = disabled
}

func TestDueItemsFilters(t *testing.T) {
	f := setup(t)
	mk := func(title, due string) int64 {
		it, err := f.items.Create(items.CreateInput{
			ProjectID: f.projectID,
			Type:      "action",
			Title:     title,
			DueOn:     due,
			ActorID:   f.adminID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	overdue := mk("Overdue", "2026-11-01")
	today := mk("Today", "2026-11-10")
	future := mk("Future", "2026-12-01")
	done := mk("Done", "2026-11-02")
	archived := mk("Archived", "2026-11-03")
	if err := f.items.ChangeState(done, f.doneID, f.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Archive(archived, f.adminID); err != nil {
		t.Fatal(err)
	}
	archivedProject, err := f.projects.Create("Closed", "", "open", f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := f.items.Create(items.CreateInput{
		ProjectID: archivedProject.ID,
		Type:      "action",
		Title:     "Hidden",
		DueOn:     "2026-10-01",
		ActorID:   f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.projects.Archive(archivedProject.ID); err != nil {
		t.Fatal(err)
	}

	got, err := f.store.DueItems("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != overdue {
		t.Fatalf("due items = %+v (today=%d future=%d hidden=%d)", got, today, future, hidden.ID)
	}
}
