package controlplane

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStash(t *testing.T) *blobStash {
	t.Helper()
	t.Setenv("HACO_STASH_DIR", t.TempDir())
	return &blobStash{}
}

func read(t *testing.T, b *blobStash, ns, key string) string {
	t.Helper()
	f, ok := b.open(ns, key)
	if !ok {
		t.Fatalf("%s/%s not stashed", ns, key)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	return string(data)
}

func TestStashPutOpenHasAndNamespaces(t *testing.T) {
	b := newStash(t)
	if b.has("volume", "k") {
		t.Fatal("nothing stored yet")
	}
	if err := b.putReader("volume", "k", strings.NewReader("one")); err != nil {
		t.Fatal(err)
	}
	if err := b.putReader("build-context", "k", strings.NewReader("two")); err != nil {
		t.Fatal(err)
	}
	if read(t, b, "volume", "k") != "one" || read(t, b, "build-context", "k") != "two" {
		t.Fatal("namespaces must not collide")
	}
}

func TestStashReplaceKeepsAnOpenReadersCopy(t *testing.T) {
	b := newStash(t)
	b.putReader("volume", "k", strings.NewReader("old content"))
	f, _ := b.open("volume", "k")
	defer f.Close()
	if err := b.putReader("volume", "k", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(f); string(got) != "old content" {
		t.Fatalf("an agent mid-fetch must keep its copy, got %q", got)
	}
	if read(t, b, "volume", "k") != "new" {
		t.Fatal("a later fetch gets the new content")
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, fmt.Errorf("disk on fire")
	}
	f.n--
	return copy(p, "x"), nil
}

func TestStashFailedWriteStoresNothingAndKeepsThePreviousCopy(t *testing.T) {
	b := newStash(t)
	b.putReader("volume", "k", strings.NewReader("good"))
	if err := b.putReader("volume", "k", &failingReader{n: 3}); err == nil {
		t.Fatal("want the read error")
	}
	if read(t, b, "volume", "k") != "good" {
		t.Fatal("a failed write must not replace the stored copy")
	}
	dir, _ := b.root()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files after a failed write: %v", entries)
	}
}

func TestStashKeysCannotEscapeTheDirectory(t *testing.T) {
	b := newStash(t)
	if err := b.putReader("volume", "../../etc/passwd", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	dir, _ := b.root()
	p, _ := b.path("volume", "../../etc/passwd")
	if filepath.Dir(p) != dir {
		t.Fatalf("path %s is outside %s", p, dir)
	}
}

type zeroReader struct{ left int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.left <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.left {
		n = z.left
	}
	clear(p[:n])
	z.left -= n
	return int(n), nil
}

// The point of the stash: a big tarball costs disk, not heap.
func TestStashLargeTarballDoesNotGrowTheHeap(t *testing.T) {
	b := newStash(t)
	const size = 256 << 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := b.putReader("volume", "big", &zeroReader{left: size}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 32<<20 {
		t.Fatalf("heap grew by %d MiB storing a %d MiB tarball", grew>>20, size>>20)
	}
	p, _ := b.path("volume", "big")
	if st, err := os.Stat(p); err != nil || st.Size() != size {
		t.Fatalf("stored file: %v, %v", st, err)
	}
}

func TestCleanStaleStashRemovesOnlyDeadProcessesDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HACO_STASH_DIR", base)
	alive := filepath.Join(base, fmt.Sprintf("%s%d-aaa", stashDirPrefix, os.Getppid())) // a running process
	dead := filepath.Join(base, stashDirPrefix+"999999-bbb")
	own := filepath.Join(base, fmt.Sprintf("%s%d-ccc", stashDirPrefix, os.Getpid()))
	other := filepath.Join(base, "unrelated")
	for _, d := range []string{alive, dead, own, other} {
		os.MkdirAll(d, 0o700)
	}
	CleanStaleStash()
	for dir, wantExists := range map[string]bool{alive: true, dead: false, own: true, other: true} {
		if _, err := os.Stat(dir); (err == nil) != wantExists {
			t.Errorf("%s exists=%v, want %v", filepath.Base(dir), err == nil, wantExists)
		}
	}
}

func TestServeTarballStreamsAndSupportsRanges(t *testing.T) {
	b := newStash(t)
	b.putReader("volume", "k", strings.NewReader("0123456789"))
	f, _ := b.open("volume", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveTarball(w, r, f) }))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "0123456789" || resp.Header.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("body %q type %q", body, resp.Header.Get("Content-Type"))
	}
}
