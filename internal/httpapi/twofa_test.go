package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/secrets"
	"jiralike/internal/totp"
)

type challengeJSON struct {
	TwoFactorRequired      bool   `json:"two_factor_required"`
	TwoFactorSetupRequired bool   `json:"two_factor_setup_required"`
	ChallengeToken         string `json:"challenge_token"`
	ExpiresAt              string `json:"expires_at"`
}

func resetLoginFailures(s *Server) {
	s.loginMu.Lock()
	s.loginAttempts = make(map[string]loginAttempt)
	s.loginMu.Unlock()
}

func passwordLogin(t *testing.T, s *Server, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	return apiDo(t, s, http.MethodPost, "/api/login", nil, body)
}

func decodeChallenge(t *testing.T, w *httptest.ResponseRecorder) challengeJSON {
	t.Helper()
	var body challengeJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode challenge: %v (%s)", err, w.Body.String())
	}
	if body.ChallengeToken == "" {
		t.Fatalf("challenge token missing: %s", w.Body.String())
	}
	return body
}

func responseCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == auth.SessionCookie && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("session cookie missing: %s", w.Body.String())
	return nil
}

func totpSecret(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	var stored string
	if err := s.db.QueryRow(`SELECT secret_enc FROM user_totp WHERE user_id = ?`, userID).Scan(&stored); err != nil {
		t.Fatalf("read TOTP secret: %v", err)
	}
	if !secrets.IsSealed(stored) {
		t.Fatalf("TOTP secret must be sealed, got %q", stored)
	}
	secret, err := s.secrets.Open(stored)
	if err != nil {
		t.Fatalf("open TOTP secret: %v", err)
	}
	return secret
}

func totpNow(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	code, err := totp.Code(totpSecret(t, s, userID), time.Now())
	if err != nil {
		t.Fatalf("compute TOTP: %v", err)
	}
	return code
}

// resetTOTPStep makes the current authenticator code usable again. Enrollment
// and a successful login both persist the accepted step, so a test that wants
// to assert a different failure (challenge lock, expiry, scope) must clear it.
func resetTOTPStep(t *testing.T, s *Server, userID int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE user_totp SET last_used_step = 0 WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("reset TOTP step: %v", err)
	}
}

