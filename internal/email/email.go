// Package email sends plain-text SMTP messages with optional STARTTLS. Email
// is optional: the binary starts and works without a configured mail server.
package email

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
)

type Config struct {
	Host string
	Port int
	User string
	Pass string
	From string
	TLS  bool
}

// Configured reports whether the minimum SMTP settings are present.
func (c Config) Configured() bool {
	return c.Host != "" && c.Port > 0 && c.From != ""
}

func SendInvite(cfg Config, to, subject, body string) error {
	if cfg.Host == "" || cfg.Port == 0 || cfg.From == "" {
		return nil
	}
	addr := cfg.Host + ":" + fmt.Sprint(cfg.Port)
	auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	var err error
	if cfg.TLS {
		err = sendWithTLS(addr, auth, cfg.From, []string{to}, subject, body, cfg.Host)
	} else {
		err = sendPlain(addr, auth, cfg.From, []string{to}, subject, body)
	}
	return err
}

func sendPlain(addr string, auth smtp.Auth, from string, to []string, subject, body string) error {
	msg := buildMessage(from, to, subject, body)
	return smtp.SendMail(addr, auth, from, to, []byte(msg))
}

func sendWithTLS(addr string, auth smtp.Auth, from string, to []string, subject, body, host string) error {
	msg := buildMessage(from, to, subject, body)
	tlsConfig := &tls.Config{
		ServerName: host,
	}
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(msg))
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	return client.Quit()
}

func buildMessage(from string, to []string, subject, body string) string {
	return "From: " + from + "\r\n" +
		"To: " + strings.Join(to, ",") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n"
}
