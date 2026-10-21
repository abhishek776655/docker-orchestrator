package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

func TestJoinVerifiesPinnedCertificateAndPersistsCredential(t *testing.T) {
	requests := 0
	cert := testCertificate(t)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if err := verifyPinnedCertificate(state, certificatePin(cert.Raw)); err != nil {
			t.Fatal(err)
		}
		requests++
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["token"] != "bootstrap" || req["node_id"] != "node-b" {
			t.Fatalf("join request = %#v", req)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"node_id":"node-b","credential":"agent-secret"}`)), Header: make(http.Header), Request: r}, nil
	})}
	pin := certificatePin(cert.Raw)
	path := filepath.Join(t.TempDir(), "credentials", "node.json")
	bundle := agentjoin.Bundle{Endpoint: "https://control.example:8443", Fingerprint: pin, Secret: "bootstrap", ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := joinWithClient(context.Background(), bundle, "node-b", path, client); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d", requests)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}
	cfg, err := LoadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "node-b" || cfg.Credential != "agent-secret" || cfg.TLSFingerprint != pin {
		t.Fatalf("loaded config = %#v", cfg)
	}
}

func TestJoinWrongPinDoesNotSendBootstrapToken(t *testing.T) {
	requests := 0
	cert := testCertificate(t)
	bundle := agentjoin.Bundle{Endpoint: "https://control.example:8443", Fingerprint: "wrong-pin", Secret: "bootstrap", ExpiresAt: time.Now().Add(time.Minute)}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if err := verifyPinnedCertificate(state, bundle.Fingerprint); err != nil {
			return nil, err
		}
		requests++
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
	})}
	if _, err := joinWithClient(context.Background(), bundle, "node-b", filepath.Join(t.TempDir(), "node.json"), client); err == nil {
		t.Fatal("wrong pin was accepted")
	}
	if requests != 0 {
		t.Fatalf("server received %d requests before pin verification", requests)
	}
}

func TestRunRejectsUnauthenticatedHTTP(t *testing.T) {
	a := New(Config{NodeID: "node-a", ControlPlaneURL: "http://127.0.0.1:8099"}, nil)
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("agent ran without a credential-pinned HTTPS connection")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
