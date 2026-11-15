package httpapi

import (
	"errors"
	"log"
	"net/http"
	"time"

	"jiralike/internal/auth"
)

// twoFactorGate decides whether a freshly authenticated password can produce a
// session. It writes the challenge response and returns true when the caller
// must stop. Guests are never challenged.
func (s *Server) twoFactorGate(w http.ResponseWriter, user auth.User) bool {
	if user.Role == "guest" {
		return false
	}
	if user.TwoFactorEnabled {
		token, expires, err := s.auth.CreateVerifyChallenge(user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "two-factor challenge failed")
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"two_factor_required": true,
			"challenge_token":     token,
			"expires_at":          expires.Format(time.RFC3339),
		})
		return true
	}
	required, err := s.auth.Require2FA()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor policy failed")
		return true
	}
	if !required {
		return false
	}
	token, expires, err := s.auth.CreateEnrollChallenge(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor challenge failed")
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"two_factor_setup_required": true,
		"challenge_token":           token,
		"expires_at":                expires.Format(time.RFC3339),
	})
	return true
}

func (s *Server) loginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	userID, err := s.auth.CompleteLogin(body.ChallengeToken, body.Code)
	if err != nil {
		s.recordLoginFailure(r)
		writeError(w, http.StatusUnauthorized, "invalid code or challenge")
		return
	}
	s.clearLoginFailures(r)
	s.finishLogin(w, userID)
}

// finishLogin creates the personal token if needed and the session cookie,
// then answers with the public user payload.
func (s *Server) finishLogin(w http.ResponseWriter, userID int64) {
	if err := s.auth.EnsureDefaultToken(userID); err != nil {
		writeError(w, http.StatusInternalServerError, "token failed")
		return
	}
	user, err := s.auth.GetUser(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if err := s.startSession(w, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusOK, s.userPayload(user))
}

func (s *Server) loginTwoFactorStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeToken string `json:"challenge_token"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	userID, err := s.auth.EnrollChallengeUser(body.ChallengeToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid code or challenge")
		return
	}
	secret, uri, err := s.auth.StartTOTP(userID)
	if errors.Is(err, auth.ErrTOTPEnabled) {
		writeError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor setup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) loginTwoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	userID, codes, err := s.auth.ConfirmEnrollment(body.ChallengeToken, body.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCode):
			s.recordLoginFailure(r)
			writeError(w, http.StatusUnauthorized, "invalid code or challenge")
		case errors.Is(err, auth.ErrTOTPEnabled), errors.Is(err, auth.ErrTOTPNotStarted):
			writeError(w, http.StatusConflict, "two-factor setup was not started")
		default:
			s.recordLoginFailure(r)
			writeError(w, http.StatusUnauthorized, "invalid code or challenge")
		}
		return
	}
	s.clearLoginFailures(r)
	if err := s.auth.EnsureDefaultToken(userID); err != nil {
		writeError(w, http.StatusInternalServerError, "token failed")
		return
	}
	user, err := s.auth.GetUser(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if err := s.startSession(w, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":           s.userPayload(user),
		"recovery_codes": codes,
	})
}

func (s *Server) twoFactorStatus(w http.ResponseWriter, r *http.Request, user auth.User) {
	status, err := s.auth.TOTPStatus(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor status failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":                user.Role != "guest",
		"enabled":                  status.Enabled,
		"pending":                  status.Pending,
		"recovery_codes_remaining": status.RecoveryCodesRemaining,
	})
}

func (s *Server) twoFactorStart(w http.ResponseWriter, r *http.Request, user auth.User) {
	if user.Role == "guest" {
		writeError(w, http.StatusForbidden, "two-factor authentication is not available for guest accounts")
		return
	}
	secret, uri, err := s.auth.StartTOTP(user.ID)
	if errors.Is(err, auth.ErrTOTPEnabled) {
		writeError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor setup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) twoFactorConfirm(w http.ResponseWriter, r *http.Request, user auth.User) {
	if user.Role == "guest" {
		writeError(w, http.StatusForbidden, "two-factor authentication is not available for guest accounts")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	codes, err := s.auth.ConfirmTOTP(user.ID, body.Code)
	if errors.Is(err, auth.ErrInvalidCode) {
		writeError(w, http.StatusBadRequest, "invalid code")
		return
	}
	if errors.Is(err, auth.ErrTOTPEnabled) {
		writeError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	if errors.Is(err, auth.ErrTOTPNotStarted) {
		writeError(w, http.StatusConflict, "two-factor setup was not started")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor confirmation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (s *Server) twoFactorDisable(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	err := s.auth.DisableTOTP(user.ID, body.Password, body.Code)
	switch {
	case errors.Is(err, auth.ErrInvalidLogin), errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusUnauthorized, "wrong password or code")
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		writeError(w, http.StatusConflict, "two-factor authentication is not enabled")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "two-factor disable failed")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) twoFactorRecoveryCodes(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	codes, err := s.auth.RegenerateRecoveryCodes(user.ID, body.Password, body.Code)
	switch {
	case errors.Is(err, auth.ErrInvalidLogin), errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusUnauthorized, "wrong password or code")
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		writeError(w, http.StatusConflict, "two-factor authentication is not enabled")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "recovery codes failed")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	}
}

func (s *Server) securityGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	required, err := s.auth.Require2FA()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "security settings failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_2fa": required})
}

func (s *Server) securityUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Require2FA bool `json:"require_2fa"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.auth.SetRequire2FA(body.Require2FA); err != nil {
		writeError(w, http.StatusInternalServerError, "security settings failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_2fa": body.Require2FA})
}

func (s *Server) userTwoFactorReset(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if id == user.ID {
		writeError(w, http.StatusForbidden, "cannot reset your own two-factor authentication")
		return
	}
	if _, err := s.auth.GetUser(id); errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "user get failed")
		return
	}
	if err := s.auth.ResetTOTP(id); err != nil {
		writeError(w, http.StatusInternalServerError, "two-factor reset failed")
		return
	}
	log.Printf("two-factor reset by admin %d for user %d", user.ID, id)
	w.WriteHeader(http.StatusNoContent)
}
