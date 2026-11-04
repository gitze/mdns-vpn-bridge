package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"mdns-vpn-bridge/internal/relay"
)

func TestFormatUptime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                           "0s",
		45 * time.Second:            "45s",
		90 * time.Second:            "1m30s",
		2*time.Hour + 5*time.Minute: "2h05m",
	} {
		if got := formatUptime(d); got != want {
			t.Errorf("formatUptime(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[uint64]string{
		0:       "0 B",
		999:     "999 B",
		2000:    "2.0 kB",
		1500000: "1.5 MB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintStatus(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, relay.Status{
		LanIf: "ens18", LanIP: "192.0.2.5", Peer: "198.51.100.2", PeerPort: 53530,
		Lan2Peer: relay.ModeQuery, Peer2LAN: relay.ModeAll,
		UptimeSeconds: 3600,
		ToPeer:        relay.Counter{Packets: 10, Bytes: 2048, Dropped: 1},
		ToLAN:         relay.Counter{Packets: 2, Bytes: 100},
	}, "test")

	out := b.String()
	for _, want := range []string{
		"mdns-vpn-bridge test",
		"lan        ens18 (192.0.2.5)",
		"peer       198.51.100.2:53530",
		"filter     lan2peer=query peer2lan=all",
		"services   -",
		"uptime     1h00m",
		"-> peer    10 packets / 2.0 kB (dropped 1)",
		"-> lan     2 packets / 100 B (dropped 0)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}
