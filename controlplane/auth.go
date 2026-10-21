package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/agentjoin"
)

const joinTokenLifetime = 10 * time.Minute

type authStore interface {
	CreateJoinToken(context.Context, string, time.Time) error
	ConsumeJoinToken(context.Context, string, time.Time) (bool, error)
	CreateAgentCredential(context.Context, string, string) error
	VerifyAgentCredential(context.Context, string, string) (bool, error)
	RevokeAgentCredential(context.Context, string) error
}

type JoinManager struct {
	store       authStore
	endpoint    string
	fingerprint string
	dnsSuffix   string
}

func NewJoinManager(s authStore, endpoint, fingerprint string, dnsSuffix ...string) *JoinManager {
	suffix := "haco"
	if len(dnsSuffix) > 0 && dnsSuffix[0] != "" {
		suffix = dnsSuffix[0]
	}
	return &JoinManager{store: s, endpoint: endpoint, fingerprint: fingerprint, dnsSuffix: suffix}
}

func (m *JoinManager) Issue(ctx context.Context) (agentjoin.Bundle, error) {
	secret, err := randomSecret()
	if err != nil {
		return agentjoin.Bundle{}, err
	}
	expires := time.Now().Add(joinTokenLifetime).UTC()
	if err := m.store.CreateJoinToken(ctx, hashSecret(secret), expires); err != nil {
		return agentjoin.Bundle{}, err
	}
	return agentjoin.Bundle{Endpoint: m.endpoint, Fingerprint: m.fingerprint, DNSSuffix: m.dnsSuffix, Secret: secret, ExpiresAt: expires}, nil
}

func (m *JoinManager) Redeem(ctx context.Context, secret, nodeID string) (string, error) {
	if secret == "" || !agentjoin.ValidNodeID(nodeID) {
		return "", errors.New("invalid join request")
	}
	consumed, err := m.store.ConsumeJoinToken(ctx, hashSecret(secret), time.Now())
	if err != nil {
		return "", err
	}
	if !consumed {
		return "", errors.New("join token is invalid, expired, or already used")
	}
	credential, err := randomSecret()
	if err != nil {
		return "", err
	}
	if err := m.store.CreateAgentCredential(ctx, nodeID, hashSecret(credential)); err != nil {
		return "", err
	}
	return credential, nil
}

func (m *JoinManager) Authenticate(ctx context.Context, nodeID, credential string) (bool, error) {
	if nodeID == "" || credential == "" {
		return false, nil
	}
	return m.store.VerifyAgentCredential(ctx, nodeID, hashSecret(credential))
}

func (m *JoinManager) Revoke(ctx context.Context, nodeID string) error {
	return m.store.RevokeAgentCredential(ctx, nodeID)
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

type nodeContextKey struct{}

func authenticatedNode(r *http.Request) string {
	node, _ := r.Context().Value(nodeContextKey{}).(string)
	return node
}

func (m *JoinManager) authenticateAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			http.Error(w, "agent credential required", http.StatusUnauthorized)
			return
		}
		credential := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		nodeID := r.Header.Get("X-HACO-Node-ID")
		if strings.HasPrefix(r.URL.Path, "/v1/nodes/") {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) == 5 && parts[4] == "heartbeat" {
				nodeID = parts[3]
			}
		} else if strings.HasPrefix(r.URL.Path, "/v1/agent/") {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) == 5 && parts[4] == "assignments" {
				nodeID = parts[3]
			}
		}
		if nodeID == "" {
			http.Error(w, "agent node identity required", http.StatusBadRequest)
			return
		}
		ok, err := m.Authenticate(r.Context(), nodeID, credential)
		if err != nil {
			http.Error(w, "authentication unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "invalid agent credential", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeContextKey{}, nodeID)))
	})
}

func LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "operator API is local-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func joinEndpoint(host string, port int) string {
	return "https://" + net.JoinHostPort(host, fmt.Sprint(port))
}
