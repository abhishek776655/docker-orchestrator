// Command agent runs the M2 node agent: register, heartbeat, poll for
// assignments, execute through Docker.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
)

func main() {
	cfg := agent.Config{
		NodeID:          envOr("HACO_NODE_ID", "node-local"),
		Address:         envOr("HACO_NODE_ADDRESS", "127.0.0.1"),
		ControlPlaneURL: envOr("HACO_CONTROLPLANE_URL", "http://localhost:8080"),
	}

	docker, err := agent.NewDocker()
	if err != nil {
		log.Fatalf("docker client: %v", err)
	}

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
