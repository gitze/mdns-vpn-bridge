//go:build linux

package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests exercise the relay's socket wiring with real UDP sockets on the
// loopback interface. The socket constructors and the multicast destination are
// swapped for loopback equivalents through the package seams in relay.go, so
// no root privileges and no real multicast membership are needed.

// listenLoopback returns a UDP4 socket bound to an ephemeral port on 127.0.0.1.
func listenLoopback(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen loopback udp: %v", err)
	}
	enlargeBuffers(c)
	return c
}

// enlargeBuffers raises a socket's kernel buffers so a short test burst is not
// dropped by the default (small) loopback buffer. Errors are ignored: the
// kernel may clamp the value, which is fine for the small bursts here.
func enlargeBuffers(c *net.UDPConn) {
	_ = c.SetReadBuffer(4 << 20)
	_ = c.SetWriteBuffer(4 << 20)
}

// loopbackSeams holds the loopback sockets handed to Run.
type loopbackSeams struct {
	mcast, uni, link *net.UDPConn
}

// installLoopbackSeams redirects the relay's sockets to loopback and points the
// "inject locally" multicast destination at dest. The originals are restored
// when the test ends.
func installLoopbackSeams(t *testing.T, dest *net.UDPAddr) *loopbackSeams {
	t.Helper()
	s := &loopbackSeams{
		mcast: listenLoopback(t),
		uni:   listenLoopback(t),
		link:  listenLoopback(t),
	}
	om, ou, ol, od := newMcastConn, newUniConn, newLinkConn, mcastDest
	newMcastConn = func(net.IP) (*net.UDPConn, error) { return s.mcast, nil }
	newUniConn = func(net.IP) (*net.UDPConn, error) { return s.uni, nil }
	newLinkConn = func(string, int) (*net.UDPConn, error) { return s.link, nil }
	mcastDest = dest
	t.Cleanup(func() {
		newMcastConn, newUniConn, newLinkConn, mcastDest = om, ou, ol, od
		_ = s.mcast.Close()
		_ = s.uni.Close()
		_ = s.link.Close()
	})
	return s
}

// testConfig returns a Config that points Run at the loopback seams. lan is set
// to 127.0.0.2 (a loopback address distinct from the 127.0.0.1 senders) so the
// own-echo filter does not swallow the test traffic.
func testConfig(peerPort int) Config {
	return Config{
		LanIf:      "lo",
		LanIP:      "127.0.0.2",
		Peer:       "127.0.0.1",
		PeerPort:   peerPort,
		Lan2Peer:   ModeAll,
		Peer2LAN:   ModeAll,
		StatusAddr: "off",
		retryDelay: time.Millisecond, retryAttempts: 1,
		Logf: func(string, ...any) {},
	}
}

// startRelay runs Run in the background and returns a stop function that
// cancels it and waits for it to return.
func startRelay(t *testing.T, cfg Config) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned unexpectedly: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	}
}

// TestMcastSocketDisablesMulticastAll verifies the raw socket options the relay
// relies on: IP_MULTICAST_ALL=0 (finding 2) and IP_MULTICAST_LOOP left on for
// the own-echo filter. It is skipped where no multicast-capable IPv4 interface
// or multicast membership is available.
func TestMcastSocketDisablesMulticastAll(t *testing.T) {
	iface, ip := multicastIface(t)
	conn, err := mcastSocket(ip)
	if err != nil {
		t.Skipf("mcastSocket(%s on %s): %v", ip, iface, err)
	}
	defer conn.Close()

	rc, err := conn.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var (
		all  int
		loop int
		gerr error
	)
	if err := rc.Control(func(fd uintptr) {
		all, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, ipMulticastAll)
		if gerr == nil {
			loop, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP)
		}
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if gerr != nil {
		t.Fatalf("getsockopt: %v", gerr)
	}
	if all != 0 {
		t.Errorf("IP_MULTICAST_ALL = %d, want 0", all)
	}
	if loop != 1 {
		t.Errorf("IP_MULTICAST_LOOP = %d, want 1 (own-echo filter depends on it)", loop)
	}
}

