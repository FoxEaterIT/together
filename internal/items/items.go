// Package items implements work item creation, filtering, full-text search,
// comments and mentions, append-only history and workflow transitions.
package items

import (
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound              = errors.New("item not found")
	ErrArchived              = errors.New("item is archived")
	ErrTitleEmpty            = errors.New("title is required")
	ErrTitleTooLong          = errors.New("title is too long")
	ErrInvalidType           = errors.New("invalid item type")
	ErrInvalidPriority       = errors.New("invalid priority")
	ErrParentRequired        = errors.New("micro items require an action parent")
	ErrParentNotFound        = errors.New("parent item not found")
	ErrParentInvalid         = errors.New("invalid parent for this item type")
	ErrParentProject         = errors.New("parent belongs to another project")
	ErrParentArchived        = errors.New("parent is archived")
	ErrChildrenRequireAction = errors.New("items with micro children must stay actions")
	ErrStateNotFound         = errors.New("state not found")
	ErrStateConflict         = errors.New("item is no longer in the expected state")
	ErrNoWorkflow            = errors.New("no workflow is assigned to this item type")
	ErrDueInvalid            = errors.New("due_on must be a date in YYYY-MM-DD form")
	ErrAssigneeNotFound      = errors.New("assignee not found")
	ErrCommentNotFound       = errors.New("comment not found")
	ErrCommentEmpty          = errors.New("comment body is required")
	ErrCommentTooLong        = errors.New("comment body is too long")
	ErrCommentAuthor         = errors.New("only the author or an admin can edit this comment")
	ErrAlreadyRun            = errors.New("automation already applied to this item")
	ErrInvalidAction         = errors.New("invalid automation action")
	ErrInvalidSearch         = errors.New("invalid search query")
	ErrInvalidSort           = errors.New("invalid sort")
	ErrInvalidFilter         = errors.New("invalid filter")
	ErrSearchUnavailable     = errors.New("search index unavailable")
)

const (
	maxTitleLength   = 300
	maxCommentLength = 20000
	defaultLimit     = 200
	maxLimit         = 500
	maxSearchLength  = 256
)

// Item is an item row joined with the names the views and detail screen need.
// DueOn and ArchivedAt are empty strings when the column is NULL.
type Item struct {
	ID            int64      `json:"id"`
	ProjectID     int64      `json:"project_id"`
	ProjectName   string     `json:"project_name"`
	Type          string     `json:"type"`
	ParentID      *int64     `json:"parent_id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Priority      string     `json:"priority"`
	StateID       int64      `json:"state_id"`
	StateName     string     `json:"state_name"`
	StateCategory string     `json:"state_category"`
	DueOn         string     `json:"due_on"`
	CreatedBy     int64      `json:"created_by"`
	CreatedByName string     `json:"created_by_name"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	ArchivedAt    string     `json:"archived_at,omitempty"`
	Assignees     []Assignee `json:"assignees"`
	// Snippet and Match are populated only by a full-text search. Snippet is
	// plain text with «» around the matched words, never HTML; Match is one of
	// title, description or comments.
	Snippet string `json:"snippet,omitempty"`
	Match   string `json:"match,omitempty"`
}

type Assignee struct {
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	AssignedAt string `json:"assigned_at"`
}

// ItemRef is the compact shape used for parent and child links.
type ItemRef struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	StateID   int64  `json:"state_id"`
	StateName string `json:"state_name"`
}

type Mention struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
}

