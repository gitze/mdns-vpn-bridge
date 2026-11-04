package relay

import "strings"

// Mode selects what is forwarded over the unicast link.
type Mode string

const (
	// ModeQuery forwards only DNS queries (QR=0). Use on the side whose own
	// announcements should not cross the link — typically the side with the
	// chatty LAN or a metered uplink.
	ModeQuery Mode = "query"
	// ModeAll forwards queries and responses.
	ModeAll Mode = "all"
)

// ParseMode validates a Mode string.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if m != ModeQuery && m != ModeAll {
		return "", &ModeError{s}
	}
	return m, nil
}

// ModeError reports an invalid mode value.
type ModeError struct{ Value string }

func (e *ModeError) Error() string {
	return "mode must be \"query\" or \"all\", got " + e.Value
}

// IsQuery reports whether an mDNS/DNS packet is a query (QR bit = 0).
// Queries must always be relayed: the peer's devices can only answer what
// they have heard.
func IsQuery(pkt []byte) bool {
	return len(pkt) >= 3 && pkt[2]&0x80 == 0
}

// ServiceAllowed reports whether the packet mentions one of the allowed
// service types. Substring match against the wire format is intentional:
// DNS names are stored as length-prefixed labels, so the raw bytes of a label
// such as "_http" appear verbatim in the packet. An empty allowlist permits
// everything.
func ServiceAllowed(pkt []byte, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	s := string(pkt)
	for _, t := range allow {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// ParseServices splits a comma separated allowlist ("_http, _esphomelib")
// and normalises each entry. Empty input yields nil (= allow all).
func ParseServices(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
