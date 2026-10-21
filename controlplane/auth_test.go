package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryAuthStore struct {
	joins       map[string]time.Time
	used        map[string]bool
	credentials map[string]string
	revoked     map[string]bool
}

func TestAgentMiddlewareRejectsMissingAndCrossNodeCredentials(t *testing.T) {
	store := &memoryAuthStore{joins: map[string]time.Time{}, used: map[string]bool{}, credentials: map[string]string{}, revoked: map[string]bool{}}
	manager := NewJoinManager(store, "https://cp.example:8443", "fingerprint")
	handler := manager.authenticateAgent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name, path, node, credential string
		want                         int
	}{
		{name: "missing credential", path: "/v1/nodes", node: "node-a", want: http.StatusUnauthorized},
		{name: "credential for another node", path: "/v1/nodes/node-b/heartbeat", node: "node-a", credential: "agent-secret", want: http.StatusUnauthorized},
		{name: "valid credential", path: "/v1/nodes", node: "node-a", credential: "agent-secret", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.credential != "" {
				store.credentials[tc.node] = hashSecret(tc.credential)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("X-HACO-Node-ID", tc.node)
			if tc.credential != "" {
				req.Header.Set("Authorization", "Bearer "+tc.credential)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestLocalOnlyRejectsRemoteOperatorRequests(t *testing.T) {
	handler := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		addr string
		want int
	}{{"127.0.0.1:1234", http.StatusNoContent}, {"192.168.1.20:1234", http.StatusForbidden}} {
		req := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
		req.RemoteAddr = tc.addr
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("remote %s status = %d, want %d", tc.addr, rr.Code, tc.want)
		}
	}
}

func (m *memoryAuthStore) CreateJoinToken(_ context.Context, hash string, expiry time.Time) error {
	m.joins[hash] = expiry
	return nil
}
func (m *memoryAuthStore) ConsumeJoinToken(_ context.Context, hash string, now time.Time) (bool, error) {
	expiry, ok := m.joins[hash]
	if !ok || m.used[hash] || !expiry.After(now) {
		return false, nil
	}
	m.used[hash] = true
	return true, nil
}
func (m *memoryAuthStore) CreateAgentCredential(_ context.Context, node, hash string) error {
	if _, exists := m.credentials[node]; exists && !m.revoked[node] {
		return fmt.Errorf("node %q already has an active credential", node)
	}
	m.credentials[node] = hash
	m.revoked[node] = false
	return nil
}
func (m *memoryAuthStore) VerifyAgentCredential(_ context.Context, node, hash string) (bool, error) {
	return m.credentials[node] == hash && !m.revoked[node], nil
}
func (m *memoryAuthStore) RevokeAgentCredential(_ context.Context, node string) error {
	m.revoked[node] = true
	return nil
}

func TestJoinManagerRedeemsTokenOnceAndBindsCredentialToNode(t *testing.T) {
	store := &memoryAuthStore{joins: map[string]time.Time{}, used: map[string]bool{}, credentials: map[string]string{}, revoked: map[string]bool{}}
	manager := NewJoinManager(store, "https://192.168.1.4:8443", "fingerprint")
	bundle, err := manager.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Redeem(context.Background(), bundle.Secret, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if credential == "" || credential == bundle.Secret {
		t.Fatal("redeem did not issue an independent credential")
	}
	if ok, err := manager.Authenticate(context.Background(), "node-a", credential); err != nil || !ok {
		t.Fatalf("auth = %v, %v", ok, err)
	}
	if ok, err := manager.Authenticate(context.Background(), "node-b", credential); err != nil || ok {
		t.Fatalf("cross-node auth = %v, %v", ok, err)
	}
	if _, err := manager.Redeem(context.Background(), bundle.Secret, "node-b"); err == nil {
		t.Fatal("replayed join token was accepted")
	}
}

func TestJoinManagerDoesNotReplaceActiveNodeCredential(t *testing.T) {
	store := &memoryAuthStore{joins: map[string]time.Time{}, used: map[string]bool{}, credentials: map[string]string{}, revoked: map[string]bool{}}
	manager := NewJoinManager(store, "https://192.168.1.4:8443", "fingerprint")
	first, err := manager.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Redeem(context.Background(), first.Secret, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Redeem(context.Background(), second.Secret, "node-a"); err == nil {
		t.Fatal("join unexpectedly replaced an active node credential")
	}
	if ok, err := manager.Authenticate(context.Background(), "node-a", credential); err != nil || !ok {
		t.Fatalf("existing credential after rejected join = %v, %v; want true, nil", ok, err)
	}
}
