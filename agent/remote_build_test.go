package agent

import (
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func TestNeedsRemoteBuild(t *testing.T) {
	spec := model.RemoteBuildSpec{BuildID: "abc"}
	cases := []struct {
		name     string
		spec     model.RemoteBuildSpec
		hasImage bool
		localID  string
		want     bool
	}{
		{"missing image", spec, false, "", true},
		{"same inputs", spec, true, "abc", false},
		{"source changed", spec, true, "old", true},
		{"image built before build IDs existed", spec, true, "", true},
		{"legacy spec without build ID keeps old behavior", model.RemoteBuildSpec{}, true, "", false},
	}
	for _, c := range cases {
		if got := needsRemoteBuild(c.spec, c.hasImage, c.localID); got != c.want {
			t.Errorf("%s: needsRemoteBuild = %v, want %v", c.name, got, c.want)
		}
	}
}
