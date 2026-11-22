package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/abhishek/hardware-aware-container-orchestrator/dockerutil"
)

// imageCmd implements `haco image push`: send an image from this machine's
// Docker to nodes that cannot pull it (no registry, a private image, a
// local build), so deploys there find it already cached.
func imageCmd(baseURL string, args []string) error {
	const usage = "usage: haco image push <image> (--node ID [--node ID ...] | --all) [--any-arch]"
	if len(args) == 0 || args[0] != "push" {
		return fmt.Errorf("%s", usage)
	}
	var image string
	var nodeFlags []string
	all, anyArch := false, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--node":
			if i+1 >= len(args) {
				return fmt.Errorf("--node needs a node ID\n%s", usage)
			}
			i++
			nodeFlags = append(nodeFlags, args[i])
		case "--all":
			all = true
		case "--any-arch":
			anyArch = true
		default:
			if strings.HasPrefix(args[i], "-") || image != "" {
				return fmt.Errorf("unexpected argument %q\n%s", args[i], usage)
			}
			image = args[i]
		}
	}
	if image == "" || (!all && len(nodeFlags) == 0) || (all && len(nodeFlags) > 0) {
		return fmt.Errorf("%s", usage)
	}

	docker, err := dockerutil.DockerCLI()
	if err != nil {
		return err
	}
	archOut, err := exec.Command(docker, "image", "inspect", "--format", "{{.Architecture}}", image).Output()
	if err != nil {
		return fmt.Errorf("image %q is not in this machine's Docker (build or pull it first)", image)
	}
	imageArch := strings.TrimSpace(string(archOut))

	var nodes []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &nodes); err != nil {
		return err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	var targets []string
	for _, n := range nodes {
		if !all && !slices.Contains(nodeFlags, n.ID) {
			continue
		}
		if n.Architecture != "" && imageArch != "" && n.Architecture != imageArch && !anyArch {
			fmt.Printf("skipping %s: it is %s but %s is %s (--any-arch pushes anyway, to run under emulation)\n", n.ID, n.Architecture, image, imageArch)
			continue
		}
		if slices.Contains(n.CachedImages, image) {
			fmt.Printf("skipping %s: already has %s\n", n.ID, image)
			continue
		}
		targets = append(targets, n.ID)
	}
	for _, want := range nodeFlags {
		if !slices.ContainsFunc(nodes, func(n nodeView) bool { return n.ID == want }) {
			return fmt.Errorf("no node %q (see 'haco nodes')", want)
		}
	}
	if len(targets) == 0 {
		fmt.Println("nothing to push")
		return nil
	}

	fmt.Printf("sending %s to %s ...\n", image, strings.Join(targets, ", "))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	save := exec.CommandContext(ctx, docker, "save", image)
	save.Stderr = os.Stderr
	stdout, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	if err := save.Start(); err != nil {
		return fmt.Errorf("docker save: %w", err)
	}

	q := neturl.Values{"image": {image}, "node": targets}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/admin/images/push?"+q.Encode(), stdout)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		_ = save.Wait()
		return err
	}
	defer resp.Body.Close()
	if err := save.Wait(); err != nil {
		return fmt.Errorf("docker save: %w", err)
	}
	return printPushResult(resp.StatusCode, resp.Body, targets)
}

func printPushResult(status int, body io.Reader, targets []string) error {
	data, _ := io.ReadAll(body)
	var res struct {
		Results map[string]string `json:"results"`
		Error   string            `json:"error"`
	}
	_ = json.Unmarshal(data, &res)
	if res.Results == nil {
		if res.Error != "" {
			return fmt.Errorf("%s", res.Error)
		}
		return fmt.Errorf("control plane returned %d: %s", status, strings.TrimSpace(string(data)))
	}
	failed := 0
	for _, n := range targets {
		if e := res.Results[n]; e != "" {
			failed++
			fmt.Printf("  %s: FAILED: %s\n", n, e)
		} else {
			fmt.Printf("  %s: loaded\n", n)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d nodes failed", failed, len(targets))
	}
	return nil
}
