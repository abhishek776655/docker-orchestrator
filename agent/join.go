package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
	"github.com/abhishek/hardware-aware-container-orchestrator/netutil"
)

type credentialFile struct {
	NodeID      string `json:"node_id"`
	Credential  string `json:"credential"`
	Endpoint    string `json:"endpoint"`
	Fingerprint string `json:"fingerprint"`
	DNSSuffix   string `json:"dns_suffix"`
}

// Join redeems a one-time bundle over certificate-pinned HTTPS and stores
// the returned node-specific credential with owner-only permissions.
func Join(ctx context.Context, bundle agentjoin.Bundle, nodeID, credentialPath string) (Config, error) {
	if time.Now().After(bundle.ExpiresAt) {
		return Config{}, fmt.Errorf("join token expired")
	}
	if !agentjoin.ValidNodeID(nodeID) || bundle.Secret == "" || bundle.Endpoint == "" || bundle.Fingerprint == "" {
		return Config{}, fmt.Errorf("join token or node ID is incomplete")
	}
	client := pinnedHTTPClient(bundle.Fingerprint, 10*time.Second)
	return joinWithClient(ctx, bundle, nodeID, credentialPath, client)
}

func joinWithClient(ctx context.Context, bundle agentjoin.Bundle, nodeID, credentialPath string, client *http.Client) (Config, error) {
	body, _ := json.Marshal(map[string]string{"token": bundle.Secret, "node_id": nodeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bundle.Endpoint+"/v1/agent/join", bytes.NewReader(body))
	if err != nil {
		return Config{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Config{}, fmt.Errorf("connect to control plane: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Config{}, fmt.Errorf("join rejected: HTTP %d", resp.StatusCode)
	}
	var result struct {
		NodeID     string `json:"node_id"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Config{}, err
	}
	if result.NodeID != nodeID || result.Credential == "" {
		return Config{}, fmt.Errorf("control plane returned invalid join response")
	}
	suffix := bundle.DNSSuffix
	if suffix == "" {
		suffix = "haco"
	}
	record := credentialFile{NodeID: nodeID, Credential: result.Credential, Endpoint: bundle.Endpoint, Fingerprint: bundle.Fingerprint, DNSSuffix: suffix}
	data, err := json.Marshal(record)
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0700); err != nil {
		return Config{}, err
	}
	if err := os.Chmod(filepath.Dir(credentialPath), 0700); err != nil {
		return Config{}, err
	}
	if err := os.WriteFile(credentialPath, data, 0600); err != nil {
		return Config{}, err
	}
	if err := os.Chmod(credentialPath, 0600); err != nil {
		return Config{}, err
	}
	u, err := url.Parse(bundle.Endpoint)
	if err != nil {
		return Config{}, err
	}
	address, err := detectAgentAddress()
	if err != nil {
		return Config{}, err
	}
	return Config{NodeID: nodeID, Address: address, ControlPlaneURL: bundle.Endpoint, DNSAddress: net.JoinHostPort(u.Hostname(), "53"), DNSSuffix: suffix, Credential: result.Credential, TLSFingerprint: bundle.Fingerprint}, nil
}

func LoadCredential(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var saved credentialFile
	if err := json.Unmarshal(data, &saved); err != nil {
		return Config{}, err
	}
	if saved.NodeID == "" || saved.Credential == "" || saved.Endpoint == "" || saved.Fingerprint == "" {
		return Config{}, fmt.Errorf("credential file is incomplete")
	}
	u, err := url.Parse(saved.Endpoint)
	if err != nil {
		return Config{}, err
	}
	address, err := detectAgentAddress()
	if err != nil {
		return Config{}, err
	}
	suffix := saved.DNSSuffix
	if suffix == "" {
		suffix = "haco"
	}
	return Config{NodeID: saved.NodeID, Address: address, ControlPlaneURL: saved.Endpoint, DNSAddress: net.JoinHostPort(u.Hostname(), "53"), DNSSuffix: suffix, Credential: saved.Credential, TLSFingerprint: saved.Fingerprint}, nil
}

func detectAgentAddress() (string, error) {
	if ip, err := netutil.DetectLocalIP(); err == nil {
		return ip, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("detect node LAN address: %w", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && ipNet.IP.IsGlobalUnicast() && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.To4().String(), nil
		}
	}
	return "", fmt.Errorf("detect node LAN address: no non-loopback IPv4 address found")
}

// certificatePin is shared with join tests and keeps the serialized
// fingerprint format explicit (lowercase SHA-256 over the leaf DER).
func certificatePin(der []byte) string { sum := sha256.Sum256(der); return hex.EncodeToString(sum[:]) }
