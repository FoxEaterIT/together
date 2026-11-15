package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// streamStatus opens a notification stream just far enough to read the status
// code, then closes it. Valid sessions answer 200 and start the event stream;
// an expired guest is refused before the hub is touched.
func streamStatus(t *testing.T, baseURL string, cookie *http.Cookie, bearer string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/notifications/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// TestRoleAccessMatrixV2 extends the v1 release matrix to every route added in
// v2: the notification stream, the search index rebuild, the profile 2FA
// endpoints, the administrator reset and the 2FA policy. Guest and expired
// guest are checked explicitly so the new surface cannot drift from the rest.
func TestRoleAccessMatrixV2(t *testing.T) {
	s, baseURL := streamServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager-2fa@example.test", "manager", 0)
	member := makeUser(t, s, admin.ID, "Member", "member-2fa@example.test", "member", 0)
	resetTarget := makeUser(t, s, admin.ID, "Reset Target", "reset-target@example.test", "member", 0)
	guest := makeUser(t, s, admin.ID, "Guest", "guest-2fa@example.test", "guest", 30)
	expiredGuest := makeUser(t, s, admin.ID, "Expired Guest", "expired-2fa@example.test", "guest", 30)
	managerCookie := login(t, s, manager.Email, "password-strong")
	memberCookie := login(t, s, member.Email, "password-strong")
	guestCookie := login(t, s, guest.Email, "password-strong")
	expiredCookie := login(t, s, expiredGuest.Email, "password-strong")
	if _, err := s.db.Exec(`UPDATE users SET guest_expires_on = '2020-01-01' WHERE id = ?`, expiredGuest.ID); err != nil {
		t.Fatal(err)
	}

	// The stream admits every live role, including guests, and rejects an
	// expired guest, an anonymous request and a personal API token.
	for name, cookie := range map[string]*http.Cookie{
		"admin": adminCookie, "manager": managerCookie, "member": memberCookie, "guest": guestCookie,
	} {
		stream := openStream(t, baseURL, cookie)
		if frame := stream.next(t); frame.event != "ready" {
			t.Fatalf("%s stream first frame = %+v", name, frame)
		}
	}
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"expired guest", expiredCookie, http.StatusForbidden},
		{"anonymous", nil, http.StatusUnauthorized},
	} {
		if status := streamStatus(t, baseURL, tc.cookie, ""); status != tc.want {
			t.Fatalf("%s stream: expected %d, got %d", tc.name, tc.want, status)
		}
	}
	token, err := s.auth.CreateToken(admin.ID, "stream-matrix")
	if err != nil {
		t.Fatal(err)
	}
	if status := streamStatus(t, baseURL, nil, token.Token); status != http.StatusUnauthorized {
		t.Fatalf("token stream: expected 401, got %d", status)
	}

	// JSON routes added in v2, checked against every actor.
	roleCases := []struct {
		name   string
		method string
		path   string
		body   string
		want   map[string]int
	}{
		{
			"2FA status", http.MethodGet, "/api/me/2fa", "",
			map[string]int{"admin": 200, "manager": 200, "member": 200, "guest": 200, "expired guest": 401},
		},
		{
			"2FA start", http.MethodPost, "/api/me/2fa/start", "",
			map[string]int{"admin": 200, "manager": 200, "member": 200, "guest": 403, "expired guest": 401},
		},
		{
			"admin reset", http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", resetTarget.ID), "",
			map[string]int{"admin": 204, "manager": 403, "member": 403, "guest": 403, "expired guest": 401},
		},
		{
			"security read", http.MethodGet, "/api/admin/security", "",
			map[string]int{"admin": 200, "manager": 403, "member": 403, "guest": 403, "expired guest": 401},
		},
		{
			"security write", http.MethodPut, "/api/admin/security", `{"require_2fa":false}`,
			map[string]int{"admin": 200, "manager": 403, "member": 403, "guest": 403, "expired guest": 401},
		},
		{
			"search rebuild", http.MethodPost, "/api/admin/search/rebuild", "",
			map[string]int{"admin": 200, "manager": 403, "member": 403, "guest": 403, "expired guest": 401},
		},
	}
	cookies := map[string]*http.Cookie{
		"admin": adminCookie, "manager": managerCookie, "member": memberCookie,
		"guest": guestCookie, "expired guest": expiredCookie,
	}
	for _, tc := range roleCases {
		for actor, want := range tc.want {
			w := apiDo(t, s, tc.method, tc.path, cookies[actor], tc.body)
			if w.Code != want {
				t.Fatalf("%s as %s: expected %d, got %d (%s)", tc.name, actor, want, w.Code, w.Body.String())
			}
		}
		if w := apiDo(t, s, tc.method, tc.path, nil, tc.body); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s anonymous: expected 401, got %d", tc.name, w.Code)
		}
	}

	// An administrator can reset somebody else, never their own account.
	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", admin.ID), adminCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("admin self reset: expected 403, got %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/me/2fa", guestCookie, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("guest 2FA status: %d %s", w.Code, w.Body.String())
	}
}
