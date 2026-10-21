package controlplane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoadOrCreateTLSIdentity keeps the agent API's self-signed TLS identity
// stable across control-plane restarts. The fingerprint is included in
// one-time join bundles and pins the agent to this exact certificate.
func LoadOrCreateTLSIdentity(dir, advertisedHost string) (tls.Certificate, string, error) {
	certPath := filepath.Join(dir, "agent-tls.crt")
	keyPath := filepath.Join(dir, "agent-tls.key")
	if _, err := os.Stat(certPath); err == nil {
		if err := os.Chmod(keyPath, 0600); err != nil {
			return tls.Certificate{}, "", err
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, certificateFingerprint(cert.Certificate[0]), nil
	} else if !os.IsNotExist(err) {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return tls.Certificate{}, "", err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "HACO control plane"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	if ip := net.ParseIP(advertisedHost); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else if advertisedHost != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, advertisedHost)
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, certificateFingerprint(der), nil
}

func certificateFingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return hex.EncodeToString(sum[:])
}
