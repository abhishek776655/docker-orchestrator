package netutil

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// lanHosts lists every other host address in the /24 around ip, the usual
// home or lab subnet. It never goes wider than a /24, so one sweep stays at
// 253 hosts even on a /16.
func lanHosts(ip net.IP) []string {
	v4 := ip.To4()
	if v4 == nil {
		return nil
	}
	hosts := make([]string, 0, 253)
	for last := 1; last <= 254; last++ {
		if byte(last) == v4[3] {
			continue
		}
		hosts = append(hosts, fmt.Sprintf("%d.%d.%d.%d", v4[0], v4[1], v4[2], last))
	}
	return hosts
}

// subnetPrefix turns "192.168.1.11" into "192.168.1." for matching addresses
// in the same /24.
func subnetPrefix(ip string) string {
	for i := len(ip) - 1; i >= 0; i-- {
		if ip[i] == '.' {
			return ip[:i+1]
		}
	}
	return ip
}

// nudgeHosts sends one tiny UDP datagram to every host. Sending forces this
// machine to ask who owns each address (ARP), so live devices answer and land
// in the neighbor table. Nothing is expected back and failures are ignored.
func nudgeHosts(ctx context.Context, hosts []string) {
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for _, h := range hosts {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()
			d := net.Dialer{Timeout: 300 * time.Millisecond}
			conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(host, "9"))
			if err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
			_, _ = conn.Write([]byte{0})
			_ = conn.Close()
		}(h)
	}
	wg.Wait()
}

// Discover finds other devices on this host's local /24. It nudges every
// address so the neighbor table fills, and at the same time probes a few
// common TCP ports and asks over multicast DNS, so devices are found even
// where the OS hides the neighbor table from this program. The result is the
// union: addresses from the table (with a MAC when readable) plus any host
// that answered a probe or announced itself.
func Discover(ctx context.Context) ([]Neighbor, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	local, err := DetectLocalIP()
	if err != nil {
		return nil, err
	}
	hosts := lanHosts(net.ParseIP(local))

	var wg sync.WaitGroup
	var probed, announced []string
	wg.Add(3)
	go func() { defer wg.Done(); nudgeHosts(ctx, hosts) }()
	go func() { defer wg.Done(); probed = probeHosts(ctx, hosts, probePorts, 250*time.Millisecond) }()
	go func() { defer wg.Done(); announced = mdnsHosts(ctx, subnetPrefix(local), 1500*time.Millisecond) }()
	wg.Wait()
	alive := append(probed, announced...)
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
	}

	arp, _ := readARP(ctx) // the table may be hidden or missing; probing still counts
	merged := mergeNeighbors(arp, alive)
	resolveNames(ctx, merged)
	return merged, nil
}
