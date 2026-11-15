// Package automations stores automation rules and executes them in order.
// Actions run inside the transaction that triggered them and never re-enter
// the rule engine, so a rule cannot cascade into another rule in one round.
package automations

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound           = errors.New("automation rule not found")
	ErrNameRequired       = errors.New("rule name is required")
	ErrNameTooLong        = errors.New("rule name is too long")
	ErrInvalidTrigger     = errors.New("invalid automation trigger")
	ErrTriggerState       = errors.New("state trigger needs a valid state")
	ErrTriggerValue       = errors.New("only the state trigger accepts a trigger value")
	ErrActionsRequired    = errors.New("at least one action is required")
	ErrInvalidAction      = errors.New("invalid automation action")
	ErrActionValue        = errors.New("automation action value is missing or invalid")
	ErrCommentEmpty       = errors.New("automation comment is empty")
	ErrCommentTooLong     = errors.New("automation comment is too long")
	ErrProjectNotFound    = errors.New("project not found")
	ErrProjectArchived    = errors.New("project is archived")
	ErrStateNotFound      = errors.New("state not found")
	ErrUserNotFound       = errors.New("user not found")
	ErrTooManyActions     = errors.New("too many actions")
	ErrTooManyProjects    = errors.New("too many projects")
	ErrTooManyMentions    = errors.New("too many mentions")
	ErrRuleNotManageable  = errors.New("automation rule is not manageable")
	ErrRuleProjectMissing = errors.New("a manager rule must name its projects")
)

const (
	maxNameLength    = 120
	maxCommentLength = 20000
	maxActions       = 20
	maxProjects      = 200
	maxMentions      = 200
)

// Rule is one installation-wide automation. An empty project list means every
// project. Actions are executed in position order.
type Rule struct {
	ID               int64        `json:"id"`
	Name             string       `json:"name"`
	Enabled          bool         `json:"enabled"`
	TriggerKind      string       `json:"trigger_kind"`
	TriggerValue     string       `json:"trigger_value"`
	TriggerStateID   int64        `json:"trigger_state_id,omitempty"`
	TriggerStateName string       `json:"trigger_state_name,omitempty"`
	ProjectIDs       []int64      `json:"project_ids"`
	Projects         []ProjectRef `json:"projects"`
	Actions          []Action     `json:"actions"`
	CreatedBy        int64        `json:"created_by"`
	CreatedAt        string       `json:"created_at"`
}

type ProjectRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Action keeps both the stored value and the explicit fields the UI and the
// engine use, so nobody has to parse free text to know what an action does.
type Action struct {
	ID         int64   `json:"id"`
	Position   int     `json:"position"`
	Kind       string  `json:"kind"`
	Value      string  `json:"value"`
	StateID    int64   `json:"state_id,omitempty"`
	UserID     int64   `json:"user_id,omitempty"`
	Body       string  `json:"body,omitempty"`
	MentionIDs []int64 `json:"mention_ids"`
}

type ActionInput struct {
	Kind       string
	StateID    int64
	UserID     int64
	Body       string
	MentionIDs []int64
}

type RuleInput struct {
	Name           string
	Enabled        bool
	TriggerKind    string
	TriggerStateID int64
	TriggerValue   string
	ProjectIDs     []int64
	Actions        []ActionInput
}

// Event is what the HTTP handlers hand to the engine. StateID is the arrival
// state for state_changed and is ignored for the other triggers.
type Event struct {
	Kind      string
	ProjectID int64
	ItemID    int64
	StateID   int64
	Day       string
}

type DueItem struct {
	ID        int64
	ProjectID int64
}

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// List returns every rule for an admin. A manager only sees rules that name at
// least one project and whose projects are all manageable by them; a global
// rule is an admin-only object.
func (s *Store) List() ([]Rule, error) {
	rows, err := s.db.Query(`
		SELECT id, name, enabled, trigger_kind, trigger_value, created_by, created_at
		FROM automation_rules
		ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []Rule{}
	ids := []int64{}
	for rows.Next() {
		var r Rule
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &enabled, &r.TriggerKind, &r.TriggerValue, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		r.ProjectIDs = []int64{}
		r.Projects = []ProjectRef{}
		r.Actions = []Action{}
		rules = append(rules, r)
		ids = append(ids, r.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.hydrate(rules, ids)
}

func (s *Store) Get(id int64) (Rule, error) {
	var r Rule
	var enabled int
	err := s.db.QueryRow(`
		SELECT id, name, enabled, trigger_kind, trigger_value, created_by, created_at
		FROM automation_rules WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &enabled, &r.TriggerKind, &r.TriggerValue, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, err
	}
	r.Enabled = enabled == 1
	r.ProjectIDs = []int64{}
	r.Projects = []ProjectRef{}
	r.Actions = []Action{}
	list, err := s.hydrate([]Rule{r}, []int64{id})
	if err != nil {
		return Rule{}, err
	}
	return list[0], nil
}

