package dockerutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// BuildxOptions describes one `docker buildx build`. Every haco image build
// goes through BuildKit this way, so Dockerfiles can use cache, secret, ssh
// and bind mounts, COPY --link, heredocs and `# syntax=` directives.
type BuildxOptions struct {
	ContextDir string // an unpacked build context
	Dockerfile string // relative to ContextDir; "" means Dockerfile
	Tag        string
	Platform   string
	NoCache    bool
	Labels     map[string]string
	// Args are build-arg values. They travel through the environment, never
	// the command line, so secrets in them stay out of the process list.
	Args map[string]string
	// SSH maps each ssh mount ID to a private-key file; Secrets maps each
	// secret ID to a file holding its content.
	SSH     map[string]string
	Secrets map[string]string
}

// BuildxArgs builds the argument list for `docker`.
func BuildxArgs(o BuildxOptions) []string {
	dockerfile := o.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	args := []string{"buildx", "build", "--load", "--pull", "--progress=plain",
		"-f", filepath.Join(o.ContextDir, dockerfile), "-t", o.Tag}
	if o.Platform != "" {
		args = append(args, "--platform", o.Platform)
	}
	if o.NoCache {
		args = append(args, "--no-cache")
	}
	for _, k := range sortedKeys(o.Labels) {
		args = append(args, "--label", k+"="+o.Labels[k])
	}
	for _, id := range sortedKeys(o.SSH) {
		args = append(args, "--ssh", id+"="+o.SSH[id])
	}
	for _, id := range sortedKeys(o.Secrets) {
		args = append(args, "--secret", "id="+id+",src="+o.Secrets[id])
	}
	for _, k := range sortedKeys(o.Args) {
		args = append(args, "--build-arg", k)
	}
	return append(args, o.ContextDir)
}

// RunBuildx runs the build and, on failure, returns the tail of BuildKit's
// own output so the real reason (a failing RUN step, a rejected mount)
// reaches the user.
func RunBuildx(ctx context.Context, o BuildxOptions) error {
	bin, err := DockerCLI()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, BuildxArgs(o)...)
	cmd.Env = os.Environ()
	for k, v := range o.Args {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out := &TailBuffer{Max: 30}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		tail := out.String()
		if strings.Contains(tail, "unknown flag: --load") || strings.Contains(tail, "'buildx' is not a docker command") || strings.Contains(tail, "unknown command: docker buildx") {
			return fmt.Errorf("build %s: this node's docker has no buildx plugin; install docker-buildx-plugin (Docker Desktop includes it)\n%s", o.Tag, tail)
		}
		return fmt.Errorf("build %s: %w\n--- last build output ---\n%s", o.Tag, err, tail)
	}
	return nil
}

// WriteBuildFiles writes ssh keys and secrets into 0600 files under dir and
// returns the id -> path maps BuildxOptions expects.
func WriteBuildFiles(dir string, ssh, secrets map[string][]byte) (sshPaths, secretPaths map[string]string, err error) {
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	n := 0
	write := func(data []byte) (string, error) {
		p := filepath.Join(dir, "f-"+strconv.Itoa(n))
		n++
		return p, os.WriteFile(p, data, 0o600)
	}
	sshPaths, secretPaths = map[string]string{}, map[string]string{}
	for id, data := range ssh {
		if sshPaths[id], err = write(data); err != nil {
			return
		}
	}
	for id, data := range secrets {
		if secretPaths[id], err = write(data); err != nil {
			return
		}
	}
	return
}

// DockerCLI finds the docker binary, including Docker Desktop's bundled one
// for a process started without it on PATH.
func DockerCLI() (string, error) {
	if p, err := exec.LookPath("docker"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/local/bin/docker", "/opt/homebrew/bin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("docker CLI not found; builds need `docker buildx` on this node")
}

// TailBuffer keeps the last Max lines written to it.
type TailBuffer struct {
	Max     int
	lines   []string
	partial string
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	parts := strings.Split(t.partial+string(p), "\n")
	t.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l = strings.TrimSpace(l); l != "" {
			t.lines = append(t.lines, l)
		}
	}
	if len(t.lines) > t.Max {
		t.lines = t.lines[len(t.lines)-t.Max:]
	}
	return len(p), nil
}

func (t *TailBuffer) String() string {
	lines := append([]string(nil), t.lines...)
	if s := strings.TrimSpace(t.partial); s != "" {
		lines = append(lines, s)
	}
	if len(lines) > t.Max {
		lines = lines[len(lines)-t.Max:]
	}
	return strings.Join(lines, "\n")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
