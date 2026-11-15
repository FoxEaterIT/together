package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type reportStateJSON struct {
	StateID       int64  `json:"state_id"`
	StateName     string `json:"state_name"`
	StateCategory string `json:"state_category"`
	Count         int    `json:"count"`
}

type reportWorkloadJSON struct {
	UserID         int64  `json:"user_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	GuestExpiresOn string `json:"guest_expires_on"`
	Disabled       bool   `json:"disabled"`
	Expired        bool   `json:"expired"`
	OpenCount      int    `json:"open_count"`
}

type reportJSON struct {
	States   []reportStateJSON    `json:"states"`
	Workload []reportWorkloadJSON `json:"workload"`
	Totals   struct {
		Open int `json:"open"`
		Done int `json:"done"`
		All  int `json:"all"`
	} `json:"totals"`
}

func decodeReport(t *testing.T, body []byte) reportJSON {
	t.Helper()
	var report reportJSON
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode report: %v (%s)", err, string(body))
	}
	return report
}

func reportState(t *testing.T, report reportJSON, name string) reportStateJSON {
	t.Helper()
	for _, state := range report.States {
		if state.StateName == name {
			return state
		}
	}
	t.Fatalf("state %q missing from report", name)
	return reportStateJSON{}
}

func reportPerson(t *testing.T, report reportJSON, id int64) reportWorkloadJSON {
	t.Helper()
	for _, person := range report.Workload {
		if person.UserID == id {
			return person
		}
	}
	t.Fatalf("person %d missing from workload", id)
	return reportWorkloadJSON{}
}

func TestReportsCountsVisibilityAndExpiredGuests(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	outsider := makeUser(t, s, admin.ID, "Outsider", "outsider@example.test", "member", 0)
	viewer := makeUser(t, s, admin.ID, "Viewer", "viewer@example.test", "guest", 30)
	memberCookie := login(t, s, member.Email, "password-strong")
	viewerCookie := login(t, s, viewer.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Open","visibility":"open"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create open project: %d %s", w.Code, w.Body.String())
	}
	open := decodeProject(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/projects", adminCookie, `{"name":"Private","visibility":"private"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create private project: %d %s", w.Code, w.Body.String())
	}
	private := decodeProject(t, w)

	create := func(projectID int64, body string) itemJSON {
		t.Helper()
		w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/projects/%d/items", projectID), adminCookie, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create item: %d %s", w.Code, w.Body.String())
		}
		return decodeItemResponse(t, w.Body.Bytes())
	}
	create(open.ID, fmt.Sprintf(`{"type":"action","title":"Member only","assignee_ids":[%d]}`, member.ID))
	create(open.ID, fmt.Sprintf(`{"type":"action","title":"Guest only","assignee_ids":[%d]}`, guest.ID))
	create(open.ID, fmt.Sprintf(`{"type":"action","title":"Shared","assignee_ids":[%d,%d]}`, member.ID, guest.ID))
	create(open.ID, `{"type":"action","title":"Unassigned"}`)
	done := create(open.ID, fmt.Sprintf(`{"type":"action","title":"Finished","assignee_ids":[%d]}`, member.ID))
	privateItem := create(private.ID, fmt.Sprintf(`{"type":"action","title":"Hidden","assignee_ids":[%d]}`, admin.ID))

	var doneState int64
	if err := s.db.QueryRow(`SELECT id FROM workflow_states WHERE category = 'done' ORDER BY id LIMIT 1`).Scan(&doneState); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE items SET state_id = ? WHERE id = ?`, doneState, done.ID); err != nil {
		t.Fatal(err)
	}

	w = apiDo(t, s, http.MethodGet, "/api/reports", memberCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("member report: %d %s", w.Code, w.Body.String())
	}
	memberReport := decodeReport(t, w.Body.Bytes())
	if memberReport.Totals.Open != 4 || memberReport.Totals.Done != 1 || memberReport.Totals.All != 5 {
		t.Fatalf("member totals: %+v", memberReport.Totals)
	}
	if got := reportState(t, memberReport, "To do").Count; got != 4 {
		t.Fatalf("member To do count: %d", got)
	}
	if got := reportState(t, memberReport, "Done").Count; got != 1 {
		t.Fatalf("member Done count: %d", got)
	}
	if got := reportPerson(t, memberReport, member.ID).OpenCount; got != 2 {
		t.Fatalf("member workload: %d", got)
	}
	if got := reportPerson(t, memberReport, guest.ID).OpenCount; got != 2 {
		t.Fatalf("guest workload: %d", got)
	}
	for _, person := range memberReport.Workload {
		if person.UserID == outsider.ID {
			t.Fatal("outsider without assignments must not appear in the workload")
		}
	}

	// A guest can read reports for the open projects they can already read.
	w = apiDo(t, s, http.MethodGet, "/api/reports", viewerCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("guest report: %d %s", w.Code, w.Body.String())
	}
	if got := decodeReport(t, w.Body.Bytes()).Totals.All; got != 5 {
		t.Fatalf("guest report total: %d", got)
	}

	// The private project and its item stay invisible to the member.
	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/reports?project_id=%d", private.ID), memberCookie, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("private project report for member: %d %s", w.Code, w.Body.String())
	}
	if itemVisibleTo(t, s, memberCookie, privateItem.ID) {
		t.Fatal("private item leaked to member")
	}

	w = apiDo(t, s, http.MethodGet, "/api/reports", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin report: %d %s", w.Code, w.Body.String())
	}
	adminReport := decodeReport(t, w.Body.Bytes())
	if adminReport.Totals.All != 6 {
		t.Fatalf("admin totals: %+v", adminReport.Totals)
	}
	if got := reportPerson(t, adminReport, member.ID).OpenCount; got != 2 {
		t.Fatalf("member workload in admin report: %d", got)
	}

	w = apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/reports?project_id=%d", private.ID), adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin private report: %d %s", w.Code, w.Body.String())
	}
	if got := decodeReport(t, w.Body.Bytes()).Totals.All; got != 1 {
		t.Fatalf("private report total: %d", got)
	}

	// An expired guest still counts if the work is still assigned to them.
	if _, err := s.db.Exec(`UPDATE users SET guest_expires_on = '2020-01-01' WHERE id = ?`, guest.ID); err != nil {
		t.Fatal(err)
	}
	w = apiDo(t, s, http.MethodGet, "/api/reports", adminCookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expired guest report: %d %s", w.Code, w.Body.String())
	}
	expired := reportPerson(t, decodeReport(t, w.Body.Bytes()), guest.ID)
	if !expired.Expired || expired.OpenCount != 2 {
		t.Fatalf("expired guest entry: %+v", expired)
	}

	if w = apiDo(t, s, http.MethodGet, "/api/reports", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous report: %d", w.Code)
	}
}

func itemVisibleTo(t *testing.T, s *Server, cookie *http.Cookie, itemID int64) bool {
	t.Helper()
	w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/items/%d", itemID), cookie, "")
	return w.Code == http.StatusOK
}
