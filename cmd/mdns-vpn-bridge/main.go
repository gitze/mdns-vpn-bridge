// Command mdns-vpn-bridge relays mDNS/Bonjour discovery between two LANs over
// a unicast-only VPN link (Tailscale, WireGuard, ZeroTier, ...). Run one
// instance on each side; both instances are symmetric.
//
//	mdns-vpn-bridge -lan ens18 -peer 100.x.y.z   # run the relay
//	mdns-vpn-bridge status [--json]              # query a running instance
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mdns-vpn-bridge/internal/relay"
)

// version and commit are set at build time via
// -ldflags "-X main.version=... -X main.commit=...". A release build pins
// version to the release number and commit to the source revision it was built
// from, instead of a git-describe string that can read like a bare commit hash.
var (
	version = "dev"
	commit  = ""
)

const defaultStatusAddr = "127.0.0.1:53531"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status":
			os.Exit(cmdStatus(os.Args[2:]))
		case "check":
			os.Exit(cmdCheck(os.Args[2:]))
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			var o relayOptions
			printUsage(os.Stderr, newRelayFlagSet(&o))
			return
		}
	}
	os.Exit(runRelay(os.Args[1:]))
}

// relayOptions holds the parsed relay flags.
type relayOptions struct {
	config   string
	lanIf    string
	lanIP    string
	peer     string
	peerPort int
	linkBind string
	lan2peer string
	peer2lan string
	services string
	dedupe   time.Duration
	verbose  bool
	stats    time.Duration
	status   string
}

// newRelayFlagSet defines the relay flags on a fresh FlagSet. Keeping the flags
// in their own set (instead of the global flag.CommandLine) makes argument
// handling testable and avoids cross-talk with the status subcommand.
func newRelayFlagSet(o *relayOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("mdns-vpn-bridge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.config, "config", "", "load settings from a JSON config file (e.g. /etc/mdns-vpn-bridge/config.json); explicit CLI flags override file values")
	fs.StringVar(&o.lanIf, "lan", "", "LAN interface carrying mDNS (e.g. ens18, br-lan)")
	fs.StringVar(&o.lanIP, "lanip", "", "override the LAN IPv4 (default: taken from -lan)")
	fs.StringVar(&o.peer, "peer", "", "peer unicast address on the overlay (Tailscale/WireGuard IPv4)")
	fs.IntVar(&o.peerPort, "peerport", 53530, "UDP port of the unicast link (both sides)")
	fs.StringVar(&o.linkBind, "linkbind", "", "local IPv4 the peer link socket binds to (default: 0.0.0.0 = every interface)")
	fs.StringVar(&o.lan2peer, "lan2peer", "all", "LAN -> peer: \"query\" (DNS queries only) or \"all\"")
	fs.StringVar(&o.peer2lan, "peer2lan", "all", "peer -> LAN: \"all\" or \"query\"")
	fs.StringVar(&o.services, "services", "", "comma-separated allowlist of service types for LAN -> peer (e.g. _http,_esphomelib); empty = all")
	fs.DurationVar(&o.dedupe, "dedupe", 2*time.Second, "loop-suppression window for identical packets; 0 selects the 2s default, negative is rejected")
	fs.BoolVar(&o.verbose, "v", false, "log every forwarded packet")
	fs.DurationVar(&o.stats, "stats", 300*time.Second, "stats log interval (0 = off)")
	fs.StringVar(&o.status, "status", defaultStatusAddr, "HTTP status endpoint listen address (\"off\" disables it)")
	return fs
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, `mdns-vpn-bridge %s — mDNS/Bonjour between two LANs over a unicast VPN link

usage:
  mdns-vpn-bridge -lan <iface> -peer <overlay-ip> [options]   run the relay
  mdns-vpn-bridge -config <file.json>                         run from a config file
  mdns-vpn-bridge check -config <file.json> [options]         validate config, no daemon
  mdns-vpn-bridge status [-addr <host:port>] [-json]          query a running instance
  mdns-vpn-bridge version

Typical asymmetric setup across a metered link:
  chatty side:  -lan2peer query -peer2lan all
  quiet side:   -lan2peer all   -peer2lan all

A JSON config file (see -config, e.g. /etc/mdns-vpn-bridge/config.json) sets the
same fields; explicit CLI flags override file values. Use "check" to validate a
file offline.

relay options:
`, versionLabel())
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// versionLabel returns the version with the source revision appended when a
// build embedded one, e.g. "0.5.1 (<short-commit>)". The plain "version" subcommand
// still prints just the version number so scripts can parse it.
func versionLabel() string {
	if commit != "" {
		return version + " (" + commit + ")"
	}
	return version
}

func runRelay(args []string) int {
	o, fs, code := parseRelayFlags(args, os.Stderr)
	if code >= 0 {
		return code
	}
	// Resolve and validate exactly as the check command does: same normalization,
	// same pure validation. Only the relay command proceeds to Run.
	cfg, err := effectiveConfig(o, fs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: %v\n", err)
		return 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: %v\n", err)
		if cfg.LanIf == "" || cfg.Peer == "" {
			printUsage(os.Stderr, fs)
		}
		return 2
	}

	// Report this process's embedded version, never an on-disk replacement binary.
	cfg.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.Run(ctx, cfg); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// cmdStatus queries a running instance and prints its state. It returns a
// process exit code and never calls os.Exit itself, so its argument handling is
// testable. Like the relay mode it rejects positional arguments instead of
// silently ignoring them (R7).
func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", defaultStatusAddr, "status endpoint of the running instance")
	asJSON := fs.Bool("json", false, "print raw JSON")
	timeout := fs.Duration("timeout", 3*time.Second, "request timeout")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: mdns-vpn-bridge status [-addr <host:port>] [-json] [-timeout <dur>]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		// flag has already printed the error and the usage text.
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge status: unexpected argument %q; the status command takes flags only (see -h)\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	if !strings.Contains(*addr, ":") {
		*addr += ":53531"
	}
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get("http://" + *addr + "/_status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: no running instance at %s: %v\n", *addr, err)
		return 1
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: read: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: status %s\n", resp.Status)
		return 1
	}

	if *asJSON {
		os.Stdout.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			fmt.Println()
		}
		return 0
	}

	var st relay.Status
	if err := json.Unmarshal(body, &st); err != nil {
		fmt.Fprintf(os.Stderr, "mdns-vpn-bridge: invalid status response: %v\n", err)
		return 1
	}
	printStatus(os.Stdout, st)
	return 0
}