type Comment struct {
	ID         int64     `json:"id"`
	ItemID     int64     `json:"item_id"`
	AuthorID   int64     `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Body       string    `json:"body"`
	CreatedAt  string    `json:"created_at"`
	EditedAt   string    `json:"edited_at,omitempty"`
	Mentions   []Mention `json:"mentions"`
}

type Event struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Field     string `json:"field,omitempty"`
	FromValue string `json:"from_value,omitempty"`
	ToValue   string `json:"to_value,omitempty"`
	ActorID   int64  `json:"actor_id,omitempty"`
	ActorName string `json:"actor_name,omitempty"`
	CreatedAt string `json:"created_at"`
}

type CreateInput struct {
	ProjectID   int64
	Type        string
	ParentID    *int64
	Title       string
	Description string
	Priority    string
	StateID     int64
	DueOn       string
	AssigneeIDs []int64
	ActorID     int64
}

type UpdateInput struct {
	Type        string
	ParentID    *int64
	Title       string
	Description string
	Priority    string
	DueOn       string
	AssigneeIDs []int64
	ActorID     int64
}

type Filter struct {
	Query           string
	ProjectID       int64
	Type            string
	StateID         int64
	StateCategory   string
	AssigneeID      int64
	Priority        string
	DueFrom         string
	DueTo           string
	HasDue          bool
	NoDue           bool
	CreatedFrom     string
	CreatedTo       string
	UpdatedFrom     string
	UpdatedTo       string
	HasAssignee     bool
	ParentID        int64
	Sort            string
	Total           bool
	IncludeArchived bool
	Limit           int
	Offset          int
}

// Result carries the page and, when Filter.Total is set, the number of rows the
// same WHERE clause would return without LIMIT/OFFSET.
type Result struct {
	Items []Item
	Total int
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(in CreateInput) (Item, error) {
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	if typ == "" {
		typ = "action"
	}
	if !validType(typ) {
		return Item{}, ErrInvalidType
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Item{}, ErrTitleEmpty
	}
	if len(title) > maxTitleLength {
		return Item{}, ErrTitleTooLong
	}
	priority := strings.TrimSpace(in.Priority)
	if priority == "" {
		priority = "none"
	}
	if !validPriority(priority) {
		return Item{}, ErrInvalidPriority
	}
	dueOn := strings.TrimSpace(in.DueOn)
	if dueOn != "" {
		if _, err := time.Parse("2006-01-02", dueOn); err != nil {
			return Item{}, ErrDueInvalid
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()

	if err := validateHierarchy(tx, in.ProjectID, typ, in.ParentID, 0); err != nil {
		return Item{}, err
	}
	stateID := in.StateID
	if stateID == 0 {
		err := tx.QueryRow(`
			SELECT ws.id
			FROM project_workflows pw
			JOIN workflow_states ws ON ws.workflow_id = pw.workflow_id AND ws.is_initial = 1
			WHERE pw.project_id = ? AND pw.item_type = ?`, in.ProjectID, typ).Scan(&stateID)
		if errors.Is(err, sql.ErrNoRows) {
			return Item{}, ErrNoWorkflow
		}
		if err != nil {
			return Item{}, err
		}
	} else if err := ensureStateInFlow(tx, stateID, in.ProjectID, typ); err != nil {
		return Item{}, err
	}
	assignees, err := normalizeUsers(tx, in.AssigneeIDs, ErrAssigneeNotFound)
	if err != nil {
		return Item{}, err
	}

	now := timestamp()
	res, err := tx.Exec(`
		INSERT INTO items (project_id, type, parent_id, title, description, priority, state_id, due_on, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, typ, nullableID(in.ParentID), title, strings.TrimSpace(in.Description), priority, stateID, nullableText(dueOn), in.ActorID, now, now,
	)
	if err != nil {
		return Item{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Item{}, err
	}
	if err := replaceAssignees(tx, id, assignees, now); err != nil {
		return Item{}, err
	}
	if err := insertEvent(tx, id, in.ActorID, "created", "", "", "", now); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return s.Get(id)
}

func (s *Store) Get(id int64) (Item, error) {
	it, err := scanItem(s.db.QueryRow(itemSelect+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	byItem, err := s.assigneesFor([]int64{id})
	if err != nil {
		return Item{}, err
	}
	if list, ok := byItem[id]; ok {
		it.Assignees = list
	}
	return it, nil
}

// withProjectVisibility adds the project-level access rule used by every item
// query. Visibility is filtered in SQL, before any row leaves the store, so a
// private project never leaks through lists, search, reports or paging.
func withProjectVisibility(where []string, args []any, role string, viewerID int64) ([]string, []any) {
	if role == "admin" {
		return where, args
	}
	where = append(where, `(p.visibility = 'open'
		OR p.created_by = ?
		OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = ?)
		OR EXISTS (SELECT 1 FROM project_teams pt
		           JOIN team_members tm ON tm.team_id = pt.team_id
		           JOIN teams t ON t.id = pt.team_id
		           WHERE pt.project_id = p.id AND tm.user_id = ? AND t.archived_at IS NULL))`)
	return where, append(args, viewerID, viewerID, viewerID)
}

func (s *Store) List(viewerID int64, role string, f Filter) ([]Item, error) {
	result, err := s.Search(viewerID, role, f)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// Search is List plus the optional total. The WHERE clause, including project
// visibility, is always built in SQL, so a search cannot return a row or an
// excerpt from a project the viewer cannot read.
func (s *Store) Search(viewerID int64, role string, f Filter) (Result, error) {
	prepared, err := s.buildList(viewerID, role, f)
	if err != nil {
		return Result{}, err
	}
	rows, err := s.db.Query(prepared.query, prepared.args...)
	if err != nil {
		return Result{}, err
	}
	out := []Item{}
	ids := []int64{}
	for rows.Next() {
		var it Item
		if prepared.searching {
			var titleSnip, descriptionSnip, commentsSnip string
			it, err = scanItemWith(rows, []any{&titleSnip, &descriptionSnip, &commentsSnip})
			if err == nil {
				it.Snippet, it.Match = chooseSnippet(titleSnip, descriptionSnip, commentsSnip)
			}
		} else {
			it, err = scanItem(rows)
		}
		if err != nil {
			rows.Close()
			return Result{}, err
		}
		out = append(out, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Result{}, err
	}
	rows.Close()

	byItem, err := s.assigneesFor(ids)
	if err != nil {
		return Result{}, err
	}
	for i := range out {
		if list, ok := byItem[out[i].ID]; ok {
			out[i].Assignees = list
		}
	}
	result := Result{Items: out}
	if f.Total {
		if err := s.db.QueryRow(prepared.countQuery, prepared.countArgs...).Scan(&result.Total); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

type preparedList struct {
	query      string
	args       []any
	countQuery string
	countArgs  []any
	searching  bool
}

// buildList assembles the one query shape used by both the page and the count.
// It is separate from Search so tests can run EXPLAIN QUERY PLAN on exactly the
// SQL production executes.
func (s *Store) buildList(viewerID int64, role string, f Filter) (preparedList, error) {
	where := []string{"p.archived_at IS NULL"}
	args := []any{}

	if !f.IncludeArchived || (role != "admin" && role != "manager") {
		where = append(where, "i.archived_at IS NULL")
	}
	where, args = withProjectVisibility(where, args, role, viewerID)
	if f.ProjectID > 0 {
		where = append(where, "i.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if typ := strings.ToLower(strings.TrimSpace(f.Type)); typ != "" {
		where = append(where, "i.type = ?")
		args = append(args, typ)
	}
	if f.StateID > 0 {
		where = append(where, "i.state_id = ?")
		args = append(args, f.StateID)
	}
	if category := strings.ToLower(strings.TrimSpace(f.StateCategory)); category != "" {
		if category != "open" && category != "done" {
			return preparedList{}, ErrInvalidFilter
		}
		where = append(where, "ws.category = ?")
		args = append(args, category)
	}
	if f.AssigneeID > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM item_assignees ia WHERE ia.item_id = i.id AND ia.user_id = ?)")
		args = append(args, f.AssigneeID)
	}
	if f.HasAssignee {
		where = append(where, "EXISTS (SELECT 1 FROM item_assignees ia WHERE ia.item_id = i.id)")
	}
	if p := strings.ToLower(strings.TrimSpace(f.Priority)); p != "" {
		where = append(where, "i.priority = ?")
		args = append(args, p)
	}
	if f.ParentID > 0 {
		where = append(where, "i.parent_id = ?")
		args = append(args, f.ParentID)
	}
	if from := strings.TrimSpace(f.DueFrom); from != "" {
		where = append(where, "i.due_on >= ?")
		args = append(args, from)
	}
	if to := strings.TrimSpace(f.DueTo); to != "" {
		where = append(where, "i.due_on <= ?")
		args = append(args, to)
	}
	if f.HasDue {
		where = append(where, "i.due_on IS NOT NULL")
	}
	if f.NoDue {
		where = append(where, "i.due_on IS NULL")
	}

	createdFrom, err := filterDate(f.CreatedFrom)
	if err != nil {
		return preparedList{}, err
	}
	if createdFrom != "" {
		where = append(where, "i.created_at >= ?")
		args = append(args, createdFrom)
	}
	createdTo, err := filterDate(f.CreatedTo)
	if err != nil {
		return preparedList{}, err
	}
	if createdTo != "" {
		where = append(where, "i.created_at < date(?, '+1 day')")
		args = append(args, createdTo)
	}
	updatedFrom, err := filterDate(f.UpdatedFrom)
	if err != nil {
		return preparedList{}, err
	}
	if updatedFrom != "" {
		where = append(where, "i.updated_at >= ?")
		args = append(args, updatedFrom)
	}
	updatedTo, err := filterDate(f.UpdatedTo)
	if err != nil {
		return preparedList{}, err
	}
	if updatedTo != "" {
		where = append(where, "i.updated_at < date(?, '+1 day')")
		args = append(args, updatedTo)
	}

	searching := false
	if query := strings.TrimSpace(f.Query); query != "" {
		if s.ftsAvailable() {
			expression, err := buildMatchQuery(query)
			if err != nil {
				return preparedList{}, err
			}
			searching = true
			where = append([]string{"items_fts MATCH ?"}, where...)
			args = append([]any{expression}, args...)
		} else {
			// Documented fallback for a database without FTS5: no excerpts,
			// but the same visibility and filters.
			like := "%" + escapeLike(query) + "%"
			where = append(where, `(i.title LIKE ? ESCAPE '\' OR i.description LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM comments c WHERE c.item_id = i.id AND c.body LIKE ? ESCAPE '\'))`)
			args = append(args, like, like, like)
		}
	}

	sortMode := strings.ToLower(strings.TrimSpace(f.Sort))
	if sortMode == "" {
		if searching {
			sortMode = "relevance"
		} else {
			sortMode = "updated"
		}
	}
	if sortMode == "relevance" && !searching {
		sortMode = "updated"
	}
	order := ""
	switch sortMode {
	case "relevance":
		order = "bm25(items_fts, 5.0, 2.0, 1.0, 0.0), i.updated_at DESC, i.id DESC"
	case "updated":
		order = "i.updated_at DESC, i.id DESC"
	case "created":
		order = "i.created_at DESC, i.id DESC"
	case "due":
		order = "i.due_on IS NULL, i.due_on ASC, i.id DESC"
	case "priority":
		order = "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END ASC, i.updated_at DESC, i.id DESC"
	default:
		return preparedList{}, ErrInvalidSort
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	from := itemFrom
	columns := itemColumns
	if searching {
		from += "\n\tJOIN items_fts ON items_fts.item_id = i.id"
		columns += `,
			snippet(items_fts, 0, '«', '»', '…', 12),
			snippet(items_fts, 1, '«', '»', '…', 12),
			snippet(items_fts, 2, '«', '»', '…', 12)`
	}
	whereSQL := strings.Join(where, " AND ")
	return preparedList{
		query:      "SELECT " + columns + "\n\t" + from + "\n\tWHERE " + whereSQL + "\n\tORDER BY " + order + " LIMIT ? OFFSET ?",
		args:       append(args, limit, offset),
		countQuery: "SELECT COUNT(*) " + from + "\n\tWHERE " + whereSQL,
		countArgs:  args,
		searching:  searching,
	}, nil
}

// RebuildSearch reindexes every item after a manual restore or suspected drift.
// Normal writes are already handled by triggers; this is the explicit repair.
func (s *Store) RebuildSearch() (int, error) {
	if !s.ftsAvailable() {
		return 0, ErrSearchUnavailable
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM items_fts`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT INTO items_fts (title, description, comments, item_id)
		SELECT i.title, i.description,
		       COALESCE((SELECT group_concat(c.body, ' ') FROM comments c WHERE c.item_id = i.id), ''),
		       i.id
		FROM items i`); err != nil {
		return 0, err
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM items_fts`).Scan(&count); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) ftsAvailable() bool {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'items_fts'`).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func filterDate(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", trimmed); err != nil {
		return "", ErrInvalidFilter
	}
	return trimmed, nil
}

type searchToken struct {
	text   string
	phrase bool
	negate bool
}

// buildMatchQuery turns user input into a deliberately small FTS5 expression.
// Every term is quoted, so user text can never become FTS syntax: AND combines
// terms, the last positive term gets a prefix wildcard, quotes make a phrase
// and a leading '-' excludes. FTS operators are rejected instead of being
// documented as a second query language.
func buildMatchQuery(raw string) (string, error) {
	if utf8.RuneCountInString(raw) > maxSearchLength {
		return "", ErrInvalidSearch
	}
	tokens, err := parseSearchTokens(raw)
	if err != nil {
		return "", err
	}
	lastPositive := -1
	for i, token := range tokens {
		if !token.negate {
			lastPositive = i
		}
	}
	if lastPositive < 0 {
		return "", ErrInvalidSearch
	}
	positive := []string{}
	negative := []string{}
	for i, token := range tokens {
		rendered := `"` + strings.ReplaceAll(token.text, `"`, `""`) + `"`
		if token.negate {
			negative = append(negative, rendered)
			continue
		}
		if i == lastPositive && !token.phrase {
			rendered += "*"
		}
		positive = append(positive, rendered)
	}
	expression := strings.Join(positive, " AND ")
	for _, term := range negative {
		expression += " NOT " + term
	}
	return expression, nil
}

func parseSearchTokens(raw string) ([]searchToken, error) {
	runes := []rune(raw)
	tokens := []searchToken{}
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		negate := false
		if runes[i] == '-' {
			negate = true
			i++
			if i >= len(runes) || unicode.IsSpace(runes[i]) {
				return nil, ErrInvalidSearch
			}
		}
		if runes[i] == '"' {
			phrase, next, err := readQuoted(runes, i)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(phrase) == "" {
				return nil, ErrInvalidSearch
			}
			tokens = append(tokens, searchToken{text: phrase, phrase: true, negate: negate})
			i = next
			if i < len(runes) && !unicode.IsSpace(runes[i]) {
				return nil, ErrInvalidSearch
			}
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			switch runes[i] {
			case '"', '(', ')', ':', '*':
				return nil, ErrInvalidSearch
			}
			i++
		}
		text := string(runes[start:i])
		if isFTSOperator(text) {
			return nil, ErrInvalidSearch
		}
		tokens = append(tokens, searchToken{text: text, negate: negate})
	}
	if len(tokens) == 0 {
		return nil, ErrInvalidSearch
	}
	return tokens, nil
}

func readQuoted(runes []rune, start int) (string, int, error) {
	var text strings.Builder
	for i := start + 1; i < len(runes); i++ {
		if runes[i] != '"' {
			text.WriteRune(runes[i])
			continue
		}
		if i+1 < len(runes) && runes[i+1] == '"' {
			text.WriteRune('"')
			i++
			continue
		}
		return text.String(), i + 1, nil
	}
	return "", 0, ErrInvalidSearch
}

func isFTSOperator(value string) bool {
	switch strings.ToUpper(value) {
	case "AND", "OR", "NOT", "NEAR":
		return true
	}
	return false
}

// chooseSnippet prefers the title, then the description, then a comment. A
// column whose snippet contains the « marker took part in the match, so the
// answer tells the client where the word was found without sending HTML.
func chooseSnippet(title, description, comments string) (string, string) {
	for _, candidate := range []struct {
		name string
		text string
	}{
		{"title", title},
		{"description", description},
		{"comments", comments},
	} {
		if strings.Contains(candidate.text, "«") {
			return cleanSnippet(candidate.text), candidate.name
		}
	}
	if text := cleanSnippet(description); text != "" {
		return text, "description"
	}
	return cleanSnippet(title), "title"
}

func cleanSnippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 400 {
		return string(runes[:400]) + "…"
	}
	return text
}

func (s *Store) Update(id int64, in UpdateInput) (Item, error) {
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	if !validType(typ) {
		return Item{}, ErrInvalidType
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Item{}, ErrTitleEmpty
	}
	if len(title) > maxTitleLength {
		return Item{}, ErrTitleTooLong
	}
	priority := strings.TrimSpace(in.Priority)
	if priority == "" {
		priority = "none"
	}
	if !validPriority(priority) {
		return Item{}, ErrInvalidPriority
	}
	dueOn := strings.TrimSpace(in.DueOn)
	if dueOn != "" {
		if _, err := time.Parse("2006-01-02", dueOn); err != nil {
			return Item{}, ErrDueInvalid
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()

	current, err := getItemTx(tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if current.ArchivedAt != "" {
		return Item{}, ErrArchived
	}
	if current.Type != typ {
		if err := ensureTypeKeepsChildren(tx, id, typ); err != nil {
			return Item{}, err
		}
	}
	// Re-validate the link only when it really changes. Archiving an Observer
	// leaves its Actions in place, and those Actions must stay editable.
	if current.Type != typ || !sameParent(current.ParentID, in.ParentID) {
		if err := validateHierarchy(tx, current.ProjectID, typ, in.ParentID, id); err != nil {
			return Item{}, err
		}
	}
	assignees, err := normalizeUsers(tx, in.AssigneeIDs, ErrAssigneeNotFound)
	if err != nil {
		return Item{}, err
	}

	now := timestamp()
	if _, err := tx.Exec(`
		UPDATE items SET type = ?, parent_id = ?, title = ?, description = ?, priority = ?, due_on = ?, updated_at = ?
		WHERE id = ? AND archived_at IS NULL`,
		typ, nullableID(in.ParentID), title, strings.TrimSpace(in.Description), priority, nullableText(dueOn), now, id,
	); err != nil {
		return Item{}, err
	}

	changes := []struct {
		field string
		from  string
		to    string
	}{
		{"title", current.Title, title},
		{"description", current.Description, strings.TrimSpace(in.Description)},
		{"priority", current.Priority, priority},
		{"due_on", current.DueOn, dueOn},
		{"type", current.Type, typ},
		{"parent", parentValue(current.ParentID), parentValue(in.ParentID)},
	}
	for _, change := range changes {
		if change.from == change.to {
			continue
		}
		if err := insertEvent(tx, id, in.ActorID, "field_changed", change.field, change.from, change.to, now); err != nil {
			return Item{}, err
		}
	}

	currentIDs, err := assigneeIDsTx(tx, id)
	if err != nil {
		return Item{}, err
	}
	if joinedIDs(currentIDs) != joinedIDs(assignees) {
		if err := replaceAssignees(tx, id, assignees, now); err != nil {
			return Item{}, err
		}
		if err := insertEvent(tx, id, in.ActorID, "assignees_changed", "assignees", joinedIDs(currentIDs), joinedIDs(assignees), now); err != nil {
			return Item{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return s.Get(id)
}

// ChangeState is used by the workflow engine and by automations in Phase 7. It
// records the same state_changed event shape the rest of the history uses.
func (s *Store) ChangeState(id, stateID, actorID int64) error {
	return s.changeState(id, 0, stateID, actorID)
}

// ChangeStateFrom applies a workflow arrow atomically: the update only happens
// if the item is still in the state the arrow starts from.
func (s *Store) ChangeStateFrom(id, fromStateID, stateID, actorID int64) error {
	return s.changeState(id, fromStateID, stateID, actorID)
}

func (s *Store) changeState(id, fromStateID, stateID, actorID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	current, err := getItemTx(tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.ArchivedAt != "" {
		return ErrArchived
	}
	if fromStateID > 0 && current.StateID != fromStateID {
		return ErrStateConflict
	}
	if err := ensureState(tx, stateID); err != nil {
		return err
	}
	var currentWorkflow, targetWorkflow int64
	if err := tx.QueryRow(`SELECT workflow_id FROM workflow_states WHERE id = ?`, current.StateID).Scan(&currentWorkflow); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT workflow_id FROM workflow_states WHERE id = ?`, stateID).Scan(&targetWorkflow); err != nil {
		return err
	}
	if currentWorkflow != targetWorkflow {
		return ErrStateNotFound
	}
	if current.StateID == stateID {
		return tx.Commit()
	}
	now := timestamp()
	if _, err := tx.Exec(`UPDATE items SET state_id = ?, updated_at = ? WHERE id = ?`, stateID, now, id); err != nil {
		return err
	}
	if err := insertEvent(tx, id, actorID, "state_changed", "state", strconv.FormatInt(current.StateID, 10), strconv.FormatInt(stateID, 10), now); err != nil {
		return err
	}
	return tx.Commit()
}

// Action is one automation step. Only the field relevant to Kind is read.
type Action struct {
	Kind       string
	StateID    int64
	UserID     int64
	Body       string
	MentionIDs []int64
}

// AppliedComment is a comment created by an automation, with the mentions the
// notification layer needs. Comments always carry the rule creator as author.
type AppliedComment struct {
	ID         int64
	Body       string
	MentionIDs []int64
}

// ActionResult reports what a rule changed so the HTTP layer can fan out the
// assignment and mention notifications outside the transaction.
type ActionResult struct {
	Item           Item
	NewAssigneeIDs []int64
	Comments       []AppliedComment
	StateChanged   bool
}

// RunMarker makes the daily due check idempotent: the marker and the actions
// commit together, so a rule is applied at most once per item.
type RunMarker struct {
	RuleID int64
	Day    string
}

// ApplyActions executes the ordered actions of one rule in a single
// transaction. Automation-driven changes never re-enter the rule engine: the
// caller only creates notifications afterwards, so a rule cannot trigger
// itself or another rule in the same round.
func (s *Store) ApplyActions(itemID, actorID int64, actions []Action, marker *RunMarker) (ActionResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return ActionResult{}, err
	}
	defer tx.Rollback()

	now := timestamp()
	if marker != nil {
		if marker.RuleID <= 0 || strings.TrimSpace(marker.Day) == "" {
			return ActionResult{}, ErrInvalidAction
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO automation_rule_runs (rule_id, item_id, day, created_at)
			VALUES (?, ?, ?, ?)`, marker.RuleID, itemID, marker.Day, now)
		if err != nil {
			return ActionResult{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return ActionResult{}, err
		}
		if affected == 0 {
			return ActionResult{}, ErrAlreadyRun
		}
	}

	current, err := getItemTx(tx, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return ActionResult{}, ErrNotFound
	}
	if err != nil {
		return ActionResult{}, err
	}
	if current.ArchivedAt != "" {
		return ActionResult{}, ErrArchived
	}

	result := ActionResult{NewAssigneeIDs: []int64{}, Comments: []AppliedComment{}}
	for _, action := range actions {
		switch strings.TrimSpace(action.Kind) {
		case "set_state":
			targetID, err := resolveActionState(tx, current.StateID, action.StateID)
			if err != nil {
				return ActionResult{}, err
			}
			if current.StateID == targetID {
				continue
			}
			if _, err := tx.Exec(`UPDATE items SET state_id = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`, targetID, now, itemID); err != nil {
				return ActionResult{}, err
			}
			if err := insertEvent(tx, itemID, actorID, "state_changed", "state", strconv.FormatInt(current.StateID, 10), strconv.FormatInt(targetID, 10), now); err != nil {
				return ActionResult{}, err
			}
			current.StateID = targetID
			result.StateChanged = true
		case "assign_user":
			if action.UserID <= 0 {
				return ActionResult{}, ErrAssigneeNotFound
			}
			if err := ensureUser(tx, action.UserID); err != nil {
				return ActionResult{}, err
			}
			currentIDs, err := assigneeIDsTx(tx, itemID)
			if err != nil {
				return ActionResult{}, err
			}
			if containsID(currentIDs, action.UserID) {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO item_assignees (item_id, user_id, assigned_at) VALUES (?, ?, ?)`, itemID, action.UserID, now); err != nil {
				return ActionResult{}, err
			}
			next := append(append([]int64{}, currentIDs...), action.UserID)
			sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
			if _, err := tx.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, now, itemID); err != nil {
				return ActionResult{}, err
			}
			if err := insertEvent(tx, itemID, actorID, "assignees_changed", "assignees", joinedIDs(currentIDs), joinedIDs(next), now); err != nil {
				return ActionResult{}, err
			}
			result.NewAssigneeIDs = append(result.NewAssigneeIDs, action.UserID)
		case "add_comment":
			body := strings.TrimSpace(action.Body)
			if body == "" {
				return ActionResult{}, ErrCommentEmpty
			}
			if len(body) > maxCommentLength {
				return ActionResult{}, ErrCommentTooLong
			}
			mentions, err := normalizeUsers(tx, action.MentionIDs, ErrCommentNotFound)
			if err != nil {
				return ActionResult{}, err
			}
			res, err := tx.Exec(`INSERT INTO comments (item_id, author_id, body, created_at) VALUES (?, ?, ?, ?)`, itemID, actorID, body, now)
			if err != nil {
				return ActionResult{}, err
			}
			commentID, err := res.LastInsertId()
			if err != nil {
				return ActionResult{}, err
			}
			if err := replaceMentions(tx, commentID, mentions); err != nil {
				return ActionResult{}, err
			}
			if err := insertEvent(tx, itemID, actorID, "comment_added", "", "", strconv.FormatInt(commentID, 10), now); err != nil {
				return ActionResult{}, err
			}
			result.Comments = append(result.Comments, AppliedComment{ID: commentID, Body: body, MentionIDs: mentions})
		default:
			return ActionResult{}, ErrInvalidAction
		}
	}
	if err := tx.Commit(); err != nil {
		return ActionResult{}, err
	}
	updated, err := s.Get(itemID)
	if err != nil {
		return ActionResult{}, err
	}
	result.Item = updated
	return result, nil
}

func ensureUser(q queryer, id int64) error {
	var count int
	if err := q.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ? AND disabled_at IS NULL`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrAssigneeNotFound
	}
	return nil
}

func containsID(list []int64, value int64) bool {
	for _, id := range list {
		if id == value {
			return true
		}
	}
	return false
}

// Archive hides the item and, for an Action, its Micro children in the same
// transaction. Nothing is deleted from the database.
func (s *Store) Archive(id, actorID int64) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := getItemTx(tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.ArchivedAt != "" {
		return nil, ErrArchived
	}

	now := timestamp()
	archived := []int64{id}
	if current.Type == "action" {
		rows, err := tx.Query(`SELECT id FROM items WHERE parent_id = ? AND type = 'micro' AND archived_at IS NULL`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var childID int64
			if err := rows.Scan(&childID); err != nil {
				rows.Close()
				return nil, err
			}
			archived = append(archived, childID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	for _, itemID := range archived {
		if _, err := tx.Exec(`UPDATE items SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`, now, now, itemID); err != nil {
			return nil, err
		}
		if err := insertEvent(tx, itemID, actorID, "archived", "", "", "", now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return archived, nil
}

func (s *Store) Relations(it Item) (*ItemRef, []ItemRef, error) {
	var parent *ItemRef
	if it.ParentID != nil {
		var ref ItemRef
		err := s.db.QueryRow(`
			SELECT i.id, i.type, i.title, i.state_id, ws.name
			FROM items i JOIN workflow_states ws ON ws.id = i.state_id
			WHERE i.id = ?`, *it.ParentID).Scan(&ref.ID, &ref.Type, &ref.Title, &ref.StateID, &ref.StateName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrParentNotFound
		}
		if err != nil {
			return nil, nil, err
		}
		parent = &ref
	}
	rows, err := s.db.Query(`
		SELECT i.id, i.type, i.title, i.state_id, ws.name
		FROM items i JOIN workflow_states ws ON ws.id = i.state_id
		WHERE i.parent_id = ? AND i.archived_at IS NULL
		ORDER BY i.type, i.title COLLATE NOCASE`, it.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	children := []ItemRef{}
	for rows.Next() {
		var ref ItemRef
		if err := rows.Scan(&ref.ID, &ref.Type, &ref.Title, &ref.StateID, &ref.StateName); err != nil {
			return nil, nil, err
		}
		children = append(children, ref)
	}
	return parent, children, rows.Err()
}

func (s *Store) CreateComment(itemID, authorID int64, body string, mentionIDs []int64) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, ErrCommentEmpty
	}
	if len(body) > maxCommentLength {
		return Comment{}, ErrCommentTooLong
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()

	item, err := getItemTx(tx, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, ErrNotFound
	}
	if err != nil {
		return Comment{}, err
	}
	if item.ArchivedAt != "" {
		return Comment{}, ErrArchived
	}
	mentions, err := normalizeUsers(tx, mentionIDs, ErrCommentNotFound)
	if err != nil {
		return Comment{}, err
	}

	now := timestamp()
	res, err := tx.Exec(`INSERT INTO comments (item_id, author_id, body, created_at) VALUES (?, ?, ?, ?)`, itemID, authorID, body, now)
	if err != nil {
		return Comment{}, err
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		return Comment{}, err
	}
	if err := replaceMentions(tx, commentID, mentions); err != nil {
		return Comment{}, err
	}
	if err := insertEvent(tx, itemID, authorID, "comment_added", "", "", strconv.FormatInt(commentID, 10), now); err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Comment{}, err
	}
	return s.GetComment(commentID)
}

func (s *Store) GetComment(id int64) (Comment, error) {
	var c Comment
	var edited sql.NullString
	err := s.db.QueryRow(`
		SELECT c.id, c.item_id, c.author_id, u.name, c.body, c.created_at, c.edited_at
		FROM comments c JOIN users u ON u.id = c.author_id
		WHERE c.id = ?`, id).Scan(&c.ID, &c.ItemID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt, &edited)
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, ErrCommentNotFound
	}
	if err != nil {
		return Comment{}, err
	}
	if edited.Valid {
		c.EditedAt = edited.String
	}
	byComment, err := s.mentionsFor([]int64{id})
	if err != nil {
		return Comment{}, err
	}
	if list, ok := byComment[id]; ok {
		c.Mentions = list
	}
	if c.Mentions == nil {
		c.Mentions = []Mention{}
	}
	return c, nil
}

func (s *Store) ListComments(itemID int64) ([]Comment, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.item_id, c.author_id, u.name, c.body, c.created_at, c.edited_at
		FROM comments c JOIN users u ON u.id = c.author_id
		WHERE c.item_id = ?
		ORDER BY c.created_at, c.id`, itemID)
	if err != nil {
		return nil, err
	}
	out := []Comment{}
	ids := []int64{}
	for rows.Next() {
		var c Comment
		var edited sql.NullString
		if err := rows.Scan(&c.ID, &c.ItemID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt, &edited); err != nil {
			rows.Close()
			return nil, err
		}
		if edited.Valid {
			c.EditedAt = edited.String
		}
		c.Mentions = []Mention{}
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	byComment, err := s.mentionsFor(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if list, ok := byComment[out[i].ID]; ok {
			out[i].Mentions = list
		}
	}
	return out, nil
}

// UpdateComment lets the author or an admin rewrite a comment. Mentions are
// replaced, never merged, so removing a mention really removes the recipient.
func (s *Store) UpdateComment(commentID, actorID int64, actorRole, body string, mentionIDs []int64) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, ErrCommentEmpty
	}
	if len(body) > maxCommentLength {
		return Comment{}, ErrCommentTooLong
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()

	var itemID, authorID int64
	err = tx.QueryRow(`SELECT item_id, author_id FROM comments WHERE id = ?`, commentID).Scan(&itemID, &authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, ErrCommentNotFound
	}
	if err != nil {
		return Comment{}, err
	}
	if authorID != actorID && actorRole != "admin" {
		return Comment{}, ErrCommentAuthor
	}
	item, err := getItemTx(tx, itemID)
	if err != nil {
		return Comment{}, err
	}
	if item.ArchivedAt != "" {
		return Comment{}, ErrArchived
	}
	mentions, err := normalizeUsers(tx, mentionIDs, ErrCommentNotFound)
	if err != nil {
		return Comment{}, err
	}
	now := timestamp()
	if _, err := tx.Exec(`UPDATE comments SET body = ?, edited_at = ? WHERE id = ?`, body, now, commentID); err != nil {
		return Comment{}, err
	}
	if err := replaceMentions(tx, commentID, mentions); err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Comment{}, err
	}
	return s.GetComment(commentID)
}

func (s *Store) Events(itemID int64) ([]Event, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.kind, COALESCE(e.field, ''), COALESCE(e.from_value, ''), COALESCE(e.to_value, ''),
		       COALESCE(e.actor_id, 0), COALESCE(u.name, ''), e.created_at
		FROM item_events e LEFT JOIN users u ON u.id = e.actor_id
		WHERE e.item_id = ?
		ORDER BY e.created_at, e.id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.Field, &e.FromValue, &e.ToValue, &e.ActorID, &e.ActorName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const itemColumns = `i.id, i.project_id, p.name, i.type, i.parent_id, i.title, i.description, i.priority,
	       i.state_id, ws.name, ws.category, i.due_on, i.created_by, u.name, i.created_at, i.updated_at, i.archived_at`

const itemFrom = `FROM items i
	JOIN projects p ON p.id = i.project_id
	JOIN workflow_states ws ON ws.id = i.state_id
	JOIN users u ON u.id = i.created_by`

const itemSelect = `SELECT ` + itemColumns + `
	` + itemFrom

type scanner interface {
	Scan(dest ...any) error
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func scanItem(row scanner) (Item, error) {
	return scanItemWith(row, nil)
}

// scanItemWith scans the shared item columns plus any extra columns a search
// query appended (the three snippets).
func scanItemWith(row scanner, extra []any) (Item, error) {
	var it Item
	var parent sql.NullInt64
	var due, archived sql.NullString
	dest := []any{
		&it.ID, &it.ProjectID, &it.ProjectName, &it.Type, &parent, &it.Title, &it.Description, &it.Priority,
		&it.StateID, &it.StateName, &it.StateCategory, &due, &it.CreatedBy, &it.CreatedByName, &it.CreatedAt, &it.UpdatedAt, &archived,
	}
	dest = append(dest, extra...)
	err := row.Scan(dest...)
	if err != nil {
		return Item{}, err
	}
	if parent.Valid {
		id := parent.Int64
		it.ParentID = &id
	}
	if due.Valid {
		it.DueOn = due.String
	}
	if archived.Valid {
		it.ArchivedAt = archived.String
	}
	it.Assignees = []Assignee{}
	return it, nil
}

func getItemTx(tx *sql.Tx, id int64) (Item, error) {
	return scanItem(tx.QueryRow(itemSelect+` WHERE i.id = ?`, id))
}

func (s *Store) assigneesFor(ids []int64) (map[int64][]Assignee, error) {
	out := map[int64][]Assignee{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT ia.item_id, u.id, u.name, u.email, u.role, ia.assigned_at
		FROM item_assignees ia JOIN users u ON u.id = ia.user_id
		WHERE ia.item_id IN (`+placeholders+`)
		ORDER BY u.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var a Assignee
		if err := rows.Scan(&itemID, &a.UserID, &a.Name, &a.Email, &a.Role, &a.AssignedAt); err != nil {
			return nil, err
		}
		out[itemID] = append(out[itemID], a)
	}
	return out, rows.Err()
}

func (s *Store) mentionsFor(ids []int64) (map[int64][]Mention, error) {
	out := map[int64][]Mention{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT cm.comment_id, u.id, u.name
		FROM comment_mentions cm JOIN users u ON u.id = cm.user_id
		WHERE cm.comment_id IN (`+placeholders+`)
		ORDER BY u.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var commentID int64
		var m Mention
		if err := rows.Scan(&commentID, &m.UserID, &m.Name); err != nil {
			return nil, err
		}
		out[commentID] = append(out[commentID], m)
	}
	return out, rows.Err()
}

func validateHierarchy(q queryer, projectID int64, typ string, parentID *int64, selfID int64) error {
	if typ == "observer" {
		if parentID != nil && *parentID != 0 {
			return ErrParentInvalid
		}
		return nil
	}
	if typ == "micro" && (parentID == nil || *parentID == 0) {
		return ErrParentRequired
	}
	if parentID == nil || *parentID == 0 {
		return nil
	}
	parent := *parentID
	if selfID != 0 && parent == selfID {
		return ErrParentInvalid
	}
	var parentProject int64
	var parentType string
	var archived sql.NullString
	err := q.QueryRow(`SELECT project_id, type, archived_at FROM items WHERE id = ?`, parent).Scan(&parentProject, &parentType, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrParentNotFound
	}
	if err != nil {
		return err
	}
	if parentProject != projectID {
		return ErrParentProject
	}
	if archived.Valid {
		return ErrParentArchived
	}
	if typ == "micro" {
		if parentType != "action" {
			return ErrParentInvalid
		}
		return nil
	}
	if parentType != "observer" {
		return ErrParentInvalid
	}
	return nil
}

func ensureTypeKeepsChildren(q queryer, itemID int64, typ string) error {
	if typ == "action" {
		return nil
	}
	var count int
	if err := q.QueryRow(`SELECT COUNT(*) FROM items WHERE parent_id = ? AND type = 'micro' AND archived_at IS NULL`, itemID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrChildrenRequireAction
	}
	return nil
}

func ensureState(q queryer, stateID int64) error {
	var count int
	if err := q.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE id = ?`, stateID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrStateNotFound
	}
	return nil
}

// ensureStateInFlow rejects a state that belongs to a different project or to a
// flow not assigned to the item type.
func ensureStateInFlow(q queryer, stateID, projectID int64, itemType string) error {
	var count int
	if err := q.QueryRow(`
		SELECT COUNT(*)
		FROM workflow_states ws
		JOIN project_workflows pw ON pw.workflow_id = ws.workflow_id
		WHERE ws.id = ? AND pw.project_id = ? AND pw.item_type = ?`,
		stateID, projectID, itemType,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrStateNotFound
	}
	return nil
}

// resolveActionState returns the state an automation should move the item to.
// Automation rules can predate a per-project copy, so a state that belongs to
// another flow is resolved by name inside the item's workflow. A target that
// cannot be resolved is an error, never a silent no-op.
func resolveActionState(q queryer, currentStateID, actionStateID int64) (int64, error) {
	var currentWorkflow int64
	var actionWorkflow int64
	if err := q.QueryRow(
		`SELECT workflow_id FROM workflow_states WHERE id = ?`, currentStateID,
	).Scan(&currentWorkflow); err != nil {
		return 0, err
	}
	if err := q.QueryRow(
		`SELECT workflow_id FROM workflow_states WHERE id = ?`, actionStateID,
	).Scan(&actionWorkflow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrStateNotFound
		}
		return 0, err
	}
	if currentWorkflow == actionWorkflow {
		return actionStateID, nil
	}
	var byName int64
	err := q.QueryRow(`
		SELECT target.id
		FROM workflow_states target
		WHERE target.workflow_id = ?
		  AND target.name = (SELECT name FROM workflow_states WHERE id = ?)`,
		currentWorkflow, actionStateID,
	).Scan(&byName)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrStateNotFound
	}
	return byName, err
}

func normalizeUsers(q queryer, ids []int64, missing error) ([]int64, error) {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id <= 0 {
			return nil, missing
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var count int
		if err := q.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, missing
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func assigneeIDsTx(tx *sql.Tx, itemID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT user_id FROM item_assignees WHERE item_id = ? ORDER BY user_id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func replaceAssignees(tx *sql.Tx, itemID int64, ids []int64, at string) error {
	if _, err := tx.Exec(`DELETE FROM item_assignees WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO item_assignees (item_id, user_id, assigned_at) VALUES (?, ?, ?)`, itemID, id, at); err != nil {
			return err
		}
	}
	return nil
}

func replaceMentions(tx *sql.Tx, commentID int64, ids []int64) error {
	if _, err := tx.Exec(`DELETE FROM comment_mentions WHERE comment_id = ?`, commentID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO comment_mentions (comment_id, user_id) VALUES (?, ?)`, commentID, id); err != nil {
			return err
		}
	}
	return nil
}

func insertEvent(tx *sql.Tx, itemID, actorID int64, kind, field, from, to, at string) error {
	_, err := tx.Exec(`
		INSERT INTO item_events (item_id, actor_id, kind, field, from_value, to_value, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		itemID, nullableID(&actorID), kind, nullableText(field), nullableText(from), nullableText(to), at,
	)
	return err
}

func validType(value string) bool {
	switch value {
	case "observer", "action", "micro", "bug":
		return true
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

func nullableID(id *int64) any {
	if id == nil || *id == 0 {
		return nil
	}
	return *id
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parentValue(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}

func sameParent(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func joinedIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
