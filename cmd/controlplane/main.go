// Command controlplane runs the M2 control-plane HTTP API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/build"
	"github.com/abhishek/hardware-aware-container-orchestrator/controlplane"
	"github.com/abhishek/hardware-aware-container-orchestrator/discovery"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
	"github.com/docker/docker/client"
)

func main() {
	addr := envOr("HACO_LISTEN_ADDR", ":8080")
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

	dnsAddr := envOr("HACO_DNS_LISTEN_ADDR", ":8053")
	dnsSuffix := envOr("HACO_DNS_SUFFIX", "haco")
	dnsServer := discovery.NewServer(svc, dnsSuffix)
	go func() {
		if err := dnsServer.ListenAndServe(ctx, dnsAddr); err != nil {
			log.Printf("dns server stopped: %v", err)
		}
	}()
	log.Printf("service-name DNS listening on %s, suffix .%s", dnsAddr, dnsSuffix)

	server := &http.Server{Addr: addr, Handler: controlplane.NewHandler(svc)}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("control plane listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
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
