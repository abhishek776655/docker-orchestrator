// Package testdata provides reusable two-node fixtures for scheduler
// tests (SRS M1 backlog: "two-node integration-style simulator
// fixtures").
package testdata

import "github.com/abhishek/hardware-aware-container-orchestrator/model"

// TwoEqualNodes returns two available amd64 nodes with identical
// capacity, useful for scoring and tie-break tests.
func TwoEqualNodes() []model.Node {
	return []model.Node{
		{
			ID:           "node-a",
			Architecture: "amd64",
			Labels:       map[string]string{},
			Capabilities: map[string]bool{},
			Allocatable:  model.Resources{CPUMillis: 4000, MemoryBytes: 8_000_000_000},
			Availability: model.Available,
		},
		{
			ID:           "node-b",
			Architecture: "amd64",
			Labels:       map[string]string{},
			Capabilities: map[string]bool{},
			Allocatable:  model.Resources{CPUMillis: 4000, MemoryBytes: 8_000_000_000},
			Availability: model.Available,
		},
	}
}

// SmallWorkload is a modest CPU/memory request that fits comfortably on
// either node in TwoEqualNodes.
func SmallWorkload(project, service string) model.Workload {
	return model.Workload{
		Project:  project,
		Service:  service,
		Revision: "r1",
		Resources: model.Resources{
			CPUMillis:   500,
			MemoryBytes: 512_000_000,
		},
		Constraints: model.Constraints{Architecture: "amd64"},
		Storage:     model.StoragePolicy{Class: model.Ephemeral},
	}
}
