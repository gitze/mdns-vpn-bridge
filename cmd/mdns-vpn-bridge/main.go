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

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const defaultStatusAddr = "127.0.0.1:53531"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status":
			os.Exit(cmdStatus(os.Args[2:]))
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	runRelay()
}

func usage() {
	fmt.Fprintf(os.Stderr, `mdns-vpn-bridge %s — mDNS/Bonjour between two LANs over a unicast VPN link

usage:
  mdns-vpn-bridge -lan <iface> -peer <overlay-ip> [options]   run the relay
  mdns-vpn-bridge status [-addr <host:port>] [-json]          query a running instance
  mdns-vpn-bridge version

Typical asymmetric setup across a metered link:
  chatty side:  -lan2peer query -peer2lan all
  quiet side:   -lan2peer all   -peer2lan all

relay options:
`, version)
	flag.CommandLine.SetOutput(os.Stderr)
	flag.PrintDefaults()
}

func runRelay() {
	var (
		lanIf    = flag.String("lan", "", "LAN interface carrying mDNS (e.g. ens18, br-lan)")
		lanIP    = flag.String("lanip", "", "override the LAN IPv4 (default: taken from -lan)")
		peer     = flag.String("peer", "", "peer unicast address on the overlay (Tailscale/WireGuard IPv4)")
		peerPort = flag.Int("peerport", 53530, "UDP port of the unicast link (both sides)")
		lan2peer = flag.String("lan2peer", "all", "LAN -> peer: \"query\" (DNS queries only) or \"all\"")
		peer2lan = flag.String("peer2lan", "all", "peer -> LAN: \"all\" or \"query\"")
		services = flag.String("services", "", "comma-separated allowlist of service types for LAN -> peer (e.g. _http,_esphomelib); empty = all")
		dedupe   = flag.Duration("dedupe", 2*time.Second, "loop-suppression window for identical packets")
		verbose  = flag.Bool("v", false, "log every forwarded packet")
		stats    = flag.Duration("stats", 300*time.Second, "stats log interval (0 = off)")
		status   = flag.String("status", defaultStatusAddr, "HTTP status endpoint listen address (\"off\" disables it)")
	)
	flag.Usage = usage
	flag.Parse()

	cfg := relay.Config{
		LanIf:      *lanIf,
		LanIP:      *lanIP,
		Peer:       *peer,
		PeerPort:   *peerPort,
		Lan2Peer:   relay.Mode(*lan2peer),
		Peer2LAN:   relay.Mode(*peer2lan),
		Services:   relay.ParseServices(*services),
		DedupeTTL:  *dedupe,
		Verbose:    *verbose,
		Stats:      *stats,
		StatusAddr: *status,
	}
	if cfg.LanIf == "" || cfg.Peer == "" {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.Run(ctx, cfg); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// cmdStatus queries a running instance and prints its state.
func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", defaultStatusAddr, "status endpoint of the running instance")
	asJSON := fs.Bool("json", false, "print raw JSON")
	timeout := fs.Duration("timeout", 3*time.Second, "request timeout")
	_ = fs.Parse(args)

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
	printStatus(os.Stdout, st, version)
	return 0
}

func printStatus(w io.Writer, st relay.Status, ver string) {
	svc := "-"
	if len(st.Services) > 0 {
		svc = strings.Join(st.Services, ",")
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
