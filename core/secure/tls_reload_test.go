// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCertificateLoaderReloadsOnCertificateMtimeChange(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	now := time.Now().UTC().Truncate(time.Second)
	firstExpiry := now.Add(24 * time.Hour)
	secondExpiry := now.Add(48 * time.Hour)

	writeServerPair(t, certFile, keyFile, 1, firstExpiry)
	loader, err := NewCertificateLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf == nil || !first.Leaf.NotAfter.Equal(firstExpiry) {
		t.Fatalf("first leaf expiry = %v, want %v", first.Leaf, firstExpiry)
	}
	firstInfo, err := os.Stat(certFile)
	if err != nil {
		t.Fatal(err)
	}

	// Operators normally rotate with an atomic file swap. Force an unambiguous
	// mtime bump so the assertion is independent of filesystem timestamp
	// resolution.
	writeServerPair(t, certFile, keyFile, 2, secondExpiry)
	bumped := firstInfo.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(certFile, bumped, bumped); err != nil {
		t.Fatal(err)
	}

	second, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Leaf == nil || second.Leaf.SerialNumber.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("loader kept the old certificate: leaf=%+v", second.Leaf)
	}
	if got, ok := loader.NotAfter(); !ok || !got.Equal(secondExpiry) {
		t.Fatalf("NotAfter = %v, %v; want %v, true", got, ok, secondExpiry)
	}
}

// TestCertificateLoaderServesLastGoodPairDuringBrokenRotation pins the M1
// fix: a non-atomic rotation (new cert on disk, key still the old one) must
// NOT abort handshakes — the loader keeps serving the retained last-good pair
// and recovers once the matching key lands.
func TestCertificateLoaderServesLastGoodPairDuringBrokenRotation(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	now := time.Now().UTC().Truncate(time.Second)

	writeServerPair(t, certFile, keyFile, 1, now.Add(24*time.Hour))
	loader, err := NewCertificateLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	goodKey, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(certFile)
	if err != nil {
		t.Fatal(err)
	}

	// Break the rotation: a NEW cert lands while the key file still holds the
	// OLD private key (mismatched pair), with an unambiguous mtime bump.
	otherKey := filepath.Join(dir, "other.key")
	writeServerPair(t, certFile, otherKey, 2, now.Add(48*time.Hour))
	bumped := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(certFile, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, goodKey, 0o600); err != nil {
		t.Fatal(err)
	}

	served, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("handshake must not fail during a broken rotation: %v", err)
	}
	if served.Leaf == nil || served.Leaf.SerialNumber.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("loader must serve the LAST GOOD pair, got leaf %+v", served.Leaf)
	}

	// The rotation completes (matching key arrives) → next handshake picks up
	// the new pair.
	newKey, err := os.ReadFile(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, newKey, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Leaf == nil || recovered.Leaf.SerialNumber.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("loader must recover once the rotation completes, got leaf %+v", recovered.Leaf)
	}
}

func writeServerPair(t *testing.T, certFile, keyFile string, serial int64, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             notAfter.Add(-72 * time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}