// Matching returns the enabled rules whose trigger and project scope match the
// event. It does not check who may edit the rule: an enabled rule runs for
// every user who can cause the event.
func (s *Store) Matching(event Event) ([]Rule, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.enabled, r.trigger_kind, r.trigger_value, r.created_by, r.created_at
		FROM automation_rules r
		WHERE r.enabled = 1
		  AND r.trigger_kind = ?
		  AND (r.trigger_kind <> 'state_changed' OR r.trigger_value = '' OR r.trigger_value = ?)
		  AND (
			NOT EXISTS (SELECT 1 FROM automation_rule_projects rp WHERE rp.rule_id = r.id)
			OR EXISTS (SELECT 1 FROM automation_rule_projects rp WHERE rp.rule_id = r.id AND rp.project_id = ?)
		  )
		ORDER BY r.id`, event.Kind, strconv.FormatInt(event.StateID, 10), event.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []Rule{}
	ids := []int64{}
	for rows.Next() {
		var r Rule
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &enabled, &r.TriggerKind, &r.TriggerValue, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		r.ProjectIDs = []int64{}
		r.Projects = []ProjectRef{}
		r.Actions = []Action{}
		rules = append(rules, r)
		ids = append(ids, r.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.hydrate(rules, ids)
}

// DueItems returns open, non-archived items whose due date passed before the
// given UTC day. Archived projects never appear. The daily rule marker makes
// repeated evaluations harmless.
func (s *Store) DueItems(day string) ([]DueItem, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.project_id
		FROM items i
		JOIN projects p ON p.id = i.project_id
		JOIN workflow_states ws ON ws.id = i.state_id
		WHERE i.archived_at IS NULL
		  AND p.archived_at IS NULL
		  AND ws.category = 'open'
		  AND i.due_on IS NOT NULL AND i.due_on <> '' AND i.due_on < ?
		ORDER BY i.id`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DueItem{}
	for rows.Next() {
		var item DueItem
		if err := rows.Scan(&item.ID, &item.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Create(in RuleInput, actorID int64) (Rule, error) {
	normalized, projectIDs, actions, err := s.validate(in)
	if err != nil {
		return Rule{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()

	now := timestamp()
	res, err := tx.Exec(`
		INSERT INTO automation_rules (name, enabled, trigger_kind, trigger_value, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		normalized.Name, boolToInt(normalized.Enabled), normalized.TriggerKind, normalized.TriggerValue, actorID, now)
	if err != nil {
		return Rule{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Rule{}, err
	}
	if err := insertProjects(tx, id, projectIDs); err != nil {
		return Rule{}, err
	}
	if err := insertActions(tx, id, actions); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return s.Get(id)
}

func (s *Store) Update(id int64, in RuleInput) (Rule, error) {
	normalized, projectIDs, actions, err := s.validate(in)
	if err != nil {
		return Rule{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		UPDATE automation_rules
		SET name = ?, enabled = ?, trigger_kind = ?, trigger_value = ?
		WHERE id = ?`,
		normalized.Name, boolToInt(normalized.Enabled), normalized.TriggerKind, normalized.TriggerValue, id)
	if err != nil {
		return Rule{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Rule{}, err
	}
	if affected == 0 {
		return Rule{}, ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM automation_rule_projects WHERE rule_id = ?`, id); err != nil {
		return Rule{}, err
	}
	if err := insertProjects(tx, id, projectIDs); err != nil {
		return Rule{}, err
	}
	if _, err := tx.Exec(`DELETE FROM automation_actions WHERE rule_id = ?`, id); err != nil {
		return Rule{}, err
	}
	if err := insertActions(tx, id, actions); err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return s.Get(id)
}

// Delete removes the rule, its actions, its project scope and its run markers.
func (s *Store) Delete(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM automation_rules WHERE id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM automation_rule_runs WHERE rule_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM automation_actions WHERE rule_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM automation_rule_projects WHERE rule_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM automation_rules WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) validate(in RuleInput) (RuleInput, []int64, []ActionInput, error) {
	out := RuleInput{
		Name:           strings.TrimSpace(in.Name),
		Enabled:        in.Enabled,
		TriggerKind:    strings.TrimSpace(in.TriggerKind),
		TriggerStateID: in.TriggerStateID,
	}
	if out.Name == "" {
		return RuleInput{}, nil, nil, ErrNameRequired
	}
	if len(out.Name) > maxNameLength {
		return RuleInput{}, nil, nil, ErrNameTooLong
	}
	switch out.TriggerKind {
	case "state_changed":
		if out.TriggerStateID <= 0 || !s.stateExists(out.TriggerStateID) {
			return RuleInput{}, nil, nil, ErrTriggerState
		}
		out.TriggerValue = strconv.FormatInt(out.TriggerStateID, 10)
	case "assigned", "comment_added", "due_passed":
		if out.TriggerStateID != 0 {
			return RuleInput{}, nil, nil, ErrTriggerValue
		}
	default:
		return RuleInput{}, nil, nil, ErrInvalidTrigger
	}

	projectIDs, err := s.normalizeProjects(in.ProjectIDs)
	if err != nil {
		return RuleInput{}, nil, nil, err
	}
	if len(in.Actions) == 0 {
		return RuleInput{}, nil, nil, ErrActionsRequired
	}
	if len(in.Actions) > maxActions {
		return RuleInput{}, nil, nil, ErrTooManyActions
	}
	actions := make([]ActionInput, 0, len(in.Actions))
	for _, action := range in.Actions {
		normalized, err := s.validateAction(action)
		if err != nil {
			return RuleInput{}, nil, nil, err
		}
		actions = append(actions, normalized)
	}
	return out, projectIDs, actions, nil
}

func (s *Store) validateAction(in ActionInput) (ActionInput, error) {
	out := ActionInput{Kind: strings.TrimSpace(in.Kind)}
	switch out.Kind {
	case "set_state":
		if in.StateID <= 0 || !s.stateExists(in.StateID) {
			return ActionInput{}, fmt.Errorf("%w: %v", ErrActionValue, ErrStateNotFound)
		}
		out.StateID = in.StateID
	case "assign_user":
		if in.UserID <= 0 || !s.userExists(in.UserID) {
			return ActionInput{}, fmt.Errorf("%w: %v", ErrActionValue, ErrUserNotFound)
		}
		out.UserID = in.UserID
	case "add_comment":
		out.Body = strings.TrimSpace(in.Body)
		if out.Body == "" {
			return ActionInput{}, ErrCommentEmpty
		}
		if len(out.Body) > maxCommentLength {
			return ActionInput{}, ErrCommentTooLong
		}
		if len(in.MentionIDs) > maxMentions {
			return ActionInput{}, ErrTooManyMentions
		}
		mentions, err := s.normalizeMentions(in.MentionIDs)
		if err != nil {
			return ActionInput{}, err
		}
		out.MentionIDs = mentions
	case "notify":
		if strings.TrimSpace(in.Body) != "" || in.StateID != 0 || in.UserID != 0 || len(in.MentionIDs) != 0 {
			return ActionInput{}, fmt.Errorf("%w: notify takes no value", ErrActionValue)
		}
	default:
		return ActionInput{}, ErrInvalidAction
	}
	return out, nil
}

func (s *Store) normalizeProjects(ids []int64) ([]int64, error) {
	if len(ids) > maxProjects {
		return nil, ErrTooManyProjects
	}
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrProjectNotFound
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var archived sql.NullString
		err := s.db.QueryRow(`SELECT archived_at FROM projects WHERE id = ?`, id).Scan(&archived)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		if err != nil {
			return nil, err
		}
		if archived.Valid && archived.String != "" {
			return nil, ErrProjectArchived
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Store) normalizeMentions(ids []int64) ([]int64, error) {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			if id <= 0 {
				return nil, ErrUserNotFound
			}
			continue
		}
		seen[id] = true
		if !s.userExists(id) {
			return nil, ErrUserNotFound
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Store) stateExists(id int64) bool {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workflow_states WHERE id = ?`, id).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func (s *Store) userExists(id int64) bool {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ? AND disabled_at IS NULL`, id).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func insertProjects(tx *sql.Tx, ruleID int64, ids []int64) error {
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO automation_rule_projects (rule_id, project_id) VALUES (?, ?)`, ruleID, id); err != nil {
			return err
		}
	}
	return nil
}

func insertActions(tx *sql.Tx, ruleID int64, actions []ActionInput) error {
	for i, action := range actions {
		value := ""
		mentions := ""
		switch action.Kind {
		case "set_state":
			value = strconv.FormatInt(action.StateID, 10)
		case "assign_user":
			value = strconv.FormatInt(action.UserID, 10)
		case "add_comment":
			value = action.Body
			mentions = joinIDs(action.MentionIDs)
		}
		if _, err := tx.Exec(`
			INSERT INTO automation_actions (rule_id, position, kind, value, mentions)
			VALUES (?, ?, ?, ?, ?)`, ruleID, i, action.Kind, value, mentions); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) hydrate(rules []Rule, ids []int64) ([]Rule, error) {
	if len(ids) == 0 {
		return rules, nil
	}
	byID := map[int64]*Rule{}
	for i := range rules {
		byID[rules[i].ID] = &rules[i]
	}
	if err := s.hydrateProjects(ids, byID); err != nil {
		return nil, err
	}
	if err := s.hydrateActions(ids, byID); err != nil {
		return nil, err
	}
	if err := s.hydrateTriggerStates(byID); err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *Store) hydrateProjects(ids []int64, byID map[int64]*Rule) error {
	query := `
		SELECT rp.rule_id, p.id, p.name
		FROM automation_rule_projects rp
		JOIN projects p ON p.id = rp.project_id
		WHERE rp.rule_id IN (` + placeholders(len(ids)) + `)
		ORDER BY p.name COLLATE NOCASE, p.id`
	rows, err := s.db.Query(query, int64Args(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ruleID int64
		var ref ProjectRef
		if err := rows.Scan(&ruleID, &ref.ID, &ref.Name); err != nil {
			return err
		}
		if rule, ok := byID[ruleID]; ok {
			rule.ProjectIDs = append(rule.ProjectIDs, ref.ID)
			rule.Projects = append(rule.Projects, ref)
		}
	}
	return rows.Err()
}

func (s *Store) hydrateActions(ids []int64, byID map[int64]*Rule) error {
	query := `
		SELECT id, rule_id, position, kind, value, mentions
		FROM automation_actions
		WHERE rule_id IN (` + placeholders(len(ids)) + `)
		ORDER BY rule_id, position, id`
	rows, err := s.db.Query(query, int64Args(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var action Action
		var ruleID int64
		var mentions string
		if err := rows.Scan(&action.ID, &ruleID, &action.Position, &action.Kind, &action.Value, &mentions); err != nil {
			return err
		}
		action.MentionIDs = splitIDs(mentions)
		switch action.Kind {
		case "set_state":
			action.StateID, _ = strconv.ParseInt(action.Value, 10, 64)
		case "assign_user":
			action.UserID, _ = strconv.ParseInt(action.Value, 10, 64)
		case "add_comment":
			action.Body = action.Value
		}
		if rule, ok := byID[ruleID]; ok {
			rule.Actions = append(rule.Actions, action)
		}
	}
	return rows.Err()
}

func (s *Store) hydrateTriggerStates(byID map[int64]*Rule) error {
	ids := []int64{}
	for _, rule := range byID {
		if rule.TriggerKind == "state_changed" && rule.TriggerValue != "" {
			if value, err := strconv.ParseInt(rule.TriggerValue, 10, 64); err == nil && value > 0 {
				rule.TriggerStateID = value
				ids = append(ids, value)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names := map[int64]string{}
	rows, err := s.db.Query(`SELECT id, name FROM workflow_states WHERE id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rule := range byID {
		if rule.TriggerStateID > 0 {
			rule.TriggerStateName = names[rule.TriggerStateID]
		}
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func joinIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func splitIDs(value string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
