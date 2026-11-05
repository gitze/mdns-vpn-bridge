package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"mdns-vpn-bridge/internal/relay"
)

// maxConfigBytes bounds how much of a config file is read, so a huge or hostile
// file cannot exhaust memory on a small router. The loader reads up to
// maxConfigBytes+1 bytes and rejects anything larger: a plain
// io.LimitReader(maxConfigBytes) would silently truncate the stream at the
// boundary and could hide trailing data past it.
const maxConfigBytes = 1 << 20 // 1 MiB

// fileConfig mirrors relay.Config as it appears in the JSON config file. Every
// field is a pointer so an omitted key is distinguishable from an explicit zero
// value: an omitted key keeps the lower-precedence value (the CLI flag
// default), while an explicit zero is honoured ("stats":"0s" disables stats,
// "dedupe":"0s" selects the 2s default, "verbose":false turns verbose off).
//
// The JSON keys are canonical and stable: lan_if, lan_ip, peer, peer_port,
// link_bind, lan2peer, peer2lan, services, dedupe, verbose, stats, status_addr.
// The loader matches keys case-sensitively against exactly this set, so an
// unknown or case-variant key is rejected instead of silently falling back to a
// default, and an explicit null is rejected too (see loadFileConfig).
type fileConfig struct {
	LanIf      *string   `json:"lan_if"`
	LanIP      *string   `json:"lan_ip"`
	Peer       *string   `json:"peer"`
	PeerPort   *int      `json:"peer_port"`
	LinkBind   *string   `json:"link_bind"`
	Lan2Peer   *string   `json:"lan2peer"`
	Peer2LAN   *string   `json:"peer2lan"`
	Services   *[]string `json:"services"`
	Dedupe     *string   `json:"dedupe"`
	Verbose    *bool     `json:"verbose"`
	Stats      *string   `json:"stats"`
	StatusAddr *string   `json:"status_addr"`
}

// canonicalConfigKeys is the exact, case-sensitive set of keys the config-file
// schema accepts. Matching against this set (rather than encoding/json's
// case-insensitive field matching) keeps the schema strictly canonical: a
// case-variant key such as "Peer" or "LAN_IF" is rejected like any other
// unknown key.
var canonicalConfigKeys = map[string]bool{
	"lan_if": true, "lan_ip": true, "peer": true, "peer_port": true,
	"link_bind": true, "lan2peer": true, "peer2lan": true, "services": true,
	"dedupe": true, "verbose": true, "stats": true, "status_addr": true,
}

// readConfigFile reads a config file, enforcing maxConfigBytes. It reads one
// byte past the limit so an oversized file is rejected rather than silently
// truncated at the boundary.
func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config %s: file is larger than the %d byte limit", path, maxConfigBytes)
	}
	return data, nil
}

// isJSONNull reports whether raw is the JSON literal null, ignoring surrounding
// whitespace.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// loadFileConfig reads and strictly decodes a JSON config file. It rejects:
//   - a file larger than maxConfigBytes (rather than truncating it),
//   - a root that is not an object (null, array, string, number or bool),
//   - an unknown or case-variant key (the schema is strictly canonical),
//   - a duplicate key,
//   - an explicit null field value (it would silently fall back to a default),
//   - a type mismatch, trailing data after the object, and an empty file.
//
// Every error names the offending file path.
func loadFileConfig(path string) (*fileConfig, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))

	// The root must be an object. A bare null decodes to a zero struct without
	// an error, and an array/scalar yields a generic unmarshal error; both are
	// rejected here with a clear message so "null" can never mean "all
	// defaults".
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if tok == nil {
		return nil, fmt.Errorf("config %s: root must be a JSON object, not null", path)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("config %s: root must be a JSON object", path)
	}

	var fc fileConfig
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		key, _ := keyTok.(string)
		if seen[key] {
			return nil, fmt.Errorf("config %s: duplicate field %q", path, key)
		}
		seen[key] = true
		if !canonicalConfigKeys[key] {
			return nil, fmt.Errorf("config %s: unknown field %q", path, key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("config %s: field %q: %w", path, key, err)
		}
		if isJSONNull(raw) {
			return nil, fmt.Errorf("config %s: field %q must not be null", path, key)
		}
		if key == "services" {
			var entries []json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("config %s: field services: %w", path, err)
			}
			for _, entry := range entries {
				if isJSONNull(entry) {
					return nil, fmt.Errorf("config %s: services entries must be strings, not null", path)
				}
			}
		}
		if err := fc.setField(key, raw); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}
	// Consume the object's closing '}'.
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	// A second value after the top-level object (a second object, trailing
	// garbage) is rejected rather than silently ignored.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("config %s: trailing data after the JSON object", path)
	}
	return &fc, nil
}

