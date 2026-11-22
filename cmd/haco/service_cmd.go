package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/service"
)

func localServiceManager() service.Manager {
	return service.Manager{GOOS: runtime.GOOS, Home: homeDir(), UID: os.Getuid(), Run: service.ExecRunner}
}

// dockerPathDirs are where Docker's CLI lives on a Mac; launchd and
// systemd start services with a minimal PATH that lacks them.
const dockerPathDirs = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:/Applications/Docker.app/Contents/Resources/bin"

// serviceSpec builds the definition for "controlplane" or "agent".
// Settings come from the environment `haco` runs in (every HACO_*
// variable) and an optional env file of KEY=VALUE lines; both are copied
// into the service, so re-run the install to change them.
func serviceSpec(name string, web, registry bool, envFile string) (service.Spec, error) {
	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	env := map[string]string{"PATH": dockerPathDirs + ":" + os.Getenv("PATH")}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "HACO_") {
			env[k] = v
		}
	}
	if envFile != "" {
		extra, err := readEnvFile(envFile)
		if err != nil {
			return service.Spec{}, err
		}
		for k, v := range extra {
			env[k] = v
		}
	}

	spec := service.Spec{Name: name, Env: env, LogPath: filepath.Join(dataDir, name+".log")}
	var err error
	switch name {
	case "controlplane":
		if spec.Binary, err = controlPlaneBinary(); err != nil {
			return spec, err
		}
		delete(env, "HACO_WEB_DIR")
		if web {
			dir, err := webConsoleDir(dataDir)
			if err != nil {
				return spec, err
			}
			env["HACO_WEB_DIR"] = dir
		}
		if registry && env["HACO_REGISTRY_ADDR"] == "" {
			env["HACO_REGISTRY_ADDR"] = "auto"
		}
	case "agent":
		if spec.Binary, err = agentBinary(); err != nil {
			return spec, err
		}
		spec.Args = []string{"run"}
	default:
		return spec, fmt.Errorf("unknown service %q: use controlplane or agent", name)
	}
	if spec.Binary, err = filepath.Abs(spec.Binary); err != nil {
		return spec, err
	}
	return spec, nil
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("%s: %q is not KEY=VALUE", path, line)
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, sc.Err()
}

// serviceCmd implements `haco service install|uninstall|status`.
func serviceCmd(baseURL string, args []string) error {
	const usage = "usage: haco service install|uninstall|status controlplane|agent [--web] [--registry] [--env-file FILE]"
	if len(args) < 2 {
		return fmt.Errorf("%s", usage)
	}
	action, name := args[0], args[1]
	var web, registry bool
	var envFile string
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--web":
			web = true
		case "--registry":
			registry = true
		case "--env-file":
			if i+1 >= len(args) {
				return fmt.Errorf("--env-file needs a file\n%s", usage)
			}
			i++
			envFile = args[i]
		default:
			return fmt.Errorf("unexpected argument %q\n%s", args[i], usage)
		}
	}
	m := localServiceManager()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch action {
	case "install":
		return installService(ctx, m, baseURL, name, web, registry, envFile)
	case "uninstall":
		removed, err := m.Uninstall(ctx, name)
		if err != nil {
			return err
		}
		if !removed {
			fmt.Printf("%s service is not installed\n", name)
			return nil
		}
		fmt.Printf("%s service stopped and removed\n", name)
		return nil
	case "status":
		path, err := m.Path(name)
		if err != nil {
			return err
		}
		if !m.Installed(name) {
			fmt.Printf("%s service: not installed\n", name)
			return nil
		}
		fmt.Printf("%s service: installed (%s)\n", name, path)
		if name == "controlplane" {
			fmt.Printf("control plane answering at %s: %v\n", baseURL, controlPlaneUp(baseURL))
		}
		return nil
	}
	return fmt.Errorf("%s", usage)
}

func installService(ctx context.Context, m service.Manager, baseURL, name string, web, registry bool, envFile string) error {
	spec, err := serviceSpec(name, web, registry, envFile)
	if err != nil {
		return err
	}
	path, err := m.Install(ctx, spec)
	if path != "" {
		fmt.Printf("%s service written to %s\n", name, path)
	}
	if err != nil {
		return err
	}
	if name == "controlplane" {
		for i := 0; i < 30; i++ {
			if controlPlaneUp(baseURL) {
				fmt.Printf("control plane ready at %s (it now starts at login and restarts if it dies)\n", baseURL)
				return nil
			}
			time.Sleep(time.Second)
		}
		return fmt.Errorf("service installed but the control plane did not answer within 30s; see %s", spec.LogPath)
	}
	fmt.Printf("agent service started (logs: %s)\n", spec.LogPath)
	return nil
}