func enableTwoFactor(t *testing.T, s *Server, userID int64) []string {
	t.Helper()
	secret, uri, err := s.auth.StartTOTP(userID)
	if err != nil {
		t.Fatalf("start TOTP: %v", err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("unexpected otpauth URI: %s", uri)
	}
	code, err := totp.Code(secret, time.Now())
	if err != nil {
		t.Fatalf("compute TOTP: %v", err)
	}
	codes, err := s.auth.ConfirmTOTP(userID, code)
	if err != nil {
		t.Fatalf("confirm TOTP: %v", err)
	}
	return codes
}

func TestTwoFactorEnrollmentLoginAndRecovery(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")

	// Start returns a secret and QR URI but changes nothing yet.
	w := apiDo(t, s, http.MethodPost, "/api/me/2fa/start", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("2fa start: %d %s", w.Code, w.Body.String())
	}
	var start struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if start.Secret == "" || !strings.HasPrefix(start.URI, "otpauth://totp/") {
		t.Fatalf("bad start response: %+v", start)
	}
	w = apiDo(t, s, http.MethodGet, "/api/me/2fa", cookie, "")
	if !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("pending status: %s", w.Body.String())
	}

	// A wrong confirmation leaves enrollment pending.
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/confirm", cookie, `{"code":"000000"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirm: %d %s", w.Code, w.Body.String())
	}
	code, err := totp.Code(start.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w = apiDo(t, s, http.MethodPost, "/api/me/2fa/confirm", cookie, fmt.Sprintf(`{"code":%q}`, code))
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var confirm struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &confirm); err != nil {
		t.Fatal(err)
	}
	if len(confirm.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(confirm.RecoveryCodes))
	}
	for _, recovery := range confirm.RecoveryCodes {
		if len(recovery) != 19 || strings.Count(recovery, "-") != 3 {
			t.Fatalf("unexpected recovery code format: %q", recovery)
		}
	}
	w = apiDo(t, s, http.MethodGet, "/api/me/2fa", cookie, "")
	if !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"recovery_codes_remaining":10`) {
		t.Fatalf("enabled status: %s", w.Body.String())
	}
	// Secrets never travel outside enrollment responses.
	if w := apiDo(t, s, http.MethodGet, "/api/me", cookie, ""); strings.Contains(w.Body.String(), start.Secret) {
		t.Fatal("secret leaked in /api/me")
	}
	if w := apiDo(t, s, http.MethodGet, "/api/users", cookie, ""); strings.Contains(w.Body.String(), start.Secret) {
		t.Fatal("secret leaked in /api/users")
	}

	// Password alone no longer produces a session or a cookie.
	resetLoginFailures(s)
	w = passwordLogin(t, s, admin.Email, "password-strong")
	if w.Code != http.StatusOK {
		t.Fatalf("password login: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("password-only login must not set a session cookie")
	}
	challenge := decodeChallenge(t, w)
	if !challenge.TwoFactorRequired || challenge.TwoFactorSetupRequired {
		t.Fatalf("expected a verify challenge: %+v", challenge)
	}
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":"000000"}`, challenge.ChallengeToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong second factor: %d %s", w.Code, w.Body.String())
	}
	resetTOTPStep(t, s, admin.ID)
	code = totpNow(t, s, admin.ID)
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa", nil, fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, challenge.ChallengeToken, code))
	if w.Code != http.StatusOK {
		t.Fatalf("second step: %d %s", w.Code, w.Body.String())
	}
	responseCookie(t, w)
	if !strings.Contains(w.Body.String(), `"two_factor_enabled":true`) {
		t.Fatalf("login payload missing 2FA flag: %s", w.Body.String())
	}

	// A challenge is single use, and the same TOTP step cannot be replayed.
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, challenge.ChallengeToken, code)); w.Code != http.StatusUnauthorized {
		t.Fatalf("consumed challenge accepted: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	w = passwordLogin(t, s, admin.Email, "password-strong")
	replay := decodeChallenge(t, w)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, replay.ChallengeToken, code)); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP accepted: %d %s", w.Code, w.Body.String())
	}

	// Recovery codes are single use.
	resetLoginFailures(s)
	w = passwordLogin(t, s, admin.Email, "password-strong")
	first := decodeChallenge(t, w)
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, first.ChallengeToken, confirm.RecoveryCodes[0]))
	if w.Code != http.StatusOK {
		t.Fatalf("recovery login: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	w = passwordLogin(t, s, admin.Email, "password-strong")
	second := decodeChallenge(t, w)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, second.ChallengeToken, confirm.RecoveryCodes[0])); w.Code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code accepted: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, second.ChallengeToken, confirm.RecoveryCodes[1])); w.Code != http.StatusOK {
		t.Fatalf("second recovery code: %d %s", w.Code, w.Body.String())
	}

	// API tokens remain single-factor.
	created, err := s.auth.CreateToken(admin.ID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Authorization", "Bearer "+created.Token)
	me := httptest.NewRecorder()
	s.Handler().ServeHTTP(me, r)
	if me.Code != http.StatusOK {
		t.Fatalf("token auth with 2FA enabled: %d %s", me.Code, me.Body.String())
	}
}

