package netutil

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestProbeHostsFindsListenerAndRefusal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, openPort, _ := net.SplitHostPort(ln.Addr().String())

	// A port that was just closed on loopback refuses, which also proves a host.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closedPort, _ := net.SplitHostPort(closed.Addr().String())
	closed.Close()

	for name, port := range map[string]string{"open": openPort, "refused": closedPort} {
		got := probeHosts(context.Background(), []string{"127.0.0.1"}, []string{port}, 500*time.Millisecond)
		if !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
			t.Fatalf("%s port: probeHosts = %v, want [127.0.0.1]", name, got)
		}
	}
}

func TestProbeHostsSkipsSilentHosts(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation and answers nothing.
	got := probeHosts(context.Background(), []string{"192.0.2.1"}, []string{"22"}, 200*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("probeHosts = %v, want none for a silent host", got)
	}
}

func TestMergeNeighborsUnionsAndSorts(t *testing.T) {
	arp := []Neighbor{{IP: "192.168.1.12", MAC: "aa:aa:aa:aa:aa:12"}, {IP: "192.168.1.6", MAC: "aa:aa:aa:aa:aa:06", Hostname: "nas"}}
	got := mergeNeighbors(arp, []string{"192.168.1.30", "192.168.1.6"})
	want := []Neighbor{
		{IP: "192.168.1.6", MAC: "aa:aa:aa:aa:aa:06", Hostname: "nas"},
		{IP: "192.168.1.12", MAC: "aa:aa:aa:aa:aa:12"},
		{IP: "192.168.1.30"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeNeighbors = %#v, want %#v", got, want)
	}
}
