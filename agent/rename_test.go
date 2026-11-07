package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMoveNodeFilesRenamesDataFolderAndCredential(t *testing.T) {
	root := t.TempDir()
	volumes := filepath.Join(root, "volumes", "192-168-1-12")
	if err := os.MkdirAll(filepath.Join(volumes, "demo_db_latest"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCred(t, root, "192-168-1-12", time.Now())
	oldCred := filepath.Join(root, "agent-credentials", "192-168-1-12.json")

	newCred, err := moveNodeFiles("192-168-1-12", "office-mac-7f3a", volumes, oldCred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "volumes", "office-mac-7f3a", "demo_db_latest")); err != nil {
		t.Fatalf("data folder not moved: %v", err)
	}
	if _, err := os.Stat(volumes); !os.IsNotExist(err) {
		t.Fatalf("old data folder still present: %v", err)
	}
	if newCred != filepath.Join(root, "agent-credentials", "office-mac-7f3a.json") {
		t.Fatalf("credential path = %s", newCred)
	}
	if _, err := os.Stat(oldCred); !os.IsNotExist(err) {
		t.Fatal("old credential file left behind")
	}
	var cred credentialFile
	data, _ := os.ReadFile(newCred)
	if err := json.Unmarshal(data, &cred); err != nil || cred.NodeID != "office-mac-7f3a" || cred.Credential != "c" {
		t.Fatalf("credential = %+v (%v), want same secret under the new ID", cred, err)
	}
	if info, _ := os.Stat(newCred); info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestMoveNodeFilesNeverOverwritesExistingFolder(t *testing.T) {
	root := t.TempDir()
	volumes := filepath.Join(root, "volumes", "a")
	for _, d := range []string{volumes, filepath.Join(root, "volumes", "b")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCred(t, root, "a", time.Now())
	if _, err := moveNodeFiles("a", "b", volumes, filepath.Join(root, "agent-credentials", "a.json")); err == nil {
		t.Fatal("moving onto an existing data folder must fail, not merge or overwrite")
	}
	if _, err := os.Stat(volumes); err != nil {
		t.Fatalf("original folder must stay put: %v", err)
	}
}
