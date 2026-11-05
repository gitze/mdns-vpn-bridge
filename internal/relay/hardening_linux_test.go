//go:build linux

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// overrideLinkConn swaps the link-socket constructor for the test and restores
// it afterwards (LIFO: this runs before installLoopbackSeams' own restore).
func overrideLinkConn(t *testing.T, fn func(string, int) (*net.UDPConn, error)) {
	t.Helper()
	orig := newLinkConn
	newLinkConn = fn
	t.Cleanup(func() { newLinkConn = orig })
}

// overrideReadMcast swaps the multicast read seam.
func overrideReadMcast(t *testing.T, fn func(*net.UDPConn, []byte) (int, *net.UDPAddr, error)) {
	t.Helper()
	orig := readMcast
	readMcast = fn
	t.Cleanup(func() { readMcast = orig })
}

// overrideWriteLink swaps the peer-write seam.
func overrideWriteLink(t *testing.T, fn func(*net.UDPConn, []byte, *net.UDPAddr) (int, error)) {
	t.Helper()
	orig := writeLink
	writeLink = fn
	t.Cleanup(func() { writeLink = orig })
}

// withLookups swaps the interface lookup seams for the test.
func withLookups(t *testing.T, byName func(string) (*net.Interface, error), addrs func(*net.Interface) ([]net.Addr, error)) {
	t.Helper()
	ob, oa := interfaceByName, ifaceAddrs
	if byName != nil {
		interfaceByName = byName
	}
	if addrs != nil {
		ifaceAddrs = addrs
	}
	t.Cleanup(func() { interfaceByName, ifaceAddrs = ob, oa })
}

func fakeIface(index int, flags net.Flags) *net.Interface {
	return &net.Interface{Name: "testlan", Index: index, Flags: flags}
}

func fetchErrors(t *testing.T, addr string) uint64 {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/_status")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0
	}
	return st.Errors
}

// runBackground starts Run and returns a channel carrying its result plus a
// stop function. stop cancels Run AND waits for it to return, so the caller's
// cleanup (which restores the global seams) never races with a live Run.
func runBackground(t *testing.T, cfg Config) (chan error, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- Run(ctx, cfg)
		close(finished)
	}()
	stop := func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	}
	return done, stop
}

// ---------------------------------------------------------------------------
// R2 — rate-limited, aggregated write-error logging and the errors counter
// ---------------------------------------------------------------------------

