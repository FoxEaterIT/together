package auth

import (
	"testing"
	"time"

	"jiralike/internal/db"
)

func TestSetupSessionAndTokenLifecycle(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := New(conn, nil)
	user, initial, err := store.Setup("Admin", "admin@example.test", "password-strong")
	if err != nil {
		t.Fatal(err)
	}
	if initial == "" {
		t.Fatal("setup should return initial personal token")
	}
	ok, err := store.SetupCompleted()
	if err != nil || !ok {
		t.Fatalf("setup not marked complete: %v %v", ok, err)
	}
	raw, expires, err := store.StartSession(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expires.Before(time.Now()) {
		t.Fatal("session expiry is in the past")
	}
	if _, err := store.UserFromSession(raw); err != nil {
		t.Fatalf("session rejected: %v", err)
	}
	if err := store.RevokeSession(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserFromSession(raw); err == nil {
		t.Fatal("revoked session accepted")
	}
	if err := store.RevokeToken(user.ID, 1); err != ErrLastToken {
		t.Fatalf("expected last-token guard, got %v", err)
	}
	created, err := store.CreateToken(user.ID, "automation")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.AuthenticateToken(created.Token); err != nil || got.ID != user.ID {
		t.Fatalf("token rejected: %+v %v", got, err)
	}
	if err := store.RevokeToken(user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateToken(created.Token); err == nil {
		t.Fatal("revoked token accepted")
	}
}
