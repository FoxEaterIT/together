package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B uses the ASCII secret "12345678901234567890" and eight
// digits. The vectors below also cover the six-digit form the app displays.
func TestRFC6238Vectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, vector := range vectors {
		at := time.Unix(vector.unix, 0).UTC()
		got, err := CodeDigits(secret, at, 8)
		if err != nil {
			t.Fatalf("T=%d: %v", vector.unix, err)
		}
		if got != vector.code {
			t.Fatalf("T=%d: got %s want %s", vector.unix, got, vector.code)
		}
		short, err := Code(secret, at)
		if err != nil {
			t.Fatalf("T=%d six digits: %v", vector.unix, err)
		}
		if short != vector.code[2:] {
			t.Fatalf("T=%d six digits: got %s want %s", vector.unix, short, vector.code[2:])
		}
	}
}

func TestValidateWindowAndReplay(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d, want 32", len(secret))
	}
	at := time.Unix(1700000000, 0).UTC()
	previous, err := Code(secret, at.Add(-Period*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	step, ok := Validate(secret, previous, at, 0)
	if !ok {
		t.Fatal("previous step should be accepted inside the window")
	}
	if step != Step(at)-1 {
		t.Fatalf("accepted step = %d, want %d", step, Step(at)-1)
	}
	// The same code cannot be used twice.
	if _, ok := Validate(secret, previous, at, step); ok {
		t.Fatal("replay of an accepted code was allowed")
	}
}

func TestValidateRejectsMalformedAndDistantCodes(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0).UTC()
	current, _ := Code(secret, at)
	for _, code := range []string{"", "12345", "1234567", "abcdef", current + "0"} {
		if _, ok := Validate(secret, code, at, 0); ok {
			t.Fatalf("malformed code %q accepted", code)
		}
	}
	distant, _ := Code(secret, at.Add(-3*Period*time.Second))
	if _, ok := Validate(secret, distant, at, 0); ok {
		t.Fatal("code three steps away accepted")
	}
}

func TestSecretNormalization(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	withSpaces := "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"
	at := time.Unix(59, 0).UTC()
	a, err := Code(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Code(withSpaces, at)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("normalization changed the code: %s vs %s", a, b)
	}
	if !strings.HasPrefix(URI("user@example.test", secret), "otpauth://totp/Together:user@example.test?") {
		t.Fatalf("unexpected URI: %s", URI("user@example.test", secret))
	}
}
