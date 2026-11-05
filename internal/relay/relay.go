package relay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

const mdnsPort = 5353

// Socket constructors and the multicast destination are variables so tests can
// substitute loopback sockets and a loopback destination. Production uses the
// real implementations from sockets_linux.go / sockets_other.go.
// Tests that override these seams must not use t.Parallel; their Run instances
// must terminate before restoring the seams.
var (
	newMcastConn = mcastSocket
	newUniConn   = uniSocket
	newLinkConn  = linkSocket
	mcastDest    = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}

	// Read/write seams let tests inject deterministic read failures (R7) and
	// write failures (R2) without a real broken socket. Production uses the
	// *net.UDPConn methods. Swapping these also forbids t.Parallel.
	readMcast = func(c *net.UDPConn, b []byte) (int, *net.UDPAddr, error) { return c.ReadFromUDP(b) }
	readUni   = func(c *net.UDPConn, b []byte) (int, *net.UDPAddr, error) { return c.ReadFromUDP(b) }
	readLink  = func(c *net.UDPConn, b []byte) (int, *net.UDPAddr, error) { return c.ReadFromUDP(b) }

	writeLink  = func(c *net.UDPConn, b []byte, addr *net.UDPAddr) (int, error) { return c.WriteToUDP(b, addr) }
	writeMcast = func(c *net.UDPConn, b []byte, addr *net.UDPAddr) (int, error) { return c.WriteToUDP(b, addr) }

	// Interface lookup seams (R1): startup and the watchdog share them so tests
	// can drive address changes, override loss and transient lookup failures.
	interfaceByName = net.InterfaceByName
	ifaceAddrs      = func(ifi *net.Interface) ([]net.Addr, error) { return ifi.Addrs() }
	ifaceIPv4Lookup = ifaceIPv4
)

// maxTransientErrors bounds how many consecutive transient read errors a reader
// tolerates before treating the socket as permanently broken. Go already
// retries EINTR/EAGAIN internally in most cases; this is defensive hardening so
// a socket that keeps surfacing them cannot spin a reader at 100% CPU.
const maxTransientErrors = 100

// transientBackoff is the context-abortable pause between transient-error
// retries.
const transientBackoff = 2 * time.Millisecond

// maxWatchFailures bounds how many consecutive lookup failures the LAN watchdog
// tolerates before giving up. Definitive changes (address lost or changed,
// interface down or recreated) are reported immediately.
const maxWatchFailures = 3

// isTransient reports whether a read error is a retryable interruption that
// must not tear down the relay. The net package already retries most of these
// internally; handling them here keeps a stray EINTR/EAGAIN from being treated
// as a fatal link failure.
func isTransient(err error) bool {
	return errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN)
}

// sleepCtx waits for d or until ctx is done; it returns false when cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// errorLimiter rate-limits error log lines for one direction and aggregates the
// suppressed errors. It is safe for concurrent use: the two LAN-facing readers
// both report peer-write failures through sendPeer.
type errorLimiter struct {
	mu      sync.Mutex
	every   time.Duration
	last    time.Time
	since   uint64
	lastErr error
}

// record counts one error and returns the log line to emit, if one is due. The
// first error is always logged; subsequent errors inside every are suppressed.
func (l *errorLimiter) record(now time.Time, err error) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.since++
	l.lastErr = err
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		return "", false
	}
	line := l.lineLocked()
	l.since = 0
	l.last = now
	return line, true
}

// flush emits a pending summary even if no line is due yet, so errors
// accumulated just before shutdown are not lost.
func (l *errorLimiter) flush() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.since == 0 {
		return "", false
	}
	line := l.lineLocked()
	l.since = 0
	return line, true
}

func (l *errorLimiter) lineLocked() string {
	if l.since <= 1 {
		return l.lastErr.Error()
	}
	return fmt.Sprintf("%v (%d errors since last report)", l.lastErr, l.since)
}

// lanMismatch marks a definitive LAN state change. Lookup errors are returned
// as plain errors and tolerated briefly by the watchdog.
type lanMismatch struct{ reason string }

