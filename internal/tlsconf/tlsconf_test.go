package tlsconf

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSelfSignedCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	if certPath != filepath.Join(dir, CertFile) || keyPath != filepath.Join(dir, KeyFile) {
		t.Fatalf("unexpected paths: %s %s", certPath, keyPath)
	}
	first, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("generated pair does not load: %v", err)
	}
	if len(first.Certificate) == 0 {
		t.Fatal("generated certificate is empty")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("key file is empty")
	}

	again, keyPathAgain, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != certPath || keyPathAgain != keyPath {
		t.Fatalf("second call changed the paths: %s %s", again, keyPathAgain)
	}
	second, err := tls.LoadX509KeyPair(again, keyPath)
	if err != nil {
		t.Fatalf("reused pair does not load: %v", err)
	}
	if string(second.Certificate[0]) != string(first.Certificate[0]) {
		t.Fatal("a valid certificate should be reused instead of regenerated")
	}
}

func TestEnsureSelfSignedReplacesBrokenPair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CertFile), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("broken pair was not replaced: %v", err)
	}
}
