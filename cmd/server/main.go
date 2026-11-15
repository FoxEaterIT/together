// Command server is the Together binary. It loads configuration, opens SQLite,
// mounts the API and the embedded SPA, runs the notification and automation
// schedulers, and drains cleanly on SIGINT/SIGTERM.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"jiralike/internal/config"
	"jiralike/internal/db"
	"jiralike/internal/email"
	"jiralike/internal/httpapi"
	"jiralike/internal/secrets"
	"jiralike/internal/teams"
	"jiralike/internal/tlsconf"
	"jiralike/internal/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	// The installation key seals values that must be readable again (TOTP
	// secrets, the SMTP password). It is created on first start.
	box, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}

	assets, err := web.FS()
	if err != nil {
		return err
	}
	certFile, keyFile, err := tlsFiles(cfg)
	if err != nil {
		return err
	}
	smtpPass, err := smtpPassword(cfg, box)
	if err != nil {
		return err
	}
	teamsStore := teams.New(conn)
	emailCfg := email.Config{
		Host: cfg.SMTPHost,
		Port: cfg.SMTPPort,
		User: cfg.SMTPUser,
		Pass: smtpPass,
		From: cfg.SMTPFrom,
		TLS:  cfg.SMTPTLS,
	}
	var sender func(email.Config, string, string, string) error
	if emailCfg.Configured() {
		sender = email.SendInvite
	} else {
		log.Printf("SMTP not configured: invite emails will not be sent")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Serving TLS means the cookie must be Secure even if config.ini was written
	// before the certificate existed.
	handler := httpapi.NewWithOptions(conn, assets, cfg.SecureCookies || cfg.TLSEnabled(), emailCfg, sender, teamsStore)
	handler.SetConfigPath(configPath())
	handler.SetSecrets(box)
	if sealed, err := handler.SealLegacy(); err != nil {
		return fmt.Errorf("seal legacy secrets: %w", err)
	} else if sealed > 0 {
		log.Printf("sealed %d secret values written by an older version", sealed)
	}
	stopDigest := handler.StartDueDigest(ctx, 0)
	defer stopDigest()

	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	scheme := "http"
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			listener.Close()
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		scheme = "https"
	} else {
		log.Printf("warning: serving plain HTTP, so passwords and cookies travel unencrypted; enable tls_auto or set tls_cert and tls_key")
	}

	redirect := startRedirect(cfg)
	log.Printf("listening on %s (%s)", listener.Addr(), scheme)
	err = serve(ctx, listener, server, stopDigest, handler.Close, 15*time.Second)
	stopRedirect(redirect)
	return err
}

// tlsFiles resolves the certificate pair: a generated self-signed one in
// tls_auto mode, the configured files otherwise. Empty names mean plain HTTP.
func tlsFiles(cfg config.Config) (certFile, keyFile string, err error) {
	if cfg.TLSAuto {
		return tlsconf.EnsureSelfSigned(cfg.DataDir)
	}
	return cfg.TLSCert, cfg.TLSKey, nil
}

// smtpPassword decrypts the stored SMTP password. A clear-text value left in
// config.ini by an older version is sealed once and the clear key is blanked;
// SMTP_PASS from the environment stays in memory and is never written to disk.
func smtpPassword(cfg config.Config, box *secrets.Box) (string, error) {
	if cfg.SMTPPass != "" {
		if _, fromEnv := os.LookupEnv("SMTP_PASS"); fromEnv {
			return cfg.SMTPPass, nil
		}
		sealed, err := box.Seal(cfg.SMTPPass)
		if err != nil {
			return "", err
		}
		if err := config.Save(configPath(), map[string]string{"smtp_pass": "", "smtp_pass_enc": sealed}); err != nil {
			log.Printf("could not remove the clear-text SMTP password from config.ini: %v", err)
			return cfg.SMTPPass, nil
		}
		log.Printf("sealed the clear-text SMTP password in config.ini")
		return cfg.SMTPPass, nil
	}
	return box.Open(cfg.SMTPPassSealed)
}

// startRedirect serves plain HTTP on tls_redirect and sends every request to
// HTTPS. It is opt-in because a second port is often managed by a proxy. The
// target port comes from the TLS listener, so a non-standard HTTPS port does
// not send the browser back to the plain listener.
func startRedirect(cfg config.Config) *http.Server {
	if cfg.TLSRedirect == "" {
		return nil
	}
	_, httpsPort, _ := net.SplitHostPort(cfg.Addr)
	server := &http.Server{
		Addr:              cfg.TLSRedirect,
		Handler:           redirectHandler(httpsPort),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP redirect listener on %s failed: %v", cfg.TLSRedirect, err)
		}
	}()
	return server
}

func stopRedirect(server *http.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

// redirectHandler builds the permanent redirect to the HTTPS port the server
// actually listens on. The incoming Host keeps the public name; only the port
// is replaced, and the default 443 is left implicit.
func redirectHandler(httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		host = strings.Trim(host, "[]")
		if httpsPort != "" && httpsPort != "443" {
			host = net.JoinHostPort(host, httpsPort)
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	}
}

// serve runs the HTTP server until the context is cancelled, then stops
// accepting connections, closes long-lived streams, drains in-flight requests up
// to timeout, stops the background scheduler and closes the listener. Streams
// are closed before Shutdown because Shutdown does not cancel request contexts:
// an open SSE handler would otherwise hold the drain for the whole timeout.
// Tests exercise this path directly so the shutdown behaviour does not depend on
// a real signal.
func serve(ctx context.Context, listener net.Listener, server *http.Server, stop, closeStreams func(), timeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() {
		// ServeTLS with empty file names uses the certificates already in
		// TLSConfig; the pair was loaded and validated by the caller.
		if server.TLSConfig != nil {
			serveErr <- server.ServeTLS(listener, "", "")
			return
		}
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if closeStreams != nil {
			closeStreams()
		}
		if stop != nil {
			stop()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Printf("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if closeStreams != nil {
		closeStreams()
	}
	err := server.Shutdown(shutdownCtx)
	if err != nil {
		_ = server.Close()
	}
	if stop != nil {
		stop()
	}
	if err != nil {
		return err
	}
	log.Printf("shutdown complete")
	return nil
}

func configPath() string {
	if path := os.Getenv("CONFIG_PATH"); path != "" {
		return path
	}
	return "config.ini"
}
