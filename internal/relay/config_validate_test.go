package relay

import (
	"context"
	"strings"
	"testing"
	"time"
)

// validBase is a minimal, fully valid configuration used as the starting point
// for the table-driven cases below. It uses documentation addresses
// (TEST-NET-1/2/3) so no real deployment address leaks into the tests.
func validBase() Config {
	return Config{LanIf: "eth0", Peer: "203.0.113.5"}
}

// TestNormalizeDefaults checks that Normalize fills every unset field with the
// documented default and leaves explicit values alone.
func TestNormalizeDefaults(t *testing.T) {
	c := Config{}
	c.Normalize()
	if c.PeerPort != 53530 {
		t.Errorf("PeerPort = %d, want 53530", c.PeerPort)
	}
	if c.Lan2Peer != ModeAll {
		t.Errorf("Lan2Peer = %q, want %q", c.Lan2Peer, ModeAll)
	}
	if c.Peer2LAN != ModeAll {
		t.Errorf("Peer2LAN = %q, want %q", c.Peer2LAN, ModeAll)
	}
	if c.DedupeTTL != 2*time.Second {
		t.Errorf("DedupeTTL = %v, want 2s", c.DedupeTTL)
	}
	if c.StatusAddr != "127.0.0.1:53531" {
		t.Errorf("StatusAddr = %q, want 127.0.0.1:53531", c.StatusAddr)
	}
	if c.Logf == nil {
		t.Error("Logf must be defaulted")
	}
	// Stats keeps its zero value: 0 means "disabled" and is not a default.
	if c.Stats != 0 {
		t.Errorf("Stats = %v, want 0 (disabled, no default)", c.Stats)
	}

	// Explicit values survive Normalize.
	c2 := Config{PeerPort: 1234, Lan2Peer: ModeQuery, Peer2LAN: ModeQuery, DedupeTTL: 5 * time.Second, StatusAddr: "off"}
	c2.Normalize()
	if c2.PeerPort != 1234 || c2.Lan2Peer != ModeQuery || c2.Peer2LAN != ModeQuery ||
		c2.DedupeTTL != 5*time.Second || c2.StatusAddr != "off" {
		t.Errorf("Normalize clobbered explicit values: %+v", c2)
	}
}

