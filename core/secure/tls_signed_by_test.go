// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// engineCA makes the engine's pair in a data directory, as the engine does at its first start, and
// returns the paths of its CA certificate and CA key.
func engineCA(t *testing.T) (caPath, caKeyPath string) {
	t.Helper()
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	dns := []string{"localhost", "server.example"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}
	if _, _, err := ensureTLSCert(cert, key, dns, ips); err != nil {
		t.Fatal(err)
	}
	return cert + ".ca", key + ".ca"
}

func rootsOf(t *testing.T, caPath string) *x509.CertPool {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(readTLSFixture(t, caPath)) {
		t.Fatalf("no certificate in %s", caPath)
	}
	return roots
}

func leafOf(t *testing.T, cert, key string) *x509.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// The second server's pair verifies, for every name it carries, with the one CA the operator trusts for
// the engine, and no second CA is made beside it.
func TestSignedByTLSCertVerifiesWithTheEnginesCA(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	caBefore, caKeyBefore := readTLSFixture(t, caPath), readTLSFixture(t, caKeyPath)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	dns := []string{"localhost", "server.example"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}
	created, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, dns, ips)
	if err != nil || !created {
		t.Fatalf("created=%v, %v", created, err)
	}
	leaf, roots := leafOf(t, cert, key), rootsOf(t, caPath)
	for _, name := range []string{"localhost", "127.0.0.1", "server.example", "192.168.1.20"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("the engine's CA does not verify %q: %v", name, err)
		}
	}
	for _, own := range []string{cert + ".ca", key + ".ca"} {
		if _, err := os.Stat(own); !os.IsNotExist(err) {
			t.Errorf("a second CA was made: %s (%v)", own, err)
		}
	}
	if !bytes.Equal(caBefore, readTLSFixture(t, caPath)) || !bytes.Equal(caKeyBefore, readTLSFixture(t, caKeyPath)) {
		t.Error("signing changed the engine's CA")
	}
	if info, err := os.Stat(key); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key custody: %v, %v", info, err)
	}
}

// A changed address or host name is re-issued from the same CA and the same key; an unchanged host is
// left as it is.
func TestSignedByTLSCertReissuesFromTheSameCAWhenNamesChange(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	dns := []string{"localhost", "server.example"}
	first := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")}
	if _, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, dns, first); err != nil {
		t.Fatal(err)
	}
	keyBefore := readTLSFixture(t, key)
	second := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.21")}
	changed, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, dns, second)
	if err != nil || !changed {
		t.Fatalf("a changed address: changed=%v, %v", changed, err)
	}
	leaf, roots := leafOf(t, cert, key), rootsOf(t, caPath)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "192.168.1.21"}); err != nil {
		t.Errorf("the new address is not verified by the engine's CA: %v", err)
	}
	if leaf.VerifyHostname("192.168.1.20") == nil {
		t.Error("the old address is still in the certificate")
	}
	if !bytes.Equal(keyBefore, readTLSFixture(t, key)) {
		t.Error("the re-issue changed the console's key")
	}
	changed, _, err = ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, dns, second)
	if err != nil || changed {
		t.Fatalf("an unchanged host was re-issued: changed=%v, %v", changed, err)
	}
}

// The engine's CA is the engine's to make. When it does not exist yet, nothing is made and nothing is
// written: a second CA would split the operator's trust.
func TestSignedByTLSCertNeverMakesACA(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	missing := t.TempDir()
	_, _, err := ensureTLSCertSignedBy(cert, key, filepath.Join(missing, "tls.crt.ca"), filepath.Join(missing, "tls.key.ca"), []string{"localhost"}, nil)
	if err == nil {
		t.Fatal("signed without a CA")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files written without a CA: %v, %v", entries, err)
	}
}

// A pair the operator installed, or a self-signed one made on request, is not the engine's to replace.
func TestSignedByTLSCertLeavesAnotherCAsPairIntact(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
	certBefore, keyBefore := readTLSFixture(t, cert), readTLSFixture(t, key)
	changed, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, []string{"localhost"}, nil)
	if err != nil || changed {
		t.Fatalf("another CA's pair: changed=%v, %v", changed, err)
	}
	if !bytes.Equal(certBefore, readTLSFixture(t, cert)) || !bytes.Equal(keyBefore, readTLSFixture(t, key)) {
		t.Error("another CA's pair was replaced")
	}
}

// A pair signed by another installation's local CA is refused, as the engine refuses it, and not
// replaced: the operator decides which trust to keep.
func TestSignedByTLSCertRefusesAnotherInstallationsPair(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := ensureTLSCert(cert, key, []string{"localhost"}, nil); err != nil {
		t.Fatal(err)
	}
	certBefore := readTLSFixture(t, cert)
	if _, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, []string{"localhost"}, nil); err == nil {
		t.Fatal("another installation's pair was accepted")
	}
	if !bytes.Equal(certBefore, readTLSFixture(t, cert)) {
		t.Error("another installation's pair was replaced")
	}
}

// A key that is already in place, staged by the caller under a name its confinement labels, is the
// key the certificate certifies.
func TestSignedByTLSCertCertifiesAKeyAlreadyInPlace(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	pemKey, err := NewTLSKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureTLSCertSignedBy(cert, key, caPath, caKeyPath, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pemKey, readTLSFixture(t, key)) {
		t.Error("the key in place was replaced")
	}
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		t.Errorf("the certificate does not pair with the key in place: %v", err)
	}
}

// The exported entry takes the names the engine's own leaf takes: this host's, localhost and every LAN
// address.
func TestEnsureTLSCertSignedByNamesTheHostLikeTheEngine(t *testing.T) {
	caPath, caKeyPath := engineCA(t)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCertSignedBy(cert, key, caPath, caKeyPath); err != nil {
		t.Fatal(err)
	}
	leaf, roots := leafOf(t, cert, key), rootsOf(t, caPath)
	names := append([]string{"localhost", "127.0.0.1", "::1"}, testLANAddresses(t)...)
	if host, err := os.Hostname(); err == nil && host != "" {
		names = append(names, host)
	}
	for _, name := range names {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("the engine's CA does not verify %q: %v", name, err)
		}
	}
}