func (e *lanMismatch) Error() string { return e.reason }

// checkLAN compares the current LAN interface state with what Run captured at
// startup: interface index, flags (up/running) and the address. A definitive
// change is returned as *lanMismatch; a lookup failure is returned as a plain
// error for the watchdog to tolerate briefly. With an explicit -lanip the
// membership of the override address on the interface is checked instead of the
// preferred address.
func checkLAN(cfg Config, lan net.IP, wantIndex int) error {
	ifi, err := interfaceByName(cfg.LanIf)
	if err != nil {
		return fmt.Errorf("interface %s: %w", cfg.LanIf, err)
	}
	if ifi.Index != wantIndex {
		return &lanMismatch{fmt.Sprintf("interface %s recreated (index %d -> %d)", cfg.LanIf, wantIndex, ifi.Index)}
	}
	if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagRunning == 0 {
		return &lanMismatch{fmt.Sprintf("interface %s is down (flags %s)", cfg.LanIf, ifi.Flags)}
	}
	if cfg.LanIP != "" {
		ok, err := ifaceHasIPv4(ifi, lan)
		if err != nil {
			return fmt.Errorf("interface %s: %w", cfg.LanIf, err)
		}
		if !ok {
			return &lanMismatch{fmt.Sprintf("override address %s no longer on %s", cfg.LanIP, cfg.LanIf)}
		}
		return nil
	}
	cur, _, _, err := ifaceIPv4Lookup(cfg.LanIf)
	if err != nil {
		var noIPv4 *NoIPv4Error
		if errors.As(err, &noIPv4) {
			return &lanMismatch{fmt.Sprintf("interface %s lost its IPv4 address", cfg.LanIf)}
		}
		return fmt.Errorf("interface %s: %w", cfg.LanIf, err)
	}
	if !cur.Equal(lan) {
		return &lanMismatch{fmt.Sprintf("LAN address changed %s -> %s", lan, cur)}
	}
	return nil
}

// bindLinkWithRetry binds the peer link socket, retrying only while the address
// is not yet available (EADDRNOTAVAIL), e.g. the overlay is still coming up.
// Every other bind error is fatal at once. The wait is context-abortable.
func bindLinkWithRetry(ctx context.Context, cfg Config, bind string) (*net.UDPConn, error) {
	var lastErr error
	for i := 0; i < cfg.retryAttempts; i++ {
		conn, err := newLinkConn(bind, cfg.PeerPort)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return nil, fmt.Errorf("relay: link socket: %w", err)
		}
		cfg.Logf("relay: link socket bind: %v (attempt %d/%d, retrying in %s)", err, i+1, cfg.retryAttempts, cfg.retryDelay)
		if !sleepCtx(ctx, cfg.retryDelay) {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("link socket bind failed")
	}
	return nil, fmt.Errorf("relay: link socket: %w", lastErr)
}

