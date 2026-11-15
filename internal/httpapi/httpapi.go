// Package httpapi is the only HTTP surface of the product. It owns routing,
// authentication and authorization gates, JSON encoding, CSRF checks, the
// notification stream and the embedded SPA fallback. Domain rules live in the
// internal packages this layer calls; the API remains the single authority.
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"jiralike/internal/auth"
	"jiralike/internal/automations"
	"jiralike/internal/email"
	"jiralike/internal/items"
	"jiralike/internal/notifications"
	"jiralike/internal/projects"
	"jiralike/internal/realtime"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
	"jiralike/internal/workflow"
)

type Server struct {
	auth               *auth.Store
	teamsStore         *teams.Store
	projectsStore      *projects.Store
	itemsStore         *items.Store
	workflowStore      *workflow.Store
	notificationsStore *notifications.Store
	automationsStore   *automations.Store
	db                 *sql.DB
	web                fs.FS
	secureCookies      bool
	maxBody            int64
	loginAttempts      map[string]loginAttempt
	emailCfg           email.Config
	emailSender        func(cfg email.Config, to, subject, body string) error
	configPath         string
	loginMu            sync.Mutex
	emailMu            sync.RWMutex
	hub                *realtime.Hub
	secrets            *secrets.Box
}

type loginAttempt struct {
	count int
	until time.Time
}

func New(db *sql.DB, web fs.FS) *Server {
	return NewWithOptions(db, web, false, email.Config{}, nil, nil)
}

func NewWithOptions(db *sql.DB, web fs.FS, secureCookies bool, emailCfg email.Config, emailSender func(cfg email.Config, to, subject, body string) error, teamsStore *teams.Store) *Server {
	return &Server{
		auth:               auth.New(db, emailSender),
		teamsStore:         teamsStore,
		projectsStore:      projects.New(db),
		itemsStore:         items.New(db),
		workflowStore:      workflow.New(db),
		notificationsStore: notifications.New(db),
		automationsStore:   automations.New(db),
		db:                 db,
		web:                web,
		secureCookies:      secureCookies,
		emailCfg:           emailCfg,
		emailSender:        emailSender,
		maxBody:            1 << 20,
		loginAttempts:      make(map[string]loginAttempt),
		hub:                realtime.NewHub(),
	}
}

// Close releases long-lived connections (notification streams) so a following
// http.Server.Shutdown can drain. http.Server.Shutdown does not cancel the
// contexts of in-flight requests, so an open SSE handler would otherwise keep
// the process alive until the drain timeout expires. Safe to call twice.
func (s *Server) Close() {
	s.hub.Close()
}

// SetConfigPath points SMTP administration at the ini file the process was
// started with. Without it, saving SMTP settings through the API is refused.
func (s *Server) SetConfigPath(path string) {
	s.configPath = path
}

// SetSecrets installs the installation key used for values that must be
// recoverable: the SMTP password at rest and the TOTP secret. The key lives in
// the data directory. Without it, those features refuse to store anything
// rather than falling back to plain text.
func (s *Server) SetSecrets(box *secrets.Box) {
	s.secrets = box
	s.auth.SetSecrets(box)
}

// SealLegacy converts rows written by versions that kept bearer secrets in
// clear text. Idempotent: it is called once per start and reports how many rows
// it rewrote.
func (s *Server) SealLegacy() (int, error) {
	if s.secrets == nil {
		return 0, secrets.ErrNoKey
	}
	return s.auth.SealLegacy(s.secrets)
}

func (s *Server) mailConfig() (email.Config, func(cfg email.Config, to, subject, body string) error) {
	s.emailMu.RLock()
	defer s.emailMu.RUnlock()
	return s.emailCfg, s.emailSender
}

func (s *Server) mailConfigured() bool {
	s.emailMu.RLock()
	defer s.emailMu.RUnlock()
	return s.emailCfg.Configured() && s.emailSender != nil
}

func (s *Server) sendMail(to, subject, body string) error {
	cfg, sender := s.mailConfig()
	if !cfg.Configured() || sender == nil {
		return fmt.Errorf("SMTP is not configured")
	}
	return sender(cfg, to, subject, body)
}

