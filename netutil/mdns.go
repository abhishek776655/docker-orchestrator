package netutil

import (
	"context"
	"net"
	"time"

	"github.com/miekg/dns"
)

// mdnsNames are service names that Apple devices, printers, TVs, NAS boxes and
// most Linux machines running Avahi answer for on the local network.
var mdnsNames = []string{
	"_services._dns-sd._udp.local.",
	"_workstation._tcp.local.",
	"_device-info._tcp.local.",
	"_airplay._tcp.local.",
	"_companion-link._tcp.local.",
}

// mdnsQuery builds a multicast-DNS PTR query. The top bit of the class asks
// responders to answer straight back to this program instead of multicasting.
func mdnsQuery(name string) ([]byte, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypePTR)
	m.RecursionDesired = false
	m.Question[0].Qclass = dns.ClassINET | 1<<15
	return m.Pack()
}

// mdnsHosts asks the local network "who is here?" over multicast DNS and
// returns the IPv4 address of every device that answers, within prefix (the
// first three octets of this host's /24, like "192.168.1."). It needs no
// access to the neighbor table.
func mdnsHosts(ctx context.Context, prefix string, wait time.Duration) []string {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil
	}
	defer conn.Close()
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	for round := 0; round < 2; round++ {
		for _, name := range mdnsNames {
			if pkt, err := mdnsQuery(name); err == nil {
				_, _ = conn.WriteToUDP(pkt, group)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	seen := map[string]bool{}
	var out []string
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		_, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		ip := from.IP.To4()
		if ip == nil {
			continue
		}
		s := ip.String()
		if len(s) > len(prefix) && s[:len(prefix)] == prefix && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
