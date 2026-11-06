package netutil

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Neighbor is another device this host has recently talked to on its local
// network, as listed by the operating system's ARP table.
type Neighbor struct {
	IP       string
	MAC      string
	Hostname string // empty when neither arp nor reverse DNS knows one
}

// arpLine matches one entry of `arp -an`, on macOS and Linux alike:
//
//	? (192.168.1.6) at d6:98:bd:54:c5:73 on en0 ifscope [ethernet]
//	host.local (192.168.1.5) at aa:bb:cc:dd:ee:ff [ether] on eth0
var arpLine = regexp.MustCompile(`^(\S+) \((\d{1,3}(?:\.\d{1,3}){3})\) at (\S+)`)

// ParseARP extracts usable neighbors from `arp -an` output. It skips
// incomplete entries (no MAC yet), broadcast and multicast addresses and
// IPv4 link-local addresses: none of those is a machine that could join.
func ParseARP(output string) []Neighbor {
	var out []Neighbor
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		m := arpLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		ip := net.ParseIP(m[2]).To4()
		mac := normalizeMAC(m[3])
		if ip == nil || mac == "" || mac == "ff:ff:ff:ff:ff:ff" {
			continue
		}
		if ip[0] >= 224 || ip[3] == 255 || (ip[0] == 169 && ip[1] == 254) {
			continue
		}
		if seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		host := m[1]
		if host == "?" {
			host = ""
		}
		out = append(out, Neighbor{IP: ip.String(), MAC: mac, Hostname: host})
	}
	sort.Slice(out, func(i, j int) bool {
		return string(net.ParseIP(out[i].IP).To4()) < string(net.ParseIP(out[j].IP).To4())
	})
	return out
}

// normalizeMAC zero-pads each byte (macOS prints 96:c:54:9:3e:87) and returns
// "" for anything that is not a six-byte hardware address.
func normalizeMAC(raw string) string {
	parts := strings.Split(raw, ":")
	if len(parts) != 6 {
		return ""
	}
	for i, p := range parts {
		if len(p) == 0 || len(p) > 2 {
			return ""
		}
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	return strings.ToLower(strings.Join(parts, ":"))
}

// Neighbors lists the devices in this host's ARP table, with a best-effort
// reverse-DNS name for any the table gives no name for. The ARP table only
// holds machines this host has recently exchanged traffic with. Discover
// adds an active sweep on top of this.
func Neighbors(ctx context.Context) ([]Neighbor, error) {
	neighbors, err := readARP(ctx)
	if err != nil {
		return nil, err
	}
	resolveNames(ctx, neighbors)
	return neighbors, nil
}

// readARP runs `arp -an` and parses it, without any name lookups.
func readARP(ctx context.Context) ([]Neighbor, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "arp", "-an").Output()
	if err != nil {
		return nil, fmt.Errorf("read ARP table: %w", err)
	}
	return ParseARP(string(out)), nil
}

func resolveNames(ctx context.Context, ns []Neighbor) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for i := range ns {
		if ns[i].Hostname != "" {
			continue
		}
		wg.Add(1)
		go func(n *Neighbor) {
			defer wg.Done()
			names, err := net.DefaultResolver.LookupAddr(ctx, n.IP)
			if err == nil && len(names) > 0 {
				n.Hostname = strings.TrimSuffix(names[0], ".")
			}
		}(&ns[i])
	}
	wg.Wait()
}

// LocalIPs returns every IPv4 address assigned to this host's interfaces.
func LocalIPs() map[string]bool {
	ips := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if v4 := ipnet.IP.To4(); v4 != nil {
				ips[v4.String()] = true
			}
		}
	}
	return ips
}
