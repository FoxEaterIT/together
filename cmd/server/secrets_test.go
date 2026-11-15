package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/httpapi"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
	"jiralike/internal/totp"
	"jiralike/internal/web"
)

// TestNoPlaintextSecretsOnDisk is the contract behind the hashing rules: after
// a setup, a personal token, 2FA enrollment and an SMTP password, none of those
// clear values can be found in the database files or in config.ini. The test
// reads the files as bytes, so it covers every page SQLite wrote.
func TestNoPlaintextSecretsOnDisk(t *testing.T) {
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	assets, err := web.FS()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.ini")
	handler := httpapi.NewWithOptions(conn, assets, false, email.Config{}, nil, teams.New(conn))
	handler.SetSecrets(box)
	handler.SetConfigPath(configPath)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- serve(t.Context(), listener, server, nil, handler.Close, 5*time.Second) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	client := newE2EClient(t, "http://"+listener.Addr().String())

	const password = "password-strong-91"
	client.decode(http.StatusCreated, "POST", "/api/setup", map[string]any{
		"name": "Admin", "email": "admin@example.test", "password": password,
	}, &struct{}{})

	var created struct {
		Token string `json:"token"`
	}
	client.decode(http.StatusCreated, "POST", "/api/tokens", map[string]any{"name": "cli"}, &created)

	var enrollment struct {
		Secret string `json:"secret"`
	}
	client.decode(http.StatusOK, "POST", "/api/me/2fa/start", nil, &enrollment)
	code, err := totp.Code(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var confirm struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	client.decode(http.StatusOK, "POST", "/api/me/2fa/confirm", map[string]any{"code": code}, &confirm)
	if len(confirm.RecoveryCodes) == 0 {
		t.Fatal("no recovery codes")
	}

	const smtpPassword = "smtp-secret-value"
	client.expect(http.StatusOK, "PUT", "/api/admin/smtp", map[string]any{
		"host": "smtp.example.test", "port": 587, "user": "mailer",
		"password": smtpPassword, "from": "work@example.test", "tls": true,
	})

	// Copy the write-ahead log into the main file so the scan is not missing
	// recent pages, then read every database file as bytes.
	if _, err := conn.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	onDisk := readFilesWithPrefix(t, dir, "app.db")
	ini, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	onDisk = append(onDisk, ini...)

	secretsToHide := map[string]string{
		"account password": password,
		"personal token":   created.Token,
		"TOTP secret":      enrollment.Secret,
		"recovery code":    confirm.RecoveryCodes[0],
		"SMTP password":    smtpPassword,
	}
	for label, value := range secretsToHide {
		if value == "" {
			t.Fatalf("%s is empty, the assertion would be vacuous", label)
		}
		if bytes.Contains(onDisk, []byte(value)) {
			t.Fatalf("%s written in clear text to the data directory", label)
		}
	}
	// Every stored token is a SHA-256 hex digest, never the value itself.
	rows, err := conn.Query(`SELECT token_hash FROM api_tokens`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stored := 0
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		stored++
		if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
			t.Fatalf("stored token is not a SHA-256 hex digest: %q", hash)
		}
	}
	if stored != 2 {
		t.Fatalf("expected two stored tokens, got %d", stored)
	}
	// The API must also stop returning a personal token after creation.
	var list struct {
		Tokens []struct {
			Token  string `json:"token"`
			Prefix string `json:"prefix"`
		} `json:"tokens"`
	}
	client.decode(http.StatusOK, "GET", "/api/tokens", nil, &list)
	if len(list.Tokens) != 2 {
		t.Fatalf("expected two tokens, got %d", len(list.Tokens))
	}
	for _, token := range list.Tokens {
		if token.Token != "" {
			t.Fatalf("token list leaked a value: %+v", token)
		}
		if token.Prefix == "" {
			t.Fatalf("token list lost the prefix: %+v", token)
		}
	}
}

func readFilesWithPrefix(t *testing.T, dir, prefix string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []byte{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if len(entry.Name()) < len(prefix) || entry.Name()[:len(prefix)] != prefix {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, content...)
	}
	if len(out) == 0 {
		t.Fatalf("no files with prefix %q in %s", prefix, dir)
	}
	return out
}
