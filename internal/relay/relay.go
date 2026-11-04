package relay

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

const mdnsPort = 5353

// Stats holds the relay counters.
type Stats struct {
	ToPeerPkts, ToPeerBytes uint64
	ToLANPkts, ToLANBytes   uint64
	DropToPeer, DropToLAN   uint64
}

// Run starts the relay and blocks until ctx is cancelled or a fatal error
// occurs. The LAN address is retried for up to a minute, because on boot the
// interface may not have an address yet and the overlay route to the peer may
// not exist.
func Run(ctx context.Context, cfg Config) error {
	cfg.defaults()
	if cfg.LanIf == "" || cfg.Peer == "" {
		return fmt.Errorf("relay: LanIf and Peer are required")
	}
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
	peerAddr := &net.UDPAddr{IP: peerIP, Port: cfg.PeerPort}
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}

	var lan net.IP
	var lanNet *net.IPNet
	if cfg.LanIP != "" {
		lan = net.ParseIP(cfg.LanIP).To4()
		if lan == nil {
			return fmt.Errorf("relay: lanip %q is not an IPv4 address", cfg.LanIP)
		}
		_, n, err := ifaceIPv4(cfg.LanIf)
		if err != nil {
			return err
		}
		lanNet = &net.IPNet{IP: lan.Mask(n.Mask), Mask: n.Mask}
	} else {
		for i := 0; i < cfg.retryAttempts; i++ {
			if lan, lanNet, err = ifaceIPv4(cfg.LanIf); err == nil {
				break
			}
			cfg.Logf("lan: %v (attempt %d/%d, retrying in %s)", err, i+1, cfg.retryAttempts, cfg.retryDelay)
			select {
			case <-time.After(cfg.retryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err != nil {
			return err
		}
	}
	ifi, err := net.InterfaceByName(cfg.LanIf)
	if err != nil {
		return err
	}

	// Two sockets on the mDNS port: the group socket carries multicast both
	// ways, the address socket carries unicast addressed to us (QU responses).
	// Keeping them apart means the Lan2Peer filter only ever sees genuine
	// multicast chatter.
	mcast, err := mcastSocket(lan)
	if err != nil {
		return fmt.Errorf("relay: multicast socket: %w", err)
	}
	defer mcast.Close()
	uni, err := uniSocket(lan)
	if err != nil {
		return fmt.Errorf("relay: unicast socket %s:%d: %w", lan, mdnsPort, err)
	}
	defer uni.Close()
	link, err := linkSocket(cfg.PeerPort)
	if err != nil {
		return fmt.Errorf("relay: link socket: %w", err)
	}
	defer link.Close()

	// No connected socket towards the peer: on boot the overlay route may not
	// exist yet and a connected socket would fail permanently (and has done).
	cfg.Logf("relay: lan=%s (%s, net %s) ifindex=%d peer=%s:%d lan2peer=%s peer2lan=%s services=%d",
		cfg.LanIf, lan, lanNet, ifi.Index, peerIP, cfg.PeerPort, l2p, p2l, len(cfg.Services))

	var s Stats
	dd := NewDedupe(cfg.DedupeTTL)
	buf := make([]byte, 9000)
	logf := cfg.Logf

	sendPeer := func(pkt []byte, src net.IP, srcPort int, filter bool) {
		if dd.IsDuplicate(pkt) {
			return
		}
		if filter {
			if l2p == ModeQuery && !IsQuery(pkt) {
				atomic.AddUint64(&s.DropToPeer, 1)
				return
			}
			if !ServiceAllowed(pkt, cfg.Services) {
				atomic.AddUint64(&s.DropToPeer, 1)
				return
			}
		}
		if _, err := link.WriteToUDP(pkt, peerAddr); err != nil {
			logf("peer write: %v", err)
			return
		}
		atomic.AddUint64(&s.ToPeerPkts, 1)
		atomic.AddUint64(&s.ToPeerBytes, uint64(len(pkt)))
		if cfg.Verbose {
			logf("->peer %dB from %s:%d query=%v", len(pkt), src, srcPort, IsQuery(pkt))
		}
	}

	// Multicast from the local LAN -> peer (filtered).
	go func() {
		for {
			n, src, err := mcast.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logf("mcast read: %v", err)
				return
			}
			if src.IP.Equal(lan) {
				continue // our own injected multicast
			}
			sendPeer(append([]byte(nil), buf[:n]...), src.IP, src.Port, true)
		}
	}()

	// Unicast addressed to our LAN address -> peer (never filtered).
	go func() {
		for {
			n, src, err := uni.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logf("uni read: %v", err)
				return
			}
			sendPeer(append([]byte(nil), buf[:n]...), src.IP, src.Port, false)
		}
	}()

	// Peer -> local multicast.
	go func() {
		for {
			n, src, err := link.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logf("link read: %v", err)
				return
			}
			if !src.IP.Equal(peerIP) {
				continue // only the configured peer
			}
			pkt := append([]byte(nil), buf[:n]...)
			if dd.IsDuplicate(pkt) {
				continue
			}
			if p2l == ModeQuery && !IsQuery(pkt) {
				atomic.AddUint64(&s.DropToLAN, 1)
				continue
			}
			if _, err := mcast.WriteToUDP(pkt, group); err != nil {
				logf("lan multicast: %v", err)
				continue
			}
			atomic.AddUint64(&s.ToLANPkts, 1)
			atomic.AddUint64(&s.ToLANBytes, uint64(n))
			if cfg.Verbose {
				logf("->lan %dB (peer) query=%v", n, IsQuery(pkt))
			}
		}
	}()

	if cfg.Stats > 0 {
		go func() {
			t := time.NewTicker(cfg.Stats)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					logf("stats: ->peer %d pkt/%d B (dropped %d) | ->lan %d pkt/%d B (dropped %d)",
						atomic.LoadUint64(&s.ToPeerPkts), atomic.LoadUint64(&s.ToPeerBytes), atomic.LoadUint64(&s.DropToPeer),
						atomic.LoadUint64(&s.ToLANPkts), atomic.LoadUint64(&s.ToLANBytes), atomic.LoadUint64(&s.DropToLAN))
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	<-ctx.Done()
	return nil
}

func logStd(format string, args ...any) { log.Printf(format, args...) }