// TestRunRelaysConcurrentPacketsIntact feeds distinct packets from many
// concurrent sources into both LAN-facing sockets and checks that every packet
// arrives at the peer byte-for-byte. Run with -race this catches the shared
// receive buffer of finding 1.
func TestRunRelaysConcurrentPacketsIntact(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()

	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))
	stop := startRelay(t, testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port))
	defer stop()

	mcastAddr := s.mcast.LocalAddr().(*net.UDPAddr)
	uniAddr := s.uni.LocalAddr().(*net.UDPAddr)

	// Collect on the peer side while the senders run, so the peer socket's
	// buffer cannot overflow and drop a tail (that would be a test artifact,
	// not a relay bug).
	got := make(map[string]int)
	var gotMu sync.Mutex
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		buf := make([]byte, 2048)
		for {
			n, _, err := peerLn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			gotMu.Lock()
			got[string(append([]byte(nil), buf[:n]...))]++
			gotMu.Unlock()
		}
	}()

	const (
		senders   = 12
		perSender = 40
	)
	expected := make(map[string]bool)
	var expMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dst, tag := mcastAddr, "m"
			if id%2 == 0 {
				dst, tag = uniAddr, "u"
			}
			c, err := net.DialUDP("udp4", nil, dst)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			for j := 0; j < perSender; j++ {
				pkt := []byte(fmt.Sprintf("pkt-%s-%02d-%04d-%s", tag, id, j, "0123456789abcdef"))
				expMu.Lock()
				expected[string(pkt)] = true
				expMu.Unlock()
				if _, err := c.Write(pkt); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Wait until every distinct packet has arrived, or give up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		gotMu.Lock()
		n := len(got)
		gotMu.Unlock()
		if n >= len(expected) || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = peerLn.SetReadDeadline(time.Now()) // unblock the collector
	<-collectorDone

	expMu.Lock()
	defer expMu.Unlock()
	gotMu.Lock()
	defer gotMu.Unlock()
	for pkt := range expected {
		if got[pkt] == 0 {
			t.Errorf("packet %q was not relayed intact", pkt)
		}
	}
	for pkt := range got {
		if !expected[pkt] {
			t.Errorf("received unexpected or corrupted packet %q", pkt)
		}
	}
}

// TestRunMinLengthValidationAndPeerToLAN checks that packets shorter than a DNS
// header are dropped in both directions (finding 6) while valid packets are
// relayed, including the unicast peer -> LAN path.
func TestRunMinLengthValidationAndPeerToLAN(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()

	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))
	stop := startRelay(t, testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port))
	defer stop()

	linkAddr := s.link.LocalAddr().(*net.UDPAddr)
	mcastAddr := s.mcast.LocalAddr().(*net.UDPAddr)

	peer, err := net.DialUDP("udp4", nil, linkAddr) // source 127.0.0.1 == peerIP
	if err != nil {
		t.Fatalf("dial peer: %v", err)
	}
	defer peer.Close()
	validToLAN := []byte("valid-peer-to-lan-packet")
	if _, err := peer.Write(validToLAN); err != nil {
		t.Fatalf("write valid to peer: %v", err)
	}
	if _, err := peer.Write([]byte{0x00, 0x00, 0x00}); err != nil { // 3-byte runt
		t.Fatalf("write runt: %v", err)
	}

	lan, err := net.DialUDP("udp4", nil, mcastAddr)
	if err != nil {
		t.Fatalf("dial lan: %v", err)
	}
	defer lan.Close()
	validToPeer := []byte("valid-lan-to-peer-packet")
	if _, err := lan.Write(validToPeer); err != nil {
		t.Fatalf("write valid to lan: %v", err)
	}
	if _, err := lan.Write([]byte{0x01, 0x02}); err != nil { // 2-byte runt
		t.Fatalf("write runt: %v", err)
	}

	if got := readOne(t, lanLn, 3*time.Second); string(got) != string(validToLAN) {
		t.Errorf("peer->LAN got %q, want %q", got, validToLAN)
	}
	if got := readOne(t, peerLn, 3*time.Second); string(got) != string(validToPeer) {
		t.Errorf("LAN->peer got %q, want %q", got, validToPeer)
	}
	if extra, ok := readMaybe(lanLn, 300*time.Millisecond); ok {
		t.Errorf("short peer->LAN packet was not dropped: %q", extra)
	}
	if extra, ok := readMaybe(peerLn, 300*time.Millisecond); ok {
		t.Errorf("short LAN->peer packet was not dropped: %q", extra)
	}
}

// TestRunReturnsErrorOnReaderFailure pulls a socket out from under the relay and
// checks that Run returns the failure instead of silently leaving one direction
// dead (finding 3).
func TestRunReturnsErrorOnReaderFailure(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()

	s := installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)) }()

	// Let the readers reach their blocking read, then close the link socket.
	time.Sleep(100 * time.Millisecond)
	if err := s.link.Close(); err != nil {
		t.Fatalf("close link: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after a reader failure")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("Run reported cancellation instead of the reader failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the reader failed")
	}
}

// TestRunFailsOnStatusBindError verifies the status listener is bound
// synchronously: a busy port fails startup rather than being logged away
// (finding 12).
func TestRunFailsOnStatusBindError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer busy.Close()

	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.StatusAddr = busy.Addr().String() // already bound -> net.Listen fails

	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("Run succeeded although the status port is in use")
	}
}

// multicastIface returns a multicast-capable, non-loopback interface and one of
// its IPv4 addresses, skipping the test when none exists.
func multicastIface(t *testing.T) (string, net.IP) {
	t.Helper()
	ifs, err := net.Interfaces()
	if err != nil {
		t.Skipf("interfaces: %v", err)
	}
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil {
					return ifi.Name, v4
				}
			}
		}
	}
	t.Skip("no multicast-capable IPv4 interface available")
	return "", nil
}

func readOne(t *testing.T, c *net.UDPConn, timeout time.Duration) []byte {
	t.Helper()
	got, ok := readMaybe(c, timeout)
	if !ok {
		t.Fatalf("no packet within %s", timeout)
	}
	return got
}

func readMaybe(c *net.UDPConn, timeout time.Duration) ([]byte, bool) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), buf[:n]...), true
}
