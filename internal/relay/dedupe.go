// Package relay implements a two-sided mDNS relay that bridges discovery
// traffic between two LANs over a unicast-only VPN link (Tailscale, WireGuard,
// ZeroTier, ...). Each instance terminates mDNS multicast on its own LAN and
// re-originates packets received from its peer, because VPN overlays do not
// carry multicast.
package relay

import (
	"crypto/sha256"
	"sync"
	"time"
)

// Dedupe suppresses re-sending of identical packets for a short window. It is
// the loop guard of the relay: a packet injected into the local multicast
// domain is seen again by our own group socket and must not travel back to the
// peer.
type Dedupe struct {
	mu   sync.Mutex
	seen map[[16]byte]time.Time
	ttl  time.Duration
}

// NewDedupe returns a Dedupe that remembers packets for ttl.
func NewDedupe(ttl time.Duration) *Dedupe {
	return &Dedupe{seen: make(map[[16]byte]time.Time), ttl: ttl}
}

// IsDuplicate reports whether b was seen within the TTL window and records it.
func (d *Dedupe) IsDuplicate(b []byte) bool {
	sum := sha256.Sum256(b)
	var k [16]byte
	copy(k[:], sum[:16])
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[k]; ok && now.Sub(t) < d.ttl {
		return true
	}
	d.seen[k] = now
	if len(d.seen) > 8192 {
		for key, t := range d.seen {
			if now.Sub(t) >= d.ttl {
				delete(d.seen, key)
			}
		}
	}
	return false
}
