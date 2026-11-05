//go:build linux

package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitSocketClosed blocks until c reports "use of closed network connection",
// which happens when Run's coordinated shutdown closes the sockets. In the
// buggy code that close ran strictly after the premature flush in the main
// select, so waiting for it makes the test's release deterministic: the release
// always lands after the old flush point.
func waitSocketClosed(t *testing.T, c *net.UDPConn) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	buf := make([]byte, 1)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if _, _, err := c.ReadFromUDP(buf); err != nil && strings.Contains(err.Error(), "closed") {
			return
		}
	}
	t.Fatal("coordinated shutdown did not close the sockets")
}

// TestRunFlushesPendingSummaryAfterReadersJoin is the deterministic regression
// test for the shutdown-flush ordering bug. flushErrLogs used to run in the main
// select, before the coordinated cancel/close/join, so a write failure that an
// in-flight reader recorded during shutdown was never summarised. Here the
// multicast reader is held inside writeLink for a second packet until after
// cancellation and the socket close, then released before the join; the final
// pending summary must still capture that last failure.
func TestRunFlushesPendingSummaryAfterReadersJoin(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()

	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	var calls atomic.Int64
	overrideWriteLink(t, func(*net.UDPConn, []byte, *net.UDPAddr) (int, error) {
		if calls.Add(1) == 1 {
			// First packet: fail at once, so the limiter emits (and resets)
			// its first line before the shutdown point.
			return 0, errors.New("first synthetic failure")
		}
		// Second packet: park the reader inside writeLink until the test
		// releases it after cancellation.
		enteredOnce.Do(func() { close(entered) })
		<-release
		return 0, errors.New("late synthetic failure")
	})

	var mu sync.Mutex
	var lines []string
	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.errorLogInterval = time.Hour // suppress everything but the first line
	cfg.Logf = func(f string, a ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(f, a...))
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var runErr error
	go func() { runErr = Run(ctx, cfg); close(finished) }()
	defer func() {
		doRelease()
		cancel()
		<-finished
	}()

	// Two distinct packets reach the multicast reader: the first records the
	// first failure, the second parks the reader inside writeLink.
	c, err := net.DialUDP("udp4", nil, s.mcast.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	for _, pkt := range []string{"valid-mcast-packet-A-0123456789", "valid-mcast-packet-B-0123456789"} {
		if _, err := c.Write([]byte(pkt)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reader never reached the held writeLink")
	}

	// Cancel while the reader is parked. The buggy code flushed its summaries
	// right here, before the reader had recorded the second failure.
	cancel()

	// Release only after the coordinated shutdown has closed the sockets, so
	// the second failure always lands after the old flush point.
	waitSocketClosed(t, s.mcast)
	doRelease()
	<-finished

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run returned %v; want nil", runErr)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if strings.Contains(l, "late synthetic failure") {
			return // the last failure reached the flushed summary
		}
	}
	t.Fatalf("final pending summary did not capture the last failure; log lines: %v", lines)
}
