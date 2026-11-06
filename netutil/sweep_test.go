package netutil

import (
	"net"
	"testing"
)

func TestLanHostsCoversTheSubnetExceptSelf(t *testing.T) {
	hosts := lanHosts(net.ParseIP("192.168.1.11"))
	if len(hosts) != 253 {
		t.Fatalf("got %d hosts, want 253", len(hosts))
	}
	if hosts[0] != "192.168.1.1" || hosts[len(hosts)-1] != "192.168.1.254" {
		t.Fatalf("range = %s..%s, want 192.168.1.1..192.168.1.254", hosts[0], hosts[len(hosts)-1])
	}
	for _, h := range hosts {
		if h == "192.168.1.11" {
			t.Fatal("own address must be skipped")
		}
	}
}

func TestLanHostsRejectsIPv6(t *testing.T) {
	if hosts := lanHosts(net.ParseIP("fe80::1")); hosts != nil {
		t.Fatalf("got %v, want nil for a non-IPv4 address", hosts)
	}
}
