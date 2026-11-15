// Package config loads config.ini, applies environment overrides, validates
// every value and writes administrator changes back to the same file without
// leaving a partially written file behind.
package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr           string
	DataDir        string
	DBPath         string
	Locale         string
	SMTPHost       string
	SMTPPort       int
	SMTPUser       string
	SMTPPass       string // legacy plain text in config.ini; sealed on start
	SMTPPassSealed string // smtp_pass_enc
	SMTPFrom       string
	SMTPTLS        bool
	SecureCookies  bool
	TLSCert        string
	TLSKey         string
	TLSAuto        bool
	TLSRedirect    string
}

// TLSEnabled reports whether the server should terminate HTTPS itself.
func (c Config) TLSEnabled() bool {
	return c.TLSAuto || (c.TLSCert != "" && c.TLSKey != "")
}

func Load(path string) (Config, error) {
	values := map[string]string{}
	if path != "" {
		fileValues, err := readFile(path)
		if err != nil && !os.IsNotExist(err) {
			return Config{}, err
		}
		for key, value := range fileValues {
			values[key] = value
		}
	}
	for _, key := range []string{"ADDR", "DATA_DIR", "DB_PATH", "LOCALE", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM", "SMTP_TLS", "SECURE_COOKIES", "TLS_CERT", "TLS_KEY", "TLS_AUTO", "TLS_REDIRECT"} {
		if value, ok := os.LookupEnv(key); ok {
			values[strings.ToLower(key)] = value
		}
	}
	cfg := Config{
		Addr:           pick(values, "addr", ":8080"),
		DataDir:        pick(values, "data_dir", "data"),
		Locale:         pick(values, "locale", "en"),
		SMTPHost:       strings.TrimSpace(values["smtp_host"]),
		SMTPUser:       values["smtp_user"],
		SMTPPass:       values["smtp_pass"],
		SMTPPassSealed: strings.TrimSpace(values["smtp_pass_enc"]),
		SMTPFrom:       strings.TrimSpace(values["smtp_from"]),
		TLSCert:        strings.TrimSpace(values["tls_cert"]),
		TLSKey:         strings.TrimSpace(values["tls_key"]),
		TLSRedirect:    strings.TrimSpace(values["tls_redirect"]),
	}
	var err error
	if cfg.SecureCookies, err = boolValue(values, "secure_cookies"); err != nil {
		return Config{}, err
	}
	if cfg.SMTPTLS, err = boolValue(values, "smtp_tls"); err != nil {
		return Config{}, err
	}
	if cfg.TLSAuto, err = boolValue(values, "tls_auto"); err != nil {
		return Config{}, err
	}
	if cfg.Locale != "en" && cfg.Locale != "it" && cfg.Locale != "es" {
		return Config{}, fmt.Errorf("locale must be en, it or es")
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return Config{}, fmt.Errorf("invalid addr %q: %w", cfg.Addr, err)
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return Config{}, fmt.Errorf("tls_cert and tls_key must be set together")
	}
	if cfg.TLSAuto && cfg.TLSCert != "" {
		return Config{}, fmt.Errorf("tls_auto cannot be combined with tls_cert")
	}
	if cfg.TLSRedirect != "" {
		if !cfg.TLSEnabled() {
			return Config{}, fmt.Errorf("tls_redirect requires tls_auto or tls_cert with tls_key")
		}
		if _, _, err := net.SplitHostPort(cfg.TLSRedirect); err != nil {
			return Config{}, fmt.Errorf("invalid tls_redirect %q: %w", cfg.TLSRedirect, err)
		}
	}
	portText := pick(values, "smtp_port", "0")
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return Config{}, fmt.Errorf("smtp_port must be between 0 and 65535")
	}
	cfg.SMTPPort = port
	if explicit := strings.TrimSpace(values["db_path"]); explicit != "" {
		cfg.DBPath = explicit
	} else {
		cfg.DBPath = filepath.Join(cfg.DataDir, "app.db")
	}
	if cfg.SMTPHost != "" && cfg.SMTPPort == 0 {
		return Config{}, fmt.Errorf("smtp_port is required when smtp_host is configured")
	}
	return cfg, nil
}
func (c Config) SMTPConfigured() bool { return c.SMTPHost != "" && c.SMTPPort > 0 && c.SMTPFrom != "" }

// boolValue reads a true/false configuration flag. An empty value means false.
func boolValue(values map[string]string, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(values[key])) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false", key)
}
func readFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid config line %q", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("empty config key")
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, scanner.Err()
}
func pick(values map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	return fallback
}
