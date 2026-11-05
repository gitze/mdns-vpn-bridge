//go:build linux

package relay

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// ipMulticastAll is IP_MULTICAST_ALL (linux/include/uapi/linux/in.h). The
// syscall package does not export it on every architecture, so it is spelled
// out here rather than pulling in golang.org/x/sys/unix.
const ipMulticastAll = 49

// listenUDP4 opens a UDP4 socket bound to addr:port with the given raw socket
// options applied before bind.
func listenUDP4(addr string, port int, ctl func(fd uintptr) error) (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) { serr = ctl(fd) }); err != nil {
				return err
			}
			return serr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("%s:%d", addr, port))
	if err != nil {
		return nil, err
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, fmt.Errorf("relay: ListenPacket did not return a UDPConn")
	}
	return uc, nil
}

// mcastSocket binds the mDNS multicast group on the LAN interface and prepares
// the socket for sending: multicast goes out with source port 5353 and TTL 255,
// which mDNS peers expect.
func mcastSocket(lan net.IP) (*net.UDPConn, error) {
	return listenUDP4("224.0.0.251", mdnsPort, func(fd uintptr) error {
		f := int(fd)
		if err := syscall.SetsockoptInt(f, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
			return err
		}
		// Deliver only the group traffic that arrived on the interface we
		// joined. Without IP_MULTICAST_ALL=0 Linux hands the socket mDNS of
		// every interface that has joined 224.0.0.251 (e.g. avahi in a guest
		// VLAN), which would leak across the tunnel (finding 2).
		if err := syscall.SetsockoptInt(f, syscall.IPPROTO_IP, ipMulticastAll, 0); err != nil {
			return fmt.Errorf("IP_MULTICAST_ALL=0: %w", err)
		}
		var lanA [4]byte
		copy(lanA[:], lan.To4())
		mreq := &syscall.IPMreq{}
		copy(mreq.Multiaddr[:], []byte{224, 0, 0, 251})
		copy(mreq.Interface[:], lanA[:])
		if err := syscall.SetsockoptIPMreq(f, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq); err != nil {
			return fmt.Errorf("IP_ADD_MEMBERSHIP on %s: %w", lan, err)
		}
		if err := syscall.SetsockoptInet4Addr(f, syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, lanA); err != nil {
			return err
		}
		// IP_MULTICAST_LOOP is deliberately left at its default (on): the
		// own-echo filter (src.IP.Equal(lan)) depends on seeing our own
		// injected multicast.
		return syscall.SetsockoptInt(f, syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 255)
	})
}

// uniSocket binds our LAN address on the mDNS port. It receives unicast
// traffic addressed to us (QU responses and unicast queries), which must
// bypass the Lan2Peer filter.
func uniSocket(lan net.IP) (*net.UDPConn, error) {
	return listenUDP4(lan.String(), mdnsPort, func(fd uintptr) error {
		return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
}

// linkSocket binds the unicast peer link. An empty bind address keeps the
// historic behaviour of listening on every interface (0.0.0.0); a set address
// narrows what the OS delivers to this socket. That is a routing/hygiene aid,
// not authentication.
func linkSocket(bind string, port int) (*net.UDPConn, error) {
	if bind == "" {
		bind = "0.0.0.0"
	}
	return listenUDP4(bind, port, func(fd uintptr) error {
		return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
}
