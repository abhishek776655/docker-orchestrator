package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// RestartError asks the caller to start the agent again as NodeID, loading
// the credential at CredentialPath: after a node rename (the new ID), or
// after a rename that didn't commit (the same ID, so its containers are
// recreated).
type RestartError struct {
	NodeID         string
	CredentialPath string
	Reason         string
}

func (e *RestartError) Error() string {
	return fmt.Sprintf("restarting agent as %s: %s", e.NodeID, e.Reason)
}

// renameNode moves this node to newID. Containers are stopped and removed
// first (their haco.node.id label can't be changed in place); the database
// is renamed next; only if that commits are local files moved, so a failed
// commit leaves this machine exactly as it was.
func (a *Agent) renameNode(ctx context.Context, newID string) error {
	oldID := a.cfg.NodeID
	commitPath := "/v1/agent/" + oldID + "/rename-commit"
	if v := os.Getenv("HACO_NODE_ID") + os.Getenv("HACO_HOST_NODE_ID"); v != "" {
		reason := "this node's ID is fixed by HACO_NODE_ID/HACO_HOST_NODE_ID; change that variable instead"
		_ = a.post(ctx, commitPath, map[string]string{"new_id": newID, "error": reason}, nil)
		return &RestartError{NodeID: oldID, CredentialPath: a.cfg.CredentialPath, Reason: "rename refused: " + reason}
	}

	log.Printf("renaming node %s to %s: stopping and removing this node's containers", oldID, newID)
	managed, err := a.docker.ManagedContainers(ctx)
	if err != nil {
		_ = a.post(ctx, commitPath, map[string]string{"new_id": newID, "error": err.Error()}, nil)
		return &RestartError{NodeID: oldID, CredentialPath: a.cfg.CredentialPath, Reason: "rename aborted: " + err.Error()}
	}
	for _, c := range managed {
		if c.Running {
			if err := a.docker.StopContainer(ctx, c.ContainerID); err != nil {
				log.Printf("stop %s: %v", c.WorkloadKey, err)
			}
		}
		if err := a.docker.StopAndRemove(ctx, c.ContainerID); err != nil {
			log.Printf("remove %s: %v", c.WorkloadKey, err)
		}
	}

	if err := a.post(ctx, commitPath, map[string]string{"new_id": newID}, nil); err != nil {
		return &RestartError{NodeID: oldID, CredentialPath: a.cfg.CredentialPath, Reason: "rename not committed (" + err.Error() + "); keeping the old ID"}
	}

	newCred, err := moveNodeFiles(oldID, newID, a.docker.dataDir, a.cfg.CredentialPath)
	if err != nil {
		log.Printf("node %s was renamed to %s in the control plane, but moving local files failed: %v", oldID, newID, err)
	}
	if err := saveNodeID(newID); err != nil {
		log.Printf("save new node ID: %v", err)
	}
	log.Printf("node renamed %s -> %s; restarting under the new ID", oldID, newID)
	return &RestartError{NodeID: newID, CredentialPath: newCred, Reason: "node renamed"}
}

// moveNodeFiles renames this node's data folder and credential file to
// newID. A folder or credential path that isn't named after the node (a
// custom HACO_DATA_DIR or credential file) stays where it is.
func moveNodeFiles(oldID, newID, volumeDir, credentialPath string) (string, error) {
	if filepath.Base(volumeDir) == oldID {
		newDir := filepath.Join(filepath.Dir(volumeDir), newID)
		if _, err := os.Stat(volumeDir); err == nil {
			if _, err := os.Stat(newDir); err == nil {
				return credentialPath, fmt.Errorf("data folder %s already exists; leaving %s in place", newDir, volumeDir)
			}
			if err := os.Rename(volumeDir, newDir); err != nil {
				return credentialPath, fmt.Errorf("move data folder: %w", err)
			}
		}
	}

	data, err := os.ReadFile(credentialPath)
	if err != nil {
		return credentialPath, fmt.Errorf("read credential: %w", err)
	}
	var cred credentialFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return credentialPath, fmt.Errorf("parse credential: %w", err)
	}
	cred.NodeID = newID
	out, err := json.Marshal(cred)
	if err != nil {
		return credentialPath, err
	}
	newPath := credentialPath
	if filepath.Base(credentialPath) == oldID+".json" {
		newPath = filepath.Join(filepath.Dir(credentialPath), newID+".json")
	}
	tmp := newPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return credentialPath, fmt.Errorf("write credential: %w", err)
	}
	if err := os.Rename(tmp, newPath); err != nil {
		return credentialPath, fmt.Errorf("write credential: %w", err)
	}
	if newPath != credentialPath {
		_ = os.Remove(credentialPath)
	}
	return newPath, nil
}

// IsRestart reports whether err asks for the agent to be started again.
func IsRestart(err error) (*RestartError, bool) {
	var re *RestartError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}
