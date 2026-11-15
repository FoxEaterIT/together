package httpapi

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"jiralike/internal/auth"
	"jiralike/internal/config"
	"jiralike/internal/email"
	"jiralike/internal/secrets"
)

type smtpBody struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	User     string  `json:"user"`
	Password *string `json:"password"`
	From     string  `json:"from"`
	TLS      bool    `json:"tls"`
}

type smtpView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	From        string `json:"from"`
	TLS         bool   `json:"tls"`
	Configured  bool   `json:"configured"`
	HasPassword bool   `json:"has_password"`
}

func smtpShape(cfg email.Config) smtpView {
	return smtpView{
		Host:        cfg.Host,
		Port:        cfg.Port,
		User:        cfg.User,
		From:        cfg.From,
		TLS:         cfg.TLS,
		Configured:  cfg.Configured(),
		HasPassword: cfg.Pass != "",
	}
}

func (s *Server) smtpGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	cfg, _ := s.mailConfig()
	writeJSON(w, 200, smtpShape(cfg))
}

// seal encrypts a value with the installation key. A missing key is an error,
// never a reason to write plain text.
func (s *Server) seal(value string) (string, error) {
	if s.secrets == nil {
		return "", secrets.ErrNoKey
	}
	return s.secrets.Seal(value)
}

// smtpUpdate writes the SMTP block to the ini file, then swaps the live
// configuration. A missing or empty password keeps the stored one; the
// password is never returned by any endpoint.
func (s *Server) smtpUpdate(w http.ResponseWriter, r *http.Request, user auth.User) {
	var body smtpBody
	if s.decodeJSON(w, r, &body) != nil {
		writeError(w, 400, "invalid json")
		return
	}
	current, _ := s.mailConfig()
	host := strings.TrimSpace(body.Host)
	from := strings.TrimSpace(body.From)
	smtpUser := strings.TrimSpace(body.User)
	port := body.Port
	disabled := host == "" && from == ""
	if !disabled {
		if host == "" || from == "" {
			writeError(w, 400, "host and sender are both required")
			return
		}
		if port < 1 || port > 65535 {
			writeError(w, 400, "port must be between 1 and 65535")
			return
		}
		if !strings.Contains(from, "@") {
			writeError(w, 400, "sender must be an email address")
			return
		}
	} else {
		port = 0
	}
	password := current.Pass
	passwordChanged := body.Password != nil && *body.Password != ""
	if passwordChanged {
		password = *body.Password
	}
	next := email.Config{
		Host: host,
		Port: port,
		User: smtpUser,
		Pass: password,
		From: from,
		TLS:  body.TLS && !disabled,
	}
	if disabled {
		next.User = ""
	}

	updates := map[string]string{
		"smtp_host": next.Host,
		"smtp_port": strconv.Itoa(next.Port),
		"smtp_user": next.User,
		"smtp_from": next.From,
		"smtp_tls":  strconv.FormatBool(next.TLS),
	}
	switch {
	case passwordChanged:
		sealed, err := s.seal(next.Pass)
		if err != nil {
			log.Printf("seal SMTP password: %v", err)
			writeError(w, 500, "SMTP settings could not be saved")
			return
		}
		// The ini file never holds the password in clear text.
		updates["smtp_pass_enc"] = sealed
		updates["smtp_pass"] = ""
	case disabled:
		updates["smtp_pass_enc"] = ""
		updates["smtp_pass"] = ""
	}
	if err := config.Save(s.configPath, updates); err != nil {
		log.Printf("save SMTP settings: %v", err)
		writeError(w, 500, "SMTP settings could not be saved")
		return
	}
	s.updateMailConfig(next)
	writeJSON(w, 200, smtpShape(next))
}

// smtpTest sends a plain message to the caller's own address.
func (s *Server) smtpTest(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.mailConfigured() {
		writeError(w, 400, "SMTP is not configured")
		return
	}
	subject := "SMTP test"
	body := "Hello " + user.Name + ",\n\nThis is a test message from Together.\n"
	if user.Locale == "it" {
		subject = "Prova SMTP"
		body = "Ciao " + user.Name + ",\n\nQuesto e' un messaggio di prova da Together.\n"
	}
	if user.Locale == "es" {
		subject = "Prueba SMTP"
		body = "Hola " + user.Name + ",\n\nEste es un mensaje de prueba de Together.\n"
	}
	if err := s.sendMail(user.Email, subject, body); err != nil {
		log.Printf("SMTP test to %s failed: %v", user.Email, err)
		writeError(w, 502, "test email failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
