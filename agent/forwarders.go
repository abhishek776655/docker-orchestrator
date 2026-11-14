package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// A service on another node is reached through a forwarder: a tiny container
// on the project network that carries the service's name and listens on its
// own container port, then relays the traffic to the other node's published
// port. The calling container keeps using `metaapi:5000` unchanged, and port
// 5000 only exists inside Docker's private network, so it never clashes with
// whatever holds that port on the node (macOS AirPlay, say).
//
// A container has one address on a network and Docker DNS can only point a
// name at that address, so one forwarder can stand in for several services
// only if their ports differ. Services are packed into as few forwarders as
// that allows.

const (
	forwarderLabel        = "haco.forwarder"
	forwarderProjectLabel = "haco.forwarder.project"
	forwarderNodeLabel    = "haco.forwarder.node"
	forwarderHashLabel    = "haco.forwarder.hash"
)

func forwarderImage() string {
	if v := os.Getenv("HACO_FORWARDER_IMAGE"); v != "" {
		return v
	}
	return "busybox:1.36"
}

// forward is one relay: listen on ListenPort under Service's name, send to Target.
type forward struct {
	Service    string
	ListenPort int
	Target     string // host:port on the other node
}

type forwarderGroup struct {
	Services []string
	Forwards []forward
}

// forwardsFor lists the relays this node needs: every published port of every
// service that runs on another node. Services that publish nothing can't be
// reached from outside their node and are returned in skipped.
func forwardsFor(localNode string, instances []model.ServiceInstance) (forwards []forward, skipped []string) {
	seen := map[string]bool{}
	for _, in := range instances {
		if seen[in.Service] {
			continue // one instance per name
		}
		seen[in.Service] = true
		if in.NodeID == localNode {
			continue
		}
		if net.ParseIP(in.Address) == nil {
			continue
		}
		if len(in.Ports) == 0 {
			if in.ContainerPort > 0 {
				skipped = append(skipped, in.Service)
			}
			continue
		}
		for _, p := range in.Ports {
			if p.ContainerPort <= 0 || p.HostPort <= 0 {
				continue
			}
			forwards = append(forwards, forward{
				Service:    in.Service,
				ListenPort: p.ContainerPort,
				Target:     net.JoinHostPort(in.Address, strconv.Itoa(p.HostPort)),
			})
		}
	}
	sort.Slice(forwards, func(i, j int) bool {
		if forwards[i].Service != forwards[j].Service {
			return forwards[i].Service < forwards[j].Service
		}
		return forwards[i].ListenPort < forwards[j].ListenPort
	})
	sort.Strings(skipped)
	return forwards, skipped
}

// groupForwards packs services into forwarders: a service goes into the first
// group where none of its ports is taken. All of a service's ports stay
// together, since its name resolves to one container.
func groupForwards(forwards []forward) []forwarderGroup {
	byService := map[string][]forward{}
	var order []string
	for _, f := range forwards {
		if _, ok := byService[f.Service]; !ok {
			order = append(order, f.Service)
		}
		byService[f.Service] = append(byService[f.Service], f)
	}
	var groups []forwarderGroup
	var used []map[int]bool
	for _, svc := range order {
		fs := byService[svc]
		placed := false
		for i := range groups {
			clash := false
			for _, f := range fs {
				if used[i][f.ListenPort] {
					clash = true
					break
				}
			}
			if clash {
				continue
			}
			groups[i].Services = append(groups[i].Services, svc)
			groups[i].Forwards = append(groups[i].Forwards, fs...)
			for _, f := range fs {
				used[i][f.ListenPort] = true
			}
			placed = true
			break
		}
		if !placed {
			u := map[int]bool{}
			for _, f := range fs {
				u[f.ListenPort] = true
			}
			groups = append(groups, forwarderGroup{Services: []string{svc}, Forwards: append([]forward(nil), fs...)})
			used = append(used, u)
		}
	}
	return groups
}

// command is what the forwarder container runs: one busybox `nc` listener per
// relay, each handing every connection to a second `nc` towards the target.
func (g forwarderGroup) command() []string {
	var parts []string
	for _, f := range g.Forwards {
		host, port, _ := net.SplitHostPort(f.Target)
		parts = append(parts, fmt.Sprintf("nc -ll -p %d -e nc %s %s &", f.ListenPort, host, port))
	}
	return []string{"sh", "-c", strings.Join(parts, " ") + " wait"}
}

