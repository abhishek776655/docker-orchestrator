package agentjoin

import (
	"strings"
	"testing"
	"time"
)

func TestBundleRoundTrip(t *testing.T) {
	want := Bundle{Endpoint: "https://192.168.1.4:8443", Fingerprint: strings.Repeat("a", 64), Secret: "one-time", ExpiresAt: time.Now().UTC().Truncate(time.Second)}
	raw, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded bundle = %#v, want %#v", got, want)
	}
}

func TestDecodeRejectsInvalidBundle(t *testing.T) {
	if _, err := Decode("not-a-token"); err == nil {
		t.Fatal("expected invalid token error")
	}
}

func TestValidNodeIDRejectsPathAndURLSeparators(t *testing.T) {
	for _, id := range []string{"node-a", "node_1", "machine.local"} {
		if !ValidNodeID(id) {
			t.Fatalf("ValidNodeID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "../agent", "node/a", "-node", "node id"} {
		if ValidNodeID(id) {
			t.Fatalf("ValidNodeID(%q) = true", id)
		}
	}
}