func (s *Server) updateMailConfig(cfg email.Config) {
	s.emailMu.Lock()
	defer s.emailMu.Unlock()
	s.emailCfg = cfg
	if cfg.Configured() {
		s.emailSender = email.SendInvite
	} else {
		s.emailSender = nil
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ready", s.ready)
	mux.HandleFunc("GET /api/setup", s.setupStatus)
	mux.HandleFunc("POST /api/setup", s.requireCSRF(s.setupCreate))
	mux.HandleFunc("POST /api/login", s.requireCSRF(s.login))
	mux.HandleFunc("POST /api/login/2fa", s.requireCSRF(s.loginTwoFactor))
	mux.HandleFunc("POST /api/login/2fa/start", s.requireCSRF(s.loginTwoFactorStart))
	mux.HandleFunc("POST /api/login/2fa/confirm", s.requireCSRF(s.loginTwoFactorConfirm))
	mux.HandleFunc("POST /api/logout", s.requireCSRF(s.logout))
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/me/2fa", s.requireUser(s.twoFactorStatus))
	mux.HandleFunc("POST /api/me/2fa/start", s.requireCSRF(s.requireUser(s.twoFactorStart)))
	mux.HandleFunc("POST /api/me/2fa/confirm", s.requireCSRF(s.requireUser(s.twoFactorConfirm)))
	mux.HandleFunc("POST /api/me/2fa/disable", s.requireCSRF(s.requireUser(s.twoFactorDisable)))
	mux.HandleFunc("POST /api/me/2fa/recovery-codes", s.requireCSRF(s.requireUser(s.twoFactorRecoveryCodes)))
	mux.HandleFunc("GET /api/people", s.requireUser(s.peopleList))
	mux.HandleFunc("GET /api/tokens", s.requireUser(s.tokensList))
	mux.HandleFunc("POST /api/tokens", s.requireCSRF(s.requireUser(s.tokensCreate)))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.requireCSRF(s.requireUser(s.tokensRevoke)))
	mux.HandleFunc("GET /api/invites", s.requireRole([]string{"admin", "manager"}, s.invitesList))
	mux.HandleFunc("GET /api/invites/{token}", s.invitePreview)
	mux.HandleFunc("POST /api/invites", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.invitesCreate)))
	mux.HandleFunc("DELETE /api/invites/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.invitesRevoke)))
	mux.HandleFunc("POST /api/invites/accept", s.requireCSRF(s.inviteAccept))
	mux.HandleFunc("GET /api/users", s.requireRole([]string{"admin"}, s.usersList))
	mux.HandleFunc("GET /api/users/{id}", s.requireRole([]string{"admin"}, s.usersGet))
	mux.HandleFunc("PUT /api/users/{id}", s.requireCSRF(s.requireRole([]string{"admin"}, s.usersUpdate)))
	mux.HandleFunc("POST /api/users/{id}/2fa/reset", s.requireCSRF(s.requireRole([]string{"admin"}, s.userTwoFactorReset)))
	mux.HandleFunc("GET /api/teams", s.requireRole([]string{"admin", "manager"}, s.teamsList))
	mux.HandleFunc("POST /api/teams", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.teamsCreate)))
	mux.HandleFunc("DELETE /api/teams/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.teamsArchive)))
	mux.HandleFunc("GET /api/teams/{id}/members", s.requireRole([]string{"admin", "manager"}, s.teamMembersList))
	mux.HandleFunc("POST /api/teams/{id}/members", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.teamMembersAdd)))
	mux.HandleFunc("DELETE /api/teams/{id}/members/{user_id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.teamMembersRemove)))
	mux.HandleFunc("GET /api/projects", s.requireUser(s.projectsList))
	mux.HandleFunc("POST /api/projects", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectsCreate)))
	mux.HandleFunc("GET /api/projects/{id}", s.requireUser(s.projectsGet))
	mux.HandleFunc("PUT /api/projects/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectsUpdate)))
	mux.HandleFunc("DELETE /api/projects/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectsArchive)))
	mux.HandleFunc("GET /api/projects/{id}/people", s.requireUser(s.projectPeopleList))
	mux.HandleFunc("GET /api/projects/{id}/members", s.requireRole([]string{"admin", "manager"}, s.projectMembersList))
	mux.HandleFunc("POST /api/projects/{id}/members", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectMembersAdd)))
	mux.HandleFunc("DELETE /api/projects/{id}/members/{user_id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectMembersRemove)))
	mux.HandleFunc("GET /api/projects/{id}/teams", s.requireRole([]string{"admin", "manager"}, s.projectTeamsList))
	mux.HandleFunc("POST /api/projects/{id}/teams", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectTeamsAdd)))
	mux.HandleFunc("DELETE /api/projects/{id}/teams/{team_id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.projectTeamsRemove)))
	mux.HandleFunc("GET /api/items", s.requireUser(s.itemsList))
	mux.HandleFunc("POST /api/projects/{id}/items", s.requireCSRF(s.requireUser(s.itemsCreate)))
	mux.HandleFunc("GET /api/items/{id}", s.requireUser(s.itemsGet))
	mux.HandleFunc("PUT /api/items/{id}", s.requireCSRF(s.requireUser(s.itemsUpdate)))
	mux.HandleFunc("DELETE /api/items/{id}", s.requireCSRF(s.requireUser(s.itemsArchive)))
	mux.HandleFunc("GET /api/items/{id}/comments", s.requireUser(s.commentsList))
	mux.HandleFunc("POST /api/items/{id}/comments", s.requireCSRF(s.requireUser(s.commentsCreate)))
	mux.HandleFunc("PUT /api/comments/{id}", s.requireCSRF(s.requireUser(s.commentUpdate)))
	mux.HandleFunc("GET /api/items/{id}/events", s.requireUser(s.eventsList))
	mux.HandleFunc("GET /api/items/{id}/transitions", s.requireUser(s.itemTransitionsList))
	mux.HandleFunc("POST /api/items/{id}/transitions/{transition_id}", s.requireCSRF(s.requireUser(s.itemTransitionApply)))
	mux.HandleFunc("GET /api/workflow", s.requireUser(s.workflowGet))
	mux.HandleFunc("POST /api/workflow/states", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowStateCreate)))
	mux.HandleFunc("PUT /api/workflow/states/{id}", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowStateUpdate)))
	mux.HandleFunc("DELETE /api/workflow/states/{id}", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowStateDelete)))
	mux.HandleFunc("PUT /api/workflow/initial-state", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowInitialSet)))
	mux.HandleFunc("POST /api/workflow/transitions", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowTransitionCreate)))
	mux.HandleFunc("PUT /api/workflow/transitions/{id}", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowTransitionUpdate)))
	mux.HandleFunc("DELETE /api/workflow/transitions/{id}", s.requireCSRF(s.requireRole([]string{"admin"}, s.workflowTransitionDelete)))
	mux.HandleFunc("GET /api/notifications", s.requireUser(s.notificationsList))
	mux.HandleFunc("GET /api/notifications/stream", s.notificationsStream)
	mux.HandleFunc("GET /api/notifications/unread-count", s.requireUser(s.notificationsUnreadCount))
	mux.HandleFunc("POST /api/notifications/read-all", s.requireCSRF(s.requireUser(s.notificationsReadAll)))
	mux.HandleFunc("POST /api/notifications/{id}/read", s.requireCSRF(s.requireUser(s.notificationsRead)))
	mux.HandleFunc("GET /api/reports", s.requireUser(s.reportsGet))
	mux.HandleFunc("GET /api/admin/backup", s.requireRole([]string{"admin"}, s.backupDownload))
	mux.HandleFunc("POST /api/admin/search/rebuild", s.requireCSRF(s.requireRole([]string{"admin"}, s.searchRebuild)))
	mux.HandleFunc("GET /api/automations", s.requireRole([]string{"admin", "manager"}, s.automationsList))
	mux.HandleFunc("POST /api/automations", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.automationsCreate)))
	mux.HandleFunc("GET /api/automations/{id}", s.requireRole([]string{"admin", "manager"}, s.automationsGet))
	mux.HandleFunc("PUT /api/automations/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.automationsUpdate)))
	mux.HandleFunc("DELETE /api/automations/{id}", s.requireCSRF(s.requireRole([]string{"admin", "manager"}, s.automationsDelete)))
	mux.HandleFunc("GET /api/admin/smtp", s.requireRole([]string{"admin"}, s.smtpGet))
	mux.HandleFunc("PUT /api/admin/smtp", s.requireCSRF(s.requireRole([]string{"admin"}, s.smtpUpdate)))
	mux.HandleFunc("POST /api/admin/smtp/test", s.requireCSRF(s.requireRole([]string{"admin"}, s.smtpTest)))
	mux.HandleFunc("GET /api/admin/security", s.requireRole([]string{"admin"}, s.securityGet))
	mux.HandleFunc("PUT /api/admin/security", s.requireCSRF(s.requireRole([]string{"admin"}, s.securityUpdate)))
	mux.HandleFunc("PUT /api/me", s.requireCSRF(s.requireUser(s.meUpdate)))
	if s.web != nil {
		mux.Handle("/", s.spa())
	}
	return s.requestLog(s.securityHeaders(s.limitBodies(mux)))
}

