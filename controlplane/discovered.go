package controlplane

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
)

// discoveredDevice is a machine seen on the control plane's local network
// that has not joined the cluster.
type discoveredDevice struct {
	Address  string `json:"address"`
	Hostname string `json:"hostname,omitempty"`
	MAC      string `json:"mac,omitempty"` // empty when the host was found by probing, not ARP
}

// discoveredResponse carries a hint when the list is empty for a reason the
// operator can fix, so the UI can say so instead of showing nothing.
type discoveredResponse struct {
	Devices []discoveredDevice `json:"devices"`
	Hint    string             `json:"hint,omitempty"`
}

// filterDiscovered drops neighbors that are this host itself or already
// registered nodes, leaving only machines that could still be added.
func filterDiscovered(neighbors []netutil.Neighbor, nodes []model.Node, local map[string]bool) []discoveredDevice {
	known := map[string]bool{}
	for ip := range local {
		known[ip] = true
	}
	for _, n := range nodes {
		known[n.Address] = true
	}
	out := []discoveredDevice{}
	for _, nb := range neighbors {
		if known[nb.IP] {
			continue
		}
		out = append(out, discoveredDevice{Address: nb.IP, Hostname: nb.Hostname, MAC: nb.MAC})
	}
	return out
}

// emptyHint explains an empty result. Even a quiet network has the router in
// it, so finding nothing after a sweep and probe usually means the operating
// system is limiting what this program can see on the local network.
func emptyHint(goos string, neighbors int) string {
	if neighbors > 0 {
		return ""
	}
	if goos == "darwin" {
		return "No other devices answered on this network. Some devices ignore probes. On macOS, also check that Local Network access is allowed for the app that started the control plane (System Settings, Privacy & Security, Local Network)."
	}
	return "No other devices answered on this network. Some devices ignore probes."
}

// neighborScan is the cached result of one sweep. A sweep sends 253 packets,
// so repeated polls within a short window reuse the last result.
type neighborScan struct {
	mu        sync.Mutex
	at        time.Time
	neighbors []netutil.Neighbor
}

const scanTTL = 45 * time.Second

var lastScan neighborScan

func (s *neighborScan) get(ctx context.Context, scan func(context.Context) ([]netutil.Neighbor, error)) ([]netutil.Neighbor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < scanTTL {
		return s.neighbors, nil
	}
	ns, err := scan(ctx)
	if err != nil {
		return nil, err
	}
	s.neighbors, s.at = ns, time.Now()
	return ns, nil
}

// handleListDiscovered lists devices on this host's local /24 that are not
// cluster nodes. It wakes the subnet and probes a few common TCP ports, then
// merges that with the ARP table when the OS lets this process read it. A
// device that answers neither is invisible.
func (svc *Service) handleListDiscovered(w http.ResponseWriter, r *http.Request) {
	neighbors, err := lastScan.get(r.Context(), netutil.Discover)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	nodes, err := svc.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, discoveredResponse{
		Devices: filterDiscovered(neighbors, nodes, netutil.LocalIPs()),
		Hint:    emptyHint(runtime.GOOS, len(neighbors)),
	})
}
