package netutil

import (
	"reflect"
	"testing"
)

func TestParseARPMacOS(t *testing.T) {
	out := `? (169.254.169.254) at (incomplete) on en0 [ethernet]
? (192.168.1.1) at 5c:3a:3d:a3:d5:53 on en0 ifscope [ethernet]
? (192.168.1.11) at 96:c:54:9:3e:87 on en0 ifscope permanent [ethernet]
nas.local (192.168.1.6) at d6:98:bd:54:c5:73 on en0 ifscope [ethernet]
? (192.168.1.255) at ff:ff:ff:ff:ff:ff on en0 ifscope [ethernet]
? (224.0.0.251) at 1:0:5e:0:0:fb on en0 ifscope permanent [ethernet]
`
	got := ParseARP(out)
	want := []Neighbor{
		{IP: "192.168.1.1", MAC: "5c:3a:3d:a3:d5:53"},
		{IP: "192.168.1.6", MAC: "d6:98:bd:54:c5:73", Hostname: "nas.local"},
		{IP: "192.168.1.11", MAC: "96:0c:54:09:3e:87"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseARP = %#v, want %#v", got, want)
	}
}

func TestParseARPLinuxSortsAndDedupes(t *testing.T) {
	out := `? (192.168.1.20) at aa:bb:cc:dd:ee:02 [ether] on eth0
? (192.168.1.3) at aa:bb:cc:dd:ee:01 [ether] on eth0
? (192.168.1.3) at aa:bb:cc:dd:ee:01 [ether] on eth0
garbage line
`
	got := ParseARP(out)
	if len(got) != 2 || got[0].IP != "192.168.1.3" || got[1].IP != "192.168.1.20" {
		t.Fatalf("ParseARP = %#v, want 192.168.1.3 then 192.168.1.20", got)
	}
}
