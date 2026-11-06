package relay

import (
	"fmt"
	"net"
	"time"
)

// Config describes one relay instance. Every field except LanIf and Peer has
// a usable default.
type Config struct {
	// Version identifies the running build for read-only status, not user configuration.
	Version string
	// LanIf is the interface whose multicast domain is served.
	LanIf string
	// LanIP overrides the IPv4 taken from LanIf. Optional.
	LanIP string
	// Peer is the peer's unicast address on the overlay (its Tailscale/WireGuard IP).
	Peer string
	// PeerPort is the UDP port of the unicast link on both sides.
	PeerPort int
	// LinkBind is the local IPv4 address the peer link socket binds to.
	// Empty means 0.0.0.0 (listen on every interface). Binding to the overlay
	// address narrows what the OS will deliver to this socket; it is a
	// routing/hygiene aid, not authentication.
	LinkBind string
	// Lan2Peer selects what leaves the local LAN towards the peer.
	Lan2Peer Mode
	// Peer2LAN selects what arrives from the peer and is injected locally.
	Peer2LAN Mode
	// Services restricts Lan2Peer to packets mentioning one of these service
	// types (case-insensitive DNS label sequences). Only LAN multicast is
	// filtered; unicast replies and peer-to-LAN traffic bypass this allowlist.
	// Empty allows all.
	Services []string
	// DedupeTTL is the loop-suppression window. 2s is a good default.
	DedupeTTL time.Duration
	// Verbose logs every forwarded packet.
	Verbose bool
	// Stats is the interval for periodic counter logs; 0 disables.
	Stats time.Duration
	// StatusAddr is the listen address of the JSON status endpoint
	// (default 127.0.0.1:53531). Use "off" to disable it.
	StatusAddr string
	// Logf receives log lines. Defaults to the standard logger.
	Logf func(format string, args ...any)

	// retryDelay is the pause between LAN address lookup attempts (tests shorten it).
	retryDelay time.Duration
	// retryAttempts is how often the LAN address lookup is retried at startup.
	retryAttempts int
	// watchInterval is how often the LAN watchdog re-checks the interface
	// address, index and flags (tests shorten it).
	watchInterval time.Duration
	// errorLogInterval is the minimum spacing between write-error log lines per
	// direction; suppressed errors are aggregated and flushed on stop.
	errorLogInterval time.Duration
}

func (c *Config) defaults() {
	if c.PeerPort == 0 {
		c.PeerPort = 53530
	}
	if c.Lan2Peer == "" {
		c.Lan2Peer = ModeAll
	}
	if c.Peer2LAN == "" {
		c.Peer2LAN = ModeAll
	}
	if c.DedupeTTL == 0 {
		c.DedupeTTL = 2 * time.Second
	}
	if c.Logf == nil {
		c.Logf = logStd
	}
	if c.StatusAddr == "" {
		c.StatusAddr = "127.0.0.1:53531"
	}
	if c.retryDelay == 0 {
		c.retryDelay = 3 * time.Second
	}
	if c.retryAttempts == 0 {
		c.retryAttempts = 20
	}
	if c.watchInterval == 0 {
		c.watchInterval = 15 * time.Second
	}
	if c.errorLogInterval == 0 {
		c.errorLogInterval = 10 * time.Second
	}
}

// ifaceIPv4 returns the preferred non-loopback IPv4 address and its network of
// ifname, together with an optional ambiguity warning (empty when the choice is
// unambiguous). A link-local address (169.254.0.0/16) is only used when no
// other IPv4 address exists. The caller logs the warning through cfg.Logf, so
// it is emitted once at startup and not repeated on every watchdog cycle.
func ifaceIPv4(ifname string) (net.IP, *net.IPNet, string, error) {
	ifi, err := interfaceByName(ifname)
	if err != nil {
		return nil, nil, "", err
	}
	addrs, err := ifaceAddrs(ifi)
	if err != nil {
		return nil, nil, "", err
	}
	ip, ipnet, warn := chooseIPv4(addrs)
	if ip == nil {
		return nil, nil, "", &NoIPv4Error{If: ifname}
	}
	return ip, ipnet, warn, nil
}

// ifaceHasIPv4 reports whether ip is one of the interface's IPv4 addresses.
func ifaceHasIPv4(ifi *net.Interface, ip net.IP) (bool, error) {
	addrs, err := ifaceAddrs(ifi)
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if v4 := ipn.IP.To4(); v4 != nil && v4.Equal(ip) {
				return true, nil
			}
		}
	}
	return false, nil
}

// chooseIPv4 picks the best IPv4 address from addrs. It prefers a routable
// address over a link-local (169.254.0.0/16) one; if only a link-local address
// exists it is returned with a warning. When more than one routable address is
// present the first is returned together with a warning suggesting -lanip.
// Loopback and non-IPv4 addresses are ignored. A nil IP means no candidate was
// found. The returned warning is empty when the choice is unambiguous.
func chooseIPv4(addrs []net.Addr) (net.IP, *net.IPNet, string) {
	var (
		primary      net.IP
		primaryNet   *net.IPNet
		linkLocal    net.IP
		linkLocalNet *net.IPNet
		routable     int
	)
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipn.IP.To4()
		if v4 == nil || v4.IsLoopback() {
			continue
		}
		network := &net.IPNet{IP: v4.Mask(ipn.Mask), Mask: ipn.Mask}
		if v4.IsLinkLocalUnicast() {
			if linkLocal == nil {
				linkLocal, linkLocalNet = v4, network
			}
			continue
		}
		routable++
		if primary == nil {
			primary, primaryNet = v4, network
		}
	}
	switch {
	case primary != nil && routable > 1:
		return primary, primaryNet, fmt.Sprintf(
			"%d IPv4 addresses available; using %s (use -lanip to pick another)", routable, primary)
	case primary != nil:
		return primary, primaryNet, ""
	case linkLocal != nil:
		return linkLocal, linkLocalNet, fmt.Sprintf(
			"only a link-local IPv4 address is available; using %s (use -lanip to override)", linkLocal)
	default:
		return nil, nil, ""
	}
}

// NoIPv4Error reports that the configured interface has no usable IPv4 address.
type NoIPv4Error struct{ If string }

func (e *NoIPv4Error) Error() string {
	return "interface " + e.If + " has no usable IPv4 address"
}
