package dockerutil

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

// Opt-in: HACO_TEST_DOCKER_VOLUME=<existing named volume>.
func TestTarNamedVolumeReal(t *testing.T) {
	name := os.Getenv("HACO_TEST_DOCKER_VOLUME")
	if name == "" {
		t.Skip("set HACO_TEST_DOCKER_VOLUME to run")
	}
	var buf bytes.Buffer
	found, err := TarNamedVolume(context.Background(), name, &buf)
	if err != nil || !found || buf.Len() == 0 {
		t.Fatalf("found=%v len=%d err=%v", found, buf.Len(), err)
	}
	if found, err := TarNamedVolume(context.Background(), name+"-does-not-exist", io.Discard); found || err != nil {
		t.Fatalf("missing volume: found=%v err=%v", found, err)
	}
	t.Logf("tar bytes: %d", buf.Len())
}
