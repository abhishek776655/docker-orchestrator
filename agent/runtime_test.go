package agent

import (
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

func TestContainerRuntimeMatches(t *testing.T) {
	r := model.Runtime{WorkingDir: "/app", Restart: model.RestartPolicy{Name: "always"}}

	// A container from before the label existed matches a workload that
	// sets nothing, so upgrading the agent doesn't recreate everything.
	if !containerRuntimeMatches(&container.Config{}, model.Runtime{}) {
		t.Fatal("unlabelled container should match an empty runtime")
	}
	if containerRuntimeMatches(&container.Config{}, r) {
		t.Fatal("unlabelled container should not match a runtime that sets something")
	}
	labelled := &container.Config{Labels: map[string]string{runtimeLabel: runtimeLabelValue(r)}}
	if !containerRuntimeMatches(labelled, r) {
		t.Fatal("same runtime should match")
	}
	changed := r
	changed.WorkingDir = "/other"
	if containerRuntimeMatches(labelled, changed) {
		t.Fatal("changed working dir should force a new container")
	}
}

func TestPrepareVolumesAllowsFileMountsOnEphemeralWorkloads(t *testing.T) {
	d := &Docker{dataDir: t.TempDir()}
	w := model.Workload{
		Project: "demo", Service: "app", Revision: "r1",
		Storage: model.StoragePolicy{Class: model.Ephemeral},
		Volumes: []model.VolumeMount{{ContainerPath: "/run/secrets/token", ReadOnly: true, FileContent: []byte("tok")}},
	}
	binds, _, err := d.prepareVolumes(w)
	if err != nil {
		t.Fatalf("prepare volumes: %v", err)
	}
	if len(binds) != 1 {
		t.Fatalf("binds = %v", binds)
	}

	w.Volumes = append(w.Volumes, model.VolumeMount{ContainerPath: "/data"})
	if _, _, err := d.prepareVolumes(w); err == nil {
		t.Fatal("a data volume on an ephemeral workload should still be refused")
	}
}

func TestPrepareVolumesMountsDockerSocketWithoutCheckingIt(t *testing.T) {
	d := &Docker{dataDir: t.TempDir()}
	missing := filepath.Join(t.TempDir(), "docker.sock")
	w := model.Workload{
		Project: "demo", Service: "v2", Revision: "r1",
		Storage: model.StoragePolicy{Class: model.NodeLocalPersistent},
		Volumes: []model.VolumeMount{{ContainerPath: "/var/run/docker.sock", HostPath: missing, DockerSocket: true}},
	}
	binds, _, err := d.prepareVolumes(w)
	if err != nil {
		t.Fatalf("prepare volumes: %v", err)
	}
	if len(binds) != 1 || binds[0] != missing+":/var/run/docker.sock" {
		t.Fatalf("binds = %v", binds)
	}
}

func TestBusyPortsSkipsHacoContainersAndNamesTheHolder(t *testing.T) {
	list := []types.Container{
		{ID: "aaa", Names: []string{"/dockerize-localhost-rabbitmq-1"}, Ports: []types.Port{{PublicPort: 5672, Type: "tcp"}, {PublicPort: 5672, Type: "tcp"}, {PrivatePort: 9999}}},
		{ID: "bbb", Names: []string{"/haco-demo-api"}, Labels: map[string]string{keyLabel: "demo/api/latest"}, Ports: []types.Port{{PublicPort: 8080}}},
	}
	got := busyPortsFrom(list)
	if len(got) != 1 || got[5672] != "container dockerize-localhost-rabbitmq-1" {
		t.Fatalf("busy = %v, want only 5672 held by the foreign container", got)
	}
}
