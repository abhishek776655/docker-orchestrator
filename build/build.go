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
	for {
		var message struct {
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
		if message.ErrorDetail.Message != "" {
			return fmt.Errorf("%s: %s", operation, message.ErrorDetail.Message)
		}
		if message.Error != "" {
			return fmt.Errorf("%s: %s", operation, message.Error)
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
