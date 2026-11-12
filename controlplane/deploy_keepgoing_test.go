package controlplane

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/store"
)

func TestComposeDeployKeepsSchedulingPastAFailure(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n := model.Node{ID: "n1", Address: "10.0.0.9", Architecture: "arm64", Labels: map[string]string{}, Capabilities: map[string]bool{},
		Availability: model.Available, HeartbeatAt: time.Now(),
		Allocatable: model.Resources{CPUMillis: 4000, MemoryBytes: 8 << 30}}
	if err := s.UpsertNode(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	svc := New(s)

	yaml := `
services:
  a:
    image: alpine
    deploy: {resources: {limits: {memory: 256M}}}
  huge:
    image: alpine
    deploy: {resources: {limits: {memory: 900G}}}
  z:
    image: alpine
    deploy: {resources: {limits: {memory: 256M}}}
`
	_, results, err := svc.ComposeDeploy(context.Background(), "kg", t.TempDir(), []byte(yaml), DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "1 of 3 services could not be scheduled") || !strings.Contains(err.Error(), "huge") {
		t.Fatalf("err = %v, want one failure naming huge", err)
	}
	placed := map[string]bool{}
	for _, r := range results {
		if r != nil && r.Workload != nil {
			placed[r.Workload.Service] = true
		}
	}
	if !placed["a"] || !placed["z"] {
		t.Fatalf("services after the failing one must still be scheduled; placed = %v", placed)
	}
}
