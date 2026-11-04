//go:build !linux

package relay

import (
	"errors"
	"net"
)

func listenUDP4(addr string, port int, ctl func(fd uintptr) error) (*net.UDPConn, error) {
	return nil, errUnsupported
}

func mcastSocket(lan net.IP) (*net.UDPConn, error) { return nil, errUnsupported }

func uniSocket(lan net.IP) (*net.UDPConn, error) { return nil, errUnsupported }

func linkSocket(port int) (*net.UDPConn, error) { return nil, errUnsupported }

var errUnsupported = errors.New("relay: only Linux is supported (multicast socket options)")
