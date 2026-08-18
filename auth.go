package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
)

// Token lifetimes. Setup tokens are printed as a QR meant to be scanned
// right away; handoff tokens are generated on demand from Claude Code.
const (
	setupTokenTTL   = 30 * time.Minute
	handoffTokenTTL = 15 * time.Minute
)

// authCookieName is the browser cookie holding the session JWT.
const authCookieName = "claude-remote-auth"

type Auth struct {
	secret         []byte
	secretPath     string
	pendingToken   string
	pendingExpires time.Time
	jwtExpiry      time.Duration
	handoffTokens  map[string]time.Time
	mu             sync.Mutex
}

func NewAuth(secretPath string) *Auth {
	return &Auth{
		secretPath:    secretPath,
		jwtExpiry:     90 * 24 * time.Hour,
		handoffTokens: make(map[string]time.Time),
	}
}

func (a *Auth) GenerateSecret() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.secretPath), 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(a.secretPath, b, 0600); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	a.secret = b
	return nil
}

func (a *Auth) LoadSecret() error {
	data, err := os.ReadFile(a.secretPath)
	if err != nil {
		return fmt.Errorf("load secret: %w", err)
	}
	// An empty secret would let anyone forge a JWT signed with an empty key.
	if len(data) < 16 {
		return fmt.Errorf("load secret: %s is empty or too short, run 'claude-remote setup'", a.secretPath)
	}
	a.mu.Lock()
	a.secret = data
	a.mu.Unlock()
	return nil
}

// setPendingToken installs a setup token with a fresh expiry.
func (a *Auth) setPendingToken(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingToken = token
	a.pendingExpires = time.Now().Add(setupTokenTTL)
}

func (a *Auth) GenerateToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the system RNG is broken; refusing to
		// hand out a weak token is the only safe response.
		log.Printf("FATAL: crypto/rand unavailable: %v", err)
		return ""
	}
	a.pendingToken = hex.EncodeToString(b)
	a.pendingExpires = time.Now().Add(setupTokenTTL)
	return a.pendingToken
}

// ValidateToken checks a setup token. The token stays usable until it
// expires rather than being burned on first use: phone camera apps and
// link previewers often fetch the URL once before the browser opens it,
// which used to consume the token and leave the user unable to pair.
func (a *Auth) ValidateToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingToken == "" || token == "" {
		return false
	}
	if !a.pendingExpires.IsZero() && time.Now().After(a.pendingExpires) {
		a.pendingToken = "" // expired
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.pendingToken)) == 1
}

// ClearPendingToken invalidates the current setup token immediately.
func (a *Auth) ClearPendingToken() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingToken = ""
}

func (a *Auth) GenerateHandoffToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Clean up expired tokens.
	now := time.Now()
	for tok, exp := range a.handoffTokens {
		if now.After(exp) {
			delete(a.handoffTokens, tok)
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("FATAL: crypto/rand unavailable: %v", err)
		return ""
	}
	token := hex.EncodeToString(b)
	a.handoffTokens[token] = now.Add(handoffTokenTTL)
	return token
}

func (a *Auth) ValidateHandoffToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if token == "" {
		return false
	}
	exp, ok := a.handoffTokens[token]
	if !ok {
		return false
	}
	delete(a.handoffTokens, token) // single-use
	return time.Now().Before(exp)
}

func (a *Auth) IssueJWT(deviceID string) (string, error) {
	a.mu.Lock()
	secret := a.secret
	a.mu.Unlock()
	if len(secret) == 0 {
		return "", fmt.Errorf("issue jwt: no secret loaded")
	}
	expiry := a.jwtExpiry
	if expiry == 0 {
		expiry = 90 * 24 * time.Hour
	}
	claims := jwt.MapClaims{
		"device_id": deviceID,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(expiry).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyJWTRemaining verifies a token and reports how long it stays valid.
func (a *Auth) VerifyJWTRemaining(tokenStr string) (string, time.Duration, error) {
	a.mu.Lock()
	secret := a.secret
	a.mu.Unlock()
	if len(secret) == 0 {
		return "", 0, fmt.Errorf("verify jwt: no secret loaded")
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("verify jwt: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", 0, fmt.Errorf("invalid token")
	}
	deviceID, _ := claims["device_id"].(string)
	var remaining time.Duration
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		remaining = time.Until(exp.Time)
	}
	return deviceID, remaining, nil
}

func (a *Auth) VerifyJWT(tokenStr string) (string, error) {
	a.mu.Lock()
	secret := a.secret
	a.mu.Unlock()
	if len(secret) == 0 {
		return "", fmt.Errorf("verify jwt: no secret loaded")
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("verify jwt: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	deviceID, _ := claims["device_id"].(string)
	return deviceID, nil
}

func (a *Auth) PrintQR(url string) {
	q, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		fmt.Fprintf(os.Stderr, "QR error: %v\n", err)
		return
	}
	fmt.Println(q.ToSmallString(false))
	fmt.Printf("\nScan this QR code or open:\n%s\n", url)
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(authCookieName)
		if err != nil {
			log.Printf("AUTH DENIED %s %s — no cookie: %v", r.Method, r.URL.Path, err)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if _, err := a.VerifyJWT(cookie.Value); err != nil {
			log.Printf("AUTH DENIED %s %s — invalid JWT: %v", r.Method, r.URL.Path, err)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
