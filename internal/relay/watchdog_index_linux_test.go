//go:build linux

package relay

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchdogDetectsInterfaceRecreated covers the index-change branch of
// checkLAN: an interface that disappears and comes back with a new index (5 ->
// 6) must be reported as "recreated" so the supervisor restarts the relay,
// even though the address is unchanged. It complements the address-change and
// override-loss watchdog tests.
func TestWatchdogDetectsInterfaceRecreated(t *testing.T) {
	peerLn := listenLoopback(t)
	defer peerLn.Close()
	lanLn := listenLoopback(t)
	defer lanLn.Close()
	installLoopbackSeams(t, lanLn.LocalAddr().(*net.UDPAddr))

	var idx atomic.Int64
	idx.Store(5)
	withLookups(t,
		func(string) (*net.Interface, error) {
			return fakeIface(int(idx.Load()), net.FlagUp|net.FlagRunning), nil
		},
		func(*net.Interface) ([]net.Addr, error) {
			// The override address stays present, so only the index change is
			// left to trigger a report.
			return []net.Addr{cidr(t, "10.0.0.5/24")}, nil
		},
	)

	cfg := testConfig(peerLn.LocalAddr().(*net.UDPAddr).Port)
	cfg.LanIP = "10.0.0.5" // override path: the index check fires first anyway
	cfg.watchInterval = 20 * time.Millisecond

	done, stop := runBackground(t, cfg)
	defer stop()

	time.Sleep(80 * time.Millisecond) // let the watchdog settle on index 5
	idx.Store(6)                      // interface recreated with a new index

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "recreated") {
			t.Fatalf("Run err = %v; want interface-recreated error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not report the recreated interface")
	}
}
