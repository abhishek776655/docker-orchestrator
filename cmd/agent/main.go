// Command agent runs the M2 node agent: register, heartbeat, poll for
// assignments, execute through Docker.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

func main() {
	nodeID := envOr("HACO_NODE_ID", agent.SavedNodeID())
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
		cfg.CredentialPath = credentialPath
		run(cfg)
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "run" {
		usage()
		os.Exit(2)
	}
	cfg, err := agent.LoadCredential(credentialPath)
	if err != nil {
		log.Fatalf("load agent credential: %v; start with: hacoctl agent join <one-time-token>", err)
	}
	if address := os.Getenv("HACO_NODE_ADDRESS"); address != "" {
		cfg.Address = address
	}
	cfg.CredentialPath = credentialPath
	run(cfg)
}

func run(cfg agent.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		home, _ := os.UserHomeDir()
		dataDir := envOr("HACO_DATA_DIR", filepath.Join(home, ".haco", "volumes", cfg.NodeID))
		docker, err := agent.NewDocker(cfg.NodeID, dataDir)
		if err != nil {
			log.Fatalf("docker client: %v", err)
		}
		log.Printf("node-local persistent volumes rooted at %s", dataDir)

		err = agent.New(cfg, docker).Run(ctx)
		if err == nil {
			return
		}
		if errors.Is(err, agent.ErrShutdownByControlPlane) {
			log.Print(err)
			return
		}
		re, ok := agent.IsRestart(err)
		if !ok {
			log.Fatalf("agent: %v", err)
		}
		log.Print(re)
		next, loadErr := agent.LoadCredential(re.CredentialPath)
		if loadErr != nil {
			log.Fatalf("reload credential after %v: %v", re, loadErr)
		}
		if address := os.Getenv("HACO_NODE_ADDRESS"); address != "" {
			next.Address = address
		}
		next.CredentialPath = re.CredentialPath
		cfg = next
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:\n  agent join <one-time-token>   Join and run this node\n  agent run                    Run using the saved node credential")
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
