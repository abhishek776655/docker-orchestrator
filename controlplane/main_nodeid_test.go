package controlplane

import (
	"os"
	"testing"
)

// Tests must never create or read the real ~/.haco/node-id.
func TestMain(m *testing.M) {
	os.Setenv("HACO_HOST_NODE_ID", "test-host")
	os.Exit(m.Run())
}
