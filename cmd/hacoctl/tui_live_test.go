package main

import (
	"os"
	"testing"
)

// TestFetchTabDataAgainstLiveControlPlane is a manual-only integration
// check (skipped unless HACO_TUI_LIVE_TEST=1) that exercises fetchTabData
// against a real, already-running control plane instead of mocked JSON —
// run during development to confirm the live server's actual response
// shapes decode cleanly, not just the hand-written fixtures in
// TestFetchTabDataShapesEveryTab.
func TestFetchTabDataAgainstLiveControlPlane(t *testing.T) {
	if os.Getenv("HACO_TUI_LIVE_TEST") != "1" {
		t.Skip("set HACO_TUI_LIVE_TEST=1 to run against a live control plane at HACO_CONTROLPLANE_URL")
	}
	baseURL := os.Getenv("HACO_CONTROLPLANE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8099"
	}
	for tab := tuiTab(0); tab < tabCount; tab++ {
		project := ""
		if tab == tabServices {
			project = "mvpdemo"
		}
		if isTextTab(tab) {
			content, err := fetchTextTabContent(baseURL, project, tab)
			if err != nil {
				t.Errorf("tab %s: %v", tuiTabNames[tab], err)
				continue
			}
			t.Logf("tab %s: %d bytes", tuiTabNames[tab], len(content))
			continue
		}
		cols, rows, err := fetchTabData(baseURL, project, tab)
		if err != nil {
			t.Errorf("tab %s: %v", tuiTabNames[tab], err)
			continue
		}
		t.Logf("tab %s: %d cols, %d rows", tuiTabNames[tab], len(cols), len(rows))
		for _, row := range rows {
			if len(row) != len(cols) {
				t.Errorf("tab %s: row width %d != column count %d", tuiTabNames[tab], len(row), len(cols))
			}
		}
	}
}