func (s *Server) limitBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		// Only sent over TLS: browsers ignore HSTS on plain HTTP, and a TLS
		// terminating proxy in front is responsible for its own policy.
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// requestLog records method, redacted path, status and duration. Request
// bodies, cookies, authorization headers and invite tokens are never logged, so
// passwords and personal API tokens cannot end up in an operational log.
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s %s", r.Method, redactLogPath(r.URL.Path), rec.status,
			time.Since(start).Round(time.Millisecond), remoteHost(r.RemoteAddr))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func redactLogPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 4 && parts[1] == "api" && parts[2] == "invites" && parts[3] != "" && parts[3] != "accept" {
		return "/api/invites/[redacted]"
	}
	if len(parts) == 3 && parts[1] == "invite" && parts[2] != "" {
		return "/invite/[redacted]"
	}
	return path
}

func remoteHost(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ready is the readiness probe: it answers 503 until the database connection is
// usable, so a process that is still starting is not sent traffic.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.db == nil || s.db.PingContext(ctx) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "ready": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ready": true})
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	done, err := s.auth.SetupCompleted()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_completed": done})
}

func (s *Server) setupCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Locale   string `json:"locale"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(body.Name) == "" || !strings.Contains(body.Email, "@") || len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "name, email and a password of 8 characters are required")
		return
	}
	user, _, err := s.auth.Setup(body.Name, body.Email, body.Password)
	if errors.Is(err, auth.ErrAlreadySetup) {
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}
	if errors.Is(err, auth.ErrEmailTaken) {
		writeError(w, http.StatusConflict, "email already used")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup failed")
		return
	}
	if locale := normalizeLocale(body.Locale); locale != user.Locale {
		if err := s.auth.UpdateProfile(user.ID, user.Name, locale, 0); err != nil {
			writeError(w, http.StatusInternalServerError, "setup failed")
			return
		}
		user.Locale = locale
	}
	if err := s.startSession(w, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusCreated, s.userPayload(user))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	user, err := s.auth.Login(body.Email, body.Password)
	if errors.Is(err, auth.ErrInvalidLogin) {
		s.recordLoginFailure(r)
	} else {
		s.clearLoginFailures(r)
	}
	switch {
	case errors.Is(err, auth.ErrInvalidLogin):
		writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	case errors.Is(err, auth.ErrDisabled):
		writeError(w, http.StatusForbidden, "account disabled")
		return
	case errors.Is(err, auth.ErrGuestExpired):
		writeError(w, http.StatusForbidden, "guest access ended on "+user.GuestExpiresOn)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if s.twoFactorGate(w, user) {
		return
	}
	if err := s.auth.EnsureDefaultToken(user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := s.startSession(w, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusOK, s.userPayload(user))
}

func (s *Server) loginKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func (s *Server) allowLogin(r *http.Request) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	a := s.loginAttempts[s.loginKey(r)]
	return time.Now().After(a.until)
}
func (s *Server) recordLoginFailure(r *http.Request) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	key := s.loginKey(r)
	a := s.loginAttempts[key]
	a.count++
	if a.count >= 5 {
		a.until = time.Now().Add(time.Minute)
	}
	s.loginAttempts[key] = a
}
func (s *Server) clearLoginFailures(r *http.Request) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginAttempts, s.loginKey(r))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
		writeError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	cookie, err := r.Cookie(auth.SessionCookie)
	if err == nil {
		_ = s.auth.RevokeSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	// /api/me is reachable both with the session cookie and with a personal API
	// token, like every other authenticated endpoint. Tokens are the CLI and
	// integration path, so they must be able to identify themselves.
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" {
			writeError(w, http.StatusUnauthorized, "invalid authorization header")
			return
		}
		user, err := s.auth.AuthenticateToken(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		writeJSON(w, http.StatusOK, s.userPayload(user))
		return
	}
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	user, err := s.auth.UserFromSession(cookie.Value)
	if errors.Is(err, auth.ErrGuestExpired) {
		writeError(w, http.StatusForbidden, "guest access ended")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	writeJSON(w, http.StatusOK, s.userPayload(user))
}

// peopleList is the minimal, signed-in directory used by assignee, mention and
// search pickers. Email addresses stay in the admin people page.
func (s *Server) peopleList(w http.ResponseWriter, r *http.Request, user auth.User) {
	list, err := s.auth.ListUsers()
	if err != nil {
		writeError(w, 500, "people list failed")
		return
	}
	out := make([]projects.Person, 0, len(list))
	for _, u := range list {
		out = append(out, projects.Person{
			ID:             u.ID,
			Name:           u.Name,
			Role:           u.Role,
			GuestExpiresOn: u.GuestExpiresOn,
			Disabled:       u.DisabledAt != "",
		})
	}
	writeJSON(w, 200, map[string]any{"people": out})
}

// projectPeopleList returns exactly the people the API accepts as assignees
// for one project. Viewers get 404 for private projects they cannot see.
func (s *Server) projectPeopleList(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid project id")
		return
	}
	p, err := s.projectsStore.Get(id)
	if errors.Is(err, projects.ErrNotFound) {
		writeError(w, 404, "project not found")
		return
	}
	if err != nil {
		writeError(w, 500, "project get failed")
		return
	}
	canSee, err := s.projectsStore.CanSee(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return
	}
	if !canSee {
		writeError(w, 404, "project not found")
		return
	}
	list, err := s.projectsStore.AssignableUsers(p)
	if err != nil {
		writeError(w, 500, "project people failed")
		return
	}
	writeJSON(w, 200, map[string]any{"people": list})
}

// userPayload adds installation capabilities the profile screen needs without
// exposing SMTP settings themselves.
func (s *Server) userPayload(user auth.User) map[string]any {
	body := userJSON(user)
	body["smtp_configured"] = s.mailConfigured()
	return body
}

type apiHandler func(http.ResponseWriter, *http.Request, auth.User)

func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			if r.Header.Get("Origin") == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, http.StatusForbidden, "cross-site request rejected")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) requireUser(next apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var user auth.User
		authorization := r.Header.Get("Authorization")
		if authorization != "" {
			if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" {
				writeError(w, http.StatusUnauthorized, "invalid authorization header")
				return
			}
			user, _ = s.auth.AuthenticateToken(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
		} else if cookie, err := r.Cookie(auth.SessionCookie); err == nil {
			user, _ = s.auth.UserFromSession(cookie.Value)
		}
		if user.ID == 0 {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next(w, r, user)
	}
}

func (s *Server) requireRole(roles []string, next apiHandler) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, user auth.User) {
		for _, role := range roles {
			if user.Role == role {
				next(w, r, user)
				return
			}
		}
		writeError(w, http.StatusForbidden, "insufficient role")
	})
}

func (s *Server) tokensList(w http.ResponseWriter, r *http.Request, user auth.User) {
	items, err := s.auth.ListTokens(user.ID)
	if err != nil {
		writeError(w, 500, "token list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": items})
}
func (s *Server) tokensCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	token, err := s.auth.CreateToken(user.ID, body.Name)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, token)
}
func (s *Server) tokensRevoke(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid token id")
		return
	}
	err = s.auth.RevokeToken(user.ID, id)
	if errors.Is(err, auth.ErrLastToken) {
		writeError(w, 409, err.Error())
		return
	}
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, 404, "token not found")
		return
	}
	if err != nil {
		writeError(w, 500, "token revoke failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) invitesList(w http.ResponseWriter, r *http.Request, user auth.User) {
	items, err := s.auth.ListInvites()
	if err != nil {
		writeError(w, 500, "invite list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"invites": items})
}
func (s *Server) invitesCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Email     string `json:"email"`
		Role      string `json:"role"`
		GuestDays int    `json:"guest_days"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	token, err := s.auth.CreateInvite(body.Email, body.Role, body.GuestDays, user.ID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	acceptURL := s.baseURL(r) + "/invite/" + token
	// The link is always returned so it can be copied; the email is a
	// best-effort extra when SMTP is configured.
	if s.mailConfigured() {
		subject := "You have been invited to Together"
		emailBody := fmt.Sprintf(
			"Hello,\n\n%s invited you to join Together as %s.\n\nOpen this link to create your account:\n%s\n\nThis link expires in 7 days.\n",
			user.Name, body.Role, acceptURL,
		)
		if err := s.sendMail(strings.TrimSpace(body.Email), subject, emailBody); err != nil {
			log.Printf("invite email to %s failed: %v", body.Email, err)
		}
	}
	writeJSON(w, 201, map[string]any{"token": token, "accept_url": acceptURL})
}

