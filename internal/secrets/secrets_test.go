package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, KeyFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("key file permissions: %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(raw))) != KeySize*2 {
		t.Fatalf("key file should hold %d hex characters, got %q", KeySize*2, string(raw))
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := first.Seal("hello")
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := second.Open(sealed); err != nil || opened != "hello" {
		t.Fatalf("second box must open the first: %q %v", opened, err)
	}
}

func TestSealRoundTrip(t *testing.T) {
	box, err := NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	if sealed, err := box.Seal(""); err != nil || sealed != "" {
		t.Fatalf("empty value must stay empty: %q %v", sealed, err)
	}
	if opened, err := box.Open(""); err != nil || opened != "" {
		t.Fatalf("empty value must open empty: %q %v", opened, err)
	}
	sealed, err := box.Seal("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) || strings.Contains(sealed, "JBSWY3DPEHPK3PXP") {
		t.Fatalf("sealed value leaks or lacks a prefix: %q", sealed)
	}
	if opened, err := box.Open(sealed); err != nil || opened != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round trip: %q %v", opened, err)
	}
	again, err := box.Seal("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if again == sealed {
		t.Fatal("sealing must use a fresh nonce")
	}

	other, err := NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("another key must not open the value")
	}
	if _, err := box.Open("v1:not-base64!!"); err == nil {
		t.Fatal("malformed value must be rejected")
	}
	if _, err := box.Open("plain-text"); err == nil {
		t.Fatal("unsealed value must be rejected")
	}
}

func TestSealWithoutKeyIsRefused(t *testing.T) {
	var box *Box
	if _, err := box.Seal("secret"); err != ErrNoKey {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
	if _, err := box.Open(SealPrefix + "abc"); err != ErrNoKey {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestCorruptKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("too-short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("a truncated key must not be silently replaced")
	}
}
