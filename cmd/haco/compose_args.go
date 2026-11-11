package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// composeFileNames are the files looked for when -f is not given, in the
// order `docker compose` itself tries them.
var composeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// composeOpts holds what plan, build and deploy share: which Compose file
// and project, which services, and the pin overrides.
type composeOpts struct {
	project  string
	file     string
	services string // comma list, as the API expects it
	yes      bool
	noCache  bool
	fresh    bool
	jsonOut  bool
	pinHost  *[]string
	pinNodes map[string][]string
	// legacy is set when the older "<project> <file>" form was used.
	legacy bool
}

// parseComposeArgs reads `[service...] [-f file] [-p name] [flags]`. flags
// lists the optional switches this command accepts: "json", "fresh",
// "no-cache", "yes". The older `<project> <compose-file>` form is still
// understood.
func parseComposeArgs(cmd string, args []string, flags ...string) (composeOpts, error) {
	allowed := map[string]bool{}
	for _, f := range flags {
		allowed[f] = true
	}
	o := composeOpts{pinNodes: map[string][]string{}}
	var rest []string
	for i := 0; i < len(args); i++ {
		name, inline, hasInline := args[i], "", false
		if strings.HasPrefix(name, "--") {
			name, inline, hasInline = strings.Cut(name, "=")
		}
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			return args[i], nil
		}
		switch name {
		case "-f", "--file":
			v, err := value()
			if err != nil {
				return o, err
			}
			o.file = v
		case "-p", "--project", "--project-name":
			v, err := value()
			if err != nil {
				return o, err
			}
			o.project = v
		case "--service":
			v, err := value()
			if err != nil {
				return o, err
			}
			o.services = addService(o.services, v)
		case "--pin-host":
			v, err := value()
			if err != nil {
				return o, err
			}
			list := splitPinHost(v)
			o.pinHost = &list
		case "--pin-node":
			v, err := value()
			if err != nil {
				return o, err
			}
			node, services, err := parsePinNode(v)
			if err != nil {
				return o, err
			}
			o.pinNodes[node] = append(o.pinNodes[node], services...)
		case "--json", "--fresh", "--no-cache", "--yes", "-y":
			key := strings.TrimPrefix(name, "--")
			if name == "-y" {
				key = "yes"
			}
			if !allowed[key] {
				return o, fmt.Errorf("unknown flag %s for haco %s", args[i], cmd)
			}
			switch key {
			case "json":
				o.jsonOut = true
			case "fresh":
				o.fresh = true
			case "no-cache":
				o.noCache = true
			case "yes":
				o.yes = true
			}
		default:
			if strings.HasPrefix(name, "-") && len(name) > 1 {
				return o, fmt.Errorf("unknown flag %s for haco %s (see haco help %s)", args[i], cmd, cmd)
			}
			rest = append(rest, args[i])
		}
	}
	if o.file == "" && o.project == "" && len(rest) == 2 && looksLikeComposeFile(rest[1]) {
		o.project, o.file, o.legacy = rest[0], rest[1], true
		rest = nil
	}
	for _, s := range rest {
		o.services = addService(o.services, s)
	}
	return o, nil
}

// looksLikeComposeFile tells the older `<project> <file>` form apart from
// `<service> <service>`: a service name never ends in .yml/.yaml.
func looksLikeComposeFile(arg string) bool {
	switch strings.ToLower(filepath.Ext(arg)) {
	case ".yml", ".yaml":
		return true
	}
	base := filepath.Base(arg)
	if strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose") {
		info, err := os.Stat(arg)
		return err == nil && info.Mode().IsRegular()
	}
	return false
}

// resolve finds the Compose file and project name, applying the defaults
// (a Compose file in the current folder; the file's folder name as the
// project), and tells the user on stderr what it chose so a wrong default
// is never silent.
func (o composeOpts) resolve(cmd string) (project, file, workingDir string, yaml []byte, err error) {
	file = o.file
	if file == "" {
		if file, err = findComposeFile("."); err != nil {
			return
		}
	}
	if yaml, err = os.ReadFile(file); err != nil {
		err = fmt.Errorf("read %s: %w", file, err)
		return
	}
	if workingDir, err = filepath.Abs(filepath.Dir(file)); err != nil {
		return
	}
	project, derived := o.project, ""
	if project == "" {
		project = os.Getenv("COMPOSE_PROJECT_NAME")
		derived = "COMPOSE_PROJECT_NAME"
	}
	if project == "" {
		if project = projectNameFromDir(workingDir); project == "" {
			err = fmt.Errorf("can't derive a project name from %q; pass one with -p", workingDir)
			return
		}
		derived = "folder name"
	}
	if o.legacy {
		fmt.Fprintf(os.Stderr, "haco: \"haco %s <project> <file>\" still works; the new form is \"haco %s -p <project> -f <file>\"\n", cmd, cmd)
	} else if derived != "" {
		fmt.Fprintf(os.Stderr, "haco: project %q (from the %s; change it with -p), file %s\n", project, derived, file)
	}
	return
}

// findComposeFile looks for a Compose file in dir, in Compose's own order.
func findComposeFile(dir string) (string, error) {
	for _, name := range composeFileNames {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Compose file in the current folder (looked for %s); use -f <file>", strings.Join(composeFileNames, ", "))
}

// projectNameFromDir derives a project name the way Compose does: the
// folder name, lowercased, keeping only letters, digits, '-' and '_'.
func projectNameFromDir(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "-_")
}
