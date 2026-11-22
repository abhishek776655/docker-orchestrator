package nodeadd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// SSH is a Runner that uses the system ssh and scp binaries.
type SSH struct {
	Target string   // user@host
	Args   []string // extra options for both ssh and scp, e.g. -i <key>
	Port   string   // optional port; ssh takes -p, scp takes -P
}

func (s SSH) sshArgs() []string {
	a := []string{"-o", "BatchMode=no", "-o", "StrictHostKeyChecking=accept-new"}
	if s.Port != "" {
		a = append(a, "-p", s.Port)
	}
	return append(a, s.Args...)
}

func (s SSH) scpArgs() []string {
	a := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if s.Port != "" {
		a = append(a, "-P", s.Port)
	}
	return append(a, s.Args...)
}

func (s SSH) Run(ctx context.Context, script string) (string, error) {
	args := append(s.sshArgs(), s.Target, "sh", "-c", shellQuote(script))
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func (s SSH) Copy(ctx context.Context, localPath, remotePath string) error {
	args := append(s.scpArgs(), localPath, s.Target+":"+remotePath)
	if out, err := exec.CommandContext(ctx, "scp", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
