package main

import (
	"strings"
	"testing"
	"time"
)

func TestTunnelArgs(t *testing.T) {
	quick := NewTunnelManager(Tunnel{Mode: TunnelQuick}, 8822)
	got := strings.Join(quick.args(), " ")
	if !strings.Contains(got, "--url http://127.0.0.1:8822") {
		t.Errorf("quick tunnel args = %q", got)
	}
	named := NewTunnelManager(Tunnel{Mode: TunnelNamed, Token: "secret-token"}, 8822)
	got = strings.Join(named.args(), " ")
	if !strings.Contains(got, "run --token secret-token") {
		t.Errorf("named tunnel args = %q", got)
	}
}

func TestTunnelScanExtractsURL(t *testing.T) {
	tn := NewTunnelManager(Tunnel{Mode: TunnelQuick}, 8822)
	out := `2026-08-18T05:00:00Z INF Requesting new quick Tunnel
2026-08-18T05:00:01Z INF +---------------------------------------+
2026-08-18T05:00:01Z INF |  https://snake-bugs-proved.trycloudflare.com  |
2026-08-18T05:00:01Z INF +---------------------------------------+`
	tn.scan(strings.NewReader(out))

	state, url, _ := tn.Status()
	if url != "https://snake-bugs-proved.trycloudflare.com" {
		t.Errorf("url = %q", url)
	}
	if state != "up" {
		t.Errorf("state = %q, want up", state)
	}
	if got := tn.WaitReady(time.Second); got != url {
		t.Errorf("WaitReady = %q, want %q", got, url)
	}
}

func TestTunnelDisabledIsNoop(t *testing.T) {
	tn := NewTunnelManager(Tunnel{Mode: TunnelOff}, 8822)
	if err := tn.Start(); err != nil {
		t.Fatalf("disabled tunnel should start cleanly, got %v", err)
	}
	if tn.URL() != "" {
		t.Error("disabled tunnel must not report a URL")
	}
	if got := tn.WaitReady(50 * time.Millisecond); got != "" {
		t.Errorf("WaitReady = %q, want empty", got)
	}
	tn.Stop() // must not panic when never started
}

func TestTunnelNamedRequiresToken(t *testing.T) {
	tn := NewTunnelManager(Tunnel{Mode: TunnelNamed}, 8822)
	if err := tn.Start(); err == nil {
		t.Error("named tunnel without a token must fail fast")
	}
}

func TestBindHost(t *testing.T) {
	withTunnel := &Config{Tunnel: Tunnel{Mode: TunnelQuick}}
	if got := withTunnel.BindHost(); got != "127.0.0.1" {
		// Binding 0.0.0.0 while tunnelling would expose the server on the
		// LAN as well, with no reason to.
		t.Errorf("BindHost with tunnel = %q, want 127.0.0.1", got)
	}
	noTunnel := &Config{Tunnel: Tunnel{Mode: TunnelOff}}
	if got := noTunnel.BindHost(); got != "0.0.0.0" {
		t.Errorf("BindHost without tunnel = %q, want 0.0.0.0", got)
	}
}
