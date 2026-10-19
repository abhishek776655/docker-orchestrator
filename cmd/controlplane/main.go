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

	"github.com/abhishek/hardware-aware-container-orchestrator/controlplane"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func main() {
	addr := envOr("HACO_LISTEN_ADDR", ":8080")
	dsn := envOr("HACO_POSTGRES_DSN", "postgres://postgres:haco@localhost:55432/haco?sslmode=disable")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer s.Close()

	svc := controlplane.New(s)
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
