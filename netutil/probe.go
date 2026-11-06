package netutil

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"syscall"
	"time"
)

// probePorts are common ports that phones, laptops, NAS boxes and servers
// either serve or actively refuse. Either answer proves a host is there.
var probePorts = []string{"22", "80", "443", "445", "62078"}

// aliveOnPort reports whether a TCP connect to host:port shows a live host.
// A completed connection or a refusal (RST) both mean someone answered. A
// timeout or "no route" means nobody did.
func aliveOnPort(ctx context.Context, host, port string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err == nil {
		_ = conn.Close()
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

// probeHosts returns the hosts that answered a TCP connect on any of ports.
// It does not depend on the operating system's neighbor table, which macOS
// can hide from a command-line program.
func probeHosts(ctx context.Context, hosts, ports []string, timeout time.Duration) []string {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		alive []string
		sem   = make(chan struct{}, 128)
	)
	for _, h := range hosts {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, p := range ports {
				if ctx.Err() != nil {
					return
				}
				if aliveOnPort(ctx, host, p, timeout) {
					mu.Lock()
					alive = append(alive, host)
					mu.Unlock()
					return
				}
			}
		}(h)
	}
	wg.Wait()
	return alive
}

// mergeNeighbors combines ARP entries (which carry a MAC) with hosts found by
// probing (which do not), one entry per address, sorted by address.
func mergeNeighbors(arp []Neighbor, alive []string) []Neighbor {
	byIP := map[string]Neighbor{}
	for _, n := range arp {
		byIP[n.IP] = n
	}
	for _, ip := range alive {
		if _, ok := byIP[ip]; !ok {
			byIP[ip] = Neighbor{IP: ip}
		}
	}
	out := make([]Neighbor, 0, len(byIP))
	for _, n := range byIP {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(net.ParseIP(out[i].IP).To4()) < string(net.ParseIP(out[j].IP).To4())
	})
	return out
}
