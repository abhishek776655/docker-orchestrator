package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTUIModelInitAndViewDoNotPanic is the "does it compile and does the
// initial model load without panicking" smoke test — a Bubble Tea model is
// hard to meaningfully unit test beyond that, so this deliberately doesn't
// try to assert on rendered content, just that constructing it, driving it
// through Init/Update/View against an unreachable control plane (the
// "disconnected, retrying" path), and a window resize all run cleanly.
func TestTUIModelInitAndViewDoNotPanic(t *testing.T) {
	m := newTUIModel("http://127.0.0.1:0", "")

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() returned a nil command")
	}
	msg := cmd()
	dataMsg, ok := msg.(tea.BatchMsg)
	_ = dataMsg
	_ = ok // Init batches fetch+tick; either shape is fine, just must not panic

	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = model.(tuiModel)

	model, _ = m.Update(tuiDataMsg{tab: tabNodes, err: errUnreachable})
	m = model.(tuiModel)

	if out := m.View(); out == "" {
		t.Fatal("View() returned an empty string")
	}
}

var errUnreachable = &testError{"connection refused"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// TestFetchTabDataShapesEveryTab exercises fetchTabData against a fake
// control plane for each tab, checking it produces well-formed columns/
// rows (every row the same width as the header) rather than panicking or
// silently returning nothing.
func TestFetchTabDataShapesEveryTab(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"ID":"n1","Address":"1.2.3.4","Architecture":"arm64","Availability":"available","Allocatable":{"CPUMillis":1000,"MemoryBytes":1073741824},"HeartbeatAt":"2026-01-01T00:00:00Z"}]`))
	})
	mux.HandleFunc("/v1/workloads", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Project":"p","Service":"s","Revision":"latest","Status":"ready","NodeID":"n1","ContainerID":"abc123"}]`))
	})
	mux.HandleFunc("/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"ID":1,"WorkloadKey":"p/s/latest","NodeID":"n1","Type":"status","Message":"ready","CreatedAt":"2026-01-01T00:00:00Z"}]`))
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"nodes_by_availability":{"available":1},"workloads_by_status":{"ready":1}}`))
	})
	mux.HandleFunc("/v1/services/p", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Project":"p","Service":"s","NodeID":"n1","Address":"1.2.3.4","ContainerPort":8080,"Status":"ready"}]`))
	})
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Project":"p","Total":1,"ByStatus":{"ready":1}}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for tab := tuiTab(0); tab < tabCount; tab++ {
		if isTextTab(tab) {
			continue // covered by TestFetchTextTabContent below
		}
		cols, rows, err := fetchTabData(srv.URL, "p", tab)
		if err != nil {
			t.Errorf("tab %s: %v", tuiTabNames[tab], err)
			continue
		}
		if len(cols) == 0 {
			t.Errorf("tab %s: no columns", tuiTabNames[tab])
			continue
		}
		for _, row := range rows {
			if len(row) != len(cols) {
				t.Errorf("tab %s: row width %d != column count %d", tuiTabNames[tab], len(row), len(cols))
			}
		}
	}
}

// TestFetchTextTabContent exercises the Dashboard (all tabs combined) and
// Logs tabs against the same fake control plane, checking they produce
// non-empty content without erroring.
func TestFetchTextTabContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"ID":"n1","Address":"1.2.3.4","Architecture":"arm64","Availability":"available","Allocatable":{"CPUMillis":1000,"MemoryBytes":1073741824},"HeartbeatAt":"2026-01-01T00:00:00Z"}]`))
	})
	mux.HandleFunc("/v1/workloads", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Project":"p","Service":"s","Revision":"latest","Status":"ready","NodeID":"n1","ContainerID":"abc123"}]`))
	})
	mux.HandleFunc("/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"nodes_by_availability":{"available":1},"workloads_by_status":{"ready":1}}`))
	})
	mux.HandleFunc("/v1/services/p", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Project":"p","Total":1,"ByStatus":{"ready":1}}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	content, err := fetchTextTabContent(srv.URL, "p", tabDashboard)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if content == "" {
		t.Fatal("dashboard: empty content")
	}
	for _, sec := range dashboardSections {
		if !strings.Contains(content, sec.title) {
			t.Errorf("dashboard content missing section %q", sec.title)
		}
	}

	content, err = fetchTextTabContent(srv.URL, "p", tabLogs)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if content == "" {
		t.Fatal("logs: empty content (expected at least a 'could not read' message)")
	}
}

// TestFetchServicesTabWithoutProjectShowsHint confirms the v1 "project
// scoping is a start-time flag only" shortcut: no project means a single
// informational row, not an error.
func TestFetchServicesTabWithoutProjectShowsHint(t *testing.T) {
	cols, rows, err := fetchServicesTab("http://127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != len(cols) {
		t.Fatalf("expected one hint row matching column count, got %+v", rows)
	}
}
