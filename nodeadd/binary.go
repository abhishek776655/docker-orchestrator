package nodeadd

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultReleaseBase is where release archives are published.
const DefaultReleaseBase = "https://github.com/abhishek776655/docker-orchestrator/releases/download"

// BinarySource decides which agent binary goes to a target.
type BinarySource struct {
	// Explicit, when set, is used for every target (--agent-binary).
	Explicit string
	// Version is the release tag to download for a different platform,
	// e.g. "v0.1.0". Empty means this haco build has no release version.
	Version string
	// ReleaseBase overrides DefaultReleaseBase (tests, mirrors).
	ReleaseBase string
	// TempDir holds downloads; defaults to os.TempDir().
	TempDir string
	Client  *http.Client
}

// Resolve returns a local agent binary for p: the explicit one, the agent
// next to this haco (or on PATH) when the target is the same platform, or a
// checksum-verified download from the release.
func (b BinarySource) Resolve(p Platform) (string, error) {
	if b.Explicit != "" {
		if _, err := os.Stat(b.Explicit); err != nil {
			return "", fmt.Errorf("--agent-binary: %w", err)
		}
		return b.Explicit, nil
	}
	if p.OS == runtime.GOOS && p.Arch == runtime.GOARCH {
		if exe, err := os.Executable(); err == nil {
			sibling := filepath.Join(filepath.Dir(exe), "agent")
			if st, err := os.Stat(sibling); err == nil && !st.IsDir() {
				return sibling, nil
			}
		}
		if path, err := exec.LookPath("agent"); err == nil {
			return path, nil
		}
	}
	if b.Version == "" {
		return "", fmt.Errorf("no agent binary for %s: pass --agent-binary <path>, or --version <tag> to download it from a release", p)
	}
	return b.download(p)
}

func (b BinarySource) download(p Platform) (string, error) {
	base := b.ReleaseBase
	if base == "" {
		base = DefaultReleaseBase
	}
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	tag := b.Version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	name := fmt.Sprintf("haco_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), p.OS, p.Arch)

	sums, err := fetch(client, base+"/"+tag+"/checksums.txt")
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("release %s has no archive %s", tag, name)
	}
	archive, err := fetch(client, base+"/"+tag+"/"+name)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return "", fmt.Errorf("checksum mismatch for %s; refusing to install it", name)
	}
	dir := b.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	out, err := os.MkdirTemp(dir, "haco-agent-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(out, "agent")
	if err := extractFile(archive, "agent", path); err != nil {
		return "", err
	}
	return path, nil
}

func fetch(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func extractFile(tgz []byte, member, dest string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive has no %q", member)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == member {
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		}
	}
}
