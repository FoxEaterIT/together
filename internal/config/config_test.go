package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndEnvironmentOverrides(t *testing.T) {
	for _, key := range []string{"ADDR", "DATA_DIR", "DB_PATH", "LOCALE", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "SMTP_TLS", "SECURE_COOKIES"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATA_DIR", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.ini"))
	if err != nil {
		t.Fatal(err)
	}
	defaultDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.Locale != "en" || cfg.DataDir != filepath.Join(defaultDir, "Together") || cfg.DBPath != filepath.Join(defaultDir, "Together", "app.db") {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	file := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(file, []byte("addr=127.0.0.1:9000\nlocale=it\ndata_dir=store\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADDR", ":9999")
	t.Setenv("LOCALE", "it")
	t.Setenv("DATA_DIR", "store")
	cfg, err = Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9999" || cfg.Locale != "it" || cfg.DBPath != filepath.Join("store", "app.db") {
		t.Fatalf("override failed: %+v", cfg)
	}
}

func TestLoadAcceptsSpanish(t *testing.T) {
	t.Setenv("LOCALE", "es")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Locale != "es" {
		t.Fatalf("locale = %q, want es", cfg.Locale)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct{ key, value string }{{"LOCALE", "fr"}, {"SMTP_PORT", "70000"}, {"SMTP_TLS", "maybe"}, {"SECURE_COOKIES", "maybe"}, {"TLS_AUTO", "maybe"}} {
		t.Run(test.key, func(t *testing.T) {
			for _, key := range []string{"ADDR", "DATA_DIR", "DB_PATH", "LOCALE", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "SMTP_TLS", "SECURE_COOKIES", "TLS_CERT", "TLS_KEY", "TLS_AUTO", "TLS_REDIRECT"} {
				t.Setenv(key, "")
			}
			t.Setenv(test.key, test.value)
			if _, err := Load(""); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestLoadTLSConfiguration(t *testing.T) {
	clear := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{"ADDR", "DATA_DIR", "DB_PATH", "LOCALE", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "SMTP_TLS", "SECURE_COOKIES", "TLS_CERT", "TLS_KEY", "TLS_AUTO", "TLS_REDIRECT"} {
			t.Setenv(key, "")
		}
	}

	t.Run("self-signed", func(t *testing.T) {
		clear(t)
		t.Setenv("TLS_AUTO", "true")
		t.Setenv("TLS_REDIRECT", ":80")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.TLSEnabled() || cfg.TLSRedirect != ":80" {
			t.Fatalf("TLS settings: %+v", cfg)
		}
	})

	t.Run("files", func(t *testing.T) {
		clear(t)
		t.Setenv("TLS_CERT", "cert.pem")
		t.Setenv("TLS_KEY", "key.pem")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.TLSEnabled() || cfg.TLSCert != "cert.pem" {
			t.Fatalf("TLS settings: %+v", cfg)
		}
	})

	t.Run("plain http", func(t *testing.T) {
		clear(t)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TLSEnabled() {
			t.Fatal("TLS must be opt-in")
		}
	})

	for _, test := range []struct{ name, key, value string }{
		{"cert without key", "TLS_CERT", "cert.pem"},
		{"key without cert", "TLS_KEY", "key.pem"},
		{"auto with cert", "TLS_AUTO", "true"},
		{"redirect without tls", "TLS_REDIRECT", ":80"},
		{"invalid redirect", "TLS_REDIRECT", "not-an-address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clear(t)
			if test.name == "auto with cert" {
				t.Setenv("TLS_CERT", "cert.pem")
				t.Setenv("TLS_KEY", "key.pem")
			}
			if test.name == "invalid redirect" {
				t.Setenv("TLS_AUTO", "true")
			}
			t.Setenv(test.key, test.value)
			if _, err := Load(""); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}
