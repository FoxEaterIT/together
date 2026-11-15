package items_test

import (
	"strings"
	"testing"

	"jiralike/internal/items"
)

func TestSearchFindsTitleDescriptionAndComments(t *testing.T) {
	f := setup(t)
	first, err := f.items.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Caffè latte",
		Description: "A searchable description", ActorID: f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.items.Create(items.CreateInput{
		ProjectID: f.openID, Type: "action", Title: "Deploy pipeline",
		Description: "Release notes for the week", ActorID: f.adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.CreateComment(first.ID, f.adminID, "This comment contains nuance.", nil); err != nil {
		t.Fatal(err)
	}

	search := func(query string) items.Result {
		t.Helper()
		result, err := f.items.Search(f.adminID, "admin", items.Filter{Query: query, Total: true})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		return result
	}
	check := func(query string, wantID int64, wantMatch string) items.Item {
		t.Helper()
		result := search(query)
		if len(result.Items) != 1 || result.Items[0].ID != wantID {
			t.Fatalf("search %q returned %+v, want item %d", query, result.Items, wantID)
		}
		if result.Total != 1 {
			t.Fatalf("search %q total = %d, want 1", query, result.Total)
		}
		item := result.Items[0]
		if item.Match != wantMatch {
			t.Fatalf("search %q match = %q, want %q", query, item.Match, wantMatch)
		}
		if !strings.Contains(item.Snippet, "«") || !strings.Contains(item.Snippet, "»") {
			t.Fatalf("search %q snippet lacks markers: %q", query, item.Snippet)
		}
		if strings.Contains(item.Snippet, "<mark>") {
			t.Fatalf("search %q snippet contains HTML: %q", query, item.Snippet)
		}
		return item
	}

	// Diacritics and case are folded by the tokenizer, but the snippet keeps
	// the original spelling.
	item := check("caffe", first.ID, "title")
	if !strings.Contains(item.Snippet, "Caffè") {
		t.Fatalf("accented snippet = %q", item.Snippet)
	}
	check("LATTE", first.ID, "title")
	check("searchable", first.ID, "description")
	check("nuance", first.ID, "comments")
	check("depl", second.ID, "title")
	check(`"release notes"`, second.ID, "description")

	if result := search(`"notes release"`); len(result.Items) != 0 {
		t.Fatalf("phrase order should not match: %+v", result.Items)
	}
	if result := search(`release notes`); len(result.Items) != 1 || result.Items[0].ID != second.ID {
		t.Fatalf("AND terms should match: %+v", result.Items)
	}
}

func TestSearchExclusionAndCase(t *testing.T) {
	f := setup(t)
	alpha := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Common alpha")
	beta := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Common beta")

	result, err := f.items.Search(f.adminID, "admin", items.Filter{Query: "common -beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != alpha.ID {
		t.Fatalf("exclusion returned %+v, want only alpha (%d), not beta (%d)", result.Items, alpha.ID, beta.ID)
	}

	result, err = f.items.Search(f.adminID, "admin", items.Filter{Query: "COMMON"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("case-insensitive search returned %d rows", len(result.Items))
	}
}

func TestSearchVisibilityArchivedAndPrivateSnippets(t *testing.T) {
	f := setup(t)
	manager := mkUser(t, f.db, "Manager", "manager-search@example.test", "manager")
	member := mkUser(t, f.db, "Member", "member-search@example.test", "member")
	outsider := mkUser(t, f.db, "Outsider", "outsider-search@example.test", "member")
	private, err := f.projects.Create("Private search", "", "private", manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.projects.AddMember(private.ID, member); err != nil {
		t.Fatal(err)
	}
	privateItem := createItem(t, f.items, private.ID, manager, "bug", nil, "Secretneedle plan")
	if _, err := f.items.CreateComment(privateItem.ID, manager, "Comment with secretneedle too", nil); err != nil {
		t.Fatal(err)
	}
	archived := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Archivedneedle task")
	if _, err := f.items.Archive(archived.ID, f.adminID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		userID  int64
		role    string
		filter  items.Filter
		wantIDs []int64
	}{
		{"outsider private", outsider, "member", items.Filter{Query: "secretneedle", Total: true}, nil},
		{"member private", member, "member", items.Filter{Query: "secretneedle", Total: true}, []int64{privateItem.ID}},
		{"admin private", f.adminID, "admin", items.Filter{Query: "secretneedle", Total: true}, []int64{privateItem.ID}},
		{"archived hidden", f.adminID, "admin", items.Filter{Query: "archivedneedle", Total: true}, nil},
		{"archived included", f.adminID, "admin", items.Filter{Query: "archivedneedle", IncludeArchived: true}, []int64{archived.ID}},
		{"archived never for member", member, "member", items.Filter{Query: "archivedneedle", IncludeArchived: true}, nil},
	}
	for _, tc := range cases {
		result, err := f.items.Search(tc.userID, tc.role, tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(result.Items) != len(tc.wantIDs) {
			t.Fatalf("%s: got %+v, want %v", tc.name, result.Items, tc.wantIDs)
		}
		for _, want := range tc.wantIDs {
			found := false
			for _, item := range result.Items {
				if item.ID == want {
					found = true
					if item.ProjectID == private.ID && tc.userID == outsider {
						t.Fatalf("private row leaked to outsider")
					}
				}
			}
			if !found {
				t.Fatalf("%s: missing %d", tc.name, want)
			}
		}
		if tc.filter.Total && result.Total != len(tc.wantIDs) {
			t.Fatalf("%s: total = %d, want %d", tc.name, result.Total, len(tc.wantIDs))
		}
	}
}

func TestSearchFiltersSortTotalAndPagination(t *testing.T) {
	f := setup(t)
	member := mkUser(t, f.db, "Member", "member-sort@example.test", "member")
	alpha := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Alpha report")
	beta := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Beta report")
	gamma := createItem(t, f.items, f.openID, f.adminID, "micro", &alpha.ID, "Gamma report")

	if _, err := f.db.Exec(`UPDATE items SET description = 'high work', priority = 'high', due_on = '2026-03-10', created_at = '2026-01-01T00:00:00Z', updated_at = '2026-01-05T00:00:00Z' WHERE id = ?`, alpha.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE items SET description = 'urgent work', priority = 'urgent', due_on = '2026-02-01', created_at = '2026-02-01T00:00:00Z', updated_at = '2026-02-05T00:00:00Z' WHERE id = ?`, beta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE items SET description = 'low work', priority = 'low', due_on = NULL, created_at = '2026-03-01T00:00:00Z', updated_at = '2026-03-05T00:00:00Z' WHERE id = ?`, gamma.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO item_assignees (item_id, user_id, assigned_at) VALUES (?, ?, '2026-01-01T00:00:00Z'), (?, ?, '2026-01-01T00:00:00Z')`, alpha.ID, member, gamma.ID, member); err != nil {
		t.Fatal(err)
	}
	var doneState int64
	if err := f.db.QueryRow(`SELECT id FROM workflow_states WHERE category = 'done' LIMIT 1`).Scan(&doneState); err != nil {
		t.Fatal(err)
	}
	if err := f.items.ChangeState(beta.ID, doneState, f.adminID); err != nil {
		t.Fatal(err)
	}
	// ChangeState refreshes updated_at; restore the deterministic value used by
	// the sort assertions.
	if _, err := f.db.Exec(`UPDATE items SET updated_at = '2026-02-05T00:00:00Z' WHERE id = ?`, beta.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(result items.Result) []int64 {
		out := make([]int64, 0, len(result.Items))
		for _, item := range result.Items {
			out = append(out, item.ID)
		}
		return out
	}
	search := func(filter items.Filter) items.Result {
		t.Helper()
		result, err := f.items.Search(f.adminID, "admin", filter)
		if err != nil {
			t.Fatalf("search %+v: %v", filter, err)
		}
		return result
	}

	if got := ids(search(items.Filter{Query: "report", Sort: "created"})); len(got) != 3 || got[0] != gamma.ID || got[1] != beta.ID || got[2] != alpha.ID {
		t.Fatalf("created sort = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", Sort: "updated"})); len(got) != 3 || got[0] != gamma.ID || got[1] != beta.ID || got[2] != alpha.ID {
		t.Fatalf("updated sort = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", Sort: "due"})); len(got) != 3 || got[0] != beta.ID || got[1] != alpha.ID || got[2] != gamma.ID {
		t.Fatalf("due sort = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", Sort: "priority"})); len(got) != 3 || got[0] != beta.ID || got[1] != alpha.ID || got[2] != gamma.ID {
		t.Fatalf("priority sort = %v", got)
	}

	if got := ids(search(items.Filter{Query: "report", HasAssignee: true})); len(got) != 2 {
		t.Fatalf("has_assignee = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", StateCategory: "done"})); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("state_category done = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", CreatedFrom: "2026-02-01", CreatedTo: "2026-02-28"})); len(got) != 1 || got[0] != beta.ID {
		t.Fatalf("created range = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", UpdatedFrom: "2026-03-01", UpdatedTo: "2026-03-31"})); len(got) != 1 || got[0] != gamma.ID {
		t.Fatalf("updated range = %v", got)
	}
	if got := ids(search(items.Filter{Query: "report", ParentID: alpha.ID})); len(got) != 1 || got[0] != gamma.ID {
		t.Fatalf("parent filter = %v", got)
	}

	firstPage := search(items.Filter{Query: "report", Sort: "created", Total: true, Limit: 1})
	secondPage := search(items.Filter{Query: "report", Sort: "created", Total: true, Limit: 1, Offset: 1})
	if firstPage.Total != 3 || secondPage.Total != 3 {
		t.Fatalf("total not preserved across pages: %d %d", firstPage.Total, secondPage.Total)
	}
	if len(firstPage.Items) != 1 || len(secondPage.Items) != 1 || firstPage.Items[0].ID == secondPage.Items[0].ID {
		t.Fatalf("pagination is not stable: %+v / %+v", firstPage.Items, secondPage.Items)
	}
}

func TestSearchRebuildRepairsDrift(t *testing.T) {
	f := setup(t)
	item := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Needle in title")

	result, err := f.items.Search(f.adminID, "admin", items.Filter{Query: "needle"})
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != item.ID {
		t.Fatalf("initial search = %+v, %v", result.Items, err)
	}
	if _, err := f.db.Exec(`DELETE FROM items_fts`); err != nil {
		t.Fatal(err)
	}
	if result, err = f.items.Search(f.adminID, "admin", items.Filter{Query: "needle"}); err != nil || len(result.Items) != 0 {
		t.Fatalf("drifted search = %+v, %v", result.Items, err)
	}
	count, err := f.items.RebuildSearch()
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("rebuild indexed %d rows", count)
	}
	if result, err = f.items.Search(f.adminID, "admin", items.Filter{Query: "needle"}); err != nil || len(result.Items) != 1 {
		t.Fatalf("rebuilt search = %+v, %v", result.Items, err)
	}
}

func TestSearchFallsBackToLikeWithoutFTS(t *testing.T) {
	f := setup(t)
	for _, trigger := range []string{
		"items_fts_item_insert",
		"items_fts_item_update",
		"items_fts_item_delete",
		"items_fts_comment_insert",
		"items_fts_comment_update",
		"items_fts_comment_delete",
	} {
		if _, err := f.db.Exec(`DROP TRIGGER IF EXISTS ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(`DROP TABLE IF EXISTS items_fts`); err != nil {
		t.Fatal(err)
	}
	item := createItem(t, f.items, f.openID, f.adminID, "action", nil, "Fallback needle in title")
	if _, err := f.items.CreateComment(item.ID, f.adminID, "Fallback needle in comment", nil); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"fallback", "comment"} {
		result, err := f.items.Search(f.adminID, "admin", items.Filter{Query: query, Total: true})
		if err != nil {
			t.Fatalf("fallback %q: %v", query, err)
		}
		if len(result.Items) != 1 || result.Items[0].ID != item.ID || result.Total != 1 {
			t.Fatalf("fallback %q = %+v total %d", query, result.Items, result.Total)
		}
		if result.Items[0].Snippet != "" {
			t.Fatalf("fallback %q produced a snippet: %q", query, result.Items[0].Snippet)
		}
	}
	if _, err := f.items.RebuildSearch(); err != items.ErrSearchUnavailable {
		t.Fatalf("rebuild without FTS = %v", err)
	}
}