// hash identifies what a forwarder does, so one whose relays changed is
// recreated and an unchanged one is left alone.
func (g forwarderGroup) hash(image string) string {
	h := sha256.New()
	fmt.Fprintln(h, image)
	for _, f := range g.Forwards {
		fmt.Fprintf(h, "%s %d %s\n", f.Service, f.ListenPort, f.Target)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func forwarderName(project string, index int) string {
	return fmt.Sprintf("haco-fwd-%s-%d", strings.TrimPrefix(projectNetworkName(project), "haco-"), index)
}

// SyncForwarders makes the forwarders on this node for one project match
// groups: missing ones are created, changed ones recreated, extra ones removed.
func (d *Docker) SyncForwarders(ctx context.Context, project string, groups []forwarderGroup) error {
	netName := projectNetworkName(project)
	if len(groups) > 0 {
		if err := d.EnsureNetwork(ctx, netName); err != nil {
			return fmt.Errorf("ensure network %s: %w", netName, err)
		}
	}
	existing, err := d.listForwarders(ctx, project)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	image := forwarderImage()
	for i, g := range groups {
		name := forwarderName(project, i)
		wanted[name] = true
		hash := g.hash(image)
		if c, ok := existing[name]; ok {
			if c.Labels[forwarderHashLabel] == hash && c.State == "running" {
				continue
			}
			if err := d.cli.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{Force: true}); err != nil {
				return fmt.Errorf("remove forwarder %s: %w", name, err)
			}
		}
		if !d.HasImage(ctx, image) {
			if err := d.PullImage(ctx, image, ""); err != nil {
				return fmt.Errorf("pull %s for the forwarders: %w", image, err)
			}
		}
		resp, err := d.cli.ContainerCreate(ctx,
			&container.Config{
				Image: image,
				Cmd:   g.command(),
				Labels: map[string]string{
					forwarderLabel:        "true",
					forwarderProjectLabel: project,
					forwarderNodeLabel:    d.nodeID,
					forwarderHashLabel:    hash,
				},
			},
			&container.HostConfig{
				NetworkMode:   container.NetworkMode(netName),
				RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
			},
			&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {Aliases: g.Services},
			}},
			nil, name)
		if err != nil {
			return fmt.Errorf("create forwarder %s: %w", name, err)
		}
		if err := d.cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("start forwarder %s: %w", name, err)
		}
		log.Printf("forwarder %s: %s", name, describeGroup(g))
	}
	for name, c := range existing {
		if !wanted[name] {
			_ = d.cli.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{Force: true})
		}
	}
	return nil
}

// RemoveForwardersExcept removes the forwarders of every project not in keep:
// projects that no longer have a workload on this node.
func (d *Docker) RemoveForwardersExcept(ctx context.Context, keep map[string]bool) error {
	f := filters.NewArgs(filters.Arg("label", forwarderLabel+"=true"), filters.Arg("label", forwarderNodeLabel+"="+d.nodeID))
	list, err := d.cli.ContainerList(ctx, types.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}
	for _, c := range list {
		if !keep[c.Labels[forwarderProjectLabel]] {
			_ = d.cli.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{Force: true})
		}
	}
	return nil
}

func (d *Docker) listForwarders(ctx context.Context, project string) (map[string]types.Container, error) {
	f := filters.NewArgs(
		filters.Arg("label", forwarderLabel+"=true"),
		filters.Arg("label", forwarderProjectLabel+"="+project),
		filters.Arg("label", forwarderNodeLabel+"="+d.nodeID),
	)
	list, err := d.cli.ContainerList(ctx, types.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := map[string]types.Container{}
	for _, c := range list {
		if len(c.Names) > 0 {
			out[strings.TrimPrefix(c.Names[0], "/")] = c
		}
	}
	return out, nil
}

func describeGroup(g forwarderGroup) string {
	var parts []string
	for _, f := range g.Forwards {
		parts = append(parts, fmt.Sprintf("%s:%d -> %s", f.Service, f.ListenPort, f.Target))
	}
	return strings.Join(parts, ", ")
}
