package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		Version: "test",
		LanIf:   "ens18", LanIP: "192.0.2.5", Peer: "198.51.100.2", PeerPort: 53530,
		Lan2Peer: relay.ModeQuery, Peer2LAN: relay.ModeAll,
		UptimeSeconds: 3600,
		ToPeer:        relay.Counter{Packets: 10, Bytes: 2048, Dropped: 1},
		ToLAN:         relay.Counter{Packets: 2, Bytes: 100},
		Dedupe:        7,
		Errors:        3,
	})

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
		"dedupe     7",
		"errors     3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// quietStderr silences the usage/error output runRelay writes so a failing
// assertion is readable; the return code is what matters here.
func quietStderr(t *testing.T) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = w.Close()
		_, _ = io.Copy(io.Discard, r)
	})
}

// runRelay must reject positional arguments instead of silently ignoring them.
func TestRunRelayRejectsPositionals(t *testing.T) {
	quietStderr(t)
	for _, args := range [][]string{
		{"extra"},
		{"-lan", "eth0", "-peer", "100.64.0.1", "extra"},
		{"-peer", "100.64.0.1", "stray", "words"},
	} {
		if got := runRelay(args); got != 2 {
			t.Errorf("runRelay(%q) = %d, want 2", args, got)
		}
	}
}

// Missing required flags and unknown flags must fail with exit code 2, before
// the relay ever starts (so the test cannot block on Run).
func TestRunRelayRejectsBadInvocation(t *testing.T) {
	quietStderr(t)
	for _, args := range [][]string{
		nil,
		{"-lan", "eth0"},
		{"-peer", "100.64.0.1"},
		{"-nope"},
	} {
		if got := runRelay(args); got != 2 {
			t.Errorf("runRelay(%q) = %d, want 2", args, got)
		}
	}
}

// -h prints usage and exits successfully (flag.ErrHelp).
func TestRunRelayHelp(t *testing.T) {
	quietStderr(t)
	if got := runRelay([]string{"-h"}); got != 0 {
		t.Errorf("runRelay(-h) = %d, want 0", got)
	}
}

// The -linkbind flag parses into LinkBind and defaults to empty (0.0.0.0).
func TestRelayFlagSetLinkBind(t *testing.T) {
	var o relayOptions
	fs := newRelayFlagSet(&o)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse nil: %v", err)
	}
	if o.linkBind != "" {
		t.Fatalf("default linkbind = %q, want empty", o.linkBind)
	}

	o = relayOptions{}
	fs = newRelayFlagSet(&o)
	if err := fs.Parse([]string{"-linkbind", "100.64.0.1"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.linkBind != "100.64.0.1" {
		t.Fatalf("linkbind = %q, want 100.64.0.1", o.linkBind)
	}
}

// cmdStatus must reject positional arguments like the relay mode does, instead
// of silently ignoring them (R7). The check runs before any network call, so
// this is deterministic and needs no running instance.
func TestCmdStatusRejectsPositionals(t *testing.T) {
	quietStderr(t)
	for _, args := range [][]string{
		{"extra"},
		{"-json", "stray"},
		{"-addr", "127.0.0.1:53531", "extra", "words"},
	} {
		if got := cmdStatus(args); got != 2 {
			t.Errorf("cmdStatus(%q) = %d, want 2", args, got)
		}
	}
}

// An unknown status flag is a usage error (exit code 2), not an os.Exit inside
// the flag package (R7). ContinueOnError makes this observable in a test.
func TestCmdStatusRejectsUnknownFlag(t *testing.T) {
	quietStderr(t)
	if got := cmdStatus([]string{"-nope"}); got != 2 {
		t.Errorf("cmdStatus(-nope) = %d, want 2", got)
	}
}

// -h prints usage and returns success without touching the network.
func TestCmdStatusHelp(t *testing.T) {
	quietStderr(t)
	if got := cmdStatus([]string{"-h"}); got != 0 {
		t.Errorf("cmdStatus(-h) = %d, want 0", got)
	}
}

// A negative -dedupe is rejected with a usage error (R5); 0 is documented as
// "the 2s default", not "off".
func TestRunRelayRejectsNegativeDedupe(t *testing.T) {
	quietStderr(t)
	if got := runRelay([]string{"-lan", "eth0", "-peer", "100.64.0.1", "-dedupe", "-1s"}); got != 2 {
		t.Errorf("runRelay(-dedupe -1s) = %d, want 2", got)
	}
}

// The human-readable status must show the version the running instance reports
// on /_status, never the local client's build version, and must say "Unknown"
// when the server omits the field. This drives the real HTTP path in cmdStatus
// against an httptest endpoint whose version deliberately differs from the
// client's (captureStdout is shared with configfile_test.go).
func TestCmdStatusShowsServerVersion(t *testing.T) {
	oldVersion := version
	version = "client-local-9999"
	t.Cleanup(func() { version = oldVersion })

	cases := []struct {
		name string
		body string
		want string
	}{
		{"server version", `{"version":"9.8.7","lan_if":"eth0","lan_ip":"192.0.2.1","peer":"198.51.100.1"}`, "mdns-vpn-bridge 9.8.7"},
		{"missing version", `{"lan_if":"eth0","lan_ip":"192.0.2.1","peer":"198.51.100.1"}`, "mdns-vpn-bridge Unknown"},
		{"empty version", `{"version":"","lan_if":"eth0","lan_ip":"192.0.2.1","peer":"198.51.100.1"}`, "mdns-vpn-bridge Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_status" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			addr := strings.TrimPrefix(srv.URL, "http://")
			out := captureStdout(t, func() {
				if got := cmdStatus([]string{"-addr", addr}); got != 0 {
					t.Fatalf("cmdStatus = %d, want 0", got)
				}
			})
			if !strings.Contains(out, tc.want) {
				t.Errorf("output missing %q\n---\n%s", tc.want, out)
			}
			if strings.Contains(out, "client-local-9999") {
				t.Errorf("output leaked the local client version\n---\n%s", out)
			}
		})
	}
}
