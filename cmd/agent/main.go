// Command agent runs the M2 node agent: register, heartbeat, poll for
// assignments, execute through Docker.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
)

func main() {
	cfg := agent.Config{
		NodeID:          envOr("HACO_NODE_ID", "node-local"),
		Address:         envOr("HACO_NODE_ADDRESS", "127.0.0.1"),
		ControlPlaneURL: envOr("HACO_CONTROLPLANE_URL", "http://localhost:8080"),
		DNSAddress:      envOr("HACO_DNS_ADDRESS", ""),
		DNSSuffix:       envOr("HACO_DNS_SUFFIX", "haco"),
	}

	home, _ := os.UserHomeDir()
	dataDir := envOr("HACO_DATA_DIR", filepath.Join(home, ".haco", "volumes", cfg.NodeID))

	docker, err := agent.NewDocker(cfg.NodeID, dataDir, cfg.DNSAddress, cfg.DNSSuffix)
	if err != nil {
		log.Fatalf("docker client: %v", err)
	}
	log.Printf("node-local persistent volumes rooted at %s", dataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := agent.New(cfg, docker)
	if err := a.Run(ctx); err != nil {
		log.Fatalf("agent: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
