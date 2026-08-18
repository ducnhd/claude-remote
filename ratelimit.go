package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// limiter is a small per-key token bucket used to slow down credential
// guessing. The tunnel makes the server reachable from the internet, so
// unauthenticated endpoints must not accept unbounded attempts.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    time.Duration // time to regain one token
	burst   int
}

type bucket struct {
	tokens int
	last   time.Time
}

func newLimiter(burst int, rate time.Duration) *limiter {
	return &limiter{buckets: make(map[string]*bucket), rate: rate, burst: burst}
}

// allow consumes a token for key, reporting whether the request may proceed.
func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 10000 { // bound memory under a flood
			l.gc(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// Refill.
	if gained := int(now.Sub(b.last) / l.rate); gained > 0 {
		b.tokens += gained
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// gc drops buckets that have been idle long enough to be full again.
func (l *limiter) gc(now time.Time) {
	idle := l.rate * time.Duration(l.burst)
	for k, b := range l.buckets {
		if now.Sub(b.last) > idle {
			delete(l.buckets, k)
		}
	}
}

// clientIP returns the address used for rate limiting. Behind the tunnel
// every request arrives from 127.0.0.1, so the Cloudflare-supplied client
// address is used instead — it is only trusted for loopback peers.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return cf
		}
	}
	return host
}

// viaProxy reports whether the request was forwarded by a proxy such as
// cloudflared, rather than made directly against the local listener.
func viaProxy(r *http.Request) bool {
	for _, h := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Forwarded-Proto", "Cf-Ray"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// isSecureRequest reports whether the browser reached us over HTTPS,
// including the case where TLS was terminated by the tunnel.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
