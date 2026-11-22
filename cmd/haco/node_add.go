package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/buildinfo"
	"github.com/abhishek/hardware-aware-container-orchestrator/nodeadd"
)

// nodeAdd implements `haco node add <user@host>`: install the agent on
// another machine over SSH, join it and run it as a service.
func nodeAdd(baseURL string, args []string) error {
	const usage = "usage: haco node add <user@host> [--ssh-key FILE] [--port N] [--agent-binary FILE] [--version TAG]"
	var (
		target, key, port, binary string
		version                   = releaseVersion()
	)
	for i := 0; i < len(args); i++ {
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value\n%s", args[i], usage)
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--ssh-key":
			key, err = next()
		case "--port":
			port, err = next()
		case "--agent-binary":
			binary, err = next()
		case "--version":
			version, err = next()
		default:
			if strings.HasPrefix(args[i], "-") || target != "" {
				return fmt.Errorf("unexpected argument %q\n%s", args[i], usage)
			}
			target = args[i]
		}
		if err != nil {
			return err
		}
	}
	if target == "" {
		return fmt.Errorf("%s", usage)
	}

	before, err := nodeIDs(baseURL)
	if err != nil {
		return fmt.Errorf("reach the control plane at %s: %w", baseURL, err)
	}
	token, err := fetchJoinToken(baseURL)
	if err != nil {
		return err
	}

	ssh := nodeadd.SSH{Target: target, Port: port}
	if key != "" {
		ssh.Args = []string{"-i", key}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = nodeadd.Add(ctx, ssh, nodeadd.Options{
		JoinToken: token,
		ResolveBinary: nodeadd.BinarySource{
			Explicit: binary,
			Version:  version,
		}.Resolve,
		Log: func(s string) { fmt.Println("  " + s) },
	})
	if err != nil {
		return err
	}

	fmt.Println("  waiting for the node to report in...")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ids, err := nodeIDs(baseURL)
		if err == nil {
			for id := range ids {
				if !before[id] {
					fmt.Printf("node %s joined; see it with 'haco nodes'\n", id)
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("the agent was installed on %s but no new node appeared within 60s; check ~/.haco/logs/agent.log (macOS) or 'journalctl --user -u haco-agent' (Linux) on it", target)
}

// releaseVersion is the tag to download agents for, or "" for a build from
// source.
func releaseVersion() string {
	v := buildinfo.Version
	if v == "dev" || strings.Contains(v, "SNAPSHOT") {
		return os.Getenv("HACO_RELEASE_VERSION")
	}
	return v
}

func nodeIDs(baseURL string) (map[string]bool, error) {
	var out []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &out); err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(out))
	for _, n := range out {
		ids[n.ID] = true
	}
	return ids, nil
}
