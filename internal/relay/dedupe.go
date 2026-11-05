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

// defaultDedupeMax is the hard upper bound on remembered packets. The relay
// also targets 32-bit MIPS/ARM routers, so the map must not grow without limit
// under a packet flood: once the cap is reached the oldest entry is evicted.
const defaultDedupeMax = 8192

// compactThreshold is how many already-dropped slots may pile up at the front
// of the FIFO before the backing array is compacted.
const compactThreshold = 1024

// Dedupe suppresses re-sending of identical packets for a short window. It is
// the loop guard of the relay: a packet injected into the local multicast
// domain is seen again by our own group socket and must not travel back to the
// peer.
//
// Memory is hard-bounded: the map holds at most max entries. Expiry is
// amortised O(1) — insertions carry monotonically increasing timestamps, so
// the expired entries always form a prefix of the insertion-ordered FIFO and
// are popped from its front, never by scanning the whole map. When the cap is
// reached the oldest live entry is evicted, which bounds memory even while the
// TTL window is saturated by a flood of distinct packets.
type Dedupe struct {
	mu   sync.Mutex
	seen map[[16]byte]time.Time
	keys [][16]byte // FIFO of keys in insertion order
	head int        // index of the oldest live key in keys
	ttl  time.Duration
	max  int
}

// NewDedupe returns a Dedupe that remembers packets for ttl, bounded to
// defaultDedupeMax entries.
func NewDedupe(ttl time.Duration) *Dedupe { return newDedupe(ttl, defaultDedupeMax) }

// newDedupe is the bounded constructor. max is clamped to at least 1 so the
// map can never grow unbounded.
func newDedupe(ttl time.Duration, max int) *Dedupe {
	if max < 1 {
		max = 1
	}
	return &Dedupe{
		seen: make(map[[16]byte]time.Time),
		ttl:  ttl,
		max:  max,
	}
}

// IsDuplicate reports whether b was seen within the TTL window and records it.
func (d *Dedupe) IsDuplicate(b []byte) bool {
	sum := sha256.Sum256(b)
	var k [16]byte
	copy(k[:], sum[:16])

	d.mu.Lock()
	defer d.mu.Unlock()

	// The timestamp is read while holding the mutex on purpose. Expiry pops
	// the expired prefix of the insertion-ordered FIFO, which is only correct
	// if insertion timestamps are monotonically non-decreasing along that
	// order. Reading time.Now() before Lock would let two racing goroutines
	// record an older timestamp after a newer one (capture order != insertion
	// order), breaking the prefix-expiry invariant and letting a live entry
	// be evicted early or a stale one linger.
	now := time.Now()
	d.expire(now)
	if t, ok := d.seen[k]; ok && now.Sub(t) < d.ttl {
		return true
	}
	d.seen[k] = now
	d.keys = append(d.keys, k)

	// Hard cap: after dropping the expired prefix, evict the oldest live
	// entries until we are back within bounds.
	for len(d.seen) > d.max && d.head < len(d.keys) {
		old := d.keys[d.head]
		delete(d.seen, old)
		d.keys[d.head] = [16]byte{}
		d.head++
	}
	d.compact()
	return false
}

// expire drops the oldest entries that have passed the TTL. Insertions carry
// monotonically increasing timestamps, so expired entries are always a prefix
// of the FIFO; this is O(1) amortised per call instead of a full map scan.
func (d *Dedupe) expire(now time.Time) {
	for d.head < len(d.keys) {
		k := d.keys[d.head]
		t, ok := d.seen[k]
		if ok && now.Sub(t) < d.ttl {
			return
		}
		delete(d.seen, k)
		d.keys[d.head] = [16]byte{}
		d.head++
	}
}

// compact reclaims the dropped prefix of the FIFO so the backing array does
// not grow without bound over a long run.
func (d *Dedupe) compact() {
	if d.head == 0 {
		return
	}
	if d.head == len(d.keys) {
		d.keys = d.keys[:0]
		d.head = 0
		return
	}
	if d.head >= compactThreshold && d.head*2 >= len(d.keys) {
		n := copy(d.keys, d.keys[d.head:])
		d.keys = d.keys[:n]
		d.head = 0
	}
}

// size reports the number of live entries. Used by tests.
func (d *Dedupe) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}
