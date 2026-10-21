// Command agent runs the M2 node agent: register, heartbeat, poll for
// assignments, execute through Docker.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

func main() {
	nodeID := envOr("HACO_NODE_ID", defaultNodeID())
	credentialPath := envOr("HACO_AGENT_CREDENTIAL_FILE", filepath.Join(defaultDataDir(), "agent-credentials", nodeID+".json"))
	if len(os.Args) == 3 && os.Args[1] == "join" {
		bundle, err := agentjoin.Decode(os.Args[2])
		if err != nil {
			log.Fatalf("decode join command: %v", err)
		}
		cfg, err := agent.Join(context.Background(), bundle, nodeID, credentialPath)
		if err != nil {
			log.Fatalf("join control plane: %v", err)
		}
		if address := os.Getenv("HACO_NODE_ADDRESS"); address != "" {
			cfg.Address = address
		}
		log.Printf("joined control plane as node %s; credential saved to %s", nodeID, credentialPath)
		run(cfg)
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "run" {
		usage()
		os.Exit(2)
	}
	cfg, err := agent.LoadCredential(credentialPath)
	if err != nil {
		log.Fatalf("load agent credential: %v; start with: ./bin/agent join <one-time-token>", err)
	}
	if address := os.Getenv("HACO_NODE_ADDRESS"); address != "" {
		cfg.Address = address
	}
	if dns := os.Getenv("HACO_DNS_ADDRESS"); dns != "" {
		cfg.DNSAddress = dns
	}
	cfg.DNSSuffix = envOr("HACO_DNS_SUFFIX", cfg.DNSSuffix)
	run(cfg)
}

func run(cfg agent.Config) {
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

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:\n  agent join <one-time-token>   Join and run this node\n  agent run                    Run using the saved node credential")
}

func defaultNodeID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "node-local"
	}
	host = strings.ToLower(host)
	id := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, host)
	if !agentjoin.ValidNodeID(id) {
		return "node-local"
	}
	return id
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".haco"
	}
	return filepath.Join(home, ".haco")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
