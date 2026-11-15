package httpapi

import (
	"errors"
	"net/http"

	"jiralike/internal/auth"
	"jiralike/internal/items"
)

// searchRebuild repairs the full-text index after a manual restore or a
// suspected drift. Normal writes are indexed by triggers, so this is an
// explicit admin action, never part of a request path.
func (s *Server) searchRebuild(w http.ResponseWriter, r *http.Request, user auth.User) {
	count, err := s.itemsStore.RebuildSearch()
	if errors.Is(err, items.ErrSearchUnavailable) {
		writeError(w, 500, "search index unavailable")
		return
	}
	if err != nil {
		writeError(w, 500, "search rebuild failed")
		return
	}
	writeJSON(w, 200, map[string]any{"indexed": count})
}
