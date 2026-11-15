package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"jiralike/internal/auth"
	"jiralike/internal/projects"
)

// reportsGet returns state and workload counts for the projects the caller can
// read. A project filter is validated with the same access rule as the project
// endpoints: an invisible or archived project answers 404, never an empty
// report that would confirm it exists.
func (s *Server) reportsGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	var projectID int64
	if value := strings.TrimSpace(r.URL.Query().Get("project_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid project id")
			return
		}
		projectID = id
	}
	if projectID > 0 {
		p, err := s.projectsStore.Get(projectID)
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project get failed")
			return
		}
		if p.ArchivedAt != "" {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		canSee, err := s.projectsStore.CanSee(p, user.ID, user.Role)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project access check failed")
			return
		}
		if !canSee {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
	}

	report, err := s.itemsStore.Report(user.ID, user.Role, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "report failed")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
