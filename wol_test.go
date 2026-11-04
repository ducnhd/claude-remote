package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMagicPacket(t *testing.T) {
	pkt, err := magicPacket("AA:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt) != 102 {
		t.Fatalf("len = %d, want 102", len(pkt))
	}
	if !bytes.Equal(pkt[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Errorf("header = % x", pkt[:6])
	}
	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for i := 0; i < 16; i++ {
		if got := pkt[6+6*i : 12+6*i]; !bytes.Equal(got, mac) {
			t.Fatalf("copy %d = % x", i, got)
		}
	}
	for _, bad := range []string{"", "nonsense", "aa:bb:cc:dd:ee", "00:00:00:00:fe:80:00:00"} {
		if _, err := magicPacket(bad); err == nil {
			t.Errorf("magicPacket(%q) accepted", bad)
		}
	}
}

func TestFindWakeDevice(t *testing.T) {
	devs := []WakeDevice{{Name: "MacBook", MAC: "aa:bb:cc:dd:ee:ff"}}
	for _, q := range []string{"macbook", " MacBook ", "AA-BB-CC-DD-EE-FF"} {
		if _, ok := findWakeDevice(devs, q); !ok {
			t.Errorf("findWakeDevice(%q) found nothing", q)
		}
	}
	if _, ok := findWakeDevice(devs, "pc"); ok {
		t.Error("unknown name matched")
	}
}

// udpSink listens on loopback and returns its address plus a channel of the
// packets it receives.
func udpSink(t *testing.T) (string, <-chan []byte) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ch := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 512)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			ch <- append([]byte(nil), buf[:n]...)
		}
	}()
	return conn.LocalAddr().String(), ch
}

func TestWakeHandler(t *testing.T) {
	addr, got := udpSink(t)
	s := &Server{config: &Config{Wake: []WakeDevice{{Name: "mac", MAC: "aa:bb:cc:dd:ee:ff", Broadcast: addr}}}}

	rec := httptest.NewRecorder()
	s.handleWakeLAN(rec, httptest.NewRequest(http.MethodGet, "/api/wol", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"mac"`) {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}

	post := func(body, ctype string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/wol", strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		s.handleWakeLAN(rec, req)
		return rec
	}
	if rec := post("name=mac", "application/x-www-form-urlencoded"); rec.Code != http.StatusBadRequest {
		t.Errorf("form post = %d, want 400", rec.Code)
	}
	// Only configured devices: the phone must not broadcast to any MAC.
	if rec := post(`{"name":"11:22:33:44:55:66"}`, "application/json"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown device = %d, want 404", rec.Code)
	}
	if rec := post(`{"name":"mac"}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("wake = %d %s", rec.Code, rec.Body)
	}
	select {
	case pkt := <-got:
		want, _ := magicPacket("aa:bb:cc:dd:ee:ff")
		if !bytes.Equal(pkt, want) {
			t.Errorf("packet = % x", pkt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no packet received")
	}
}

func TestWakeBroadcastTargets(t *testing.T) {
	if got := wakeTargets(WakeDevice{Broadcast: "192.168.1.255"}); len(got) != 1 || got[0] != "192.168.1.255:9" {
		t.Errorf("targets = %v", got)
	}
	got := wakeTargets(WakeDevice{})
	if len(got) == 0 || got[0] != "255.255.255.255:9" {
		t.Errorf("default targets = %v", got)
	}
}

func TestMCPWake(t *testing.T) {
	addr, got := udpSink(t)
	s := &Server{config: &Config{Wake: []WakeDevice{{Name: "mac", MAC: "aa:bb:cc:dd:ee:ff", Broadcast: addr}}}}
	call := func(args string) map[string]any {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wake","arguments":` + args + `}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		s.handleMCP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := call(`{}`); !strings.Contains(jsonString(out), "mac (aa:bb:cc:dd:ee:ff)") {
		t.Errorf("list = %v", out)
	}
	if out := call(`{"name":"mac"}`); out["error"] != nil {
		t.Fatalf("wake = %v", out)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no packet received")
	}
	if out := call(`{"name":"nope"}`); out["error"] == nil {
		t.Errorf("unknown device accepted: %v", out)
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
