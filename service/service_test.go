package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type rec struct {
	calls  []string
	failOn string
}

func (r *rec) run(_ context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.failOn != "" && strings.Contains(call, r.failOn) {
		return "denied", errors.New("exit 1")
	}
	return "", nil
}

func spec() Spec {
	return Spec{
		Name:    "controlplane",
		Binary:  "/opt/haco/bin/controlplane",
		Env:     map[string]string{"HACO_WEB_DIR": "/home/u/.haco/web", "TOKEN": `a&b"<c>%d`},
		LogPath: "/home/u/.haco/controlplane.log", // fake: Install tests override it
	}
}

func TestPlistEscapesAndSorts(t *testing.T) {
	p := Plist(spec())
	for _, want := range []string{
		"<key>Label</key><string>co.haco.controlplane</string>",
		"<string>/opt/haco/bin/controlplane</string>",
		"<key>TOKEN</key><string>a&amp;b&#34;&lt;c&gt;%d</string>",
		"<key>KeepAlive</key><true/>",
		"<key>StandardOutPath</key><string>/home/u/.haco/controlplane.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q\n%s", want, p)
		}
	}
	if strings.Index(p, "HACO_WEB_DIR") > strings.Index(p, "TOKEN") {
		t.Error("env should be sorted")
	}
}

func TestSystemdUnitQuotesArgsAndEnv(t *testing.T) {
	s := spec()
	s.Binary = "/opt/my haco/agent"
	s.Args = []string{"run"}
	u := SystemdUnit(s)
	for _, want := range []string{
		`ExecStart="/opt/my haco/agent" run`,
		`Environment="TOKEN=a&b\"<c>%%d"`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q\n%s", want, u)
		}
	}
}

func TestInstallMacWritesPrivateFileAndBootstraps(t *testing.T) {
	r := &rec{}
	m := Manager{GOOS: "darwin", Home: t.TempDir(), UID: 501, Run: r.run}
	path, err := m.Install(context.Background(), specIn(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "Library/LaunchAgents/co.haco.controlplane.plist") {
		t.Fatalf("path = %s", path)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (the env holds secrets)", st.Mode().Perm())
	}
	got := strings.Join(r.calls, "\n")
	if !strings.Contains(got, "launchctl bootout gui/501 "+path) || !strings.Contains(got, "launchctl bootstrap gui/501 "+path) {
		t.Fatalf("calls = %s", got)
	}
	if !m.Installed("controlplane") || m.Installed("agent") {
		t.Fatal("Installed wrong")
	}
}

func TestInstallLinuxEnablesAndWarnsWithoutLinger(t *testing.T) {
	r := &rec{failOn: "enable-linger"}
	m := Manager{GOOS: "linux", Home: t.TempDir(), UID: 1000, Run: r.run}
	path, err := m.Install(context.Background(), spec())
	if err == nil || !strings.Contains(err.Error(), "enable-linger") {
		t.Fatalf("err = %v, want a linger hint", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("the unit should still be written")
	}
	got := strings.Join(r.calls, "\n")
	for _, want := range []string{"systemctl --user daemon-reload", "systemctl --user enable haco-controlplane.service", "systemctl --user restart haco-controlplane.service"} {
		if !strings.Contains(got, want) {
			t.Errorf("calls missing %q:\n%s", want, got)
		}
	}
}

func TestInstallFailureNamesTheTool(t *testing.T) {
	r := &rec{failOn: "launchctl bootstrap"}
	m := Manager{GOOS: "darwin", Home: t.TempDir(), UID: 501, Run: r.run}
	if _, err := m.Install(context.Background(), specIn(t)); err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopKeepsDefinitionUninstallRemovesIt(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		r := &rec{}
		m := Manager{GOOS: goos, Home: t.TempDir(), UID: 501, Run: r.run}
		if _, err := m.Install(context.Background(), specIn(t)); err != nil {
			t.Fatal(err)
		}
		if err := m.Stop(context.Background(), "controlplane"); err != nil || !m.Installed("controlplane") {
			t.Fatalf("%s: Stop err=%v installed=%v", goos, err, m.Installed("controlplane"))
		}
		if removed, err := m.Uninstall(context.Background(), "controlplane"); !removed || err != nil || m.Installed("controlplane") {
			t.Fatalf("%s: Uninstall removed=%v err=%v", goos, removed, err)
		}
		if removed, _ := m.Uninstall(context.Background(), "controlplane"); removed {
			t.Fatalf("%s: second Uninstall should report nothing to remove", goos)
		}
	}
}

func TestUnsupportedOS(t *testing.T) {
	m := Manager{GOOS: "windows", Home: t.TempDir(), Run: (&rec{}).run}
	if _, err := m.Install(context.Background(), spec()); err == nil {
		t.Fatal("windows should be refused")
	}
}

func specIn(t *testing.T) Spec {
	s := spec()
	s.LogPath = t.TempDir() + "/logs/controlplane.log"
	return s
}