// TestValidateValid covers configurations that must pass validation. Validate is
// pure, so none of these touch the network.
func TestValidateValid(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
	}{
		{"minimal", func(c *Config) {}},
		{"peer port 1", func(c *Config) { c.PeerPort = 1 }},
		{"peer port 65535", func(c *Config) { c.PeerPort = 65535 }},
		{"lanip set", func(c *Config) { c.LanIP = "192.0.2.10" }},
		{"linkbind set", func(c *Config) { c.LinkBind = "198.51.100.7" }},
		{"lan2peer query", func(c *Config) { c.Lan2Peer = ModeQuery }},
		{"peer2lan query", func(c *Config) { c.Peer2LAN = ModeQuery }},
		{"dedupe zero", func(c *Config) { c.DedupeTTL = 0 }},
		{"stats zero", func(c *Config) { c.Stats = 0 }},
		{"services dotted", func(c *Config) { c.Services = []string{"_http._tcp", "_ipp"} }},
		{"services trailing dot", func(c *Config) { c.Services = []string{"_http._tcp."} }},
		{"services repeated dots", func(c *Config) { c.Services = []string{"_http.._tcp"} }},
		// A non-ASCII label is not restricted: the allowlist only has to avoid
		// whitespace and an all-dots (label-less) entry, nothing more.
		{"services unicode label", func(c *Config) { c.Services = []string{"_hëllo"} }},
		{"status off", func(c *Config) { c.StatusAddr = "off" }},
		{"status empty", func(c *Config) { c.StatusAddr = "" }},
		{"status localhost", func(c *Config) { c.StatusAddr = "localhost:53531" }},
		{"status wildcard host", func(c *Config) { c.StatusAddr = ":53531" }},
		{"status ip", func(c *Config) { c.StatusAddr = "127.0.0.1:53531" }},
		{"vlan ifname", func(c *Config) { c.LanIf = "eth0.10" }},
		{"alias ifname", func(c *Config) { c.LanIf = "br-lan" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			c.Normalize()
			tc.mut(&c)
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestValidateInvalid covers the rejections. Each case mutates the valid base.
func TestValidateInvalid(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Config)
		wantSub string
	}{
		{"missing lan", func(c *Config) { c.LanIf = "" }, "required"},
		{"missing peer", func(c *Config) { c.Peer = "" }, "required"},
		{"peer dns name", func(c *Config) { c.Peer = "peer.example.com" }, "IPv4"},
		{"peer ipv6", func(c *Config) { c.Peer = "fd00::1" }, "IPv4"},
		{"peer empty label", func(c *Config) { c.Peer = "10.0.0" }, "IPv4"},
		{"lanip invalid", func(c *Config) { c.LanIP = "not-an-ip" }, "lanip"},
		{"lanip ipv6", func(c *Config) { c.LanIP = "fd00::1" }, "lanip"},
		{"linkbind invalid", func(c *Config) { c.LinkBind = "host.local" }, "linkbind"},
		{"peer port zero is defaulted not rejected", func(c *Config) { c.PeerPort = 0 }, ""}, // placeholder, replaced below
		{"peer port negative", func(c *Config) { c.PeerPort = -1 }, "port"},
		{"peer port too big", func(c *Config) { c.PeerPort = 70000 }, "port"},
		{"lan2peer invalid", func(c *Config) { c.Lan2Peer = "sometimes" }, "mode"},
		{"peer2lan invalid", func(c *Config) { c.Peer2LAN = "sometimes" }, "mode"},
		{"dedupe negative", func(c *Config) { c.DedupeTTL = -time.Second }, "dedupe"},
		{"stats negative", func(c *Config) { c.Stats = -time.Second }, "stats"},
		{"status no port", func(c *Config) { c.StatusAddr = "127.0.0.1" }, "status_addr"},
		{"status bad port", func(c *Config) { c.StatusAddr = "127.0.0.1:99999" }, "port"},
		{"status hostname", func(c *Config) { c.StatusAddr = "example.com:53531" }, "status_addr"},
		{"ifname space", func(c *Config) { c.LanIf = "eth 0" }, "interface"},
		{"ifname slash", func(c *Config) { c.LanIf = "eth/0" }, "interface"},
		{"ifname leading dash", func(c *Config) { c.LanIf = "-eth0" }, "interface"},
		{"ifname dot", func(c *Config) { c.LanIf = "." }, "interface"},
		{"ifname dotdot", func(c *Config) { c.LanIf = ".." }, "interface"},
		{"services empty entry", func(c *Config) { c.Services = []string{""} }, "services"},
		{"services whitespace entry", func(c *Config) { c.Services = []string{"_http _ipp"} }, "services"},
		// Whitespace must be rejected on the *original* string, not after a
		// TrimSpace: " _http " would trim clean but the matcher compares the raw
		// entry against a DNS label, so it could never match.
		{"services leading space", func(c *Config) { c.Services = []string{" _http"} }, "services"},
		{"services trailing space", func(c *Config) { c.Services = []string{"_http "} }, "services"},
		{"services padded", func(c *Config) { c.Services = []string{" _http "} }, "services"},
		{"services inner space", func(c *Config) { c.Services = []string{"_ht tp"} }, "services"},
		{"services tab", func(c *Config) { c.Services = []string{"_http	_ipp"} }, "services"},
		{"services newline", func(c *Config) { c.Services = []string{"_http\n"} }, "services"},
		// A dots-only entry splits into empty labels: it can never match a real
		// service name and is a meaningless (usually typo'd) entry.
		{"services single dot", func(c *Config) { c.Services = []string{"."} }, "services"},
		{"services dots only", func(c *Config) { c.Services = []string{"..."} }, "services"},
	}
	for _, tc := range tests {
		if tc.wantSub == "" {
			continue // skip the placeholder row
		}
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			c.Normalize()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate() = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestValidatePeerPortZeroBecomesDefault documents the "explicit zero means
// default" rule for the port: after Normalize a zero port is the default, so it
// is valid.
func TestValidatePeerPortZeroBecomesDefault(t *testing.T) {
	c := validBase()
	c.PeerPort = 0
	c.Normalize()
	if c.PeerPort != 53530 {
		t.Fatalf("Normalize did not default the port: %d", c.PeerPort)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestRunUsesSharedValidation proves Run validates through the exact same
// Normalize+Validate boundary the CLI and the config loader use: for a config
// that fails validation, Run must return the identical error message. Every
// case fails validation, so Run returns before it ever opens a socket or looks
// up an interface (pure path, no network needed).
func TestRunUsesSharedValidation(t *testing.T) {
	cases := []Config{
		{},
		{LanIf: "eth0"},
		{LanIf: "eth0", Peer: "nope"},
		{LanIf: "eth0", Peer: "203.0.113.5", Lan2Peer: "sometimes"},
		{LanIf: "eth0", Peer: "203.0.113.5", DedupeTTL: -time.Second},
		{LanIf: "eth0", Peer: "203.0.113.5", PeerPort: 70000},
	}
	for i, base := range cases {
		want := base
		want.Normalize()
		wantErr := want.Validate()
		if wantErr == nil {
			t.Fatalf("case %d: Validate() = nil, the test case is not a rejection", i)
		}
		got := base
		err := Run(context.Background(), got)
		if err == nil {
			t.Fatalf("case %d: Run() = nil, want %v", i, wantErr)
		}
		if err.Error() != wantErr.Error() {
			t.Fatalf("case %d: Run() = %q, Validate() = %q; Run must use the shared boundary",
				i, err, wantErr)
		}
	}
}
