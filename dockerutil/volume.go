package dockerutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// VolumeHelperImage is the image used to read a named volume's files.
const VolumeHelperImage = "busybox:1.36"

// TarNamedVolume packs the contents of an existing Docker named volume into
// w as a tar stream, by mounting it read-only in a throwaway container.
// found is false (with a nil error) when the volume does not exist or is
// empty, so callers can treat "nothing to adopt" as the normal case; w may
// have received nothing then. The named volume itself is never modified.
func TarNamedVolume(ctx context.Context, name string, w io.Writer) (found bool, err error) {
	docker, err := DockerCLI()
	if err != nil {
		return false, err
	}
	if err := exec.CommandContext(ctx, docker, "volume", "inspect", name).Run(); err != nil {
		return false, nil // no such volume
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, docker, "run", "--rm", "--network", "none",
		"-v", name+":/v:ro", "--entrypoint", "sh", VolumeHelperImage,
		"-c", `[ -n "$(ls -A /v)" ] && tar -c -C /v . || exit 3`)
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
			return false, nil // volume exists but is empty
		}
		return false, fmt.Errorf("read docker volume %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
}
