package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const localRegistryContainerName = "haco-registry"

// ensureLocalRegistry makes `HACO_REGISTRY_ADDR=auto` genuinely a single
// command: rather than requiring a separate `docker run registry:2`
// first, it starts (or reuses, or restarts) a local registry container
// itself, bound to port on every interface. Only called when the
// registry address was auto-detected — an explicit HACO_REGISTRY_ADDR
// means an operator-managed registry that this process shouldn't touch.
func ensureLocalRegistry(ctx context.Context, cli *client.Client, port string) error {
	existing, err := cli.ContainerInspect(ctx, localRegistryContainerName)
	if err == nil {
		if existing.State.Running {
			return nil
		}
		return cli.ContainerStart(ctx, existing.ID, types.ContainerStartOptions{})
	}
	if !client.IsErrNotFound(err) {
		return fmt.Errorf("inspect %s: %w", localRegistryContainerName, err)
	}

	rc, err := cli.ImagePull(ctx, "registry:2", types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull registry:2: %w", err)
	}
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()

	containerPort, err := nat.NewPort("tcp", "5000")
	if err != nil {
		return err
	}
	hostPort, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("registry port %q: %w", port, err)
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:        "registry:2",
			ExposedPorts: nat.PortSet{containerPort: struct{}{}},
		},
		&container.HostConfig{
			PortBindings:  nat.PortMap{containerPort: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(hostPort)}}},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
		nil, nil, localRegistryContainerName,
	)
	if err != nil {
		return fmt.Errorf("create %s: %w", localRegistryContainerName, err)
	}
	return cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
}
