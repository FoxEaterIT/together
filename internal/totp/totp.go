// Package totp implements time-based one-time passwords (RFC 6238) with the
// standard library only: SHA-1, six digits, a thirty second step and a window
// of one step in each direction. The verifier refuses a step it has already
// accepted, which blocks replay inside the same window.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Digits is the code length shown by authenticator applications.
	Digits = 6
	// Period is the length of one time step in seconds.
	Period = 30
	// Issuer labels the entry inside the authenticator application.
	Issuer = "Together"
)

var base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh 160-bit secret encoded in Base32 without
// padding, the format authenticator applications expect.
func GenerateSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32NoPadding.EncodeToString(buf), nil
}

// NormalizeSecret accepts the separators people copy from a screen: spaces,
// hyphens and trailing padding are ignored, and the value is upper-cased.
func NormalizeSecret(secret string) string {
	replacer := strings.NewReplacer(" ", "", "-", "", "=", "", "\t", "")
	return strings.ToUpper(replacer.Replace(strings.TrimSpace(secret)))
}

func decodeSecret(secret string) ([]byte, error) {
	normalized := NormalizeSecret(secret)
	if normalized == "" {
		return nil, fmt.Errorf("empty TOTP secret")
	}
	key, err := base32NoPadding.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("invalid TOTP secret: %w", err)
	}
	return key, nil
}

// Code returns the six-digit code for the step containing at.
func Code(secret string, at time.Time) (string, error) {
	return CodeDigits(secret, at, Digits)
}

// CodeDigits is Code with a configurable length, used by the RFC 6238 vectors.
func CodeDigits(secret string, at time.Time, digits int) (string, error) {
	if digits < 6 || digits > 8 {
		return "", fmt.Errorf("unsupported TOTP digit count %d", digits)
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeForStep(key, uint64(at.Unix()/Period), digits)
}

func codeForStep(key []byte, step uint64, digits int) (string, error) {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], step)
	mac := hmac.New(sha1.New, key)
	if _, err := mac.Write(counter[:]); err != nil {
		return "", err
	}
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus), nil
}

// Step returns the TOTP step containing at.
func Step(at time.Time) int64 {
	return at.Unix() / Period
}

// Validate checks code against the current step and its two neighbours. It
// returns the accepted step so callers can persist it; a step at or below
// lastStep is rejected, so the same code cannot be replayed. Comparison is
// constant time.
func Validate(secret, code string, at time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	current := Step(at)
	for _, delta := range []int64{0, -1, 1} {
		step := current + delta
		if step <= lastStep || step < 0 {
			continue
		}
		expected, err := codeForStep(key, uint64(step), Digits)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// URI builds the otpauth:// URI encoded in the enrollment QR code.
func URI(account, secret string) string {
	label := url.PathEscape(Issuer + ":" + account)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", Issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", strconv.Itoa(Digits))
	query.Set("period", strconv.Itoa(Period))
	return "otpauth://totp/" + label + "?" + query.Encode()
}
