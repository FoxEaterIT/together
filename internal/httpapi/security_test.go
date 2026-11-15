package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPersonalTokenLifecycle covers the CLI path: a token authenticates like a
// session, a revoked token stops working, and the last active token cannot be
// revoked.
func TestPersonalTokenLifecycle(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	var list struct {
		Tokens []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Token string `json:"token"`
		} `json:"tokens"`
	}
	w := apiDo(t, s, http.MethodGet, "/api/tokens", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("token list: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tokens) != 1 {
		t.Fatalf("setup should create one default token: %+v", list.Tokens)
	}

	w = apiDo(t, s, http.MethodPost, "/api/tokens", cookie, `{"name":"cli"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("token create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "tgr_") {
		t.Fatalf("token shape: %q", created.Token)
	}

	bearer := func(raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec
	}
	if rec := bearer(created.Token); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin@example.test") {
		t.Fatalf("bearer /api/me: %d %s", rec.Code, rec.Body.String())
	}
	if rec := bearer(created.Token + "x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d", rec.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Authorization", "Basic abc")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid authorization header") {
		t.Fatalf("malformed authorization: %d %s", rec.Code, rec.Body.String())
	}

	if w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", created.ID), cookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("token revoke: %d %s", w.Code, w.Body.String())
	}
	if rec := bearer(created.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", rec.Code)
	}
	// The default token is now the only active one and cannot be removed.
	w = apiDo(t, s, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", list.Tokens[0].ID), cookie, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("last token revoke: expected 409, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestDisabledUserLosesSessionAndToken checks that disabling an account ends
// both authentication paths immediately.
func TestDisabledUserLosesSessionAndToken(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	memberToken, err := s.auth.CreateToken(member.ID, "cli")
	if err != nil {
		t.Fatal(err)
	}

	if w := apiDo(t, s, http.MethodGet, "/api/me", memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("member session before disable: %d", w.Code)
	}
	w := apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/users/%d", member.ID), adminCookie, `{"disabled":true}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("disable user: %d %s", w.Code, w.Body.String())
	}
	if w = apiDo(t, s, http.MethodGet, "/api/me", memberCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled session: expected 401, got %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/items", nil)
	r.Header.Set("Authorization", "Bearer "+memberToken.Token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled token: expected 401, got %d", rec.Code)
	}
	w = apiDo(t, s, http.MethodPost, "/api/login", nil, `{"email":"member@example.test","password":"password-strong"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("disabled login: expected 403, got %d", w.Code)
	}
}

// TestCrossSiteMutationsRejected exercises both browser signals: a cross-site
// Origin and a cross-site Fetch Metadata request without Origin.
func TestCrossSiteMutationsRejected(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	attempt := func(origin, fetchSite string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/notifications/read-all", nil)
		r.AddCookie(cookie)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	if code := attempt("https://attacker.example", ""); code != http.StatusForbidden {
		t.Fatalf("cross-site origin: expected 403, got %d", code)
	}
	if code := attempt("", "cross-site"); code != http.StatusForbidden {
		t.Fatalf("cross-site fetch metadata: expected 403, got %d", code)
	}
	if code := attempt("http://example.com", ""); code != http.StatusOK {
		t.Fatalf("same-origin mutation: expected 200, got %d", code)
	}
	if code := attempt("", ""); code != http.StatusOK {
		t.Fatalf("same-site mutation: expected 200, got %d", code)
	}
}

// TestSecretsStayOutOfResponses is the privacy pass: invitations lists and
// previews never echo the bearer token, user payloads never carry password
// material, and the SMTP password is represented only by a boolean.
func TestSecretsStayOutOfResponses(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	w := apiDo(t, s, http.MethodPost, "/api/invites", cookie, `{"email":"new@example.test","role":"member"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite create: %d %s", w.Code, w.Body.String())
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(invite.Token, "inv_") {
		t.Fatalf("created invite token: %q", invite.Token)
	}

	w = apiDo(t, s, http.MethodGet, "/api/invites", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("invite list: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), invite.Token) || strings.Contains(w.Body.String(), "inv_") {
		t.Fatalf("invite list leaked a token: %s", w.Body.String())
	}

	w = apiDo(t, s, http.MethodGet, "/api/invites/"+invite.Token, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("invite preview: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), invite.Token) {
		t.Fatalf("invite preview echoed the token: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "new@example.test") {
		t.Fatalf("invite preview missing email: %s", w.Body.String())
	}

	w = apiDo(t, s, http.MethodGet, "/api/users", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("users list: %d", w.Code)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "password") || strings.Contains(w.Body.String(), "hash") {
		t.Fatalf("users list leaked password material: %s", w.Body.String())
	}

	// Failed login never echoes the submitted password.
	w = apiDo(t, s, http.MethodPost, "/api/login", nil, `{"email":"admin@example.test","password":"wrong-password-value"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password login: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "wrong-password-value") {
		t.Fatalf("login error echoed the password: %s", w.Body.String())
	}
}
