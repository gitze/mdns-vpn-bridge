package relay

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

// Normalize applies the documented default for every unset field. It is the one
// normalization step shared by Run, the CLI and the config-file loader, so a
// value omitted in a config file and a value omitted on the command line end up
// identical.
//
// Note that a zero Stats interval is *not* defaulted here: 0 means "stats
// disabled" for a programmatic Run, and only the CLI layer raises it to its
// 300s flag default. See cmd/mdns-vpn-bridge for that split.
func (c *Config) Normalize() { c.defaults() }

// Validate checks the effective configuration for syntactic correctness. It is
// deliberately pure: it never opens a socket, resolves a name, or inspects an
// interface, so the "check" subcommand, the config-file loader and Run share
// exactly one validation path and it stays safe to call offline. Network-
// dependent checks (does the interface exist, does it have an address) stay in
// Run, which is the only place that talks to the kernel.
//
// Call Validate after Normalize so a zero PeerPort / Lan2Peer / Peer2LAN /
// DedupeTTL has already been replaced by its default.
func (c *Config) Validate() error {
	if c.LanIf == "" || c.Peer == "" {
		return fmt.Errorf("relay: LanIf and Peer are required")
	}
	if err := validateIfName(c.LanIf); err != nil {
		return fmt.Errorf("relay: lan %q: %w", c.LanIf, err)
	}
	if c.LanIP != "" && net.ParseIP(c.LanIP).To4() == nil {
		return fmt.Errorf("relay: lanip %q is not an IPv4 address", c.LanIP)
	}
	if net.ParseIP(c.Peer).To4() == nil {
		return fmt.Errorf("relay: peer %q is not an IPv4 address", c.Peer)
	}
	if c.LinkBind != "" && net.ParseIP(c.LinkBind).To4() == nil {
		return fmt.Errorf("relay: linkbind %q is not an IPv4 address", c.LinkBind)
	}
	if c.PeerPort < 1 || c.PeerPort > 65535 {
		return fmt.Errorf("relay: peer port %d out of range 1..65535", c.PeerPort)
	}
	if _, err := ParseMode(string(c.Lan2Peer)); err != nil {
		return err
	}
	if _, err := ParseMode(string(c.Peer2LAN)); err != nil {
		return err
	}
	for _, s := range c.Services {
		if err := validateServiceEntry(s); err != nil {
			return err
		}
	}
	if c.DedupeTTL < 0 {
		return fmt.Errorf("relay: dedupe TTL %s must not be negative", c.DedupeTTL)
	}
	if c.Stats < 0 {
		return fmt.Errorf("relay: stats interval %s must not be negative", c.Stats)
	}
	if err := validateStatusAddr(c.StatusAddr); err != nil {
		return err
	}
	return nil
}

// validateIfName checks that s looks like a Linux network interface name. It is
// deliberately conservative and does not enforce the 15-byte IFNAMSIZ limit: a
// too-long name simply fails at socket bind, and rejecting it here would break
// a real but unusual name for no benefit. What the kernel genuinely cannot use
// is rejected: an empty name, "." / "..", a leading '-' (which the interface
// ioctls read as a flag), and any '/' or whitespace/control byte. Legitimate
// names such as eth0, br-lan, eth0.10 (VLAN) or eth0:1 (alias) pass.
func validateIfName(s string) error {
	if s == "." || s == ".." {
		return fmt.Errorf("invalid interface name")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("interface name must not start with '-'")
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b <= ' ' || b == 0x7f || b == '/' {
			return fmt.Errorf("interface name must not contain whitespace, control characters or '/'")
		}
	}
	return nil
}

// validateServiceEntry rejects an allowlist entry that is empty, carries any
// whitespace, or is made up only of dots. It does not otherwise restrict the
// label characters, so a non-ASCII label passes.
//
// The whitespace check runs against the original string, not a trimmed copy:
// the matcher compares the entry verbatim against a DNS label, so " _http "
// would trim clean yet never match a real "_http" label. Rejecting it here is
// what keeps "validate accepts it" and "the matcher can match it" in sync.
//
// A dots-only entry ("." or "...") splits into nothing but empty labels, so it
// can never name a service and is rejected as a meaningless (usually typo'd)
// entry. Repeated internal dots are fine: the matcher drops empty labels, so
// "_http.._tcp" still matches "_http._tcp".
func validateServiceEntry(s string) error {
	if s == "" {
		return fmt.Errorf("relay: services entry must not be empty")
	}
	if strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return fmt.Errorf("relay: services entry %q must not contain whitespace", s)
	}
	if strings.Trim(s, ".") == "" {
		return fmt.Errorf("relay: services entry %q has no labels", s)
	}
	return nil
}

// validateStatusAddr accepts "off" (or empty, both mean disabled) or a
// host:port whose port is in range and whose host is empty, "localhost" or an
// IP address. This matches the status endpoint's actual use (a local HTTP
// listener) without allowing an accidental hostname that would need DNS.
func validateStatusAddr(s string) error {
	if s == "" || s == "off" {
		return nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("relay: status_addr %q is not host:port (or \"off\")", s)
	}
	if err := validatePortNum(port); err != nil {
		return fmt.Errorf("relay: status_addr %q: %w", s, err)
	}
	switch {
	case host == "" || host == "localhost":
	case net.ParseIP(host) != nil:
	default:
		return fmt.Errorf("relay: status_addr host %q must be empty, localhost or an IP", host)
	}
	return nil
}

// validatePortNum parses a decimal port and checks its range.
func validatePortNum(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q out of range 1..65535", p)
	}
	return nil
}
