package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
)

type Auth struct {
	secret        []byte
	secretPath    string
	pendingToken  string
	jwtExpiry     time.Duration
	handoffTokens map[string]time.Time
	mu            sync.Mutex
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
	a.secret = b
	return os.WriteFile(a.secretPath, b, 0600)
}

func (a *Auth) LoadSecret() error {
	data, err := os.ReadFile(a.secretPath)
	if err != nil {
		return fmt.Errorf("load secret: %w", err)
	}
	a.secret = data
	return nil
}

func (a *Auth) GenerateToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	rand.Read(b)
	a.pendingToken = hex.EncodeToString(b)
	return a.pendingToken
}

func (a *Auth) ValidateToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingToken == "" || token != a.pendingToken {
		return false
	}
	a.pendingToken = "" // single-use
	return true
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
	rand.Read(b)
	token := hex.EncodeToString(b)
	a.handoffTokens[token] = now.Add(5 * time.Minute)
	return token
}

func (a *Auth) ValidateHandoffToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.handoffTokens[token]
	if !ok {
		return false
	}
	delete(a.handoffTokens, token) // single-use
	return time.Now().Before(exp)
}

func (a *Auth) IssueJWT(deviceID string) (string, error) {
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
	return token.SignedString(a.secret)
}

func (a *Auth) VerifyJWT(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.secret, nil
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
		cookie, err := r.Cookie("claude-remote-auth")
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if _, err := a.VerifyJWT(cookie.Value); err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
