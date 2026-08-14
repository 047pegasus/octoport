package auth

import (
	"strings"
	"testing"
	"time"
)

func TestIssueAndParse(t *testing.T) {
	m := NewManager("test-secret", time.Hour, "octoport-test")
	tok, exp, err := m.Issue("user-1", "a@b.com", "api")
	if err != nil {
		t.Fatal(err)
	}
	if exp.Before(time.Now()) {
		t.Fatal("expiry in the past")
	}
	claims, err := m.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" || claims.Email != "a@b.com" || claims.Scope != "api" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	m := NewManager("secret-a", time.Hour, "octoport")
	other := NewManager("secret-b", time.Hour, "octoport")
	tok, _, _ := m.Issue("user-1", "a@b.com", "api")
	if _, err := other.Parse(tok); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestParseRejectsExpired(t *testing.T) {
	m := NewManager("secret", time.Millisecond, "octoport")
	tok, _, _ := m.Issue("user-1", "a@b.com", "api")
	time.Sleep(5 * time.Millisecond)
	if _, err := m.Parse(tok); err == nil {
		t.Fatal("expected expired token error")
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(hash, "$2") == false {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if !CheckPassword(hash, "hunter2") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
}
