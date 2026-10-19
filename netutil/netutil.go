// Package netutil holds small host-networking helpers shared by the
// agent and control-plane binaries.
package netutil

import "net"

// DetectLocalIP finds this host's outbound-facing local IP — the address
// other machines on the same network would use to reach it — without
// actually sending any traffic. This is the standard trick: opening a UDP
// "connection" only resolves routing locally; it never requires the
// remote end to be reachable.
func DetectLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
