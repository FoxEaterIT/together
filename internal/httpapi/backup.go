package httpapi

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/db"
)

// backupDownload streams a consistent SQLite snapshot to an administrator. The
// snapshot is built in a temporary directory and removed after the response,
// so no backup copies accumulate on the server.
func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request, user auth.User) {
	if s.db == nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	dir, err := os.MkdirTemp("", "together-backup-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup failed")
		return
	}
	defer os.RemoveAll(dir)

	name := "together-backup-" + time.Now().UTC().Format("20060102-150405") + ".db"
	path := filepath.Join(dir, name)
	if err := db.Backup(s.db, path); err != nil {
		log.Printf("backup by user %d failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "backup failed")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		log.Printf("backup by user %d open failed: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "backup failed")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup failed")
		return
	}

	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), file)
	log.Printf("backup by user %d: %d bytes", user.ID, info.Size())
}
