package agent

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// Every project gets one Docker network per node, and every container joins
// it under its service name, so a call to `metaapi:5000` between two services
// on the same node reaches metaapi's own port 5000 exactly as under Docker
// Compose. Services on other nodes are reached through forwarders on the same
// network (forwarders.go). HACO_PROJECT_NETWORK=false turns all of this off
// and goes back to name -> node address entries.

func projectNetworksEnabled() bool {
	return os.Getenv("HACO_PROJECT_NETWORK") != "false"
}

// projectNetworkName is the Docker network for a project: haco-<project>.
func projectNetworkName(project string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(project) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return "haco-" + b.String()
}

var networkMu sync.Mutex

// EnsureNetwork creates the bridge network if it does not exist yet.
func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	networkMu.Lock()
	defer networkMu.Unlock()
	if _, err := d.cli.NetworkInspect(ctx, name, types.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !client.IsErrNotFound(err) {
		return err
	}
	_, err := d.cli.NetworkCreate(ctx, name, types.NetworkCreate{
		Driver: "bridge",
		Labels: map[string]string{"haco.network": "true", nodeLabel: d.nodeID},
	})
	if err != nil {
		// Created by something else in the meantime is fine.
		if _, ierr := d.cli.NetworkInspect(ctx, name, types.NetworkInspectOptions{}); ierr == nil {
			return nil
		}
	}
	return err
}

// containerOnNetwork reports whether an existing container is attached to the
// project network under its service name. A container created before project
// networks existed sits on Docker's default bridge and has to be recreated.
func containerOnNetwork(info types.ContainerJSON, network, service string) bool {
	if info.NetworkSettings == nil {
		return false
	}
	ep, ok := info.NetworkSettings.Networks[network]
	return ok && ep != nil && slices.Contains(ep.Aliases, service)
}
