// Command mdns-vpn-bridge relays mDNS/Bonjour discovery between two LANs over
// a unicast-only VPN link (Tailscale, WireGuard, ZeroTier, ...). Run one
// instance on each side; both instances are symmetric.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mdns-vpn-bridge/internal/relay"
)

func main() {
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
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s -lan <iface> -peer <overlay-ip> [options]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Typical asymmetric setup across a metered link:")
		fmt.Fprintln(os.Stderr, "  chatty side:   -lan2peer query -peer2lan all")
		fmt.Fprintln(os.Stderr, "  quiet side:    -lan2peer all   -peer2lan all")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg := relay.Config{
		LanIf:     *lanIf,
		LanIP:     *lanIP,
		Peer:      *peer,
		PeerPort:  *peerPort,
		Lan2Peer:  relay.Mode(*lan2peer),
		Peer2LAN:  relay.Mode(*peer2lan),
		Services:  relay.ParseServices(*services),
		DedupeTTL: *dedupe,
		Verbose:   *verbose,
		Stats:     *stats,
	}
	if cfg.LanIf == "" || cfg.Peer == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.Run(ctx, cfg); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
