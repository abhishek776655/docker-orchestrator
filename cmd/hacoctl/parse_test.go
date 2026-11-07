package main

import "testing"

func TestParseByteSize(t *testing.T) {
	for in, want := range map[string]int64{"8GiB": 8 << 30, "8g": 8 << 30, "512MiB": 512 << 20, "1.5G": 3 << 29, "1024": 1024} {
		got, err := parseByteSize(in)
		if err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1G", "0"} {
		if _, err := parseByteSize(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestAddServiceBuildsCommaSeparatedScope(t *testing.T) {
	got := addService(addService(addService("", "api"), "worker,cron"), " ")
	if got != "api,worker,cron" {
		t.Fatalf("scope = %q, want api,worker,cron", got)
	}
}
