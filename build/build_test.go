package build

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
)

func TestConsumeDockerStreamReturnsEmbeddedBuildError(t *testing.T) {
	err := consumeDockerStream(strings.NewReader(`{"stream":"building\n"}{"error":"exec format error","errorDetail":{"message":"exec format error"}}`), "build")
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("consumeDockerStream() error = %v, want embedded daemon error", err)
	}
}

func TestValidateImagePlatformRejectsWrongBuiltArchitecture(t *testing.T) {
	err := validateImagePlatform(types.ImageInspect{Os: "linux", Architecture: "arm64"}, "linux/amd64")
	if err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("validateImagePlatform() error = %v, want actual platform mismatch", err)
	}
}