func TestTwoFactorGuestExemptionAndRoleTransitions(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	enableTwoFactor(t, s, guest.ID)
	if err := s.auth.SetRequire2FA(true); err != nil {
		t.Fatal(err)
	}

	// A guest is never challenged, even with an enrollment and the admin switch.
	resetLoginFailures(s)
	w := passwordLogin(t, s, guest.Email, "password-strong")
	if w.Code != http.StatusOK {
		t.Fatalf("guest login: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "challenge_token") {
		t.Fatalf("guest was challenged: %s", w.Body.String())
	}
	responseCookie(t, w)

	// Promoting the guest re-enables enforcement without losing the enrollment.
	if w := apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/users/%d", guest.ID), adminCookie, `{"role":"member"}`); w.Code != http.StatusNoContent {
		t.Fatalf("promote guest: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	w = passwordLogin(t, s, guest.Email, "password-strong")
	challenge := decodeChallenge(t, w)
	if !challenge.TwoFactorRequired {
		t.Fatalf("promoted user not challenged: %+v", challenge)
	}

	// Demoting back to guest disables enforcement but keeps the secret.
	if w := apiDo(t, s, http.MethodPut, fmt.Sprintf("/api/users/%d", guest.ID), adminCookie, `{"role":"guest"}`); w.Code != http.StatusNoContent {
		t.Fatalf("demote guest: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	if w := passwordLogin(t, s, guest.Email, "password-strong"); strings.Contains(w.Body.String(), "challenge_token") {
		t.Fatalf("guest challenged after demotion: %s", w.Body.String())
	}
	var still int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_totp WHERE user_id = ? AND confirmed_at IS NOT NULL`, guest.ID).Scan(&still); err != nil || still != 1 {
		t.Fatalf("enrollment lost on demotion: %d %v", still, err)
	}
}

func TestRequire2FAEnrollmentFlowAndExistingSessions(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	// A session opened before the switch stays valid until it expires.
	memberCookie := login(t, s, member.Email, "password-strong")

	if w := apiDo(t, s, http.MethodPut, "/api/admin/security", adminCookie, `{"require_2fa":true}`); w.Code != http.StatusOK {
		t.Fatalf("enable require_2fa: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/me", memberCookie, ""); w.Code != http.StatusOK {
		t.Fatalf("existing session was invalidated: %d %s", w.Code, w.Body.String())
	}

	resetLoginFailures(s)
	w := passwordLogin(t, s, member.Email, "password-strong")
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("enrollment-required login set a cookie")
	}
	challenge := decodeChallenge(t, w)
	if !challenge.TwoFactorSetupRequired || challenge.TwoFactorRequired {
		t.Fatalf("expected setup challenge: %+v", challenge)
	}

	// A verify-only endpoint rejects an enrollment challenge and vice versa.
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":"123456"}`, challenge.ChallengeToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("enroll challenge accepted on verify endpoint: %d %s", w.Code, w.Body.String())
	}

	w = apiDo(t, s, http.MethodPost, "/api/login/2fa/start", nil,
		fmt.Sprintf(`{"challenge_token":%q}`, challenge.ChallengeToken))
	if w.Code != http.StatusOK {
		t.Fatalf("enroll start: %d %s", w.Code, w.Body.String())
	}
	var start struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil || start.Secret == "" {
		t.Fatalf("enroll start payload: %+v %v", start, err)
	}
	code, err := totp.Code(start.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resetLoginFailures(s)
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa/confirm", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":"000000"}`, challenge.ChallengeToken))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong enrollment code: %d %s", w.Code, w.Body.String())
	}
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa/confirm", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, challenge.ChallengeToken, code))
	if w.Code != http.StatusOK {
		t.Fatalf("enroll confirm: %d %s", w.Code, w.Body.String())
	}
	responseCookie(t, w)
	if !strings.Contains(w.Body.String(), `"recovery_codes"`) || !strings.Contains(w.Body.String(), `"user"`) {
		t.Fatalf("enroll confirm payload: %s", w.Body.String())
	}

	// Turning the switch off does not remove the enrollment.
	if w := apiDo(t, s, http.MethodPut, "/api/admin/security", adminCookie, `{"require_2fa":false}`); w.Code != http.StatusOK {
		t.Fatalf("disable require_2fa: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	if w := passwordLogin(t, s, member.Email, "password-strong"); !strings.Contains(w.Body.String(), `"two_factor_required":true`) {
		t.Fatalf("enrolled user should still be challenged: %s", w.Body.String())
	}
}

func TestTwoFactorChallengeLimits(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	enableTwoFactor(t, s, admin.ID)

	// A verify challenge cannot be used for enrollment.
	resetLoginFailures(s)
	w := passwordLogin(t, s, admin.Email, "password-strong")
	challenge := decodeChallenge(t, w)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa/start", nil,
		fmt.Sprintf(`{"challenge_token":%q}`, challenge.ChallengeToken)); w.Code != http.StatusUnauthorized {
		t.Fatalf("verify challenge used for enrollment: %d %s", w.Code, w.Body.String())
	}

	// Five wrong codes lock the challenge even if the sixth code is right.
	for i := 0; i < 5; i++ {
		resetLoginFailures(s)
		if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
			fmt.Sprintf(`{"challenge_token":%q,"code":"000000"}`, challenge.ChallengeToken)); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	resetLoginFailures(s)
	resetTOTPStep(t, s, admin.ID)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, challenge.ChallengeToken, totpNow(t, s, admin.ID))); w.Code != http.StatusUnauthorized {
		t.Fatalf("locked challenge accepted: %d %s", w.Code, w.Body.String())
	}

	// Tampered and expired tokens fail generically.
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil, `{"challenge_token":"not-a-token","code":"123456"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d", w.Code)
	}
	expired, _, err := s.auth.CreateChallenge(admin.ID, "verify", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resetLoginFailures(s)
	resetTOTPStep(t, s, admin.ID)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, expired, totpNow(t, s, admin.ID))); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired challenge: %d %s", w.Code, w.Body.String())
	}

	// An enrollment challenge cannot be replayed as a verify challenge.
	enroll, _, err := s.auth.CreateEnrollChallenge(admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	resetLoginFailures(s)
	resetTOTPStep(t, s, admin.ID)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, enroll, totpNow(t, s, admin.ID))); w.Code != http.StatusUnauthorized {
		t.Fatalf("enroll challenge accepted as verify: %d %s", w.Code, w.Body.String())
	}
}

