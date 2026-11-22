// Package buildinfo carries the version a release binary was built from.
// GoReleaser fills Version, Commit and Date through -ldflags; a plain
// `go build` or `go install` leaves them at their defaults.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String is the one-line version text printed by `--version`.
func String(binary string) string {
	commit := Commit
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					commit = s.Value[:7]
				}
			}
		}
	}
	out := fmt.Sprintf("%s %s", binary, Version)
	if commit != "" {
		out += " (" + commit + ")"
	}
	if Date != "" {
		out += " built " + Date
	}
	return out + fmt.Sprintf(" %s/%s", runtime.GOOS, runtime.GOARCH)
}

// IsVersionArg reports whether arg asks for the version.
func IsVersionArg(arg string) bool {
	return arg == "--version" || arg == "-version"
}
