package dockerutil

import (
	"context"
	"os"
	"testing"
)

// Opt-in: HACO_TEST_DOCKER_VOLUME=<existing named volume>.
func TestTarNamedVolumeReal(t *testing.T) {
	name := os.Getenv("HACO_TEST_DOCKER_VOLUME")
	if name == "" {
		t.Skip("set HACO_TEST_DOCKER_VOLUME to run")
	}
	data, found, err := TarNamedVolume(context.Background(), name)
	if err != nil || !found || len(data) == 0 {
		t.Fatalf("found=%v len=%d err=%v", found, len(data), err)
	}
	if _, found, err := TarNamedVolume(context.Background(), name+"-does-not-exist"); found || err != nil {
		t.Fatalf("missing volume: found=%v err=%v", found, err)
	}
	t.Logf("tar bytes: %d", len(data))
}
