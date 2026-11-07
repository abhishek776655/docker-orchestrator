package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func writeCred(t *testing.T, dir, id string, mod time.Time) {
	t.Helper()
	credDir := filepath.Join(dir, "agent-credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(credDir, id+".json")
	if err := os.WriteFile(p, []byte(`{"node_id":"`+id+`","credential":"c","endpoint":"e","fingerprint":"f"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestNodeIDKeepsCurrentIdentityOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeCred(t, dir, "192-168-1-5", now)                  // stale IP, but newer file
	writeCred(t, dir, "192-168-1-11", now.Add(-time.Hour)) // matches today's hostname
	if got := loadOrCreateNodeID(dir, "192-168-1-11", "Abhishek's MacBook Air"); got != "192-168-1-11" {
		t.Fatalf("id = %q, want the credential matching the current hostname", got)
	}
}

func TestNodeIDSurvivesIPChangeBeforeUpgrade(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeCred(t, dir, "192-168-1-3", now.Add(-48*time.Hour))
	writeCred(t, dir, "192-168-1-12", now)
	// The IP (and so the hostname) already moved on to .20.
	if got := loadOrCreateNodeID(dir, "192-168-1-20", "Office Mac"); got != "192-168-1-12" {
		t.Fatalf("id = %q, want the most recently used identity", got)
	}
}

func TestNodeIDIsSavedAndNeverChanges(t *testing.T) {
	dir := t.TempDir()
	first := loadOrCreateNodeID(dir, "192-168-1-30", "Abhishek's MacBook Air")
	if !regexp.MustCompile(`^abhisheks-macbook-air-[0-9a-f]{4}$`).MatchString(first) {
		t.Fatalf("new id = %q, want readable name plus suffix", first)
	}
	// Later the machine is renamed and its IP changes: the ID must not.
	if again := loadOrCreateNodeID(dir, "10-0-0-7", "Renamed Mac"); again != first {
		t.Fatalf("id changed from %q to %q", first, again)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "node-id"))
	if string(saved) != first+"\n" {
		t.Fatalf("saved = %q", saved)
	}
}

func TestNodeIDNeverUsesAnAddressLikeName(t *testing.T) {
	if got := newNodeID("192-168-1-12"); !regexp.MustCompile(`^node-[0-9a-f]{4}$`).MatchString(got) {
		t.Fatalf("id = %q, want node-xxxx for an IP-derived name", got)
	}
}
