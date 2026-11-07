// Package build implements the M4 build service: build an image from a
// Compose service's build context and push it to a registry, so node
// agents on any node can pull it (SRS 5.1 "Build Service", FR-004).
package build

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/abhishek/hardware-aware-container-orchestrator/compose"
)

// Service builds images and pushes them to a registry. It uses a plain
// Docker Engine API build, not full BuildKit, which is why
// compose.BuildSpec's SSH/secret mounts are not honored yet (flagged as a
// warning by the importer, not silently dropped).
type Service struct {
	cli      *client.Client
	registry string // e.g. "localhost:5050"
}

func New(cli *client.Client, registry string) *Service {
	return &Service{cli: cli, registry: registry}
}

// Registry is the configured push target, or "" when this Service builds
// local-only (no registry configured): Build then skips the push step
// entirely and tags without a registry prefix, since the only Docker
// daemon that will ever run the image is this one.
func (s *Service) Registry() string { return s.registry }

// Build builds spec's context and tags the result. With a registry
// configured, that's <registry>/<project>-<service>:<revision>, pushed so
// any node's agent can pull it. With no registry, it's a local-only
// <project>-<service>:<revision> tag that's never pushed — correct only
// for a workload that stays on this same Docker daemon (the control
// plane's own host); ComposeDeploy enforces that placement constraint.
// noCache forces a full rebuild (Docker's build-layer cache is bypassed),
// for when a service's image needs to be rebuilt even though its tag
// already exists locally (e.g. source changed but the Dockerfile/context
// hash alone wouldn't tell Docker that).
func (s *Service) Build(ctx context.Context, project, revision string, spec compose.BuildSpec, noCache bool) (string, error) {
	tarball, err := TarBuildContext(spec.Context, spec.Dockerfile)
	if err != nil {
		return "", fmt.Errorf("package build context: %w", err)
	}

	ref := fmt.Sprintf("%s-%s:%s", project, spec.Service, revision)
	if s.registry != "" {
		ref = fmt.Sprintf("%s/%s", s.registry, ref)
	}

	buildArgs := map[string]*string{}
	for k, v := range spec.Args {
		v := v
		buildArgs[k] = &v
	}

	resp, err := s.cli.ImageBuild(ctx, tarball, types.ImageBuildOptions{
		Dockerfile: spec.Dockerfile,
		BuildArgs:  buildArgs,
		Tags:       []string{ref},
		Remove:     true,
		PullParent: true,
		Platform:   spec.Platform,
		NoCache:    noCache,
	})
	if err != nil {
		return "", fmt.Errorf("build %s: %w", spec.Service, err)
	}
	defer resp.Body.Close()
	if err := consumeDockerStream(resp.Body, "build "+spec.Service); err != nil {
		return "", err
	}

	built, _, err := s.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect built image %s: %w", ref, err)
	}
	if err := validateImagePlatform(built, spec.Platform); err != nil {
		return "", fmt.Errorf("build %s: %w", spec.Service, err)
	}

	if s.registry == "" {
		return ref, nil
	}

	pushRC, err := s.cli.ImagePush(ctx, ref, types.ImagePushOptions{
		RegistryAuth: base64.URLEncoding.EncodeToString([]byte("{}")),
	})
	if err != nil {
		return "", fmt.Errorf("push %s: %w", ref, err)
	}
	defer pushRC.Close()
	if err := consumeDockerStream(pushRC, "push "+spec.Service); err != nil {
		return "", err
	}

	return ref, nil
}

func consumeDockerStream(r io.Reader, operation string) error {
	decoder := json.NewDecoder(r)
	// tail keeps the last few lines of Docker's own log stream (what
	// `docker build`/`docker push` prints to the terminal) so a failure's
	// error can show the actual command output — e.g. git/ssh's real
	// stderr ("Permission denied (publickey)") — instead of just
	// "returned a non-zero code: N", which says nothing about why.
	const tailLines = 20
	var tail []string
	for {
		var message struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read %s output: %w", operation, err)
		}
		if message.Stream != "" {
			for _, line := range strings.Split(message.Stream, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					tail = append(tail, line)
					if len(tail) > tailLines {
						tail = tail[1:]
					}
				}
			}
		}
		detail := message.ErrorDetail.Message
		if detail == "" {
			detail = message.Error
		}
		if detail != "" {
			if len(tail) > 0 {
				return fmt.Errorf("%s: %s\n--- last build output ---\n%s", operation, detail, strings.Join(tail, "\n"))
			}
			return fmt.Errorf("%s: %s", operation, detail)
		}
	}
}

func validateImagePlatform(image types.ImageInspect, requested string) error {
	if requested == "" {
		return nil
	}
	parts := strings.Split(requested, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid requested platform %q", requested)
	}
	actual := strings.ToLower(image.Os + "/" + image.Architecture)
	expected := strings.ToLower(parts[0] + "/" + parts[1])
	if actual != expected {
		return fmt.Errorf("built image platform is %s, requested %s", actual, expected)
	}
	return nil
}

// TarContext packages a build context directory as the tar stream the
// Docker Engine API build endpoint expects. Exported so the control plane
// can package a build context once and ship the resulting bytes to a
// remote agent to build there itself (registry-optional builds,
// multi-machine case) — this function does pure local file I/O, no Docker
// client needed, so a control plane with no Docker at all can still do
// this part.
func TarContext(dir string) (io.Reader, error) {
	return tarDir(dir, nil)
}

// TarBuildContext is TarContext for a Docker build: it honors the
// context's .dockerignore the way `docker build` does, always keeping the
// Dockerfile and .dockerignore themselves. Without this, a `context: .`
// service at a monorepo root ships the whole tree (every repo, every
// database's data directory) even when .dockerignore narrows it to a few
// files.
func TarBuildContext(dir, dockerfile string) (io.Reader, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if os.IsNotExist(err) {
		return tarDir(dir, nil)
	}
	if err != nil {
		return nil, err
	}
	patterns, err := ignorefile.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read .dockerignore: %w", err)
	}
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	patterns = append(patterns, "!"+filepath.ToSlash(filepath.Clean(dockerfile)), "!.dockerignore")
	pm, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("parse .dockerignore: %w", err)
	}
	return tarDir(dir, pm)
}

func tarDir(dir string, pm *patternmatcher.PatternMatcher) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel != "." && pm != nil {
			excluded, err := pm.MatchesOrParentMatches(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			if excluded {
				if !info.IsDir() {
					return nil
				}
				// Same rule as Docker's own archiver: an excluded directory
				// is still walked only if some !exception pattern could
				// re-include something beneath it.
				if !pm.Exclusions() {
					return filepath.SkipDir
				}
				dirSlash := filepath.ToSlash(rel) + "/"
				for _, p := range pm.Patterns() {
					if p.Exclusion() && strings.HasPrefix(p.String()+"/", dirSlash) {
						return nil
					}
				}
				return filepath.SkipDir
			}
		}
		if info.IsDir() {
			if rel == "." {
				return nil // the root itself needs no entry
			}
			// A directory still needs its own tar entry even with no files
			// of its own directly inside it — e.g. Postgres's pg_notify,
			// which must exist (empty) for the server to start at all.
			// Skipping directory entries entirely (as this used to) loses
			// any such empty directory on extraction, since nothing else
			// in the tarball references it.
			return tw.WriteHeader(&tar.Header{Name: rel + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: rel, Mode: int64(info.Mode().Perm() | 0o644), Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}
