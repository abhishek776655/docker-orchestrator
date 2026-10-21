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

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
	"github.com/abhishek/hardware-aware-container-orchestrator/build"
	"github.com/abhishek/hardware-aware-container-orchestrator/controlplane"
	"github.com/abhishek/hardware-aware-container-orchestrator/discovery"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
	"github.com/docker/docker/client"
)

func main() {
	addr := envOr("HACO_LISTEN_ADDR", "127.0.0.1:8099")
	agentAddr := envOr("HACO_AGENT_LISTEN_ADDR", ":8443")
	dsn := envOr("HACO_POSTGRES_DSN", "postgres://postgres:haco@localhost:55432/haco?sslmode=disable")
	registry := resolveRegistryAddr()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer s.Close()

	svc := controlplane.New(s)
	dataDir := envOr("HACO_DATA_DIR", defaultDataDir())
	advertiseHost := os.Getenv("HACO_AGENT_ADVERTISE_HOST")
	if advertiseHost == "" {
		var err error
		advertiseHost, err = netutil.DetectLocalIP()
		if err != nil {
			log.Fatalf("detect control-plane LAN address: %v (set HACO_AGENT_ADVERTISE_HOST)", err)
		}
	}
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
	if registry != "" {
		dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			log.Fatalf("docker client for build service: %v", err)
		}
		svc = svc.WithBuilder(build.New(dockerCli, registry))
		log.Printf("compose build service enabled, pushing to %s", registry)
	}
	heartbeatTimeout := envDuration("HACO_HEARTBEAT_TIMEOUT", 15*time.Second)
	reconcileInterval := envDuration("HACO_RECONCILE_INTERVAL", 5*time.Second)
	go svc.RunReconciler(ctx, reconcileInterval, heartbeatTimeout)
	log.Printf("reconciler running every %s, heartbeat timeout %s", reconcileInterval, heartbeatTimeout)

	dnsAddr := envOr("HACO_DNS_LISTEN_ADDR", ":53")
	dnsServer := discovery.NewServer(svc, dnsSuffix)
	go func() {
		if err := dnsServer.ListenAndServe(ctx, dnsAddr); err != nil {
			log.Printf("dns server stopped: %v", err)
		}
	}()
	log.Printf("service-name DNS listening on %s, suffix .%s", dnsAddr, dnsSuffix)

	server := &http.Server{Addr: addr, Handler: controlplane.LocalOnly(controlplane.NewHandler(svc))}
	agentServer := &http.Server{Addr: agentAddr, Handler: controlplane.NewAgentHandler(svc), TLSConfig: &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12}}
	agentListener, err := net.Listen("tcp", agentAddr)
	if err != nil {
		log.Fatalf("listen for agent HTTPS on %s: %v", agentAddr, err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
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
	fmt.Printf("One-time agent join command (expires in 10 minutes): ./bin/agent join '%s'\n", encoded)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve operator API: %v", err)
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
// is unchanged. Setting it to "auto" auto-detects this host's own LAN IP
// and appends HACO_REGISTRY_PORT (default 5050), so a two-machine
// deployment needs no manual IP lookup on the control-plane side either.
func resolveRegistryAddr() string {
	addr := os.Getenv("HACO_REGISTRY_ADDR")
	if addr != "auto" {
		return addr
	}
	ip, err := netutil.DetectLocalIP()
	if err != nil {
		log.Fatalf("HACO_REGISTRY_ADDR=auto but IP auto-detection failed: %v; set it explicitly", err)
	}
	port := envOr("HACO_REGISTRY_PORT", "5050")
	resolved := ip + ":" + port
	log.Printf("HACO_REGISTRY_ADDR=auto resolved to %s", resolved)
	return resolved
}