// Run starts the relay and blocks until ctx is cancelled or a fatal error
// occurs. The LAN address is retried for up to a minute, because on boot the
// interface may not have an address yet and the overlay route to the peer may
// not exist.
//
// A fatal error in any reader (a closed or broken socket that is not the result
// of cancellation) is propagated back to the caller so the supervisor
// (systemd/procd) restarts the process instead of leaving one direction
// silently dead. A LAN watchdog re-checks the interface address, index and
// flags periodically and reports a change the same way (R1).
func Run(ctx context.Context, cfg Config) error {
	// One shared normalization + validation boundary: the CLI, the config-file
	// loader and Run all call exactly these two methods, so the "check"
	// subcommand reports the same verdict Run would. Validate is pure (no
	// socket, no interface lookup); the network-dependent checks stay below.
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// flushErrLogs is assigned once the limiters exist below. The shutdown
	// defer calls it only after wg.Wait has joined every reader, so a write
	// failure recorded by an in-flight reader while shutting down still
	// reaches the final pending summary. It is a no-op on the early-return
	// paths that run before the limiters are created.
	flushErrLogs := func() {}
	// Registered first so it runs last: after the sockets below have been
	// closed, every reader goroutine has been joined, and the final pending
	// error summary has been flushed.
	defer func() {
		wg.Wait()
		flushErrLogs()
	}()
	defer cancel()

	l2p, err := ParseMode(string(cfg.Lan2Peer))
	if err != nil {
		return err
	}
	p2l, err := ParseMode(string(cfg.Peer2LAN))
	if err != nil {
		return err
	}
	peerIP := net.ParseIP(cfg.Peer).To4()
	if peerIP == nil {
		return fmt.Errorf("relay: peer %q is not an IPv4 address", cfg.Peer)
	}
	linkBind := cfg.LinkBind
	if linkBind != "" && net.ParseIP(linkBind).To4() == nil {
		return fmt.Errorf("relay: linkbind %q is not an IPv4 address", linkBind)
	}
	peerAddr := &net.UDPAddr{IP: peerIP, Port: cfg.PeerPort}
	group := mcastDest

	// lan is either the explicit override or the interface's preferred IPv4.
	// The former lanNet computation was dropped (finding 10): the mask taken
	// from a different address on the interface was only used in the log line
	// and was plainly wrong for -lanip.
	var (
		lan     net.IP
		lanWarn string
		ifErr   error
	)
	if cfg.LanIP != "" {
		lan = net.ParseIP(cfg.LanIP).To4()
		if lan == nil {
			return fmt.Errorf("relay: lanip %q is not an IPv4 address", cfg.LanIP)
		}
	} else {
		for i := 0; i < cfg.retryAttempts; i++ {
			if lan, _, lanWarn, ifErr = ifaceIPv4Lookup(cfg.LanIf); ifErr == nil {
				break
			}
			cfg.Logf("lan: %v (attempt %d/%d, retrying in %s)", ifErr, i+1, cfg.retryAttempts, cfg.retryDelay)
			if !sleepCtx(ctx, cfg.retryDelay) {
				return ctx.Err()
			}
		}
		if ifErr != nil {
			return ifErr
		}
		// Emitted once here, never repeated on watchdog cycles (R5/R1).
		if lanWarn != "" {
			cfg.Logf("lan: interface %s: %s", cfg.LanIf, lanWarn)
		}
	}
	ifi, err := interfaceByName(cfg.LanIf)
	if err != nil {
		return err
	}

	// Two sockets on the mDNS port: the group socket carries multicast both
	// ways, the address socket carries unicast addressed to us (QU responses).
	// Keeping them apart means the Lan2Peer filter only ever sees genuine
	// multicast chatter.
	mcast, err := newMcastConn(lan)
	if err != nil {
		return fmt.Errorf("relay: multicast socket: %w", err)
	}
	defer mcast.Close()
	uni, err := newUniConn(lan)
	if err != nil {
		return fmt.Errorf("relay: unicast socket %s:%d: %w", lan, mdnsPort, err)
	}
	defer uni.Close()
	link, err := bindLinkWithRetry(ctx, cfg, linkBind)
	if err != nil {
		return err
	}
	defer link.Close()

	// Coordinated shutdown, registered after the socket defers so it runs
	// first: cancel the internal context so the readers treat the imminent read
	// errors as a normal stop, close the sockets to unblock the reads, then
	// let the deferred wg.Wait join every goroutine.
	defer func() {
		cancel()
		_ = mcast.Close()
		_ = uni.Close()
		_ = link.Close()
	}()

	// No connected socket towards the peer: on boot the overlay route may not
	// exist yet and a connected socket would fail permanently (and has done).
	lb := linkBind
	if lb == "" {
		lb = "0.0.0.0"
	}
	cfg.Logf("relay: lan=%s (%s) ifindex=%d peer=%s:%d lan2peer=%s peer2lan=%s services=%d linkbind=%s",
		cfg.LanIf, lan, ifi.Index, peerIP, cfg.PeerPort, l2p, p2l, len(cfg.Services), lb)

	cnt := &counters{}
	// One rate-limited logger per write direction (R2). The two LAN-facing
	// readers share peerErr through sendPeer, so it must be concurrency-safe.
	peerErr := &errorLimiter{every: cfg.errorLogInterval}
	lanErr := &errorLimiter{every: cfg.errorLogInterval}
	base := Status{
		LanIf:    cfg.LanIf,
		LanIP:    lan.String(),
		Peer:     peerIP.String(),
		PeerPort: cfg.PeerPort,
		Lan2Peer: l2p,
		Peer2LAN: p2l,
		Services: cfg.Services,
		Started:  time.Now().UTC().Format(time.RFC3339),
	}

	// errc carries a fatal reader/status/watchdog failure out of Run. Sends are
	// non-blocking: the first error wins and the rest are dropped, so no
	// goroutine can ever block on a full channel while shutting down.
	errc := make(chan error, 3)
	report := func(err error) {
		select {
		case errc <- err:
		default:
		}
	}

	if addr := cfg.StatusAddr; addr != "" && addr != "off" {
		// Bind synchronously so a startup failure (port in use, bad address)
		// fails Run instead of being logged away in a goroutine.
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("relay: status server listen %s: %w", addr, err)
		}
		srv := &http.Server{Handler: statusHandler(cnt, base), ReadHeaderTimeout: 5 * time.Second}
		cfg.Logf("status: http://%s/_status", addr)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				report(fmt.Errorf("relay: status server: %w", err))
			}
		}()
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
	}

	dd := NewDedupe(cfg.DedupeTTL)
	logf := cfg.Logf

	sendPeer := func(pkt []byte, src net.IP, srcPort int, filter bool) {
		// Reject anything that cannot be a DNS/mDNS packet before it reaches
		// the dedupe map or the wire (finding 6).
		if len(pkt) < dnsHeaderLen {
			cnt.toPeerDrop.Add(1)
			return
		}
		if filter {
			if l2p == ModeQuery && !IsQuery(pkt) {
				cnt.toPeerDrop.Add(1)
				return
			}
			if !ServiceAllowed(pkt, cfg.Services) {
				cnt.toPeerDrop.Add(1)
				return
			}
		}
		// Dedupe runs after the filters and the length check so a filtered or
		// malformed packet is never recorded as "seen" (finding 7).
		if dd.IsDuplicate(pkt) {
			cnt.dedupe.Add(1)
			return
		}
		if _, err := writeLink(link, pkt, peerAddr); err != nil {
			cnt.errors.Add(1)
			if line, ok := peerErr.record(time.Now(), err); ok {
				logf("peer write: %s", line)
			}
			return
		}
		cnt.toPeerPkts.Add(1)
		cnt.toPeerBytes.Add(uint64(len(pkt)))
		if cfg.Verbose {
			logf("->peer %dB from %s:%d query=%v", len(pkt), src, srcPort, IsQuery(pkt))
		}
	}

	// Multicast from the local LAN -> peer (filtered). Each reader owns its own
	// buffer: the three goroutines read concurrently and must never share a
	// slice, or packets racing in from different sources would overwrite one
	// another before being copied (finding 1).
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 9000)
		streak := 0
		for {
			n, src, err := readMcast(mcast, buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isTransient(err) {
					streak++
					if streak >= maxTransientErrors {
						report(fmt.Errorf("relay: mcast read: %w (after %d consecutive transient errors)", err, streak))
						return
					}
					if !sleepCtx(ctx, transientBackoff) {
						return
					}
					continue
				}
				report(fmt.Errorf("relay: mcast read: %w", err))
				return
			}
			streak = 0
			if src.IP.Equal(lan) {
				continue // our own injected multicast
			}
			sendPeer(append([]byte(nil), buf[:n]...), src.IP, src.Port, true)
		}
	}()

	// Unicast addressed to our LAN address -> peer (never filtered).
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 9000)
		streak := 0
		for {
			n, src, err := readUni(uni, buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isTransient(err) {
					streak++
					if streak >= maxTransientErrors {
						report(fmt.Errorf("relay: uni read: %w (after %d consecutive transient errors)", err, streak))
						return
					}
					if !sleepCtx(ctx, transientBackoff) {
						return
					}
					continue
				}
				report(fmt.Errorf("relay: uni read: %w", err))
				return
			}
			streak = 0
			sendPeer(append([]byte(nil), buf[:n]...), src.IP, src.Port, false)
		}
	}()

	// Peer -> local multicast.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 9000)
		streak := 0
		for {
			n, src, err := readLink(link, buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isTransient(err) {
					streak++
					if streak >= maxTransientErrors {
						report(fmt.Errorf("relay: link read: %w (after %d consecutive transient errors)", err, streak))
						return
					}
					if !sleepCtx(ctx, transientBackoff) {
						return
					}
					continue
				}
				report(fmt.Errorf("relay: link read: %w", err))
				return
			}
			streak = 0
			if !src.IP.Equal(peerIP) {
				continue // only the configured peer
			}
			if n < dnsHeaderLen {
				cnt.toLANDrop.Add(1)
				continue
			}
			pkt := append([]byte(nil), buf[:n]...)
			if p2l == ModeQuery && !IsQuery(pkt) {
				cnt.toLANDrop.Add(1)
				continue
			}
			// Dedupe after the filter and the length check (finding 7).
			if dd.IsDuplicate(pkt) {
				cnt.dedupe.Add(1)
				continue
			}
			if _, err := writeMcast(mcast, pkt, group); err != nil {
				cnt.errors.Add(1)
				if line, ok := lanErr.record(time.Now(), err); ok {
					logf("lan multicast: %s", line)
				}
				continue
			}
			cnt.toLANPkts.Add(1)
			cnt.toLANBytes.Add(uint64(n))
			if cfg.Verbose {
				logf("->lan %dB (peer) query=%v", n, IsQuery(pkt))
			}
		}
	}()

	// LAN watchdog (R1): open UDP sockets do not report a read error when the
	// interface loses its address, is recreated or goes down, so Run would keep
	// running as a half-dead instance the supervisor never restarts. Poll the
	// interface index, flags and address and report a definitive change.
	// Documented gap: a down/up cycle shorter than watchInterval is not seen.
	// On the tested kernel (Linux 6.12, isolated netns) such a cycle with an
	// unchanged IP kept the IPv4 multicast membership and address and packet
	// flow continued, so no detection was needed there
	// (docs/network-flap-experiment.md). That is an observation on the tested
	// kernel, not a general guarantee.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(cfg.watchInterval)
		defer t.Stop()
		fails := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				err := checkLAN(cfg, lan, ifi.Index)
				if err == nil {
					fails = 0
					continue
				}
				var mm *lanMismatch
				if errors.As(err, &mm) {
					report(fmt.Errorf("relay: lan watchdog: %w", err))
					return
				}
				fails++
				if fails >= maxWatchFailures {
					report(fmt.Errorf("relay: lan watchdog: %w (after %d failed checks)", err, fails))
					return
				}
			}
		}
	}()

	if cfg.Stats > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(cfg.Stats)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					st := cnt.snapshot(base)
					logf("stats: ->peer %d pkt/%d B (dropped %d) | ->lan %d pkt/%d B (dropped %d) | dedupe %d | errors %d",
						st.ToPeer.Packets, st.ToPeer.Bytes, st.ToPeer.Dropped,
						st.ToLAN.Packets, st.ToLAN.Bytes, st.ToLAN.Dropped, st.Dedupe, st.Errors)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Pending error summaries are flushed from the shutdown defer, after every
	// reader has been joined, so a write failure that an in-flight reader
	// records during shutdown still reaches the summary. Flushing here in the
	// main select (before the coordinated cancel/close/join) lost exactly
	// those errors.
	flushErrLogs = func() {
		if line, ok := peerErr.flush(); ok {
			logf("peer write: %s", line)
		}
		if line, ok := lanErr.flush(); ok {
			logf("lan multicast: %s", line)
		}
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

func logStd(format string, args ...any) { log.Printf(format, args...) }
