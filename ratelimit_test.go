package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	l := newLimiter(3, 10*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed within the burst", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Error("4th request should be throttled")
	}
	// A different caller has its own bucket.
	if !l.allow("5.6.7.8") {
		t.Error("other IPs must not be affected")
	}
	time.Sleep(25 * time.Millisecond)
	if !l.allow("1.2.3.4") {
		t.Error("bucket should refill over time")
	}
}

func TestClientIPTrustsCloudflareOnlyFromLoopback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP behind tunnel = %q", got)
	}

	// From a non-loopback peer the header is attacker-controlled and ignored.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "192.168.1.50:5555"
	r2.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientIP(r2); got != "192.168.1.50" {
		t.Errorf("clientIP from LAN = %q, want the real peer", got)
	}
}

func TestIsSecureRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if isSecureRequest(r) {
		t.Error("plain HTTP request is not secure")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !isSecureRequest(r) {
		t.Error("tunnel-terminated HTTPS must count as secure")
	}
}

func TestViaProxy(t *testing.T) {
	direct := httptest.NewRequest(http.MethodGet, "/", nil)
	if viaProxy(direct) {
		t.Error("direct request must not look proxied")
	}
	fwd := httptest.NewRequest(http.MethodGet, "/", nil)
	fwd.Header.Set("X-Forwarded-For", "203.0.113.7")
	if !viaProxy(fwd) {
		t.Error("forwarded request must be detected")
	}
}
