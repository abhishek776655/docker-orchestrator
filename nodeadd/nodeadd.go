// Package nodeadd installs the haco agent on another machine over SSH:
// detect its OS and architecture, copy the right agent binary, join the
// cluster with a one-time token and run the agent as a service so it
// survives reboots. It shells out to the system ssh/scp (the same keys,
// agent and ~/.ssh/config the user already has), through the Runner
// interface so the flow can be tested without a network.
package nodeadd

import (
	"context"
	"fmt"
	"strings"
)

// Runner runs commands on, and copies files to, the target machine.
type Runner interface {
	// Run executes script with `sh -c` on the target and returns its
	// combined output.
	Run(ctx context.Context, script string) (string, error)
	// Copy uploads a local file to remotePath.
	Copy(ctx context.Context, localPath, remotePath string) error
}

// Platform is the target machine's OS and CPU architecture in Go terms.
type Platform struct{ OS, Arch string }

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// ParseUname turns `uname -sm` output into a Platform.
func ParseUname(out string) (Platform, error) {
	f := strings.Fields(out)
	if len(f) != 2 {
		return Platform{}, fmt.Errorf("unexpected `uname -sm` output %q", strings.TrimSpace(out))
	}
	var p Platform
	switch strings.ToLower(f[0]) {
	case "darwin":
		p.OS = "darwin"
	case "linux":
		p.OS = "linux"
	default:
		return Platform{}, fmt.Errorf("unsupported operating system %q (haco nodes run on macOS or Linux)", f[0])
	}
	switch strings.ToLower(f[1]) {
	case "arm64", "aarch64":
		p.Arch = "arm64"
	case "x86_64", "amd64":
		p.Arch = "amd64"
	default:
		return Platform{}, fmt.Errorf("unsupported CPU architecture %q", f[1])
	}
	return p, nil
}

// Options describes one `haco node add`.
type Options struct {
	// JoinToken is the encoded one-time join bundle.
	JoinToken string
	// ResolveBinary returns the local path of an agent binary for the
	// target platform (a local file or a verified download).
	ResolveBinary func(Platform) (string, error)
	// Log receives progress lines.
	Log func(string)
}

const (
	remoteDir    = "$HOME/.haco/bin"
	remoteBinary = remoteDir + "/agent"
	dockerPath   = `PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin"`
)

// Add runs the whole flow against r.
func Add(ctx context.Context, r Runner, o Options) error {
	log := o.Log
	if log == nil {
		log = func(string) {}
	}

	out, err := r.Run(ctx, "uname -sm")
	if err != nil {
		return fmt.Errorf("connect over ssh: %w", err)
	}
	plat, err := ParseUname(out)
	if err != nil {
		return err
	}
	log("target is " + plat.String())

	if _, err := r.Run(ctx, dockerPath+"; docker info >/dev/null 2>&1 || { echo 'docker is not installed or not running' >&2; exit 1; }"); err != nil {
		return fmt.Errorf("the target needs Docker installed and running before it can join: %w", err)
	}
	log("docker is available")

	local, err := o.ResolveBinary(plat)
	if err != nil {
		return err
	}
	if _, err := r.Run(ctx, `mkdir -p "`+remoteDir+`"`); err != nil {
		return fmt.Errorf("create %s: %w", remoteDir, err)
	}
	// Copy beside the final path and rename, so a running agent's binary is
	// never half-written.
	if err := r.Copy(ctx, local, "~/.haco/bin/agent.new"); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	if _, err := r.Run(ctx, `chmod +x "`+remoteBinary+`.new" && mv "`+remoteBinary+`.new" "`+remoteBinary+`"`); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	log("agent copied to ~/.haco/bin/agent")

	if _, err := r.Run(ctx, `"`+remoteBinary+`" join `+shellQuote(o.JoinToken)+` --no-run`); err != nil {
		return fmt.Errorf("join the cluster: %w", err)
	}
	log("joined the cluster")

	if err := installService(ctx, r, plat); err != nil {
		return err
	}
	log("agent installed as a service and started")
	return nil
}

func installService(ctx context.Context, r Runner, p Platform) error {
	switch p.OS {
	case "linux":
		script := `mkdir -p "$HOME/.config/systemd/user" && cat > "$HOME/.config/systemd/user/haco-agent.service" <<'HACO_EOF'
` + SystemdUnit() + `HACO_EOF
systemctl --user daemon-reload && systemctl --user enable haco-agent && systemctl --user restart haco-agent
loginctl enable-linger "$USER" 2>/dev/null || echo "note: could not enable linger; the agent stops when $USER logs out (run: sudo loginctl enable-linger $USER)"`
		if out, err := r.Run(ctx, script); err != nil {
			return fmt.Errorf("install systemd user service: %w", err)
		} else if strings.Contains(out, "note:") {
			return fmt.Errorf("%s", strings.TrimSpace(out))
		}
	case "darwin":
		script := `mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.haco/logs" && cat > "$HOME/Library/LaunchAgents/co.haco.agent.plist" <<HACO_EOF
` + LaunchdPlist() + `HACO_EOF
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/co.haco.agent.plist" 2>/dev/null
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/co.haco.agent.plist"`
		if _, err := r.Run(ctx, script); err != nil {
			return fmt.Errorf("install launchd agent (the user must be logged in to the Mac): %w", err)
		}
	}
	return nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