// printStatus renders the human-readable status. The version line shows the
// version the running instance reported on /_status (st.Version), never the
// local client's own build version, and reports "Unknown" when the server
// omitted it.
func printStatus(w io.Writer, st relay.Status) {
	svc := "-"
	if len(st.Services) > 0 {
		svc = strings.Join(st.Services, ",")
	}
	ver := st.Version
	if ver == "" {
		ver = "Unknown"
	}
	fmt.Fprintf(w, "mdns-vpn-bridge %s\n", ver)
	fmt.Fprintf(w, "  lan        %s (%s)\n", st.LanIf, st.LanIP)
	fmt.Fprintf(w, "  peer       %s:%d\n", st.Peer, st.PeerPort)
	fmt.Fprintf(w, "  filter     lan2peer=%s peer2lan=%s\n", st.Lan2Peer, st.Peer2LAN)
	fmt.Fprintf(w, "  services   %s\n", svc)
	fmt.Fprintf(w, "  uptime     %s\n", formatUptime(time.Duration(st.UptimeSeconds)*time.Second))
	fmt.Fprintf(w, "  -> peer    %d packets / %s (dropped %d)\n",
		st.ToPeer.Packets, humanBytes(st.ToPeer.Bytes), st.ToPeer.Dropped)
	fmt.Fprintf(w, "  -> lan     %d packets / %s (dropped %d)\n",
		st.ToLAN.Packets, humanBytes(st.ToLAN.Bytes), st.ToLAN.Dropped)
	fmt.Fprintf(w, "  dedupe     %d suppressed\n", st.Dedupe)
	fmt.Fprintf(w, "  errors     %d write errors\n", st.Errors)
}

func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func humanBytes(b uint64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < len("kMGTPE")-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}
