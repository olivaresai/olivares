// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicTLSCertificate_ExactlyServedChainWithoutKey(t *testing.T) {
	dir := t.TempDir()
	cert, key, public := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "public", "tls.crt")
	if _, _, err := EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	// Even an operator bundle containing a private PEM block must never copy
	// that block into the public directory. Publish only the loaded chain.
	certData, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cert, append(certData, keyData...), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := NewCertificateLoaderWithPublicCertificate(cert, key, public)
	if err != nil {
		t.Fatal(err)
	}
	served, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicChain(t, public, served)
	info, err := os.Stat(public)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("public certificate mode: %v, %v", info, err)
	}
	info, err = os.Stat(filepath.Dir(public))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("public directory mode: %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(public))
	if err != nil || len(entries) != 1 || entries[0].Name() != "tls.crt" {
		t.Fatalf("public directory contains more than the certificate: %v, %v", entries, err)
	}
}

func TestPublicTLSCertificate_RotationAtomicallyFollowsServedPair(t *testing.T) {
	dir := t.TempDir()
	cert, key, public := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "public", "tls.crt")
	writeServerPair(t, cert, key, 1, time.Now().Add(24*time.Hour))
	loader, err := NewCertificateLoaderWithPublicCertificate(cert, key, public)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicChain(t, public, first)
	old, err := os.Open(public)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	oldData, err := os.ReadFile(public)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cert)
	if err != nil {
		t.Fatal(err)
	}
	writeServerPair(t, cert, key, 2, time.Now().Add(48*time.Hour))
	bumped := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(cert, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	second, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicChain(t, public, second)
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("rotation did not change the served leaf")
	}
	retained, err := io.ReadAll(old)
	if err != nil || !bytes.Equal(retained, oldData) {
		t.Fatalf("rotation rewrote an open public certificate instead of replacing it atomically: %v", err)
	}
	// A broken rotation must keep both the public and served last-good chain.
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	bumped = bumped.Add(2 * time.Second)
	if err := os.Chtimes(cert, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	lastGood, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicChain(t, public, lastGood)
	if !bytes.Equal(second.Certificate[0], lastGood.Certificate[0]) {
		t.Fatal("broken rotation replaced the last good leaf")
	}
}

func TestPublicTLSCertificate_RefusesSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeServerPair(t, cert, key, 1, time.Now().Add(24*time.Hour))
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "public")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCertificateLoaderWithPublicCertificate(cert, key, filepath.Join(dir, "public", "tls.crt")); err == nil {
		t.Fatal("publication followed a symlink directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("publication wrote outside its directory: %v, %v", entries, err)
	}
}

func TestPublicTLSCertificate_RefusesPrivateSourcePath(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeServerPair(t, cert, key, 1, time.Now().Add(24*time.Hour))
	for _, source := range []string{cert, key} {
		before, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewCertificateLoaderWithPublicCertificate(cert, key, source); err == nil {
			t.Fatal("public copy was allowed to replace a private source")
		}
		after, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("source changed on refused publication")
		}
	}
}

func TestPublicTLSCertificate_RejectsKeyDisguisedAsCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key, public := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "public", "tls.crt")
	writeServerPair(t, cert, key, 1, time.Now().Add(24*time.Hour))
	certData, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(keyData)
	if err := os.WriteFile(cert, append(certData, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCertificateLoaderWithPublicCertificate(cert, key, public); err == nil {
		t.Fatal("public copy accepted a private key disguised as a certificate")
	}
	if _, err := os.Stat(public); !os.IsNotExist(err) {
		t.Fatalf("invalid chain was published: %v", err)
	}
}

func assertPublicChain(t *testing.T, path string, served *tls.Certificate) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("public certificate is missing: %v", err)
	}
	for _, der := range served.Certificate {
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || !bytes.Equal(block.Bytes, der) {
			t.Fatal("public copy differs from the served certificate chain")
		}
		data = rest
	}
	if len(bytes.TrimSpace(data)) != 0 {
		t.Fatal("public copy contains material beyond the served certificate chain")
	}
}
