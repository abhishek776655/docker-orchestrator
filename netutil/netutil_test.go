package netutil

import (
	"net"
	"testing"
)

func TestSelectLANIPv4PrefersPrivateAddress(t *testing.T) {
	got := selectLANIPv4([]net.IP{
		net.ParseIP("169.254.2.3"),
		net.ParseIP("203.0.113.9"),
		net.ParseIP("192.168.1.25"),
	})
	if got != "192.168.1.25" {
		t.Fatalf("selected address = %q, want private LAN address", got)
	}
}

func TestSelectLANIPv4RejectsNonIPv4AndLinkLocal(t *testing.T) {
	got := selectLANIPv4([]net.IP{net.ParseIP("fe80::1"), net.ParseIP("169.254.2.3")})
	if got != "" {
		t.Fatalf("selected address = %q, want none", got)
	}
}
