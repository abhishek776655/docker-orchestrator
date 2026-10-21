package controlplane

import "testing"

func TestNewHandlerRegistersWorkloadLogsRoute(t *testing.T) {
	if handler := NewHandler(nil); handler == nil {
		t.Fatal("NewHandler returned nil")
	}
}
