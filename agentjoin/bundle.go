// Package agentjoin defines the copyable, short-lived agent bootstrap bundle.
package agentjoin

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Bundle struct {
	Endpoint    string    `json:"endpoint"`
	Fingerprint string    `json:"fingerprint"`
	DNSSuffix   string    `json:"dns_suffix"`
	Secret      string    `json:"secret"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func Encode(b Bundle) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func Decode(encoded string) (Bundle, error) {
	var b Bundle
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return b, errors.New("invalid join token encoding")
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, errors.New("invalid join token payload")
	}
	if !strings.HasPrefix(b.Endpoint, "https://") || b.Fingerprint == "" || b.Secret == "" || b.ExpiresAt.IsZero() {
		return Bundle{}, errors.New("join token is missing required fields")
	}
	if len(b.Fingerprint) != 64 {
		return Bundle{}, errors.New("join token has invalid certificate fingerprint")
	}
	if _, err := hex.DecodeString(b.Fingerprint); err != nil {
		return Bundle{}, errors.New("join token has invalid certificate fingerprint")
	}
	return b, nil
}

func ValidNodeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_' || r == '.')) {
			continue
		}
		return false
	}
	return true
}
