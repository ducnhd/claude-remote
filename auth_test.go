package main

import (
	"os"
	"testing"
	"time"
)

func TestGenerateSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	if err := a.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	if len(a.secret) != 32 {
		t.Errorf("want 32 bytes, got %d", len(a.secret))
	}
	info, err := os.Stat(a.secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("want 0600, got %o", info.Mode().Perm())
	}
}

func TestLoadSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()
	original := make([]byte, len(a.secret))
	copy(original, a.secret)

	a2 := &Auth{secretPath: dir + "/secret.key"}
	if err := a2.LoadSecret(); err != nil {
		t.Fatal(err)
	}
	if string(a2.secret) != string(original) {
		t.Error("loaded secret doesn't match")
	}
}

func TestGenerateToken(t *testing.T) {
	a := &Auth{}
	token := a.GenerateToken()
	if len(token) != 64 {
		t.Errorf("want 64 chars, got %d", len(token))
	}
	if a.pendingToken != token {
		t.Error("pending token not set")
	}
}

func TestTokenSingleUse(t *testing.T) {
	a := &Auth{}
	token := a.GenerateToken()
	if !a.ValidateToken(token) {
		t.Error("first use should succeed")
	}
	if a.ValidateToken(token) {
		t.Error("second use should fail")
	}
}

func TestJWTSignVerify(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()

	tokenStr, err := a.IssueJWT("device-1")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := a.VerifyJWT(tokenStr)
	if err != nil {
		t.Fatal(err)
	}
	if deviceID != "device-1" {
		t.Errorf("want device-1, got %s", deviceID)
	}
}

func TestJWTExpired(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key", jwtExpiry: -1 * time.Hour}
	a.GenerateSecret()

	tokenStr, _ := a.IssueJWT("device-1")
	_, err := a.VerifyJWT(tokenStr)
	if err == nil {
		t.Error("expired JWT should fail")
	}
}

func TestGenerateHandoffToken(t *testing.T) {
	a := NewAuth(t.TempDir() + "/secret.key")
	token := a.GenerateHandoffToken()
	if len(token) != 64 {
		t.Errorf("want 64 chars, got %d", len(token))
	}
	if _, ok := a.handoffTokens[token]; !ok {
		t.Error("token not stored in handoffTokens")
	}
}

func TestHandoffTokenSingleUse(t *testing.T) {
	a := NewAuth(t.TempDir() + "/secret.key")
	token := a.GenerateHandoffToken()
	if !a.ValidateHandoffToken(token) {
		t.Error("first use should succeed")
	}
	if a.ValidateHandoffToken(token) {
		t.Error("second use should fail")
	}
}

func TestHandoffTokenExpired(t *testing.T) {
	a := NewAuth(t.TempDir() + "/secret.key")
	token := a.GenerateHandoffToken()
	// Manually back-date the expiry.
	a.mu.Lock()
	a.handoffTokens[token] = time.Now().Add(-1 * time.Minute)
	a.mu.Unlock()
	if a.ValidateHandoffToken(token) {
		t.Error("expired token should fail")
	}
}

func TestHandoffTokenInvalid(t *testing.T) {
	a := NewAuth(t.TempDir() + "/secret.key")
	if a.ValidateHandoffToken("not-a-real-token") {
		t.Error("invalid token should fail")
	}
}

func TestGenerateHandoffTokenCleansExpired(t *testing.T) {
	a := NewAuth(t.TempDir() + "/secret.key")
	// Insert a fake expired token.
	a.mu.Lock()
	a.handoffTokens["old"] = time.Now().Add(-10 * time.Minute)
	a.mu.Unlock()
	a.GenerateHandoffToken()
	a.mu.Lock()
	_, stillPresent := a.handoffTokens["old"]
	a.mu.Unlock()
	if stillPresent {
		t.Error("GenerateHandoffToken should clean up expired tokens")
	}
}

func TestJWTWrongSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()
	tokenStr, _ := a.IssueJWT("device-1")

	// Regenerate secret (simulates revoke)
	a.GenerateSecret()
	_, err := a.VerifyJWT(tokenStr)
	if err == nil {
		t.Error("JWT signed with old secret should fail")
	}
}
