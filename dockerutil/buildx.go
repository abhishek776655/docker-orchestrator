package dockerutil

import (
	"context"
	"fmt"
	"log"
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
		if hint := ExplainBuildFailure(tail); hint != "" {
			return fmt.Errorf("build %s: %w\n%s\n--- last build output ---\n%s", o.Tag, err, hint, tail)
		}
		return fmt.Errorf("build %s: %w\n--- last build output ---\n%s", o.Tag, err, tail)
	}
	if err := PruneBuildCache(ctx, BuildCacheLimit()); err != nil {
		log.Printf("%v", err)
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

// ExplainBuildFailure turns BuildKit's terse context errors into a plain
// sentence saying what to check; "" when the output is not one it knows.
func ExplainBuildFailure(tail string) string {
	switch {
	case strings.Contains(tail, "forbidden path outside the build context"):
		return "hint: the Dockerfile refers to a path outside the build context (the `context:` folder); BuildKit forbids `..` paths. Move the file into the context, or make the context a parent folder."
	case strings.Contains(tail, "failed to compute cache key") && strings.Contains(tail, "not found"),
		strings.Contains(tail, "failed to calculate checksum") && strings.Contains(tail, "not found"):
		return "hint: a file the Dockerfile copies is not in the build context. Check the path, and check .dockerignore: an ignored file is not sent to the build (the Dockerfile itself is always sent)."
	case strings.Contains(tail, "failed to read dockerfile"):
		return "hint: BuildKit could not read the Dockerfile; check `build.dockerfile` and that .dockerignore does not exclude it."
	}
	return ""
}

// HasBuildx reports whether `docker buildx` works on this machine.
func HasBuildx(ctx context.Context) bool {
	bin, err := DockerCLI()
	if err != nil {
		return false
	}
	return exec.CommandContext(ctx, bin, "buildx", "version").Run() == nil
}

// DefaultBuildCacheLimit is how much BuildKit cache a node keeps.
const DefaultBuildCacheLimit = "10GB"

// BuildCacheLimit is the node's cache cap: HACO_BUILD_CACHE_LIMIT (for
// example "20GB"), the default when unset, and "" (no pruning) for "0".
func BuildCacheLimit() string {
	v := strings.TrimSpace(os.Getenv("HACO_BUILD_CACHE_LIMIT"))
	switch v {
	case "":
		return DefaultBuildCacheLimit
	case "0", "off", "none":
		return ""
	}
	return v
}

// PruneBuildCache trims BuildKit's cache to limit, oldest entries first, so
// repeated builds don't fill the disk. Best effort: the caller logs errors.
func PruneBuildCache(ctx context.Context, limit string) error {
	if limit == "" {
		return nil
	}
	bin, err := DockerCLI()
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, bin, "buildx", "prune", "--force", "--max-used-space", limit).CombinedOutput()
	if err != nil && strings.Contains(string(out), "unknown flag: --max-used-space") {
		// buildx before 0.17 spells it --keep-storage.
		out, err = exec.CommandContext(ctx, bin, "buildx", "prune", "--force", "--keep-storage", limit).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("prune build cache: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