// baseURL rebuilds the public origin for links built from a request.
func (s *Server) baseURL(r *http.Request) string {
	scheme := "http"
	if s.secureCookies || r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func (s *Server) invitesRevoke(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid invite id")
		return
	}
	err = s.auth.RevokeInvite(id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, 404, "invite not found")
		return
	}
	if err != nil {
		writeError(w, 500, "invite revoke failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// invitePreview exposes only the decisions already made on a pending invite.
// It is public because the invite link is the bearer credential.
func (s *Server) invitePreview(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		writeError(w, 404, "invite not found")
		return
	}
	email, role, guestDays, expiresAt, err := s.auth.InvitePreview(token)
	if errors.Is(err, auth.ErrInviteInvalid) {
		writeError(w, 410, "invite invalid or expired")
		return
	}
	if err != nil {
		writeError(w, 500, "invite preview failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"email":      email,
		"role":       role,
		"guest_days": guestDays,
		"expires_at": expiresAt,
	})
}

func (s *Server) inviteAccept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Locale   string `json:"locale"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	user, err := s.auth.AcceptInvite(body.Token, body.Name, body.Password)
	if errors.Is(err, auth.ErrInviteInvalid) {
		writeError(w, 410, "invite invalid or expired")
		return
	}
	if errors.Is(err, auth.ErrEmailTaken) {
		writeError(w, 409, "account already exists")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if locale := normalizeLocale(body.Locale); locale != user.Locale {
		if err := s.auth.UpdateProfile(user.ID, user.Name, locale, 0); err != nil {
			writeError(w, 500, "session failed")
			return
		}
		user.Locale = locale
	}
	if s.twoFactorGate(w, user) {
		return
	}
	if err := s.startSession(w, user.ID); err != nil {
		writeError(w, 500, "session failed")
		return
	}
	writeJSON(w, 201, s.userPayload(user))
}
func (s *Server) usersList(w http.ResponseWriter, r *http.Request, user auth.User) {
	items, err := s.auth.ListUsers()
	if err != nil {
		writeError(w, 500, "user list failed")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, u := range items {
		out = append(out, userJSON(u))
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

func (s *Server) usersGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid user id")
		return
	}
	u, err := s.auth.GetUser(id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, 404, "user not found")
		return
	}
	if err != nil {
		writeError(w, 500, "user get failed")
		return
	}
	writeJSON(w, 200, userJSON(u))
}

func (s *Server) usersUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid user id")
		return
	}
	if id == user.ID {
		writeError(w, 403, "cannot update your own profile via admin endpoint")
		return
	}
	target, err := s.auth.GetUser(id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, 404, "user not found")
		return
	}
	if err != nil {
		writeError(w, 500, "user get failed")
		return
	}
	// Decode into a map first to distinguish absent fields from zero values.
	raw := map[string]json.RawMessage{}
	if err := s.decodeJSON(w, r, &raw); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	// Start from the current values, then validate every supplied field before
	// writing anything so a rejected request never leaves a partial update.
	newRole := target.Role
	newEmailNotifications := 0
	if target.EmailNotifications {
		newEmailNotifications = 1
	}
	disabledAt := sql.NullString{}
	if target.DisabledAt != "" {
		disabledAt = sql.NullString{String: target.DisabledAt, Valid: true}
	}
	guestDays := 0
	if v, ok := raw["role"]; ok {
		var role string
		if err := json.Unmarshal(v, &role); err != nil {
			writeError(w, 400, "role must be a string")
			return
		}
		switch role {
		case "admin", "manager", "member", "guest":
		default:
			writeError(w, 400, "invalid role")
			return
		}
		newRole = role
	}
	if v, ok := raw["email_notifications"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			writeError(w, 400, "email_notifications must be 0 or 1")
			return
		}
		if n != 0 && n != 1 {
			writeError(w, 400, "email_notifications must be 0 or 1")
			return
		}
		newEmailNotifications = n
	}
	if v, ok := raw["disabled"]; ok {
		var d bool
		if err := json.Unmarshal(v, &d); err != nil {
			writeError(w, 400, "disabled must be a boolean")
			return
		}
		if d {
			disabledAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}
		} else {
			disabledAt = sql.NullString{}
		}
	}
	if v, ok := raw["guest_days"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			writeError(w, 400, "guest_days must be 7, 30 or 90")
			return
		}
		if n != 7 && n != 30 && n != 90 {
			writeError(w, 400, "guest_days must be 7, 30 or 90")
			return
		}
		guestDays = n
	}
	if guestDays > 0 && newRole != "guest" {
		writeError(w, 400, "guest_days only applies to guests")
		return
	}
	// An active admin can only be demoted or disabled when another active
	// admin remains.
	if target.Role == "admin" && target.DisabledAt == "" && (newRole != "admin" || disabledAt.Valid) {
		count, err := s.auth.CountActiveAdmins()
		if err != nil {
			writeError(w, 500, "admin count failed")
			return
		}
		if count <= 1 {
			writeError(w, 409, "cannot change the last active admin")
			return
		}
	}
	if err := s.auth.AdminUpdateUser(id, newRole, newEmailNotifications, disabledAt, guestDays); err != nil {
		writeError(w, 500, "user update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) teamsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	items, err := s.teamsStore.List()
	if err != nil {
		writeError(w, 500, "team list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"teams": items})
}

func (s *Server) teamsCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	t, err := s.teamsStore.Create(body.Name)
	if errors.Is(err, teams.ErrNameEmpty) {
		writeError(w, 400, "team name is required")
		return
	}
	if errors.Is(err, teams.ErrExists) {
		writeError(w, 409, "team already exists")
		return
	}
	if err != nil {
		writeError(w, 500, "team create failed")
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) teamsArchive(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid team id")
		return
	}
	if err := s.teamsStore.Archive(id); err != nil {
		if errors.Is(err, teams.ErrNotFound) {
			writeError(w, 404, "team not found")
			return
		}
		writeError(w, 500, "team archive failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) teamMembersList(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid team id")
		return
	}
	items, err := s.teamsStore.Members(id)
	if err != nil {
		writeError(w, 500, "team members failed")
		return
	}
	writeJSON(w, 200, map[string]any{"members": items})
}

func (s *Server) teamMembersAdd(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid team id")
		return
	}
	var body struct {
		UserID int64 `json:"user_id"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	if err := s.teamsStore.AddMember(id, body.UserID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) teamMembersRemove(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid team id")
		return
	}
	userID, err := parseID(r.PathValue("user_id"))
	if err != nil {
		writeError(w, 400, "invalid user id")
		return
	}
	if err := s.teamsStore.RemoveMember(id, userID); err != nil {
		if errors.Is(err, teams.ErrMemberNotFound) {
			writeError(w, 404, "member not found")
			return
		}
		writeError(w, 500, "remove member failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	query := r.URL.Query()
	filter := projects.ListFilter{Query: query.Get("q"), Visibility: query.Get("visibility")}
	if query.Get("archived") == "1" && (user.Role == "admin" || user.Role == "manager") {
		filter.IncludeArchived = true
	}
	items, err := s.projectsStore.List(user.ID, user.Role, filter)
	if err != nil {
		writeError(w, 500, "project list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"projects": items})
}

func (s *Server) projectsCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	p, err := s.projectsStore.Create(body.Name, body.Description, body.Visibility, user.ID)
	if errors.Is(err, projects.ErrNameEmpty) || errors.Is(err, projects.ErrNameTooLong) || errors.Is(err, projects.ErrInvalidVisibility) {
		writeError(w, 400, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "project create failed")
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) projectsGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid project id")
		return
	}
	p, err := s.projectsStore.Get(id)
	if errors.Is(err, projects.ErrNotFound) {
		writeError(w, 404, "project not found")
		return
	}
	if err != nil {
		writeError(w, 500, "project get failed")
		return
	}
	canSee, err := s.projectsStore.CanSee(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return
	}
	// Unauthorized private projects answer exactly like missing ones so IDs
	// cannot be probed.
	if !canSee {
		writeError(w, 404, "project not found")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) projectsUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Visibility  *string `json:"visibility"`
	}
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	name, description, visibility := p.Name, p.Description, p.Visibility
	if body.Name != nil {
		name = *body.Name
	}
	if body.Description != nil {
		description = *body.Description
	}
	if body.Visibility != nil {
		visibility = *body.Visibility
	}
	if err := s.projectsStore.Update(p.ID, name, description, visibility); err != nil {
		if errors.Is(err, projects.ErrNameEmpty) || errors.Is(err, projects.ErrNameTooLong) || errors.Is(err, projects.ErrInvalidVisibility) {
			writeError(w, 400, err.Error())
			return
		}
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, 409, "project is archived")
			return
		}
		writeError(w, 500, "project update failed")
		return
	}
	updated, err := s.projectsStore.Get(p.ID)
	if err != nil {
		writeError(w, 500, "project update failed")
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) projectsArchive(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is already archived")
		return
	}
	if err := s.projectsStore.Archive(p.ID); err != nil {
		if errors.Is(err, projects.ErrAlreadyArchived) {
			writeError(w, 409, "project is already archived")
			return
		}
		writeError(w, 500, "project archive failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectMembersList(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	items, err := s.projectsStore.Members(p.ID)
	if err != nil {
		writeError(w, 500, "project member list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"members": items})
}

func (s *Server) projectMembersAdd(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return
	}
	var body struct {
		UserID int64 `json:"user_id"`
	}
	if s.decodeJSON(w, r, &body) != nil || body.UserID <= 0 {
		writeError(w, 400, "user_id is required")
		return
	}
	if err := s.projectsStore.AddMember(p.ID, body.UserID); err != nil {
		if errors.Is(err, projects.ErrUserNotFound) {
			writeError(w, 404, "user not found")
			return
		}
		writeError(w, 500, "add project member failed")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) projectMembersRemove(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return
	}
	userID, err := parseID(r.PathValue("user_id"))
	if err != nil {
		writeError(w, 400, "invalid user id")
		return
	}
	if err := s.projectsStore.RemoveMember(p.ID, userID); err != nil {
		if errors.Is(err, projects.ErrMemberNotFound) {
			writeError(w, 404, "project member not found")
			return
		}
		writeError(w, 500, "remove project member failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectTeamsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	items, err := s.projectsStore.Teams(p.ID)
	if err != nil {
		writeError(w, 500, "project team list failed")
		return
	}
	writeJSON(w, 200, map[string]any{"teams": items})
}

func (s *Server) projectTeamsAdd(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return
	}
	var body struct {
		TeamID int64 `json:"team_id"`
	}
	if s.decodeJSON(w, r, &body) != nil || body.TeamID <= 0 {
		writeError(w, 400, "team_id is required")
		return
	}
	if err := s.projectsStore.AddTeam(p.ID, body.TeamID); err != nil {
		if errors.Is(err, projects.ErrTeamNotFound) {
			writeError(w, 404, "team not found")
			return
		}
		writeError(w, 500, "add project team failed")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) projectTeamsRemove(w http.ResponseWriter, r *http.Request, user auth.User) {
	p, ok := s.projectForManagement(w, r, user)
	if !ok {
		return
	}
	if p.ArchivedAt != "" {
		writeError(w, 409, "project is archived")
		return
	}
	teamID, err := parseID(r.PathValue("team_id"))
	if err != nil {
		writeError(w, 400, "invalid team id")
		return
	}
	if err := s.projectsStore.RemoveTeam(p.ID, teamID); err != nil {
		if errors.Is(err, projects.ErrTeamNotLinked) {
			writeError(w, 404, "project team not found")
			return
		}
		writeError(w, 500, "remove project team failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectForManagement loads the project named in the path and checks manage
// rights. Callers that may not manage get 404, the same answer as a missing
// project, to avoid leaking private project IDs.
func (s *Server) projectForManagement(w http.ResponseWriter, r *http.Request, user auth.User) (projects.Project, bool) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid project id")
		return projects.Project{}, false
	}
	p, err := s.projectsStore.Get(id)
	if errors.Is(err, projects.ErrNotFound) {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	if err != nil {
		writeError(w, 500, "project get failed")
		return projects.Project{}, false
	}
	manage, err := s.projectsStore.CanManage(p, user.ID, user.Role)
	if err != nil {
		writeError(w, 500, "project access check failed")
		return projects.Project{}, false
	}
	if !manage {
		writeError(w, 404, "project not found")
		return projects.Project{}, false
	}
	return p, true
}

func (s *Server) meUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	raw := map[string]json.RawMessage{}
	if err := s.decodeJSON(w, r, &raw); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	name := strings.TrimSpace(user.Name)
	locale := user.Locale
	emailNotifications := boolToInt(user.EmailNotifications)
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &name); err != nil || strings.TrimSpace(name) == "" {
			writeError(w, 400, "name is required")
			return
		}
		name = strings.TrimSpace(name)
	}
	if v, ok := raw["locale"]; ok {
		if err := json.Unmarshal(v, &locale); err != nil || !validLocale(locale) {
			writeError(w, 400, "locale must be en, it or es")
			return
		}
	}
	if v, ok := raw["email_notifications"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err != nil || (n != 0 && n != 1) {
			writeError(w, 400, "email_notifications must be 0 or 1")
			return
		}
		if !s.mailConfigured() {
			n = 0
		}
		emailNotifications = n
	}
	if err := s.auth.UpdateProfile(user.ID, name, locale, emailNotifications); err != nil {
		writeError(w, 500, "profile update failed")
		return
	}
	writeJSON(w, 200, s.userPayload(auth.User{ID: user.ID, Name: name, Email: user.Email, Role: user.Role, Locale: locale, EmailNotifications: emailNotifications == 1}))
}

func validLocale(locale string) bool {
	return locale == "en" || locale == "it" || locale == "es"
}

func normalizeLocale(locale string) string {
	if validLocale(locale) {
		return locale
	}
	return "en"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseID(value string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(value, &id)
	if err == nil && id < 1 {
		err = errors.New("invalid id")
	}
	return id, err
}

func (s *Server) startSession(w http.ResponseWriter, userID int64) error {
	raw, expires, err := s.auth.StartSession(userID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    raw,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secureCookies,
	})
	return nil
}

func (s *Server) spa() http.Handler {
	files := http.FileServer(http.FS(s.web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if _, err := fs.Stat(s.web, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

func userJSON(user auth.User) map[string]any {
	return map[string]any{
		"id":                  user.ID,
		"name":                user.Name,
		"email":               user.Email,
		"role":                user.Role,
		"locale":              user.Locale,
		"email_notifications": user.EmailNotifications,
		"guest_expires_on":    user.GuestExpiresOn,
		"disabled":            user.DisabledAt != "",
		"created_at":          user.CreatedAt,
		"two_factor_enabled":  user.TwoFactorEnabled,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// API responses can carry a freshly created token, an invitation link or
	// personal data, so no shared cache may keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
