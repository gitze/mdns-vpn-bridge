package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mdns-vpn-bridge/internal/relay"
)

// writeConfig writes a JSON config file into a temp dir and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// resolveForTest parses args, then resolves the effective config the same way
// the run and check commands do.
func resolveForTest(t *testing.T, args []string) (relay.Config, error) {
	t.Helper()
	o, fs, code := parseRelayFlags(args, io.Discard)
	if code >= 0 {
		t.Fatalf("parseRelayFlags(%q) returned exit code %d, want success", args, code)
	}
	return effectiveConfig(o, fs)
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. Used to assert on the check command's success output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}

// TestLoadFileConfigStrict rejects unknown keys, trailing JSON, type mismatches,
// bad durations and an empty file: the loader is strict on purpose so a typo in
// a key never silently falls back to a default.
func TestLoadFileConfigStrict(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{"unknown field", `{"lan_if":"eth0","bogus":1}`, "bogus"},
		{"trailing object", `{"lan_if":"eth0"}{"peer":"203.0.113.5"}`, "trailing"},
		{"trailing garbage", `{"lan_if":"eth0"} nonsense`, "trailing"},
		{"type mismatch port", `{"peer_port":"53530"}`, "peer_port"},
		{"type mismatch bool", `{"verbose":"yes"}`, "verbose"},
		{"empty file", ``, "config"},
		{"array not object", `[]`, "config"},
		// The root must be a JSON object: a bare null, string or number is not
		// a config and must not silently decode to "all defaults".
		{"root null", `null`, "object"},
		{"root string", `"eth0"`, "object"},
		{"root number", `42`, "object"},
		{"root bool", `true`, "object"},
		// An explicit null field would silently keep the pointer nil and fall
		// back to the default; reject it so "null" never means "omitted".
		{"null field", `{"lan_if":null}`, "null"},
		{"null field second", `{"peer":"203.0.113.5","stats":null}`, "null"},
		// Keys are matched case-sensitively against the canonical schema: a
		// case-variant key would otherwise be silently accepted by
		// encoding/json's case-insensitive matching.
		{"case-variant key", `{"Peer":"203.0.113.5"}`, "Peer"},
		{"case-variant key mixed", `{"LAN_IF":"eth0"}`, "LAN_IF"},
		// A duplicate key is ambiguous; the last one would silently win.
		{"duplicate key", `{"peer":"203.0.113.5","peer":"198.51.100.9"}`, "duplicate"},
		{"duplicate key nonadjacent", `{"lan_if":"eth0","peer":"203.0.113.5","lan_if":"eth1"}`, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeConfig(t, tc.content)
			_, err := loadFileConfig(p)
			if err == nil {
				t.Fatalf("loadFileConfig(%s) = nil, want an error", tc.content)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadFileConfigRejectsOversize: a file larger than maxConfigBytes is
// rejected outright. Reading at most maxConfigBytes+1 is what makes the limit
// observable: a plain io.LimitReader(maxConfigBytes) silently truncates the
// stream at the boundary and can hide trailing data past it, so the loader must
// notice the file is over the limit instead of decoding a prefix.
func TestLoadFileConfigRejectsOversize(t *testing.T) {
	// A valid object followed by enough padding that the file is over the
	// limit; trailing whitespace is legal JSON, so only the size check rejects
	// this.
	pad := strings.Repeat(" ", maxConfigBytes)
	p := writeConfig(t, `{"lan_if":"eth0","peer":"203.0.113.5"}`+pad)
	_, err := loadFileConfig(p)
	if err == nil {
		t.Fatal("loadFileConfig accepted a file larger than the limit")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error %q does not mention the size limit", err)
	}
}

// TestLoadFileConfigAcceptsAtLimit: a file exactly at the limit is still read
// and decoded, so the boundary is inclusive and not off by one.
func TestLoadFileConfigAcceptsAtLimit(t *testing.T) {
	base := `{"lan_if":"eth0","peer":"203.0.113.5"}`
	if len(base) > maxConfigBytes {
		t.Fatalf("test setup: base object %d bytes already exceeds the limit", len(base))
	}
	p := writeConfig(t, base+strings.Repeat(" ", maxConfigBytes-len(base)))
	fc, err := loadFileConfig(p)
	if err != nil {
		t.Fatalf("loadFileConfig rejected a file exactly at the limit: %v", err)
	}
	if fc.LanIf == nil || *fc.LanIf != "eth0" {
		t.Fatalf("LanIf = %v", fc.LanIf)
	}
}

// TestFileSchemaTypeErrorNotMaskedByCLIOverride: a type error in the file is a
// schema error, not a value the CLI can override. A valid CLI override for
// another field must never make an ill-typed file acceptable, so check and run
// keep rejecting it.
func TestFileSchemaTypeErrorNotMaskedByCLIOverride(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"not-an-ip","peer_port":"53530"}`)
	_, err := resolveForTest(t, []string{"-config", p, "-peer", "203.0.113.5"})
	if err == nil {
		t.Fatal("resolve accepted a file with a type-invalid field despite a CLI override")
	}
	if !strings.Contains(err.Error(), "peer_port") {
		t.Fatalf("error %q does not name the offending field peer_port", err)
	}
}

// TestResolveRejectsBadDuration: a duration that is not a valid Go duration
// string is rejected while resolving, naming the field.
func TestResolveRejectsBadDuration(t *testing.T) {
	for _, tc := range []struct {
		name, content, wantSub string
	}{
		{"dedupe", `{"lan_if":"eth0","peer":"203.0.113.5","dedupe":"abc"}`, "dedupe"},
		{"stats", `{"lan_if":"eth0","peer":"203.0.113.5","stats":"10"}`, "stats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeConfig(t, tc.content)
			_, err := resolveForTest(t, []string{"-config", p})
			if err == nil {
				t.Fatalf("resolve accepted %s", tc.content)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadFileConfigOK parses a complete, valid file into the typed file struct.
func TestLoadFileConfigOK(t *testing.T) {
	p := writeConfig(t, `{
		"lan_if": "eth0",
		"peer": "203.0.113.5",
		"peer_port": 1234,
		"services": ["_http", "_ipp"],
		"dedupe": "5s",
		"stats": "0s",
		"verbose": true,
		"status_addr": "off"
	}`)
	fc, err := loadFileConfig(p)
	if err != nil {
		t.Fatalf("loadFileConfig: %v", err)
	}
	if fc.LanIf == nil || *fc.LanIf != "eth0" {
		t.Errorf("LanIf = %v", fc.LanIf)
	}
	if fc.PeerPort == nil || *fc.PeerPort != 1234 {
		t.Errorf("PeerPort = %v", fc.PeerPort)
	}
	if fc.Services == nil || len(*fc.Services) != 2 {
		t.Errorf("Services = %v", fc.Services)
	}
	if fc.Stats == nil || *fc.Stats != "0s" {
		t.Errorf("Stats = %v", fc.Stats)
	}
	if fc.Verbose == nil || !*fc.Verbose {
		t.Errorf("Verbose = %v", fc.Verbose)
	}
}

// TestDefaultRelayConfigMatchesFlagDefaults pins the base config that the
// no-config path uses to the flag defaults, so backwards compatibility is
// explicit.
func TestDefaultRelayConfigMatchesFlagDefaults(t *testing.T) {
	var o relayOptions
	_ = newRelayFlagSet(&o) // fills o with the flag defaults
	cfg := defaultRelayConfig(o)
	if cfg.PeerPort != 53530 || cfg.Lan2Peer != relay.ModeAll || cfg.Peer2LAN != relay.ModeAll ||
		cfg.DedupeTTL != 2*time.Second || cfg.Stats != 300*time.Second ||
		cfg.StatusAddr != defaultStatusAddr {
		t.Fatalf("defaultRelayConfig = %+v, want the flag defaults", cfg)
	}
}

// TestEffectiveConfigFileOmittedStatsUsesCLIDefault documents the deliberate
// split: through the CLI a file that omits stats keeps the CLI default (300s),
// while a programmatic Run with Stats==0 keeps stats disabled.
func TestEffectiveConfigFileOmittedStatsUsesCLIDefault(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"203.0.113.5"}`)
	cfg, err := resolveForTest(t, []string{"-config", p})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Stats != 300*time.Second {
		t.Fatalf("Stats = %v, want the CLI default 300s", cfg.Stats)
	}
	// A bare Run with Stats 0 must stay disabled.
	rc := relay.Config{LanIf: "eth0", Peer: "203.0.113.5"}
	rc.Normalize()
	if rc.Stats != 0 {
		t.Fatalf("Run base Stats = %v, want 0 (disabled)", rc.Stats)
	}
}

// TestEffectiveConfigFileStatsZeroDisables: an explicit "0s" in the file turns
// stats off, overriding the CLI default.
func TestEffectiveConfigFileStatsZeroDisables(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"203.0.113.5","stats":"0s"}`)
	cfg, err := resolveForTest(t, []string{"-config", p})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Stats != 0 {
		t.Fatalf("Stats = %v, want 0 (disabled)", cfg.Stats)
	}
}

// TestEffectiveConfigPrecedence: defaults < file < explicit CLI flags. It also
// covers explicit CLI false / empty overriding a file value (empty is only
// meaningful where a field has no default: services and link_bind; a disabled
// status endpoint is spelled "off", because an empty status_addr is normalized
// to the default like before).
func TestEffectiveConfigPrecedence(t *testing.T) {
	p := writeConfig(t, `{
		"lan_if": "eth0",
		"peer": "203.0.113.5",
		"lan2peer": "query",
		"link_bind": "198.51.100.7",
		"verbose": true,
		"stats": "300s",
		"services": ["_http"]
	}`)
	// Explicitly override peer, stats, verbose (false), services (empty) and
	// link_bind (empty).
	cfg, err := resolveForTest(t, []string{
		"-config", p,
		"-peer", "198.51.100.9",
		"-stats", "60s",
		"-v=false",
		"-services", "",
		"-linkbind", "",
		"-status", "off",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.LanIf != "eth0" {
		t.Errorf("LanIf = %q, want eth0 (from file)", cfg.LanIf)
	}
	if cfg.Peer != "198.51.100.9" {
		t.Errorf("Peer = %q, want 198.51.100.9 (CLI override)", cfg.Peer)
	}
	if cfg.Lan2Peer != relay.ModeQuery {
		t.Errorf("Lan2Peer = %q, want query (from file)", cfg.Lan2Peer)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("Services = %v, want empty (explicit CLI -services=)", cfg.Services)
	}
	if cfg.LinkBind != "" {
		t.Errorf("LinkBind = %q, want empty (explicit CLI -linkbind=)", cfg.LinkBind)
	}
	if cfg.Stats != 60*time.Second {
		t.Errorf("Stats = %v, want 60s (CLI override)", cfg.Stats)
	}
	if cfg.Verbose {
		t.Errorf("Verbose = true, want false (explicit CLI -v=false)")
	}
	if cfg.StatusAddr != "off" {
		t.Errorf("StatusAddr = %q, want off (explicit CLI -status=off)", cfg.StatusAddr)
	}
}

// TestEffectiveConfigFileOnlyUsesFileValues: with no CLI overrides the file
// values win over the flag defaults.
func TestEffectiveConfigFileOnlyUsesFileValues(t *testing.T) {
	p := writeConfig(t, `{
		"lan_if": "br-lan",
		"peer": "203.0.113.5",
		"peer_port": 1234,
		"link_bind": "198.51.100.7",
		"peer2lan": "query",
		"dedupe": "5s",
		"status_addr": "off"
	}`)
	cfg, err := resolveForTest(t, []string{"-config", p})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.LanIf != "br-lan" || cfg.Peer != "203.0.113.5" || cfg.PeerPort != 1234 ||
		cfg.LinkBind != "198.51.100.7" || cfg.Peer2LAN != relay.ModeQuery ||
		cfg.DedupeTTL != 5*time.Second || cfg.StatusAddr != "off" {
		t.Fatalf("resolved config = %+v", cfg)
	}
}

// TestCmdCheckValid: check validates a file without opening any socket and
// prints a success line.
func TestCmdCheckValid(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"203.0.113.5"}`)
	out := captureStdout(t, func() {
		if code := cmdCheck([]string{"-config", p}); code != 0 {
			t.Errorf("cmdCheck = %d, want 0", code)
		}
	})
	if !strings.Contains(out, "OK") {
		t.Errorf("check output missing OK: %q", out)
	}
}