// setField decodes one object member's raw value into the matching fileConfig
// field. The key is already known to be canonical. A type mismatch is reported
// against the field name so the caller can name it, and is never masked by a
// CLI override for another field.
func (fc *fileConfig) setField(key string, raw json.RawMessage) error {
	var target any
	switch key {
	case "lan_if":
		target = &fc.LanIf
	case "lan_ip":
		target = &fc.LanIP
	case "peer":
		target = &fc.Peer
	case "peer_port":
		target = &fc.PeerPort
	case "link_bind":
		target = &fc.LinkBind
	case "lan2peer":
		target = &fc.Lan2Peer
	case "peer2lan":
		target = &fc.Peer2LAN
	case "services":
		target = &fc.Services
	case "dedupe":
		target = &fc.Dedupe
	case "verbose":
		target = &fc.Verbose
	case "stats":
		target = &fc.Stats
	case "status_addr":
		target = &fc.StatusAddr
	default:
		return fmt.Errorf("unknown field %q", key)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("field %q: %w", key, err)
	}
	return nil
}

// parseDurationField parses a duration string for one named field, so a bad
// value names the field instead of returning a bare ParseDuration error.
func parseDurationField(field, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", field, value, err)
	}
	return d, nil
}

// apply copies the fields that are present in the file onto cfg, leaving every
// omitted field untouched. It does not normalize; the caller does that once at
// the end so the defaults and the validation are applied exactly once.
func (fc *fileConfig) apply(cfg *relay.Config) error {
	if fc.LanIf != nil {
		cfg.LanIf = *fc.LanIf
	}
	if fc.LanIP != nil {
		cfg.LanIP = *fc.LanIP
	}
	if fc.Peer != nil {
		cfg.Peer = *fc.Peer
	}
	if fc.PeerPort != nil {
		cfg.PeerPort = *fc.PeerPort
	}
	if fc.LinkBind != nil {
		cfg.LinkBind = *fc.LinkBind
	}
	if fc.Lan2Peer != nil {
		cfg.Lan2Peer = relay.Mode(*fc.Lan2Peer)
	}
	if fc.Peer2LAN != nil {
		cfg.Peer2LAN = relay.Mode(*fc.Peer2LAN)
	}
	if fc.Services != nil {
		cfg.Services = *fc.Services
	}
	if fc.Dedupe != nil {
		d, err := parseDurationField("dedupe", *fc.Dedupe)
		if err != nil {
			return err
		}
		cfg.DedupeTTL = d
	}
	if fc.Verbose != nil {
		cfg.Verbose = *fc.Verbose
	}
	if fc.Stats != nil {
		d, err := parseDurationField("stats", *fc.Stats)
		if err != nil {
			return err
		}
		cfg.Stats = d
	}
	if fc.StatusAddr != nil {
		cfg.StatusAddr = *fc.StatusAddr
	}
	return nil
}

// defaultRelayConfig turns the parsed flag defaults into the base relay.Config.
// Because newRelayFlagSet seeds the options with their flag defaults, this is
// the "CLI default" layer: a config file overrides it, and an explicitly set
// flag overrides the file.
func defaultRelayConfig(o relayOptions) relay.Config {
	return relay.Config{
		LanIf:      o.lanIf,
		LanIP:      o.lanIP,
		Peer:       o.peer,
		PeerPort:   o.peerPort,
		LinkBind:   o.linkBind,
		Lan2Peer:   relay.Mode(o.lan2peer),
		Peer2LAN:   relay.Mode(o.peer2lan),
		Services:   relay.ParseServices(o.services),
		DedupeTTL:  o.dedupe,
		Verbose:    o.verbose,
		Stats:      o.stats,
		StatusAddr: o.status,
	}
}

