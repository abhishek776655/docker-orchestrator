// Command controlplane runs the M2 control-plane HTTP API.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agent"
	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
	"github.com/abhishek/hardware-aware-container-orchestrator/build"
	"github.com/abhishek/hardware-aware-container-orchestrator/controlplane"
	"github.com/abhishek/hardware-aware-container-orchestrator/dockerutil"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func main() {
	addr := envOr("HACO_LISTEN_ADDR", "127.0.0.1:8099")
	agentAddr := envOr("HACO_AGENT_LISTEN_ADDR", ":8443")

	advertiseHost := os.Getenv("HACO_AGENT_ADVERTISE_HOST")
	if advertiseHost == "" {
		var err error
		advertiseHost, err = netutil.DetectLocalIP()
		if err != nil {
			log.Fatalf("detect control-plane LAN address: %v (set HACO_AGENT_ADVERTISE_HOST)", err)
		}
	}

	registryPort := envOr("HACO_REGISTRY_PORT", "5050")
	registry := resolveRegistryAddr(advertiseHost, registryPort)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbPath := envOr("HACO_DB_PATH", filepath.Join(defaultDataDir(), "control-plane.db"))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		log.Fatalf("create db directory: %v", err)
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer s.Close()
	log.Printf("metadata stored at %s", dbPath)

	svc := controlplane.New(s).WithShutdownHook(stop)
	dataDir := envOr("HACO_DATA_DIR", defaultDataDir())
	port := 8443
	if _, p, err := net.SplitHostPort(agentAddr); err == nil {
		if _, scanErr := fmt.Sscanf(p, "%d", &port); scanErr != nil {
			log.Fatalf("invalid HACO_AGENT_LISTEN_ADDR port: %s", p)
		}
	}
	dnsSuffix := envOr("HACO_DNS_SUFFIX", "haco")
	tlsCert, fingerprint, err := controlplane.LoadOrCreateTLSIdentity(filepath.Join(dataDir, "tls"), advertiseHost)
	if err != nil {
		log.Fatalf("load agent TLS identity: %v", err)
	}
	joinManager := controlplane.NewJoinManager(s, fmt.Sprintf("https://%s", net.JoinHostPort(advertiseHost, fmt.Sprint(port))), fingerprint, dnsSuffix)
	svc = svc.WithJoinManager(joinManager)
	// A builder is always wired up, registry or not: with no registry
	// configured, `build:` services build locally and are never pushed
	// (build.Service.Build — registry-optional builds, single-machine
	// case) — only a genuinely unavailable Docker daemon disables it, and
	// that's soft (logged, not fatal), since a control plane with no local
	// Docker is an explicitly supported mode (see runHostAgent).
	if dockerCli, err := dockerutil.NewClient(); err != nil {
		log.Printf("compose build service disabled: docker client: %v", err)
	} else {
		if registry != "" {
			if host, port, splitErr := net.SplitHostPort(registry); splitErr == nil && isLocalRegistryHost(host, advertiseHost) {
				if err := ensureLocalRegistry(ctx, dockerCli, port); err != nil {
					log.Fatalf("auto-start local registry: %v", err)
				}
				log.Printf("local registry container %s running on port %s", localRegistryContainerName, port)
			}
			log.Printf("compose build service enabled, pushing to %s", registry)
		} else {
			log.Printf("compose build service enabled, local-only (no registry configured; built images stay on this host)")
		}
		svc = svc.WithBuilder(build.New(dockerCli, registry))
	}
	heartbeatTimeout := envDuration("HACO_HEARTBEAT_TIMEOUT", 15*time.Second)
	reconcileInterval := envDuration("HACO_RECONCILE_INTERVAL", 5*time.Second)
	go svc.RunReconciler(ctx, reconcileInterval, heartbeatTimeout)
	log.Printf("reconciler running every %s, heartbeat timeout %s", reconcileInterval, heartbeatTimeout)

	var operator http.Handler = controlplane.NewHandler(svc)
	if webDir := os.Getenv("HACO_WEB_DIR"); webDir != "" {
		operator = controlplane.WithWebUI(operator, webDir)
		log.Printf("web console served from %s at http://%s/", webDir, addr)
	}
	server := &http.Server{Addr: addr, Handler: controlplane.LocalOnly(operator)}
	agentServer := &http.Server{Addr: agentAddr, Handler: controlplane.NewAgentHandler(svc), TLSConfig: &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12}}
	agentListener, err := net.Listen("tcp", agentAddr)
	if err != nil {
		log.Fatalf("listen for agent HTTPS on %s: %v", agentAddr, err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The host's own agent stops with this process and couldn't
		// reach the closing listeners to pause itself, so pause it here:
		// its containers keep running and must not be failed over.
		if err := s.PauseNode(shutdownCtx, agent.HostNodeID()); err != nil {
			log.Printf("pause host node: %v", err)
		}
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = agentServer.Shutdown(shutdownCtx)
	}()

	log.Printf("control plane listening on %s", addr)
	go func() {
		if err := agentServer.ServeTLS(agentListener, "", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("agent TLS server: %v", err)
		}
	}()
	joinBundle, err := joinManager.Issue(ctx)
	if err != nil {
		log.Fatalf("issue initial agent join token: %v", err)
	}
	encoded, err := agentjoin.Encode(joinBundle)
	if err != nil {
		log.Fatalf("encode agent join token: %v", err)
	}
	log.Printf("agent API listening on https://%s (certificate pin %s)", net.JoinHostPort(advertiseHost, fmt.Sprint(port)), fingerprint)
	fmt.Printf("One-time agent join command (expires in 10 minutes): hacoctl agent join '%s'\n", encoded)

	if envOr("HACO_AUTO_JOIN_HOST", "true") != "false" {
		go runHostAgent(ctx, joinManager, advertiseHost, dataDir)
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve operator API: %v", err)
	}
}