// TestCmdCheckInvalid: an invalid effective config (bad peer) fails.
func TestCmdCheckInvalid(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"not-an-ip"}`)
	quietStderr(t)
	captureStdout(t, func() {
		if code := cmdCheck([]string{"-config", p}); code == 0 {
			t.Errorf("cmdCheck = 0, want non-zero for an invalid peer")
		}
	})
}

// TestCmdCheckMissingFile: a -config path that does not exist is an error.
func TestCmdCheckMissingFile(t *testing.T) {
	quietStderr(t)
	captureStdout(t, func() {
		if code := cmdCheck([]string{"-config", filepath.Join(t.TempDir(), "nope.json")}); code == 0 {
			t.Errorf("cmdCheck = 0, want non-zero for a missing file")
		}
	})
}

// TestCmdCheckCLIOverride: check accepts the usual CLI flags as overrides, so a
// file with a bad peer passes when the peer is fixed on the command line.
func TestCmdCheckCLIOverride(t *testing.T) {
	p := writeConfig(t, `{"lan_if":"eth0","peer":"not-an-ip"}`)
	out := captureStdout(t, func() {
		if code := cmdCheck([]string{"-config", p, "-peer", "203.0.113.5"}); code != 0 {
			t.Errorf("cmdCheck = %d, want 0 with a CLI peer override", code)
		}
	})
	if !strings.Contains(out, "OK") {
		t.Errorf("check output missing OK: %q", out)
	}
}

// TestRunRelayConfigFileMissing: runRelay must report a missing config file and
// not start the relay.
func TestRunRelayConfigFileMissing(t *testing.T) {
	quietStderr(t)
	if code := runRelay([]string{"-config", filepath.Join(t.TempDir(), "nope.json")}); code != 2 {
		t.Errorf("runRelay missing config = %d, want 2", code)
	}
}

// TestShippedSampleConfigDecodes guards the sample config shipped in the Debian
// tarball: it must stay strictly decodable (valid JSON, only known keys), so the
// documented template never drifts from the schema.
func TestShippedSampleConfigDecodes(t *testing.T) {
	p := filepath.Join("..", "..", "packaging", "debian", "config.json")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("sample config not present: %v", err)
	}
	fc, err := loadFileConfig(p)
	if err != nil {
		t.Fatalf("shipped sample config does not decode: %v", err)
	}
	if fc.LanIf == nil || fc.Peer == nil {
		t.Fatal("shipped sample config must document lan_if and peer")
	}
}