func TestTwoFactorDisableAndRecoveryRotation(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, s, admin.Email, "password-strong")
	codes := enableTwoFactor(t, s, admin.ID)

	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/disable", cookie,
		fmt.Sprintf(`{"password":"wrong-password","code":%q}`, codes[0])); w.Code != http.StatusUnauthorized {
		t.Fatalf("disable with wrong password: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/disable", cookie,
		`{"password":"password-strong","code":"000000"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("disable with wrong code: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/disable", cookie,
		fmt.Sprintf(`{"password":"password-strong","code":%q}`, codes[0])); w.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/me/2fa", cookie, ""); !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("status after disable: %s", w.Body.String())
	}
	resetLoginFailures(s)
	if w := passwordLogin(t, s, admin.Email, "password-strong"); strings.Contains(w.Body.String(), "challenge_token") {
		t.Fatalf("disabled account still challenged: %s", w.Body.String())
	}

	// Re-enroll and rotate recovery codes. The previous set stops working.
	codes = enableTwoFactor(t, s, admin.ID)
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/recovery-codes", cookie,
		fmt.Sprintf(`{"password":"wrong-password","code":%q}`, codes[1])); w.Code != http.StatusUnauthorized {
		t.Fatalf("rotate with wrong password: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/recovery-codes", cookie,
		`{"password":"password-strong","code":"000000"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("rotate with wrong code: %d %s", w.Code, w.Body.String())
	}
	w := apiDo(t, s, http.MethodPost, "/api/me/2fa/recovery-codes", cookie,
		fmt.Sprintf(`{"password":"password-strong","code":%q}`, codes[1]))
	if w.Code != http.StatusOK {
		t.Fatalf("rotate codes: %d %s", w.Code, w.Body.String())
	}
	var rotated struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil || len(rotated.RecoveryCodes) != 10 {
		t.Fatalf("rotated codes: %+v %v", rotated, err)
	}

	resetLoginFailures(s)
	w = passwordLogin(t, s, admin.Email, "password-strong")
	old := decodeChallenge(t, w)
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, old.ChallengeToken, codes[2])); w.Code != http.StatusUnauthorized {
		t.Fatalf("old recovery code accepted: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/login/2fa", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, old.ChallengeToken, rotated.RecoveryCodes[0])); w.Code != http.StatusOK {
		t.Fatalf("new recovery code rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminResetTwoFactor(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	enableTwoFactor(t, s, member.ID)
	memberCookie := login(t, s, member.Email, "password-strong")
	manager := makeUser(t, s, admin.ID, "Manager", "manager@example.test", "manager", 0)
	managerCookie := login(t, s, manager.Email, "password-strong")

	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", member.ID), managerCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("manager reset: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", admin.ID), adminCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("self reset: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, "/api/users/99999/2fa/reset", adminCookie, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown user reset: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodPost, fmt.Sprintf("/api/users/%d/2fa/reset", member.ID), adminCookie, ""); w.Code != http.StatusNoContent {
		t.Fatalf("admin reset: %d %s", w.Code, w.Body.String())
	}
	// The reset revokes open sessions so a stolen cookie cannot survive it.
	if w := apiDo(t, s, http.MethodGet, "/api/me", memberCookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("session survived reset: %d %s", w.Code, w.Body.String())
	}
	resetLoginFailures(s)
	if w := passwordLogin(t, s, member.Email, "password-strong"); strings.Contains(w.Body.String(), "challenge_token") {
		t.Fatalf("reset user still challenged: %s", w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, fmt.Sprintf("/api/users/%d", member.ID), adminCookie, ""); strings.Contains(w.Body.String(), `"two_factor_enabled":true`) {
		t.Fatalf("user still flagged with 2FA after reset: %s", w.Body.String())
	}
}

func TestTwoFactorAccessAndCSRF(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	member := makeUser(t, s, admin.ID, "Member", "member@example.test", "member", 0)
	memberCookie := login(t, s, member.Email, "password-strong")
	guest := makeUser(t, s, admin.ID, "Guest", "guest@example.test", "guest", 30)
	guestCookie := login(t, s, guest.Email, "password-strong")

	if w := apiDo(t, s, http.MethodGet, "/api/admin/security", memberCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("member security read: %d", w.Code)
	}
	if w := apiDo(t, s, http.MethodPut, "/api/admin/security", memberCookie, `{"require_2fa":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("member security write: %d", w.Code)
	}
	if w := apiDo(t, s, http.MethodGet, "/api/admin/security", adminCookie, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"require_2fa":false`) {
		t.Fatalf("admin security default: %d %s", w.Code, w.Body.String())
	}
	// Cross-site mutations keep being rejected.
	r := httptest.NewRequest(http.MethodPut, "/api/admin/security", strings.NewReader(`{"require_2fa":true}`))
	r.AddCookie(adminCookie)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site security write: %d %s", w.Code, w.Body.String())
	}
	// Guests cannot enroll.
	if w := apiDo(t, s, http.MethodPost, "/api/me/2fa/start", guestCookie, ""); w.Code != http.StatusForbidden {
		t.Fatalf("guest 2FA start: %d %s", w.Code, w.Body.String())
	}
	if w := apiDo(t, s, http.MethodGet, "/api/me/2fa", guestCookie, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("guest 2FA status: %d %s", w.Code, w.Body.String())
	}
	// Anonymous access is rejected before any handler runs.
	if w := apiDo(t, s, http.MethodGet, "/api/me/2fa", nil, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous 2FA status: %d", w.Code)
	}
}

