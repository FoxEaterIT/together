package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Save updates keys in the configuration file while preserving comments,
// unknown keys and the original order. Missing keys are appended. The write is
// atomic: a temporary file in the same directory is renamed over the target.
func Save(path string, updates map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is not configured")
	}
	lines := []string{}
	existing, err := os.ReadFile(path)
	if err == nil {
		text := strings.ReplaceAll(string(existing), "\r\n", "\n")
		lines = strings.Split(text, "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, strings.ToLower(strings.TrimSpace(key)))
	}
	sort.Strings(keys)

	seen := map[string]bool{}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			kept = append(kept, line)
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			kept = append(kept, line)
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value, ok := updates[key]
		if !ok {
			kept = append(kept, line)
			continue
		}
		seen[key] = true
		// An empty value removes the key: that is how a secret that must not
		// stay on disk (the clear-text SMTP password) is deleted from the file.
		if value == "" {
			continue
		}
		kept = append(kept, key+"="+value)
	}
	for _, key := range keys {
		if seen[key] || updates[key] == "" {
			continue
		}
		kept = append(kept, key+"="+updates[key])
	}
	content := strings.Join(kept, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.WriteString(content); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}
