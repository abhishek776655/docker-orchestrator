package controlplane

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WithWebUI serves the built web console from dir next to the operator API:
// /v1/... goes to api, anything else is a file from dir, and a path with no
// file behind it gets index.html so the console's client-side routes
// (/projects/x, /nodes/y) load on a refresh. `hacoctl start --web` turns it on
// by setting HACO_WEB_DIR; without it the control plane serves the API only.
func WithWebUI(api http.Handler, dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/v1" {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := filepath.Clean("/" + r.URL.Path)
		if info, err := os.Stat(filepath.Join(dir, clean)); err == nil && !info.IsDir() {
			// Hashed asset names never change, so they can be cached; index.html must not be.
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}
