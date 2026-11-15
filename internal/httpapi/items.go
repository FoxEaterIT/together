package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/automations"
	"jiralike/internal/items"
	"jiralike/internal/projects"
)

type itemCreateBody struct {
	Type        string  `json:"type"`
	ParentID    *int64  `json:"parent_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Priority    string  `json:"priority"`
	DueOn       string  `json:"due_on"`
	AssigneeIDs []int64 `json:"assignee_ids"`
}

type itemUpdateBody struct {
	Type        *string         `json:"type"`
	ParentID    json.RawMessage `json:"parent_id"`
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	Priority    *string         `json:"priority"`
	DueOn       *string         `json:"due_on"`
	AssigneeIDs *[]int64        `json:"assignee_ids"`
}

type commentBody struct {
	Body     string  `json:"body"`
	Mentions []int64 `json:"mentions"`
}

func (s *Server) itemsCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	projectID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid project id")
		return
	}
	p, ok := s.projectWritable(w, projectID, user)
	if !ok {
		return
	}
	var body itemCreateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if message, err := s.validateProjectUsers(p, body.AssigneeIDs); err != nil {
		writeError(w, 500, "assignee check failed")
		return
	} else if message != "" {
		writeError(w, 400, message)
		return
	}
	it, err := s.itemsStore.Create(items.CreateInput{
		ProjectID:   p.ID,
		Type:        body.Type,
		ParentID:    body.ParentID,
		Title:       body.Title,
		Description: body.Description,
		Priority:    body.Priority,
		DueOn:       body.DueOn,
		AssigneeIDs: body.AssigneeIDs,
		ActorID:     user.ID,
	})
	if err != nil {
		writeItemError(w, err, "item create failed")
		return
	}
	s.notifyNewAssignees(it, body.AssigneeIDs, user)
	if len(body.AssigneeIDs) > 0 {
		s.runAutomations(automations.Event{
			Kind:      "assigned",
			ProjectID: it.ProjectID,
			ItemID:    it.ID,
			StateID:   it.StateID,
		})
	}
	writeJSON(w, 201, it)
}

func (s *Server) itemsGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	it, ok := s.itemVisibleByID(w, itemID, user)
	if !ok {
		return
	}
	parent, children, err := s.itemsStore.Relations(it)
	if err != nil {
		writeError(w, 500, "item relations failed")
		return
	}
	transitions, err := s.workflowStore.Available(workflowSubject(it), workflowActor(user))
	if err != nil {
		writeError(w, 500, "transition list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"item": it, "parent": parent, "children": children, "transitions": transitions})
}

func (s *Server) itemsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	query := r.URL.Query()
	filter := items.Filter{
		Query:           query.Get("q"),
		Type:            query.Get("type"),
		Priority:        query.Get("priority"),
		StateCategory:   strings.ToLower(strings.TrimSpace(query.Get("state_category"))),
		DueFrom:         query.Get("due_from"),
		DueTo:           query.Get("due_to"),
		CreatedFrom:     query.Get("created_from"),
		CreatedTo:       query.Get("created_to"),
		UpdatedFrom:     query.Get("updated_from"),
		UpdatedTo:       query.Get("updated_to"),
		HasDue:          query.Get("has_due") == "1",
		NoDue:           query.Get("no_due") == "1",
		HasAssignee:     query.Get("has_assignee") == "1",
		Sort:            strings.ToLower(strings.TrimSpace(query.Get("sort"))),
		Total:           query.Get("total") == "1",
		Offset:          atoiOrZero(query.Get("offset")),
		IncludeArchived: query.Get("archived") == "1" && (user.Role == "admin" || user.Role == "manager"),
	}
	filter.Limit = atoiOrZero(query.Get("limit"))

	if filter.StateCategory != "" && filter.StateCategory != "open" && filter.StateCategory != "done" {
		writeError(w, 400, "invalid state category")
		return
	}
	if filter.Sort != "" {
		switch filter.Sort {
		case "relevance", "updated", "created", "due", "priority":
		default:
			writeError(w, 400, "invalid sort")
			return
		}
	}
	for name, value := range map[string]string{
		"created_from": filter.CreatedFrom,
		"created_to":   filter.CreatedTo,
		"updated_from": filter.UpdatedFrom,
		"updated_to":   filter.UpdatedTo,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", strings.TrimSpace(value)); err != nil {
			writeError(w, 400, "invalid "+name)
			return
		}
	}

	if value := strings.TrimSpace(query.Get("project_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 400, "invalid project id")
			return
		}
		filter.ProjectID = id
	}
	if value := strings.TrimSpace(query.Get("state_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 400, "invalid state id")
			return
		}
		filter.StateID = id
	}
	if value := strings.TrimSpace(query.Get("parent_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 400, "invalid parent id")
			return
		}
		filter.ParentID = id
	}
	if value := strings.TrimSpace(query.Get("assignee_id")); value != "" {
		if value == "me" {
			filter.AssigneeID = user.ID
		} else {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, 400, "invalid assignee id")
				return
			}
			filter.AssigneeID = id
		}
	}

	result, err := s.itemsStore.Search(user.ID, user.Role, filter)
	if err != nil {
		switch {
		case errors.Is(err, items.ErrInvalidSearch):
			writeError(w, 400, "invalid search query")
		case errors.Is(err, items.ErrInvalidSort):
			writeError(w, 400, "invalid sort")
		case errors.Is(err, items.ErrInvalidFilter):
			writeError(w, 400, "invalid filter")
		default:
			writeError(w, 500, "item list failed")
		}
		return
	}
	body := map[string]any{"items": result.Items}
	if filter.Total {
		body["total"] = result.Total
	}
	writeJSON(w, 200, body)
}

func (s *Server) itemsUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	current, ok := s.itemWritableByID(w, itemID, user)
	if !ok {
		return
	}
	var body itemUpdateBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}

	update := items.UpdateInput{
		Type:        current.Type,
		ParentID:    current.ParentID,
		Title:       current.Title,
		Description: current.Description,
		Priority:    current.Priority,
		DueOn:       current.DueOn,
		AssigneeIDs: assigneeIDs(current.Assignees),
		ActorID:     user.ID,
	}
	if body.Type != nil {
		update.Type = *body.Type
	}
	if body.Title != nil {
		update.Title = *body.Title
	}
	if body.Description != nil {
		update.Description = *body.Description
	}
	if body.Priority != nil {
		update.Priority = *body.Priority
	}
	if body.DueOn != nil {
		update.DueOn = *body.DueOn
	}
	if body.AssigneeIDs != nil {
		update.AssigneeIDs = *body.AssigneeIDs
	}
	if len(body.ParentID) > 0 {
		if string(body.ParentID) == "null" {
			update.ParentID = nil
		} else {
			var parentID int64
			if err := json.Unmarshal(body.ParentID, &parentID); err != nil || parentID <= 0 {
				writeError(w, 400, "invalid parent id")
				return
			}
			update.ParentID = &parentID
		}
	}

	p, err := s.projectsStore.Get(current.ProjectID)
	if err != nil {
		writeError(w, 500, "project get failed")
		return
	}
	if message, err := s.validateProjectUsers(p, update.AssigneeIDs); err != nil {
		writeError(w, 500, "assignee check failed")
		return
	} else if message != "" {
		writeError(w, 400, message)
		return
	}

	updated, err := s.itemsStore.Update(itemID, update)
	if err != nil {
		writeItemError(w, err, "item update failed")
		return
	}
	added := differenceIDs(update.AssigneeIDs, assigneeIDs(current.Assignees))
	s.notifyNewAssignees(updated, added, user)
	if len(added) > 0 {
		s.runAutomations(automations.Event{
			Kind:      "assigned",
			ProjectID: updated.ProjectID,
			ItemID:    updated.ID,
			StateID:   updated.StateID,
		})
	}
	writeJSON(w, 200, updated)
}

func (s *Server) itemsArchive(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	if _, ok := s.itemWritableByID(w, itemID, user); !ok {
		return
	}
	archived, err := s.itemsStore.Archive(itemID, user.ID)
	if err != nil {
		writeItemError(w, err, "item archive failed")
		return
	}
	writeJSON(w, 200, map[string]any{"archived_ids": archived})
}

func (s *Server) commentsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	if _, ok := s.itemVisibleByID(w, itemID, user); !ok {
		return
	}
	list, err := s.itemsStore.ListComments(itemID)
	if err != nil {
		writeError(w, 500, "comment list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"comments": list})
}

func (s *Server) commentsCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	it, ok := s.itemWritableByID(w, itemID, user)
	if !ok {
		return
	}
	var body commentBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	p, err := s.projectsStore.Get(it.ProjectID)
	if err != nil {
		writeError(w, 500, "project get failed")
		return
	}
	if message, err := s.validateProjectUsers(p, body.Mentions); err != nil {
		writeError(w, 500, "mention check failed")
		return
	} else if message != "" {
		writeError(w, 400, message)
		return
	}
	comment, err := s.itemsStore.CreateComment(itemID, user.ID, body.Body, body.Mentions)
	if err != nil {
		writeItemError(w, err, "comment create failed")
		return
	}
	s.notifyNewMentions(it, comment, body.Mentions, user)
	s.runAutomations(automations.Event{
		Kind:      "comment_added",
		ProjectID: it.ProjectID,
		ItemID:    it.ID,
		StateID:   it.StateID,
	})
	writeJSON(w, 201, comment)
}

func (s *Server) commentUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	commentID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid comment id")
		return
	}
	current, err := s.itemsStore.GetComment(commentID)
	if errors.Is(err, items.ErrCommentNotFound) {
		writeError(w, 404, "comment not found")
		return
	}
	if err != nil {
		writeError(w, 500, "comment get failed")
		return
	}
	it, ok := s.itemWritableByID(w, current.ItemID, user)
	if !ok {
		return
	}
	var body commentBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	p, err := s.projectsStore.Get(it.ProjectID)
	if err != nil {
		writeError(w, 500, "project get failed")
		return
	}
	if message, err := s.validateProjectUsers(p, body.Mentions); err != nil {
		writeError(w, 500, "mention check failed")
		return
	} else if message != "" {
		writeError(w, 400, message)
		return
	}
	comment, err := s.itemsStore.UpdateComment(commentID, user.ID, user.Role, body.Body, body.Mentions)
	if err != nil {
		if errors.Is(err, items.ErrCommentAuthor) {
			writeError(w, 403, err.Error())
			return
		}
		writeItemError(w, err, "comment update failed")
		return
	}
	added := differenceIDs(body.Mentions, mentionIDs(current.Mentions))
	s.notifyNewMentions(it, comment, added, user)
	writeJSON(w, 200, comment)
}

func (s *Server) eventsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	itemID, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid item id")
		return
	}
	if _, ok := s.itemVisibleByID(w, itemID, user); !ok {
		return
	}
	list, err := s.itemsStore.Events(itemID)
	if err != nil {
		writeError(w, 500, "event list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"events": list})
}

// projectWritable resolves a project for item creation or editing. Missing and
// invisible private projects both answer 404; guests and readers get 403.
func (s *Server) projectWritable(w http.ResponseWriter, projectID int64, user auth.User) (projects.Project, bool) {
	p, err := s.projectsStore.Get(projectID)
	if errors.Is(err, projects.ErrNotFound) {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	if err != nil {
		writeError(w, 500, "project get failed")
		return projects.Project{}, false
	}
	canView, err := s.projectsStore.CanView(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return projects.Project{}, false
	}
	if !canView {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return projects.Project{}, false
	}
	canWrite, err := s.projectsStore.CanWrite(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return projects.Project{}, false
	}
	if !canWrite {
		writeError(w, 403, "read-only access")
		return projects.Project{}, false
	}
	return p, true
}

func (s *Server) itemVisibleByID(w http.ResponseWriter, itemID int64, user auth.User) (items.Item, bool) {
	it, err := s.itemsStore.Get(itemID)
	if errors.Is(err, items.ErrNotFound) {
		writeError(w, 404, "item not found")
		return items.Item{}, false
	}
	if err != nil {
		writeError(w, 500, "item get failed")
		return items.Item{}, false
	}
	if it.ArchivedAt != "" {
		writeError(w, 404, "item not found")
		return items.Item{}, false
	}
	p, err := s.projectsStore.Get(it.ProjectID)
	if err != nil {
		writeError(w, 500, "project get failed")
		return items.Item{}, false
	}
	canSee, err := s.projectsStore.CanSee(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return items.Item{}, false
	}
	if !canSee {
		writeError(w, 404, "item not found")
		return items.Item{}, false
	}
	return it, true
}

func (s *Server) itemWritableByID(w http.ResponseWriter, itemID int64, user auth.User) (items.Item, bool) {
	it, ok := s.itemVisibleByID(w, itemID, user)
	if !ok {
		return items.Item{}, false
	}
	p, err := s.projectsStore.Get(it.ProjectID)
	if err != nil {
		writeError(w, 500, "project get failed")
		return items.Item{}, false
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return items.Item{}, false
	}
	canWrite, err := s.projectsStore.CanWrite(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return items.Item{}, false
	}
	if !canWrite {
		writeError(w, 403, "read-only access")
		return items.Item{}, false
	}
	return it, true
}

// validateProjectUsers checks that every assignee or mentioned user exists and
// can read the project. An empty message means everyone passed.
func (s *Server) validateProjectUsers(p projects.Project, ids []int64) (string, error) {
	for _, id := range ids {
		if id <= 0 {
			return "invalid user id", nil
		}
		u, err := s.auth.GetUser(id)
		if errors.Is(err, auth.ErrNotFound) {
			return fmt.Sprintf("user %d not found", id), nil
		}
		if err != nil {
			return "", err
		}
		ok, err := s.projectsStore.CanView(p, u.ID, u.Role)
		if err != nil {
			return "", err
		}
		if !ok {
			return fmt.Sprintf("user %d cannot access this project", id), nil
		}
	}
	return "", nil
}

func writeItemError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, items.ErrNotFound):
		writeError(w, 404, "item not found")
	case errors.Is(err, items.ErrParentNotFound):
		writeError(w, 404, "parent item not found")
	case errors.Is(err, items.ErrArchived):
		writeError(w, 409, "item is archived")
	case errors.Is(err, items.ErrStateConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, items.ErrParentArchived):
		writeError(w, 409, "parent is archived")
	case errors.Is(err, items.ErrTitleEmpty),
		errors.Is(err, items.ErrTitleTooLong),
		errors.Is(err, items.ErrInvalidType),
		errors.Is(err, items.ErrInvalidPriority),
		errors.Is(err, items.ErrParentRequired),
		errors.Is(err, items.ErrParentInvalid),
		errors.Is(err, items.ErrParentProject),
		errors.Is(err, items.ErrChildrenRequireAction),
		errors.Is(err, items.ErrStateNotFound),
		errors.Is(err, items.ErrDueInvalid),
		errors.Is(err, items.ErrAssigneeNotFound),
		errors.Is(err, items.ErrCommentEmpty),
		errors.Is(err, items.ErrCommentTooLong):
		writeError(w, 400, err.Error())
	case errors.Is(err, items.ErrCommentNotFound):
		writeError(w, 404, "comment not found")
	case errors.Is(err, items.ErrCommentAuthor):
		writeError(w, 403, err.Error())
	default:
		writeError(w, 500, fallback)
	}
}

func assigneeIDs(list []items.Assignee) []int64 {
	out := make([]int64, 0, len(list))
	for _, a := range list {
		out = append(out, a.UserID)
	}
	return out
}

func mentionIDs(list []items.Mention) []int64 {
	out := make([]int64, 0, len(list))
	for _, m := range list {
		out = append(out, m.UserID)
	}
	return out
}

func atoiOrZero(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}