// applyVisitedFlags copies onto cfg only the flags the user actually set on the
// command line (fs.Visit reports exactly those), so an explicitly given value —
// including false, 0 and the empty string — overrides the config file while an
// untouched flag leaves the file's value in place.
func applyVisitedFlags(fs *flag.FlagSet, o *relayOptions, cfg *relay.Config) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "lan":
			cfg.LanIf = o.lanIf
		case "lanip":
			cfg.LanIP = o.lanIP
		case "peer":
			cfg.Peer = o.peer
		case "peerport":
			cfg.PeerPort = o.peerPort
		case "linkbind":
			cfg.LinkBind = o.linkBind
		case "lan2peer":
			cfg.Lan2Peer = relay.Mode(o.lan2peer)
		case "peer2lan":
			cfg.Peer2LAN = relay.Mode(o.peer2lan)
		case "services":
			cfg.Services = relay.ParseServices(o.services)
		case "dedupe":
			cfg.DedupeTTL = o.dedupe
		case "v":
			cfg.Verbose = o.verbose
		case "stats":
			cfg.Stats = o.stats
		case "status":
			cfg.StatusAddr = o.status
		case "config":
			// -config selects the file; it is not a relay field.
		}
	})
}

// effectiveConfig resolves the effective relay.Config from the flag defaults,
// the optional config file and the explicitly set flags, in that order, then
// normalizes. It does not validate; callers run cfg.Validate so the check
// command and Run share the exact same verdict.
func effectiveConfig(o relayOptions, fs *flag.FlagSet) (relay.Config, error) {
	cfg := defaultRelayConfig(o)
	if o.config != "" {
		fc, err := loadFileConfig(o.config)
		if err != nil {
			return cfg, err
		}
		if err := fc.apply(&cfg); err != nil {
			return cfg, err
		}
	}
	applyVisitedFlags(fs, &o, &cfg)
	cfg.Normalize()
	return cfg, nil
}

// parseRelayFlags parses the relay/check flags and enforces that no positional
// arguments are given. It returns an exit code >= 0 when it has already handled
// the invocation (0 for -h, 2 for a usage error) and -1 when the caller should
// continue.
func parseRelayFlags(args []string, errW io.Writer) (relayOptions, *flag.FlagSet, int) {
	var o relayOptions
	fs := newRelayFlagSet(&o)
	fs.SetOutput(errW)
	fs.Usage = func() { printUsage(errW, fs) }

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return o, fs, 0
		}
		// flag has already printed the error and the usage text.
		return o, fs, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errW, "mdns-vpn-bridge: unexpected argument %q; the relay takes flags only (see -h)\n", fs.Arg(0))
		fs.Usage()
		return o, fs, 2
	}
	return o, fs, -1
}

// cmdCheck validates the effective configuration (config file plus explicit CLI
// overrides) and prints the result. It opens no socket and starts no daemon:
// it only runs the shared pure validation, so it is safe to call on a router
// before a service restart.
func cmdCheck(args []string) int {
	o, fs, code := parseRelayFlags(args, os.Stderr)
	if code >= 0 {
		return code
	}
	cfg, err := effectiveConfig(o, fs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge check: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge check: %v\n", err)
		return 1
	}
	src := "flags"
	if o.config != "" {
		src = o.config
	}
	svc := "-"
	if len(cfg.Services) > 0 {
		svc = fmt.Sprint(cfg.Services)
	}
	fmt.Fprintf(os.Stdout, "mdns-vpn-bridge check: configuration OK (%s)\n", src)
	fmt.Fprintf(os.Stdout, "  lan=%s peer=%s:%d lan2peer=%s peer2lan=%s services=%s dedupe=%s stats=%s status=%s\n",
		cfg.LanIf, cfg.Peer, cfg.PeerPort, cfg.Lan2Peer, cfg.Peer2LAN, svc,
		cfg.DedupeTTL, cfg.Stats, cfg.StatusAddr)
	return 0
}
