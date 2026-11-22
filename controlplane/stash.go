package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// blobStash keeps the tarballs agents fetch from the control plane (build
// contexts, volume snapshots and seeds) in files instead of memory, so a
// multi-GB MySQL seed costs disk, not RAM, however many are packaged in
// parallel. Like the in-memory maps it replaces, it belongs to one control
// plane process: the directory is removed at the next start.
type blobStash struct {
	mu  sync.Mutex
	dir string
}

const stashDirPrefix = "haco-stash-"

func (b *blobStash) root() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dir != "" {
		return b.dir, nil
	}
	base := os.Getenv("HACO_STASH_DIR")
	if base != "" {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", err
		}
	}
	dir, err := os.MkdirTemp(base, fmt.Sprintf("%s%d-", stashDirPrefix, os.Getpid()))
	if err != nil {
		return "", err
	}
	b.dir = dir
	return dir, nil
}

// path derives a file name from the namespace and key, so no key can name a
// path outside the stash.
func (b *blobStash) path(ns, key string) (string, error) {
	dir, err := b.root()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(ns + "\x00" + key))
	return filepath.Join(dir, hex.EncodeToString(sum[:])), nil
}

// put stores what write produces under ns/key. The file appears all at once
// (written aside, then renamed), so a reader never sees half a tarball and
// one already reading an older copy keeps it.
func (b *blobStash) put(ns, key string, write func(io.Writer) error) error {
	dst, err := b.path(ns, key)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), "incoming-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := write(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// putReader stores r under ns/key.
func (b *blobStash) putReader(ns, key string, r io.Reader) error {
	return b.put(ns, key, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// open returns the stored file, or false when there is none.
func (b *blobStash) open(ns, key string) (*os.File, bool) {
	p, err := b.path(ns, key)
	if err != nil {
		return nil, false
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	return f, true
}

func (b *blobStash) has(ns, key string) bool {
	f, ok := b.open(ns, key)
	if ok {
		f.Close()
	}
	return ok
}

// CleanStaleStash removes stash directories left by control plane processes
// that no longer exist (a crash or kill -9 skips any cleanup).
func CleanStaleStash() {
	bases := []string{os.TempDir()}
	if d := os.Getenv("HACO_STASH_DIR"); d != "" {
		bases = append(bases, d)
	}
	for _, base := range bases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			rest, ok := strings.CutPrefix(e.Name(), stashDirPrefix)
			if !e.IsDir() || !ok {
				continue
			}
			pidText, _, _ := strings.Cut(rest, "-")
			pid, err := strconv.Atoi(pidText)
			if err != nil || pid == os.Getpid() {
				continue
			}
			if err := syscall.Kill(pid, 0); err == nil || err == syscall.EPERM {
				continue // still running
			}
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}
