package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

// A node's ID used to be derived from the hostname, which on DHCP networks
// follows the IP address: a machine whose IP changed came back as a new
// node and stranded its pins, data folders and credential. Now each
// machine picks its ID once and saves it next to the data it names.

var nodeIDMu sync.Mutex

// SavedNodeID returns this machine's permanent node ID, creating and
// saving it on first use. It re-reads the file each call so a rename
// (`haco node rename`) takes effect without restarting the process.
func SavedNodeID() string {
	nodeIDMu.Lock()
	defer nodeIDMu.Unlock()
	dir := dataDir()
	if data, err := os.ReadFile(filepath.Join(dir, "node-id")); err == nil {
		if id := strings.TrimSpace(string(data)); agentjoin.ValidNodeID(id) {
			return id
		}
	}
	return loadOrCreateNodeID(dir, legacyHostnameNodeID(), machineName())
}

// saveNodeID records a new permanent ID (after a rename).
func saveNodeID(id string) error {
	nodeIDMu.Lock()
	defer nodeIDMu.Unlock()
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "node-id")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dataDir() string {
	if v := os.Getenv("HACO_DATA_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".haco"
	}
	return filepath.Join(home, ".haco")
}

// loadOrCreateNodeID returns the ID saved in dir/node-id. Without one, it
// keeps an identity this machine already has — the credential matching the
// old hostname-derived ID, else the most recently used credential — so an
// upgrade changes nothing; only a machine with no identity at all gets a
// new readable one.
func loadOrCreateNodeID(dir, legacyID, name string) string {
	path := filepath.Join(dir, "node-id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); agentjoin.ValidNodeID(id) {
			return id
		}
	}
	id := adoptExistingNodeID(filepath.Join(dir, "agent-credentials"), legacyID)
	if id == "" {
		id = newNodeID(name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("save node ID: %v (using %s for this run only)", err, id)
		return id
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err == nil {
		err = os.Rename(tmp, path)
		if err != nil {
			log.Printf("save node ID: %v (using %s for this run only)", err, id)
		}
	}
	return id
}

func adoptExistingNodeID(credDir, legacyID string) string {
	if legacyID != "" {
		if _, err := os.Stat(filepath.Join(credDir, legacyID+".json")); err == nil {
			return legacyID
		}
	}
	entries, err := os.ReadDir(credDir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(credDir, e.Name()))
		if err != nil {
			continue
		}
		var cred credentialFile
		if json.Unmarshal(data, &cred) != nil || !agentjoin.ValidNodeID(cred.NodeID) {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = cred.NodeID, info.ModTime()
		}
	}
	return best
}

// newNodeID builds a readable ID from the machine's name plus a random
// suffix, so two machines with the same name still differ.
func newNodeID(name string) string {
	base := slugify(name)
	if base == "" || looksLikeAddress(base) {
		base = "node"
	}
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-")
	}
	suffix := make([]byte, 2)
	_, _ = rand.Read(suffix)
	return base + "-" + hex.EncodeToString(suffix)
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’':
			// "Abhishek's" -> "abhisheks"
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// looksLikeAddress catches DHCP-assigned hostnames like "192-168-1-12",
// exactly the kind of name that must not become a permanent ID.
func looksLikeAddress(s string) bool {
	return strings.Trim(s, "0123456789-") == ""
}

// machineName is the user-facing name of this machine: the macOS Computer
// Name ("Abhishek's MacBook Air"), else the hostname.
func machineName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if name := strings.TrimSpace(string(out)); name != "" {
				return name
			}
		}
	}
	host, _ := os.Hostname()
	return strings.TrimSuffix(host, ".local")
}

// legacyHostnameNodeID is how node IDs used to be derived (from the
// hostname); kept only to recognise an existing credential on upgrade.
func legacyHostnameNodeID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return ""
	}
	id := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.ToLower(host))
	if !agentjoin.ValidNodeID(id) {
		return ""
	}
	return id
}
