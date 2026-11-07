package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirListing is one folder on the control-plane machine and the folders
// inside it, for the web console's project-folder picker. A browser cannot
// read absolute paths from the user's disk, and the folder that matters is
// the one on this machine anyway (builds and bind mounts resolve here).
type dirListing struct {
	Path        string   `json:"path"`
	Parent      string   `json:"parent"` // "" at the filesystem root
	Home        string   `json:"home"`
	Dirs        []string `json:"dirs"`         // names only, sorted, hidden ones left out
	HasCompose  bool     `json:"has_compose"`  // a compose file sits directly in Path
	ComposeFile string   `json:"compose_file"` // its name, when HasCompose
}

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// listDirs reads one folder. Only folder names are returned, never file
// contents; the operator API is loopback-only (LocalOnly), so this shows
// the operator their own machine.
func listDirs(path string) (*dirListing, error) {
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(path) == "" {
		path = home
	}
	if strings.HasPrefix(path, "~") {
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path %q is not absolute", path)
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := &dirListing{Path: path, Home: home, Dirs: []string{}}
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(path, name)); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir && !strings.HasPrefix(name, ".") {
			out.Dirs = append(out.Dirs, name)
		}
		if !isDir && out.ComposeFile == "" {
			for _, c := range composeNames {
				if name == c {
					out.HasCompose, out.ComposeFile = true, name
				}
			}
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i]) < strings.ToLower(out.Dirs[j]) })
	return out, nil
}

// maxComposeBytes matches the console's own upload limit.
const maxComposeBytes = 1 << 20

// composeIn reads the compose file directly inside dir, so the console can
// load it and know which folder it came from (a browser upload can't say).
// Only the standard compose file names are ever opened.
func composeIn(dir string) (name string, data []byte, err error) {
	listing, err := listDirs(dir)
	if err != nil {
		return "", nil, err
	}
	if !listing.HasCompose {
		return "", nil, fmt.Errorf("no compose file in %s: %w", listing.Path, os.ErrNotExist)
	}
	path := filepath.Join(listing.Path, listing.ComposeFile)
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if info.Size() > maxComposeBytes {
		return "", nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, info.Size(), maxComposeBytes)
	}
	data, err = os.ReadFile(path)
	return listing.ComposeFile, data, err
}

func (svc *Service) handleReadCompose(w http.ResponseWriter, r *http.Request) {
	name, data, err := composeIn(r.URL.Query().Get("dir"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		} else if errors.Is(err, os.ErrPermission) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	dir := r.URL.Query().Get("dir")
	if listing, err := listDirs(dir); err == nil {
		dir = listing.Path
	}
	writeJSON(w, http.StatusOK, map[string]string{"dir": dir, "name": name, "yaml": string(data)})
}

func (svc *Service) handleListDirs(w http.ResponseWriter, r *http.Request) {
	listing, err := listDirs(r.URL.Query().Get("path"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		} else if errors.Is(err, os.ErrPermission) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}
