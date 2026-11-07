package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListDirsShowsFoldersAndComposeFile(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"api", "Web", ".git"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services: {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := listDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"api", "Web"}) {
		t.Fatalf("dirs = %v, want [api Web] (hidden left out, case-insensitive order)", got.Dirs)
	}
	if !got.HasCompose || got.ComposeFile != "docker-compose.yml" {
		t.Fatalf("compose = %v %q", got.HasCompose, got.ComposeFile)
	}
	if got.Parent != filepath.Dir(root) {
		t.Fatalf("parent = %q", got.Parent)
	}
}

func TestListDirsRejectsRelativeAndMissing(t *testing.T) {
	if _, err := listDirs("relative/path"); err == nil {
		t.Fatal("want an error for a relative path")
	}
	if _, err := listDirs(filepath.Join(t.TempDir(), "nope")); !os.IsNotExist(err) {
		t.Fatalf("want not-exist, got %v", err)
	}
}

func TestWithWebUIRoutes(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>console</html>"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("js"), 0o644)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("api")) })
	h := WithWebUI(api, dir)
	for path, want := range map[string]string{"/v1/nodes": "api", "/assets/app.js": "js", "/projects/shop": "<html>console</html>", "/": "<html>console</html>"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Body.String(); got != want {
			t.Errorf("%s: body = %q, want %q", path, got, want)
		}
	}
}

func TestReadComposeReturnsFileAndItsFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets.env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&Service{}).handleReadCompose(rec, httptest.NewRequest(http.MethodGet, "/v1/fs/compose?dir="+root+"/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["dir"] != filepath.Clean(root) || got["name"] != "compose.yaml" || got["yaml"] == "" {
		t.Fatalf("response = %+v", got)
	}
}

func TestReadComposeOnlyEverOpensComposeFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secrets.env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&Service{}).handleReadCompose(rec, httptest.NewRequest(http.MethodGet, "/v1/fs/compose?dir="+filepath.Join(root, "secrets.env"), nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("a non-compose path must never be read: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	(&Service{}).handleReadCompose(rec, httptest.NewRequest(http.MethodGet, "/v1/fs/compose?dir="+root, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("folder without a compose file: status = %d, want 404", rec.Code)
	}
}
