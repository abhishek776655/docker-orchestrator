package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys.env")
	os.WriteFile(p, []byte("# keys\nexport AUTH_KEY=\"abc=def\"\n\nLJS='x y'\n"), 0o600)
	got, err := readEnvFile(p)
	if err != nil || got["AUTH_KEY"] != "abc=def" || got["LJS"] != "x y" || len(got) != 2 {
		t.Fatalf("got %v err %v", got, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.env")
	os.WriteFile(bad, []byte("no equals sign\n"), 0o600)
	if _, err := readEnvFile(bad); err == nil {
		t.Fatal("a line without = should fail")
	}
}

func TestServiceSpecCopiesHacoEnvAndEnvFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	t.Setenv("HACO_AGENT_BIN", bin)
	t.Setenv("HACO_NODE_ID", "node-a")
	t.Setenv("HACO_DATA_DIR", dir)
	envFile := filepath.Join(dir, "e.env")
	os.WriteFile(envFile, []byte("AUTH_PRIVATE_KEY=k1\n"), 0o600)

	spec, err := serviceSpec("agent", false, false, envFile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Binary != bin || len(spec.Args) != 1 || spec.Args[0] != "run" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Env["HACO_NODE_ID"] != "node-a" || spec.Env["AUTH_PRIVATE_KEY"] != "k1" {
		t.Fatalf("env = %v", spec.Env)
	}
	if _, err := serviceSpec("bogus", false, false, ""); err == nil {
		t.Fatal("unknown service should fail")
	}
}

func TestServiceSpecControlPlaneRegistryAndWeb(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "controlplane")
	os.WriteFile(bin, []byte("x"), 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>"), 0o644)
	t.Setenv("HACO_CONTROLPLANE_BIN", bin)
	t.Setenv("HACO_DATA_DIR", dir)
	t.Setenv("HACO_WEB_DIR", dir)

	spec, err := serviceSpec("controlplane", true, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["HACO_WEB_DIR"] != dir || spec.Env["HACO_REGISTRY_ADDR"] != "auto" {
		t.Fatalf("env = %v", spec.Env)
	}
	spec, _ = serviceSpec("controlplane", false, false, "")
	if _, ok := spec.Env["HACO_WEB_DIR"]; ok {
		t.Fatal("HACO_WEB_DIR must not leak in without --web")
	}
}
