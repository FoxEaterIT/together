package items

import (
	"database/sql"
	"strings"
	"time"
)

// StateCount is one workflow state name and category with the number of
// visible, active items in it. Flows are owned by projects, so the same state
// name can exist in many of them: the report groups by name and category, which
// keeps one "Done" row instead of one per project copy. States with no items are
// included so the report mirrors the workflows in use.
type StateCount struct {
	StateName     string `json:"state_name"`
	StateCategory string `json:"state_category"`
	Count         int    `json:"count"`
}

// WorkloadCount is the number of open items assigned to one person. An item
// with several assignees is counted for each of them.
type WorkloadCount struct {
	UserID         int64  `json:"user_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	GuestExpiresOn string `json:"guest_expires_on"`
	Disabled       bool   `json:"disabled"`
	Expired        bool   `json:"expired"`
	OpenCount      int    `json:"open_count"`
}

type ReportTotals struct {
	Open int `json:"open"`
	Done int `json:"done"`
	All  int `json:"all"`
}

type Report struct {
	States   []StateCount    `json:"states"`
	Workload []WorkloadCount `json:"workload"`
	Totals   ReportTotals    `json:"totals"`
}

// Report aggregates state and workload counts over the projects the viewer can
// read. The visibility predicate is applied in SQL, so a member or guest never
// receives counts from a private project they cannot open.
func (s *Store) Report(viewerID int64, role string, projectID int64) (Report, error) {
	report := Report{States: []StateCount{}, Workload: []WorkloadCount{}}

	join := []string{"p.archived_at IS NULL"}
	joinArgs := []any{}
	if projectID > 0 {
		join = append(join, "p.id = ?")
		joinArgs = append(joinArgs, projectID)
	}
	join, joinArgs = withProjectVisibility(join, joinArgs, role, viewerID)

	stateQuery := `SELECT ws.name, ws.category, COUNT(i.id)
		FROM workflow_states ws
		JOIN workflows wf ON wf.id = ws.workflow_id
		JOIN projects p ON p.id = wf.project_id
		LEFT JOIN items i ON i.state_id = ws.id AND i.archived_at IS NULL
		WHERE ` + strings.Join(join, " AND ") + `
		  AND EXISTS (SELECT 1 FROM project_workflows pw WHERE pw.workflow_id = ws.workflow_id)
		GROUP BY ws.name, ws.category
		ORDER BY MIN(ws.position) ASC, ws.name COLLATE NOCASE ASC`
	stateRows, err := s.db.Query(stateQuery, joinArgs...)
	if err != nil {
		return Report{}, err
	}
	for stateRows.Next() {
		var entry StateCount
		if err := stateRows.Scan(&entry.StateName, &entry.StateCategory, &entry.Count); err != nil {
			stateRows.Close()
			return Report{}, err
		}
		report.States = append(report.States, entry)
		if entry.StateCategory == "done" {
			report.Totals.Done += entry.Count
		} else {
			report.Totals.Open += entry.Count
		}
	}
	if err := stateRows.Err(); err != nil {
		stateRows.Close()
		return Report{}, err
	}
	stateRows.Close()
	report.Totals.All = report.Totals.Open + report.Totals.Done

	where := []string{"p.archived_at IS NULL"}
	args := []any{}
	if projectID > 0 {
		where = append(where, "p.id = ?")
		args = append(args, projectID)
	}
	where, args = withProjectVisibility(where, args, role, viewerID)

	workloadQuery := `SELECT u.id, u.name, u.role, COALESCE(u.guest_expires_on, ''), u.disabled_at,
		       COUNT(DISTINCT i.id)
		FROM item_assignees ia
		JOIN items i ON i.id = ia.item_id AND i.archived_at IS NULL
		JOIN workflow_states ws ON ws.id = i.state_id AND ws.category = 'open'
		JOIN projects p ON p.id = i.project_id
		JOIN users u ON u.id = ia.user_id
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY u.id, u.name, u.role, u.guest_expires_on, u.disabled_at
		ORDER BY COUNT(DISTINCT i.id) DESC, u.name COLLATE NOCASE ASC, u.id ASC`
	workloadRows, err := s.db.Query(workloadQuery, args...)
	if err != nil {
		return Report{}, err
	}
	defer workloadRows.Close()
	today := time.Now().UTC().Format("2006-01-02")
	for workloadRows.Next() {
		var entry WorkloadCount
		var disabled sql.NullString
		if err := workloadRows.Scan(&entry.UserID, &entry.Name, &entry.Role, &entry.GuestExpiresOn, &disabled, &entry.OpenCount); err != nil {
			return Report{}, err
		}
		entry.Disabled = disabled.Valid
		entry.Expired = entry.Role == "guest" && entry.GuestExpiresOn != "" && entry.GuestExpiresOn < today
		report.Workload = append(report.Workload, entry)
	}
	if err := workloadRows.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}
