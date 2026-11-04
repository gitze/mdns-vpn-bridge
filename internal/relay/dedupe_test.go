package relay

import (
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