// TestRunWriteErrorsRateLimitedAndCounted feeds many packets whose peer write
// always fails and checks that (a) the top-level errors counter reaches the
// packet count, (b) only the first error and the shutdown summary are logged
// (the limiter suppresses the rest), and (c) the summary is flushed on stop
// even though no packet was ever delivered.
func TestRunWriteErrorsRateLimitedAndCounted(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()

	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))
	overrideWriteLink(t, func(*net.UDPConn, []byte, *net.UDPAddr) (int, error) {
		return 0, errors.New("synthetic write failure")
	})

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	statusAddr := probe.Addr().String()
	_ = probe.Close()

	var mu sync.Mutex
	var lines []string
	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.StatusAddr = statusAddr
	cfg.errorLogInterval = time.Hour // suppress everything during the run
	cfg.Logf = func(f string, a ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(f, a...))
		mu.Unlock()
	}

	stop := startRelay(t, cfg)

	const n = 60
	c, err := net.DialUDP("udp4", nil, s.mcast.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	for i := 0; i < n; i++ {
		if _, err := c.Write([]byte(fmt.Sprintf("pkt-%03d-0123456789abcdef", i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	var got uint64
	for {
		got = fetchErrors(t, statusAddr)
		if got >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != n {
		t.Fatalf("errors counter = %d, want %d", got, n)
	}

	stop() // cancels; the shutdown summary is flushed before Run returns

	mu.Lock()
	defer mu.Unlock()
	var writeLines []string
	for _, l := range lines {
		if strings.HasPrefix(l, "peer write:") {
			writeLines = append(writeLines, l)
		}
	}
	if len(writeLines) != 2 {
		t.Fatalf("peer write log lines = %d %v; want 2 (first + flush)", len(writeLines), writeLines)
	}
	if !strings.Contains(writeLines[0], "synthetic write failure") {
		t.Fatalf("first line %q does not carry the error", writeLines[0])
	}
	wantSummary := fmt.Sprintf("%d errors since last report", n-1)
	if !strings.Contains(writeLines[1], wantSummary) {
		t.Fatalf("flush line %q does not report %q", writeLines[1], wantSummary)
	}
}

// ---------------------------------------------------------------------------
// R1 — LAN watchdog (address loss/change, override loss, flags, bounded lookups)
// ---------------------------------------------------------------------------

func TestWatchdogDetectsAddressChange(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var cur atomic.Value
	cur.Store("10.0.0.5")
	withLookups(t,
		func(string) (*net.Interface, error) { return fakeIface(5, net.FlagUp|net.FlagRunning), nil },
		nil,
	)
	oi := ifaceIPv4Lookup
	ifaceIPv4Lookup = func(string) (net.IP, *net.IPNet, string, error) {
		return net.ParseIP(cur.Load().(string)).To4(), nil, "", nil
	}
	t.Cleanup(func() { ifaceIPv4Lookup = oi })

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "" // exercise the preferred-address path
	cfg.watchInterval = 20 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run returned before the address changed: %v", err)
	default:
	}
	cur.Store("10.0.0.6")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("Run err = %v; want address-changed error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not report the address change")
	}
}

func TestWatchdogDetectsOverrideLoss(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var present atomic.Bool
	present.Store(true)
	withLookups(t,
		func(string) (*net.Interface, error) { return fakeIface(5, net.FlagUp|net.FlagRunning), nil },
		func(*net.Interface) ([]net.Addr, error) {
			if present.Load() {
				return []net.Addr{cidr(t, "10.0.0.5/24")}, nil
			}
			return nil, nil
		},
	)

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "10.0.0.5"
	cfg.watchInterval = 20 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	time.Sleep(80 * time.Millisecond)
	present.Store(false)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no longer") {
			t.Fatalf("Run err = %v; want override-lost error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not report the lost override address")
	}
}

func TestWatchdogDetectsInterfaceDown(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	withLookups(t,
		func(string) (*net.Interface, error) { return fakeIface(5, net.FlagUp), nil }, // no FlagRunning
		nil,
	)
	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "10.0.0.5"
	cfg.watchInterval = 20 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("Run err = %v; want interface-down error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not report the down interface")
	}
}

func TestWatchdogToleratesTransientLookupFailures(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var calls atomic.Int64
	withLookups(t, func(string) (*net.Interface, error) {
		n := calls.Add(1)
		if n == 1 { // startup lookup
			return fakeIface(5, net.FlagUp|net.FlagRunning), nil
		}
		if n%3 == 0 { // at most two consecutive failures
			return fakeIface(5, net.FlagUp|net.FlagRunning), nil
		}
		return nil, errors.New("temporary lookup failure")
	}, func(*net.Interface) ([]net.Addr, error) {
		return []net.Addr{cidr(t, "10.0.0.5/24")}, nil
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "10.0.0.5"
	cfg.watchInterval = 10 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	time.Sleep(300 * time.Millisecond) // many fail/fail/ok cycles
	select {
	case err := <-done:
		t.Fatalf("Run died on tolerated transient lookup failures: %v", err)
	default:
	}
}

func TestWatchdogFailsAfterBoundedTransientFailures(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var calls atomic.Int64
	withLookups(t, func(string) (*net.Interface, error) {
		if calls.Add(1) == 1 {
			return fakeIface(5, net.FlagUp|net.FlagRunning), nil
		}
		return nil, errors.New("persistent lookup failure")
	}, nil)

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "10.0.0.5"
	cfg.watchInterval = 10 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "failed checks") {
			t.Fatalf("Run err = %v; want bounded-failure error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not give up after bounded transient failures")
	}
}

// ---------------------------------------------------------------------------
// R6 — link-socket bind retry, only for EADDRNOTAVAIL
// ---------------------------------------------------------------------------

func TestLinkBindRetriesEADDRNOTAVAILThenSucceeds(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var calls atomic.Int64
	overrideLinkConn(t, func(string, int) (*net.UDPConn, error) {
		if calls.Add(1) < 3 {
			return nil, syscall.EADDRNOTAVAIL
		}
		return s.link, nil
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.retryDelay = time.Millisecond
	cfg.retryAttempts = 5

	done, stop := runBackground(t, cfg)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	default:
	}
	if calls.Load() != 3 {
		t.Fatalf("bind attempts = %d, want 3", calls.Load())
	}
}

func TestLinkBindFatalOnOtherError(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var calls atomic.Int64
	overrideLinkConn(t, func(string, int) (*net.UDPConn, error) {
		calls.Add(1)
		return nil, syscall.EACCES
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.retryDelay = time.Millisecond
	cfg.retryAttempts = 5

	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("Run succeeded although the link bind failed")
	}
	if calls.Load() != 1 {
		t.Fatalf("bind attempts = %d, want 1 (non-EADDRNOTAVAIL must be fatal at once)", calls.Load())
	}
}

func TestLinkBindExhaustsRetries(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var calls atomic.Int64
	overrideLinkConn(t, func(string, int) (*net.UDPConn, error) {
		calls.Add(1)
		return nil, syscall.EADDRNOTAVAIL
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.retryDelay = time.Millisecond
	cfg.retryAttempts = 3

	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("Run succeeded although the bind never became available")
	}
	if calls.Load() != 3 {
		t.Fatalf("bind attempts = %d, want 3", calls.Load())
	}
}

func TestLinkBindContextAbortDuringRetry(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	overrideLinkConn(t, func(string, int) (*net.UDPConn, error) {
		return nil, syscall.EADDRNOTAVAIL
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.retryDelay = 5 * time.Second // long wait: only cancellation ends it
	cfg.retryAttempts = 5

	done, stop := runBackground(t, cfg)
	time.Sleep(50 * time.Millisecond)
	stop() // cancels and waits for Run to return

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err = %v; want context.Canceled", err)
		}
	default:
		t.Fatal("Run returned no result after cancellation")
	}
}

// ---------------------------------------------------------------------------
// R7 — reader transient-error backoff, streak reset and bounded give-up
// ---------------------------------------------------------------------------

func TestReaderTransientErrorsBackoffThenFatal(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	overrideReadMcast(t, func(*net.UDPConn, []byte) (int, *net.UDPAddr, error) {
		return 0, nil, syscall.EAGAIN
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	done, stop := runBackground(t, cfg)
	defer stop()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "transient") {
			t.Fatalf("Run err = %v; want transient-error give-up", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader spun on persistent transient errors instead of giving up")
	}
}

func TestReaderTransientStreakResetsOnSuccess(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var n atomic.Int64
	overrideReadMcast(t, func(_ *net.UDPConn, b []byte) (int, *net.UDPAddr, error) {
		if n.Add(1)%100 == 0 { // a real packet every 100 reads -> streak never hits the cap
			pkt := []byte("valid-mcast-packet-0123456789")
			copy(b, pkt)
			return len(pkt), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}, nil
		}
		return 0, nil, syscall.EAGAIN
	})

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	done, stop := runBackground(t, cfg)
	defer stop()

	time.Sleep(600 * time.Millisecond) // ~300 reads, several resets
	select {
	case err := <-done:
		t.Fatalf("Run died although the transient streak kept resetting: %v", err)
	default:
	}
}
