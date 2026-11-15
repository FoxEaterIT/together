package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/httpapi"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
	"jiralike/internal/tlsconf"
	"jiralike/internal/web"
)

// TestServeHTTPSWithSelfSignedCertificate covers the built-in TLS path: the
// certificate is generated in the data directory, the API answers over HTTPS,
// HSTS is sent, and the session cookie is Secure because the transport is.
func TestServeHTTPSWithSelfSignedCertificate(t *testing.T) {
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
	handler := httpapi.NewWithOptions(conn, assets, true, email.Config{}, nil, teams.New(conn))
	handler.SetSecrets(box)

	certPath, keyPath, err := tlsconf.EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, server, nil, handler.Close, 5*time.Second) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	base := "https://" + listener.Addr().String()
	response, err := client.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health over HTTPS: %d", response.StatusCode)
	}
	if response.TLS == nil {
		t.Fatal("the response did not use TLS")
	}
	if response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS header missing on a TLS response")
	}

	setup, err := client.Post(base+"/api/setup", "application/json",
		strings.NewReader(`{"name":"Admin","email":"admin@example.test","password":"password-strong"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup over HTTPS: %d", setup.StatusCode)
	}
	var cookie *http.Cookie
	for _, candidate := range setup.Cookies() {
		if candidate.Name == "session" {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	if !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("cookie over TLS must be Secure and HttpOnly: %+v", cookie)
	}

	var payload struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(setup.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Email != "admin@example.test" || payload.Role != "admin" {
		t.Fatalf("setup payload: %+v", payload)
	}
}
