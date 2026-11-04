package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
)

// WakeDevice is a machine on the LAN that can be woken with a Wake-on-LAN
// magic packet, e.g. the Mac while this server runs on an always-on PC.
type WakeDevice struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	// Broadcast is an optional "host[:port]" to send to, such as the LAN's
	// directed broadcast "192.168.1.255". By default the packet goes to
	// 255.255.255.255 and to the broadcast address of every LAN interface.
	Broadcast string `json:"broadcast,omitempty"`
}

const wolPort = "9"

// magicPacket is 6 × 0xFF followed by the MAC address 16 times.
func magicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("invalid MAC address %q", mac)
	}
	pkt := bytes.Repeat([]byte{0xFF}, 6)
	for i := 0; i < 16; i++ {
		pkt = append(pkt, hw...)
	}
	return pkt, nil
}

// wakeTargets lists where the packet is sent. 255.255.255.255 only leaves
// through the default route's interface; a PC with Docker, Hyper-V or VPN
// adapters may pick the wrong one, so every interface's own broadcast
// address gets a copy too.
func wakeTargets(d WakeDevice) []string {
	if d.Broadcast != "" {
		if _, _, err := net.SplitHostPort(d.Broadcast); err == nil {
			return []string{d.Broadcast}
		}
		return []string{net.JoinHostPort(d.Broadcast, wolPort)}
	}
	targets := []string{net.JoinHostPort("255.255.255.255", wolPort)}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || len(ipn.Mask) != net.IPv4len {
				continue
			}
			bc := make(net.IP, net.IPv4len)
			for i := range ip4 {
				bc[i] = ip4[i] | ^ipn.Mask[i]
			}
			targets = append(targets, net.JoinHostPort(bc.String(), wolPort))
		}
	}
	return targets
}

// sendWake broadcasts the magic packet for d. It succeeds if at least one
// copy went out: Wake-on-LAN has no reply, so whether the machine wakes
// cannot be known here.
func sendWake(d WakeDevice) error {
	pkt, err := magicPacket(d.MAC)
	if err != nil {
		return err
	}
	var sent int
	var lastErr error
	for _, target := range wakeTargets(d) {
		addr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			lastErr = err
			continue
		}
		// Go enables SO_BROADCAST on UDP sockets.
		conn, err := net.DialUDP("udp4", nil, addr)
		if err != nil {
			lastErr = err
			continue
		}
		// Sleeping NICs miss packets now and then; a few copies are cheap.
		for i := 0; i < 3; i++ {
			if _, err := conn.Write(pkt); err == nil {
				sent++
			} else {
				lastErr = err
			}
		}
		conn.Close()
	}
	if sent == 0 {
		return fmt.Errorf("magic packet not sent: %w", lastErr)
	}
	return nil
}

// findWakeDevice matches a configured device by name (case-insensitive) or
// MAC address.
func findWakeDevice(devices []WakeDevice, nameOrMAC string) (WakeDevice, bool) {
	want, macErr := net.ParseMAC(strings.TrimSpace(nameOrMAC))
	for _, d := range devices {
		if strings.EqualFold(d.Name, strings.TrimSpace(nameOrMAC)) {
			return d, true
		}
		if macErr == nil {
			if hw, err := net.ParseMAC(d.MAC); err == nil && bytes.Equal(hw, want) {
				return d, true
			}
		}
	}
	return WakeDevice{}, false
}

// handleWakeLAN lists the configured devices (GET) or wakes one (POST
// {"name"}). Only configured devices can be woken from the phone, so the
// page cannot be used to broadcast arbitrary packets onto the LAN.
func (s *Server) handleWakeLAN(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		type item struct {
			Name string `json:"name"`
			MAC  string `json:"mac"`
		}
		list := []item{}
		for _, d := range s.config.Wake {
			list = append(list, item{d.Name, d.MAC})
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": list})
		return
	}
	if !jsonPost(w, r) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	d, ok := findWakeDevice(s.config.Wake, body.Name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown device")
		return
	}
	if err := sendWake(d); err != nil {
		log.Printf("wake %s: %v", d.Name, err)
		writeJSONError(w, http.StatusInternalServerError, "could not send the wake packet")
		return
	}
	log.Printf("wake-on-lan %s (%s) from %s", d.Name, d.MAC, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": d.Name})
}
