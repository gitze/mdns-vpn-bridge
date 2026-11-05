package relay

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// cidr builds a *net.IPNet address the way net.Interface.Addrs returns them.
func cidr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

func TestChooseIPv4(t *testing.T) {
	tests := []struct {
		name     string
		addrs    []string
		wantIP   string
		wantNet  string
		wantWarn bool
	}{
		{
			name:    "single routable address",
			addrs:   []string{"192.0.2.5/24"},
			wantIP:  "192.0.2.5",
			wantNet: "192.0.2.0/24",
		},
		{
			name:    "link-local skipped when a routable address exists",
			addrs:   []string{"169.254.10.20/16", "192.0.2.5/24"},
			wantIP:  "192.0.2.5",
			wantNet: "192.0.2.0/24",
		},
		{
			name:     "two routable addresses warn",
			addrs:    []string{"192.0.2.5/24", "192.168.1.9/24"},
			wantIP:   "192.0.2.5",
			wantNet:  "192.0.2.0/24",
			wantWarn: true,
		},
		{
			name:     "only link-local warns",
			addrs:    []string{"169.254.10.20/16"},
			wantIP:   "169.254.10.20",
			wantNet:  "169.254.0.0/16",
			wantWarn: true,
		},
		{
			name:    "loopback ignored",
			addrs:   []string{"127.0.0.1/8"},
			wantIP:  "",
			wantNet: "",
		},
		{
			name:    "ipv6 ignored",
			addrs:   []string{"fd00::1/64", "fe80::1/64"},
			wantIP:  "",
			wantNet: "",
		},
		{
			name:    "loopback mixed with routable",
			addrs:   []string{"127.0.0.1/8", "10.0.0.5/24"},
			wantIP:  "10.0.0.5",
			wantNet: "10.0.0.0/24",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var addrs []net.Addr
			for _, a := range tc.addrs {
				addrs = append(addrs, cidr(t, a))
			}
			ip, ipnet, warn := chooseIPv4(addrs)
			if tc.wantIP == "" {
				if ip != nil {
					t.Fatalf("expected no address, got %v", ip)
				}
				if warn != "" {
					t.Fatalf("expected no warning, got %q", warn)
				}
				return
			}
			if ip == nil || ip.String() != tc.wantIP {
				t.Fatalf("ip = %v, want %s", ip, tc.wantIP)
			}
			if ipnet == nil || ipnet.String() != tc.wantNet {
				t.Fatalf("net = %v, want %s", ipnet, tc.wantNet)
			}
			if tc.wantWarn && warn == "" {
				t.Fatalf("expected a warning, got none")
			}
			if !tc.wantWarn && warn != "" {
				t.Fatalf("unexpected warning %q", warn)
			}
		})
	}
}

// The ambiguous-choice warnings must point the operator at -lanip.
func TestChooseIPv4WarnMentionsLanip(t *testing.T) {
	_, _, warn := chooseIPv4([]net.Addr{
		cidr(t, "192.0.2.5/24"),
		cidr(t, "192.168.1.9/24"),
	})
	if !strings.Contains(warn, "-lanip") {
		t.Fatalf("warning %q does not mention -lanip", warn)
	}
	_, _, warn = chooseIPv4([]net.Addr{cidr(t, "169.254.10.20/16")})
	if !strings.Contains(warn, "-lanip") {
		t.Fatalf("link-local warning %q does not mention -lanip", warn)
	}
}

// ifaceIPv4 on the loopback interface must report NoIPv4Error, not a loopback IP.
func TestIfaceIPv4LoopbackRejected(t *testing.T) {
	ifname := "lo"
	if _, err := net.InterfaceByName(ifname); err != nil {
		t.Skipf("no %s interface: %v", ifname, err)
	}
	_, _, _, err := ifaceIPv4(ifname)
	var noIPv4 *NoIPv4Error
	if !errors.As(err, &noIPv4) {
		t.Fatalf("ifaceIPv4(%s) err = %v, want NoIPv4Error", ifname, err)
	}
	if !strings.Contains(err.Error(), ifname) {
		t.Fatalf("error %q does not name the interface", err)
	}
}
