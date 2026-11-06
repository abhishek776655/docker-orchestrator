package netutil

import (
	"testing"

	"github.com/miekg/dns"
)

func TestMdnsQueryAsksForUnicastReplies(t *testing.T) {
	pkt, err := mdnsQuery("_services._dns-sd._udp.local.")
	if err != nil {
		t.Fatal(err)
	}
	var m dns.Msg
	if err := m.Unpack(pkt); err != nil {
		t.Fatal(err)
	}
	q := m.Question[0]
	if q.Name != "_services._dns-sd._udp.local." || q.Qtype != dns.TypePTR {
		t.Fatalf("question = %+v, want a PTR query for _services._dns-sd._udp.local.", q)
	}
	if q.Qclass&(1<<15) == 0 {
		t.Fatalf("qclass = %#x, want the unicast-response bit set", q.Qclass)
	}
}

func TestSubnetPrefix(t *testing.T) {
	if got := subnetPrefix("192.168.1.11"); got != "192.168.1." {
		t.Fatalf("subnetPrefix = %q, want 192.168.1.", got)
	}
}
