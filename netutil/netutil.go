// Package netutil holds small host-networking helpers shared by the
// agent and control-plane binaries.
package netutil

import (
	"fmt"
	"net"
)

// DetectLocalIP finds this host's outbound-facing local IP — the address
// other machines on the same network would use to reach it — without
// actually sending any traffic. This is the standard trick: opening a UDP
// "connection" only resolves routing locally; it never requires the
// remote end to be reachable.
func DetectLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if ip := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip != nil && !ip.IsUnspecified() {
			return ip.String(), nil
		}
	}

	interfaces, listErr := net.Interfaces()
	if listErr != nil {
		if err != nil {
			return "", fmt.Errorf("route probe failed (%v); list interfaces: %w", err, listErr)
		}
		return "", listErr
	}
	var candidates []net.IP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip != nil {
				candidates = append(candidates, ip)
			}
		}
	}
	if ip := selectLANIPv4(candidates); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}

func selectLANIPv4(ips []net.IP) string {
	var fallback string
	for _, candidate := range ips {
		ip := candidate.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		if fallback == "" && ip.IsGlobalUnicast() {
			fallback = ip.String()
		}
	}
	return fallback
}