func TestInviteAcceptEnforcesRequire2FA(t *testing.T) {
	s := testServer(t)
	admin, _, err := s.auth.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := login(t, s, admin.Email, "password-strong")
	if w := apiDo(t, s, http.MethodPut, "/api/admin/security", adminCookie, `{"require_2fa":true}`); w.Code != http.StatusOK {
		t.Fatalf("enable require_2fa: %d %s", w.Code, w.Body.String())
	}
	w := apiDo(t, s, http.MethodPost, "/api/invites", adminCookie, `{"email":"invited@example.test","role":"member"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", w.Code, w.Body.String())
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}
	resetLoginFailures(s)
	w = apiDo(t, s, http.MethodPost, "/api/invites/accept", nil,
		fmt.Sprintf(`{"token":%q,"name":"Invited","password":"password-strong"}`, invite.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("invite accept: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("invite acceptance set a session before enrollment")
	}
	challenge := decodeChallenge(t, w)
	if !challenge.TwoFactorSetupRequired {
		t.Fatalf("invite acceptance should require enrollment: %+v", challenge)
	}
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa/start", nil,
		fmt.Sprintf(`{"challenge_token":%q}`, challenge.ChallengeToken))
	if w.Code != http.StatusOK {
		t.Fatalf("invite enroll start: %d %s", w.Code, w.Body.String())
	}
	var start struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	code, err := totp.Code(start.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w = apiDo(t, s, http.MethodPost, "/api/login/2fa/confirm", nil,
		fmt.Sprintf(`{"challenge_token":%q,"code":%q}`, challenge.ChallengeToken, code))
	if w.Code != http.StatusOK {
		t.Fatalf("invite enroll confirm: %d %s", w.Code, w.Body.String())
	}
	responseCookie(t, w)
	if !strings.Contains(w.Body.String(), `"recovery_codes"`) {
		t.Fatalf("invite confirm missing recovery codes: %s", w.Body.String())
	}
}
