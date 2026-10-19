// Command hacoctl is a thin CLI over the control-plane HTTP API, so using
// the platform doesn't require hand-assembling JSON with curl. It has no
// state of its own — every subcommand is a single HTTP call.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	url := envOr("HACO_CONTROLPLANE_URL", "http://localhost:8099")

	var err error
	switch os.Args[1] {
	case "deploy":
		err = runCompose(url, "/v1/compose/deploy", os.Args[2:])
	case "plan":
		err = runCompose(url, "/v1/compose/plan", os.Args[2:])
	case "nodes":
		err = get(url, "/v1/nodes")
	case "status":
		err = status(url, os.Args[2:])
	case "events":
		err = get(url, "/v1/events")
	case "metrics":
		err = get(url, "/v1/metrics")
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `hacoctl: a thin CLI over the control-plane API (HACO_CONTROLPLANE_URL, default http://localhost:8099)

Usage:
  hacoctl deploy <project> <docker-compose.yml>   build, schedule, and run a Compose project
  hacoctl plan   <project> <docker-compose.yml>   dry-run the same, no side effects
  hacoctl status <project> <service> <revision>   one workload's current status
  hacoctl nodes                                   list registered nodes
  hacoctl events                                  recent audit events
  hacoctl metrics                                 node/workload counts`)
}

func runCompose(baseURL, path string, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: hacoctl %s <project> <docker-compose.yml>", filepath.Base(path))
	}
	project, file := args[0], args[1]

	yaml, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	workingDir, err := filepath.Abs(filepath.Dir(file))
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]string{
		"project":     project,
		"working_dir": workingDir,
		"yaml":        string(yaml),
	})

	return post(baseURL, path, body)
}

func status(baseURL string, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: hacoctl status <project> <service> <revision>")
	}
	return get(baseURL, "/v1/workloads/"+args[0]+"/"+args[1]+"/"+args[2])
}

func get(baseURL, path string) error {
	resp, err := http.Get(baseURL + path)
	if err != nil {
		return err
	}
	return printResponse(resp)
}

func post(baseURL, path string, body []byte) error {
	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	return printResponse(resp)
}

func printResponse(resp *http.Response) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(data))
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("control plane returned %s", resp.Status)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
