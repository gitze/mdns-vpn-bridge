package relay

import (
	"net"
	"time"
)

// Config describes one relay instance. Every field except LanIf and Peer has
// a usable default.
type Config struct {
	// LanIf is the interface whose multicast domain is served.
	LanIf string
	// LanIP overrides the IPv4 taken from LanIf. Optional.
	LanIP string
	// Peer is the peer's unicast address on the overlay (its Tailscale/WireGuard IP).
	Peer string
	// PeerPort is the UDP port of the unicast link on both sides.
	PeerPort int
	// Lan2Peer selects what leaves the local LAN towards the peer.
	Lan2Peer Mode
	// Peer2LAN selects what arrives from the peer and is injected locally.
	Peer2LAN Mode
	// Services restricts Lan2Peer to packets mentioning one of these service
	// types (substring match on the wire format). Empty allows all.
	Services []string
	// DedupeTTL is the loop-suppression window. 2s is a good default.
	DedupeTTL time.Duration
	// Verbose logs every forwarded packet.
	Verbose bool
	// Stats is the interval for periodic counter logs; 0 disables.
	Stats time.Duration
	// Logf receives log lines. Defaults to the standard logger.
	Logf func(format string, args ...any)

	// retryDelay is the pause between LAN address lookup attempts (tests shorten it).
	retryDelay time.Duration
	// retryAttempts is how often the LAN address lookup is retried at startup.
	retryAttempts int
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
	if c.retryDelay == 0 {
		c.retryDelay = 3 * time.Second
	}
	if c.retryAttempts == 0 {
		c.retryAttempts = 20
	}
}

// ifaceIPv4 returns the first non-loopback IPv4 address and its network of ifname.
func ifaceIPv4(ifname string) (net.IP, *net.IPNet, error) {
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, nil, err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, nil, err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLoopback() {
			return v4, &net.IPNet{IP: v4.Mask(ipn.Mask), Mask: ipn.Mask}, nil
		}
	}
	return nil, nil, &NoIPv4Error{If: ifname}
}

// NoIPv4Error reports that the configured interface has no usable IPv4 address.
type NoIPv4Error struct{ If string }

func (e *NoIPv4Error) Error() string {
	return "interface " + e.If + " has no usable IPv4 address"
}
