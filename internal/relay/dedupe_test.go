package relay

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

func TestDedupeSuppressesIdenticalPacket(t *testing.T) {
	d := NewDedupe(500 * time.Millisecond)
	p := []byte("hello-mdns")
	if d.IsDuplicate(p) {
		t.Fatal("first sighting must not be a duplicate")
	}
	if !d.IsDuplicate(p) {
		t.Fatal("second sighting within TTL must be a duplicate")
	}
}

func TestDedupeExpires(t *testing.T) {
	d := NewDedupe(30 * time.Millisecond)
	p := []byte("hello-mdns")
	d.IsDuplicate(p)
	time.Sleep(40 * time.Millisecond)
	if d.IsDuplicate(p) {
		t.Fatal("after TTL the packet must pass again")
	}
}

func TestDedupeIsConcurrent(t *testing.T) {
	d := NewDedupe(time.Second)
	p := []byte("same")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.IsDuplicate(p)
		}()
	}
	wg.Wait()
}

func TestDedupeEvictsOldest(t *testing.T) {
	d := newDedupe(time.Second, 2)
	a, b, c := []byte("a"), []byte("b"), []byte("c")
	d.IsDuplicate(a)
	d.IsDuplicate(b)
	d.IsDuplicate(c) // evicts a
	if got := d.size(); got != 2 {
		t.Fatalf("size = %d, want 2 (hard cap)", got)
	}
	if !d.IsDuplicate(c) {
		t.Fatal("most recent entry must still be a duplicate")
	}
	if d.IsDuplicate(a) {
		t.Fatal("evicted entry must no longer be a duplicate")
	}
}

// TestDedupeBoundedFlood inserts far more distinct packets than the cap and
// asserts the map never grows past it.
func TestDedupeBoundedFlood(t *testing.T) {
	const max = 64
	d := newDedupe(time.Minute, max)
	for i := 0; i < 20000; i++ {
		var p [8]byte
		binary.BigEndian.PutUint64(p[:], uint64(i))
		d.IsDuplicate(p[:])
		if s := d.size(); s > max {
			t.Fatalf("iteration %d: size %d exceeds cap %d", i, s, max)
		}
	}
	if s := d.size(); s != max {
		t.Fatalf("final size = %d, want %d", s, max)
	}
}

// TestDedupeExpiryReclaims verifies expired entries are dropped so the map
// shrinks back rather than staying at the cap.
func TestDedupeExpiryReclaims(t *testing.T) {
	const max = 1024
	d := newDedupe(20*time.Millisecond, max)
	for i := 0; i < 500; i++ {
		var p [8]byte
		binary.BigEndian.PutUint64(p[:], uint64(i))
		d.IsDuplicate(p[:])
	}
	if got := d.size(); got != 500 {
		t.Fatalf("size before expiry = %d, want 500", got)
	}
	time.Sleep(40 * time.Millisecond)
	d.IsDuplicate([]byte("fresh")) // triggers the expiry pass
	if got := d.size(); got != 1 {
		t.Fatalf("size after expiry = %d, want 1", got)
	}
}

// TestDedupeConcurrentFloodBounded hammers the same map from many goroutines
// with distinct keys and asserts the cap holds. Run with -race.
func TestDedupeConcurrentFloodBounded(t *testing.T) {
	const (
		max       = 128
		goroutine = 16
		perG      = 4000
	)
	d := newDedupe(time.Minute, max)
	var wg sync.WaitGroup
	for g := 0; g < goroutine; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				var p [16]byte
				binary.BigEndian.PutUint64(p[:8], uint64(g))
				binary.BigEndian.PutUint64(p[8:], uint64(i))
				d.IsDuplicate(p[:])
			}
		}(g)
	}
	wg.Wait()
	if s := d.size(); s > max {
		t.Fatalf("size %d exceeds cap %d", s, max)
	}
}

// TestDedupeTimestampAcquiredUnderLock guards the FIFO ordering invariant: the
// per-insertion timestamp must be read while holding the mutex.
//
// Expiry pops the expired prefix of the insertion-ordered FIFO, which is only
// sound if insertion timestamps are monotonically non-decreasing along that
// order. If time.Now() were read before Lock, two racing goroutines could
// record an older timestamp after a newer one (capture order != insertion
// order), and expire() could then stop early on a live entry or drop a live
// one behind a stale head.
//
// The test holds the lock so a caller blocks at Lock. A later wall-clock
// instant passes; if the timestamp is read before Lock the recorded value
// predates that instant, if it is read under the lock it cannot.
func TestDedupeTimestampAcquiredUnderLock(t *testing.T) {
	d := NewDedupe(time.Hour)

	d.mu.Lock() // hold the lock so the goroutine blocks at Lock
	done := make(chan struct{})
	go func() {
		d.IsDuplicate([]byte("race"))
		close(done)
	}()

	// Give the goroutine time to reach the lock and, with the bug present, to
	// have already read time.Now() before blocking.
	time.Sleep(100 * time.Millisecond)
	released := time.Now()
	d.mu.Unlock()
	<-done

	d.mu.Lock()
	var ts time.Time
	for _, v := range d.seen {
		ts = v
	}
	d.mu.Unlock()

	if ts.Before(released) {
		t.Fatalf("timestamp %v was acquired before the mutex was released at %v", ts, released)
	}
}
