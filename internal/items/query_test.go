package items

import (
	"strings"
	"testing"

	"jiralike/internal/db"
)

func TestBuildMatchQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"and terms with prefix", "caffe latte", `"caffe" AND "latte"*`},
		{"phrase then prefix", `"foo bar" baz`, `"foo bar" AND "baz"*`},
		{"exclusion", "foo -bar", `"foo"* NOT "bar"`},
		{"exclusion first", "-foo bar", `"bar"* NOT "foo"`},
		{"punctuation is literal", "e-mail work_item", `"e-mail" AND "work_item"*`},
		{"apostrophe", "don't stop", `"don't" AND "stop"*`},
	}
	for _, tc := range cases {
		got, err := buildMatchQuery(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"foo OR bar",
		"foo and bar",
		"foo NOT bar",
		"near",
		"foo(",
		"foo)",
		"field:value",
		"foo*",
		"-onlynegative",
		`"unclosed`,
		`"phrase"trailing`,
		strings.Repeat("a", maxSearchLength+1),
	}
	for _, raw := range invalid {
		if got, err := buildMatchQuery(raw); err == nil {
			t.Fatalf("%q should be rejected, got %q", raw, got)
		}
	}
}

func TestSearchQueryPlanUsesFTS(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stamp := "2026-01-01T00:00:00Z"
	if _, err := conn.Exec(`INSERT INTO users (id, name, email, password_hash, role, created_at) VALUES (1, 'A', 'a@example.test', 'x', 'admin', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id, name, visibility, created_by, created_at) VALUES (1, 'P', 'open', 1, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, project_id, type, title, state_id, created_by, created_at, updated_at) VALUES (1, 1, 'action', 'Needle', 1, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	store := New(conn)
	prepared, err := store.buildList(1, "admin", Filter{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query("EXPLAIN QUERY PLAN "+prepared.query, prepared.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	plan := strings.Join(details, " | ")
	if !strings.Contains(plan, "items_fts") || !strings.Contains(plan, "VIRTUAL TABLE INDEX") {
		t.Fatalf("query plan does not use the FTS index: %s", plan)
	}
}
