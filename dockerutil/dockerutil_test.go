package dockerutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCandidateSocketsIncludesDockerDesktopPerUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available in this environment")
	}

	want := filepath.Join(home, ".docker", "run", "docker.sock")
	for _, got := range candidateSockets() {
		if got == want {
			return
		}
	}
	t.Fatalf("candidateSockets() = %v, want it to include %q (Docker Desktop's per-user socket)", candidateSockets(), want)
}

func TestCandidateSocketsStartsWithTheStandardPath(t *testing.T) {
	sockets := candidateSockets()
	if len(sockets) == 0 || sockets[0] != "/var/run/docker.sock" {
		t.Fatalf("candidateSockets()[0] = %v, want /var/run/docker.sock first (the common case should need no fallback probing)", sockets)
	}
}
