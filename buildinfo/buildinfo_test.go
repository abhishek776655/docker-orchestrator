package buildinfo

import (
	"strings"
	"testing"
)

func TestStringNamesBinaryVersionAndPlatform(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })
	Version, Commit, Date = "v1.2.3", "abc1234", "2026-11-22"
	got := String("haco")
	for _, want := range []string{"haco v1.2.3", "(abc1234)", "built 2026-11-22", "/"} {
		if !strings.Contains(got, want) {
			t.Fatalf("String() = %q, missing %q", got, want)
		}
	}
}

func TestIsVersionArg(t *testing.T) {
	if !IsVersionArg("--version") || IsVersionArg("version-x") {
		t.Fatal("IsVersionArg wrong")
	}
}
