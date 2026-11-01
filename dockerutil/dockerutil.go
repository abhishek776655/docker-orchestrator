// Package dockerutil holds small Docker-client-construction helpers
// shared by the agent and control-plane binaries.
package dockerutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/client"
)

// NewClient builds a Docker Engine API client. If DOCKER_HOST is set
// explicitly, it's always respected unchanged — auto-detection never
// overrides an explicit value. Otherwise it probes the handful of socket
// paths real Docker installs actually use (plain Linux, Docker Desktop on
// macOS, Rancher Desktop, Colima, rootless Docker) and uses the first one
// that's both present and answers a ping, which is the friction this
// exists to remove: on macOS with Docker Desktop, the daemon doesn't live
// at the historical default (/var/run/docker.sock), so every command
// otherwise needs DOCKER_HOST set by hand.
func NewClient() (*client.Client, error) {
	if os.Getenv("DOCKER_HOST") != "" {
		return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	}

	for _, socketPath := range candidateSockets() {
		if _, err := os.Stat(socketPath); err != nil {
			continue
		}
		cli, err := client.NewClientWithOpts(client.WithHost("unix://"+socketPath), client.WithAPIVersionNegotiation())
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, pingErr := cli.Ping(ctx)
		cancel()
		if pingErr == nil {
			return cli, nil
		}
		cli.Close()
	}

	// Nothing found; fall back to the standard client.FromEnv default so
	// the error message a caller sees is the familiar one, not a custom
	// "no socket found" dead end that hides the real cause.
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

func candidateSockets() []string {
	home, _ := os.UserHomeDir()
	paths := []string{"/var/run/docker.sock"}
	if home != "" {
		paths = append(paths,
			filepath.Join(home, ".docker", "run", "docker.sock"),     // Docker Desktop (macOS/Linux, per-user)
			filepath.Join(home, ".rd", "docker.sock"),                // Rancher Desktop
			filepath.Join(home, ".colima", "default", "docker.sock"), // Colima
		)
	}
	if uid := os.Getuid(); uid >= 0 {
		paths = append(paths, fmt.Sprintf("/run/user/%d/docker.sock", uid)) // rootless Docker (Linux)
	}
	return paths
}