// runHostAgent auto-joins and runs an agent for the machine the control
// plane itself is on, so a single-machine (or first-node) deployment needs
// no separate `./bin/agent join ...` step. It fails soft — logged, not
// fatal — since running a control plane with no local Docker workload is
// an explicitly supported mode (HACO_AUTO_JOIN_HOST=false opts out, or a
// missing Docker daemon here just skips it).
func runHostAgent(ctx context.Context, jm *controlplane.JoinManager, advertiseHost, dataDir string) {
	nodeID := agent.HostNodeID()
	credentialPath := filepath.Join(dataDir, "agent-credentials", nodeID+".json")

	cfg, err := agent.LoadCredential(credentialPath)
	if err != nil {
		bundle, issueErr := jm.Issue(ctx)
		if issueErr != nil {
			log.Printf("auto-join host node %s: issue token: %v", nodeID, issueErr)
			return
		}
		cfg, err = agent.Join(ctx, bundle, nodeID, credentialPath)
		if err != nil {
			log.Printf("auto-join host node %s: %v", nodeID, err)
			return
		}
		log.Printf("host auto-joined control plane as node %s", nodeID)
	}
	if address := os.Getenv("HACO_HOST_NODE_ADDRESS"); address != "" {
		cfg.Address = address
	} else {
		cfg.Address = advertiseHost
	}
	cfg.Role = "host"
	cfg.CredentialPath = credentialPath

	docker, err := agent.NewDocker(nodeID, filepath.Join(dataDir, "volumes", nodeID))
	if err != nil {
		log.Printf("auto-join host node %s: docker unavailable, running control-plane-only (%v)", nodeID, err)
		return
	}

	log.Printf("host agent running as node %s", nodeID)
	err = agent.New(cfg, docker).Run(ctx)
	if re, ok := agent.IsRestart(err); ok && ctx.Err() == nil {
		// After a rename the saved node-id file already holds the new ID,
		// so starting over picks up the renamed credential and data folder.
		log.Printf("host agent: %v", re)
		runHostAgent(ctx, jm, advertiseHost, dataDir)
		return
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("host agent: %v", err)
	}
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

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// resolveRegistryAddr returns the registry address to advertise to agents
// (used both to tag pushed images and to push them). An empty
// HACO_REGISTRY_ADDR disables the build service entirely — that default
// is unchanged. Setting it to "auto" uses advertiseHost (this host's own
// LAN IP, already detected for the agent API) and port — a two-machine
// deployment needs no manual IP lookup on the control-plane side either.
func resolveRegistryAddr(advertiseHost, port string) string {
	addr := os.Getenv("HACO_REGISTRY_ADDR")
	if addr != "auto" {
		return addr
	}
	resolved := advertiseHost + ":" + port
	log.Printf("HACO_REGISTRY_ADDR=auto resolved to %s", resolved)
	return resolved
}

// isLocalRegistryHost reports whether a registry address points at this
// same machine — "localhost"/"127.0.0.1" always does, as does the
// control plane's own detected LAN address (the "auto" case). Only then
// does ensureLocalRegistry manage it; a genuinely remote, operator-owned
// registry (e.g. registry.example.com) is never touched.
func isLocalRegistryHost(host, advertiseHost string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == advertiseHost
}
