// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tlsx

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildNoMaterialReturnsNil(t *testing.T) {
	cfg, err := Build(Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("no TLS material should yield a nil config, got %+v", cfg)
	}
}

func TestBuildSecureDefaults(t *testing.T) {
	cfg, err := Build(Options{Enable: true})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS floor must be 1.2, got %x", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("verification must be ON by default")
	}
}

func TestBuildInsecureIsExplicitOptIn(t *testing.T) {
	cfg, err := Build(Options{Enable: true, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("explicit opt-in should disable verification")
	}
}

func TestBuildMTLSRequiresBothCertAndKey(t *testing.T) {
	if _, err := Build(Options{CertFile: "x.pem"}); err == nil {
		t.Fatal("cert without key must error (no silent downgrade to one-way TLS)")
	}
	if _, err := Build(Options{KeyFile: "x.key"}); err == nil {
		t.Fatal("key without cert must error")
	}
}

func TestCAPoolRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CAPool(bad); err == nil {
		t.Fatal("a file with no valid certificate must error (never silently fall back to system roots)")
	}
	if _, err := CAPool(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("a missing CA file must error")
	}
}
