package controlplane

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateTLSIdentityPersistsCertificateAndRestrictsKey(t *testing.T) {
	dir := t.TempDir()
	first, firstFingerprint, err := LoadOrCreateTLSIdentity(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	second, secondFingerprint, err := LoadOrCreateTLSIdentity(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprint changed across reload: %q != %q", firstFingerprint, secondFingerprint)
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("certificate changed across reload")
	}
	keyInfo, err := os.Stat(filepath.Join(dir, "agent-tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("private key mode = %o, want 600", keyInfo.Mode().Perm())
	}
	cert, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Fatalf("certificate IP SANs = %v", cert.IPAddresses)
	}
}
