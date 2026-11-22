// Package service installs haco's control plane or agent as a per-user
// background service, so a reboot or logout doesn't need a manual
// `haco start` / `haco agent run`: a LaunchAgent on macOS, a systemd user
// unit on Linux. Files are written under Home and the platform tool is run
// through Runner, so tests need neither.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Spec describes one service.
type Spec struct {
	Name    string            // "controlplane" or "agent"
	Binary  string            // absolute path
	Args    []string          // e.g. ["run"] for the agent
	Env     map[string]string // baked into the service definition
	LogPath string            // macOS only; systemd logs to the journal
}

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// ExecRunner runs real commands.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Manager installs services for one user on one OS.
type Manager struct {
	GOOS string
	Home string
	UID  int
	Run  Runner
}

// Label is the launchd label and systemd unit stem.
func Label(name string) string { return "co.haco." + name }

func (m Manager) unitName(name string) string { return "haco-" + name + ".service" }

// Path is where the definition file lives.
func (m Manager) Path(name string) (string, error) {
	switch m.GOOS {
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label(name)+".plist"), nil
	case "linux":
		return filepath.Join(m.Home, ".config", "systemd", "user", m.unitName(name)), nil
	}
	return "", fmt.Errorf("services are supported on macOS and Linux, not %s", m.GOOS)
}

// Install writes the definition and (re)starts the service.
func (m Manager) Install(ctx context.Context, s Spec) (string, error) {
	path, err := m.Path(s.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	var body string
	if m.GOOS == "darwin" {
		if s.LogPath != "" {
			if err := os.MkdirAll(filepath.Dir(s.LogPath), 0o755); err != nil {
				return "", err
			}
		}
		body = Plist(s)
	} else {
		body = SystemdUnit(s)
	}
	// 0600: the environment may hold secrets (auth keys, tokens).
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}

	switch m.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", m.UID)
		_, _ = m.Run(ctx, "launchctl", "bootout", domain, path) // not loaded yet is fine
		if out, err := m.Run(ctx, "launchctl", "bootstrap", domain, path); err != nil {
			return path, fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(out))
		}
	case "linux":
		unit := m.unitName(s.Name)
		for _, args := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
			if out, err := m.Run(ctx, "systemctl", append([]string{"--user"}, args...)...); err != nil {
				return path, fmt.Errorf("systemctl %s: %w: %s", args[0], err, strings.TrimSpace(out))
			}
		}
		// Without linger the user's services stop at logout. May need sudo.
		if out, err := m.Run(ctx, "loginctl", "enable-linger", fmt.Sprint(m.UID)); err != nil {
			return path, fmt.Errorf("installed, but the service will stop when you log out; run: sudo loginctl enable-linger $USER (%s)", strings.TrimSpace(out))
		}
	}
	return path, nil
}

// Uninstall stops the service and deletes its definition.
func (m Manager) Uninstall(ctx context.Context, name string) (bool, error) {
	path, err := m.Path(name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	switch m.GOOS {
	case "darwin":
		_, _ = m.Run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", m.UID), path)
	case "linux":
		unit := m.unitName(name)
		_, _ = m.Run(ctx, "systemctl", "--user", "disable", "--now", unit)
	}
	if err := os.Remove(path); err != nil {
		return true, err
	}
	if m.GOOS == "linux" {
		_, _ = m.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return true, nil
}

// Installed reports whether name's definition exists.
func (m Manager) Installed(name string) bool {
	path, err := m.Path(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func sortedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Plist renders the LaunchAgent.
func Plist(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", esc(Label(s.Name)))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range append([]string{s.Binary}, s.Args...) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", esc(a))
	}
	b.WriteString("  </array>\n")
	if len(s.Env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range sortedEnv(s.Env) {
			fmt.Fprintf(&b, "    <key>%s</key><string>%s</string>\n", esc(k), esc(s.Env[k]))
		}
		b.WriteString("  </dict>\n")
	}
	b.WriteString("  <key>RunAtLoad</key><true/>\n  <key>KeepAlive</key><true/>\n")
	if s.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n  <key>StandardErrorPath</key><string>%s</string>\n", esc(s.LogPath), esc(s.LogPath))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// SystemdUnit renders the user unit.
func SystemdUnit(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=haco %s\nAfter=network-online.target docker.service\nWants=network-online.target\n\n[Service]\n", s.Name)
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(quoteAll(append([]string{s.Binary}, s.Args...)), " "))
	for _, k := range sortedEnv(s.Env) {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s.Env[k])
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", k, v)
	}
	b.WriteString("Restart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, a := range in {
		if strings.ContainsAny(a, " \t\"'\\") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
		}
		out[i] = strings.ReplaceAll(a, "%", "%%")
	}
	return out
}

// Start loads an already installed service.
func (m Manager) Start(ctx context.Context, name string) error {
	path, err := m.Path(name)
	if err != nil {
		return err
	}
	switch m.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", m.UID)
		_, _ = m.Run(ctx, "launchctl", "bootout", domain, path)
		if out, err := m.Run(ctx, "launchctl", "bootstrap", domain, path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(out))
		}
	case "linux":
		if out, err := m.Run(ctx, "systemctl", "--user", "restart", m.unitName(name)); err != nil {
			return fmt.Errorf("systemctl restart: %w: %s", err, strings.TrimSpace(out))
		}
	}
	return nil
}

// Stop unloads the service without deleting its definition, so it stays
// stopped (KeepAlive/Restart=always would otherwise bring it back) until
// Start.
func (m Manager) Stop(ctx context.Context, name string) error {
	path, err := m.Path(name)
	if err != nil {
		return err
	}
	switch m.GOOS {
	case "darwin":
		_, _ = m.Run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", m.UID), path)
	case "linux":
		if out, err := m.Run(ctx, "systemctl", "--user", "stop", m.unitName(name)); err != nil {
			return fmt.Errorf("systemctl stop: %w: %s", err, strings.TrimSpace(out))
		}
	}
	return nil
}
