package controlplane

import (
	"context"
	"reflect"
	"testing"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
)

func TestFilterDiscoveredDropsSelfAndRegisteredNodes(t *testing.T) {
	neighbors := []netutil.Neighbor{
		{IP: "192.168.1.3", MAC: "aa:aa:aa:aa:aa:03"},
		{IP: "192.168.1.6", MAC: "aa:aa:aa:aa:aa:06", Hostname: "nas.local"},
		{IP: "192.168.1.11", MAC: "aa:aa:aa:aa:aa:11"},
		{IP: "192.168.1.12", MAC: "aa:aa:aa:aa:aa:12"},
	}
	nodes := []model.Node{{ID: "n1", Address: "192.168.1.3"}}
	local := map[string]bool{"192.168.1.11": true}

	got := filterDiscovered(neighbors, nodes, local)
	want := []discoveredDevice{
		{Address: "192.168.1.6", Hostname: "nas.local", MAC: "aa:aa:aa:aa:aa:06"},
		{Address: "192.168.1.12", MAC: "aa:aa:aa:aa:aa:12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterDiscovered = %#v, want %#v", got, want)
	}
}

func TestEmptyHint(t *testing.T) {
	if got := emptyHint("darwin", 0); got == "" {
		t.Fatal("an empty table on macOS should explain Local Network access")
	}
	if got := emptyHint("linux", 0); got == "" {
		t.Fatal("an empty table should always carry a hint")
	}
	if got := emptyHint("darwin", 3); got != "" {
		t.Fatalf("hint = %q, want none when devices were seen", got)
	}
}

func TestNeighborScanCachesWithinTTL(t *testing.T) {
	var s neighborScan
	calls := 0
	scan := func(context.Context) ([]netutil.Neighbor, error) {
		calls++
		return []netutil.Neighbor{{IP: "192.168.1.6"}}, nil
	}
	for i := 0; i < 3; i++ {
		if _, err := s.get(context.Background(), scan); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("scan ran %d times, want 1 within the TTL", calls)
	}
}

func TestFilterDiscoveredEmptyIsNotNil(t *testing.T) {
	if got := filterDiscovered(nil, nil, nil); got == nil {
		t.Fatal("want an empty slice so the API encodes [] rather than null")
	}
}
