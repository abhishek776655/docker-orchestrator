// Package build implements the M4 build service: build an image from a
// Compose service's build context and push it to a registry, so node
// agents on any node can pull it (SRS 5.1 "Build Service", FR-004).
package build

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

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

// Build builds spec's context, tags the result <registry>/<project>-<service>:<revision>,
// pushes it, and returns the pushed reference.
func (s *Service) Build(ctx context.Context, project, revision string, spec compose.BuildSpec) (string, error) {
	tarball, err := tarContext(spec.Context)
	if err != nil {
		return "", fmt.Errorf("package build context: %w", err)
	}

	ref := fmt.Sprintf("%s/%s-%s:%s", s.registry, project, spec.Service, revision)

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
		Platform:   spec.Platform,
	})
	if err != nil {
		return "", fmt.Errorf("build %s: %w", spec.Service, err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return "", fmt.Errorf("read build output for %s: %w", spec.Service, err)
	}

	pushRC, err := s.cli.ImagePush(ctx, ref, types.ImagePushOptions{
		RegistryAuth: base64.URLEncoding.EncodeToString([]byte("{}")),
	})
	if err != nil {
		return "", fmt.Errorf("push %s: %w", ref, err)
	}
	defer pushRC.Close()
	if _, err := io.Copy(io.Discard, pushRC); err != nil {
		return "", fmt.Errorf("read push output for %s: %w", spec.Service, err)
	}

	return ref, nil
}

// tarContext packages a build context directory as the tar stream the
// Docker Engine API build endpoint expects.
func tarContext(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: int64(len(data))}
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
