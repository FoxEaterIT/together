// Package secrets keeps recoverable secrets out of plain text. Values that must
// be read back, such as TOTP secrets and the SMTP password, are sealed with
// AES-256-GCM under a key file in the data directory; values that only need
// verification are hashed by the package that owns them.
//
// The key file protects backups, database copies and configuration files that
// leak on their own. It does not protect a compromised host, because the key
// lives on the same machine.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// KeyFile is the key file name inside the data directory.
	KeyFile = "secret.key"
	// KeySize is the AES-256 key length in bytes.
	KeySize = 32
	// SealPrefix marks a sealed value so legacy plain text stays detectable.
	SealPrefix = "v1:"
)

// ErrNoKey means the box was not configured, so sealing is refused rather than
// silently writing plain text.
var ErrNoKey = errors.New("secret key is not configured")

// Box seals and opens values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// LoadOrCreate reads the key file from dir, creating a random one on first run.
func LoadOrCreate(dir string) (*Box, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("data directory is required for the secret key")
	}
	path := filepath.Join(dir, KeyFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, KeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate secret key: %w", err)
		}
		created, err := writeKey(path, key)
		if err != nil {
			return nil, err
		}
		if !created {
			// Another process won the race: use the key that reached the disk.
			return LoadOrCreate(dir)
		}
		return newBox(key)
	}
	if err != nil {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != KeySize {
		return nil, fmt.Errorf("secret key %s is not %d hex-encoded bytes; delete it to let the server create a new one (sealed values will be lost)", path, KeySize)
	}
	return newBox(key)
}

// NewHex builds a box from a hex-encoded key, for tests and tooling.
func NewHex(keyHex string) (*Box, error) {
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil {
		return nil, fmt.Errorf("decode secret key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret key must be %d bytes", KeySize)
	}
	return newBox(key)
}

// NewRandom builds a box with a fresh ephemeral key, for tests.
func NewRandom() (*Box, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return newBox(key)
}

func newBox(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns SealPrefix followed by the base64url encoded nonce and
// ciphertext. An empty input stays empty, so optional secrets keep their
// meaning.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if b == nil || b.aead == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return SealPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. An empty input stays empty.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if b == nil || b.aead == nil {
		return "", ErrNoKey
	}
	encoded, ok := strings.CutPrefix(sealed, SealPrefix)
	if !ok {
		return "", errors.New("sealed value has an unknown format")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode sealed value: %w", err)
	}
	if len(raw) < b.aead.NonceSize() {
		return "", errors.New("sealed value is truncated")
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("sealed value cannot be opened: data/secret.key does not match the stored data")
	}
	return string(plain), nil
}

// IsSealed reports whether a stored value is already encrypted.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, SealPrefix)
}

// writeKey creates the key file exclusively and reports whether it did.
func writeKey(path string, key []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create secret key: %w", err)
	}
	if _, err := file.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		file.Close()
		return false, fmt.Errorf("write secret key: %w", err)
	}
	return true, file.Close()
}
