// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/ddil"
)

// The audit planes stay a Business capability on their own: with the group gate open, the
// !enterprise stubs still refuse an export and an import that carries audit segments.
func TestCommunityDDILAuditRefusesBeforeWrites(t *testing.T) {
	asEdition(t, "enterprise")
	dir := filepath.Join(t.TempDir(), "not-created")
	_, _, err := runDDILCommand(context.Background(), "export", "--tenant", "22222222-2222-7222-8222-222222222222", "--out", dir, "--sign-key", "invalid", "--data-dir", dir)
	if exitcode.From(err) != exitcode.Edition {
		t.Errorf("export must return9: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	tenant := "22222222-2222-7222-8222-222222222222"
	if err := ddil.Export(&b, ddil.ExportInput{Tenant: tenant, CreatedAt: time.Now().UTC(), Segments: []ddil.Segment{{FromSeq: 1, ToSeq: 1, FirstHash: "aa", LastHash: "aa", ManifestJSON: []byte(`{}`), EventsJSONL: []byte("{}\n")}}, Evidence: map[string][]byte{"document.txt": []byte("carried")}}, priv); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "carried.ddil")
	if err := os.WriteFile(bundle, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runDDILCommand(context.Background(), "import", "--tenant", tenant, "--bundle", bundle, "--pubkey", base64.StdEncoding.EncodeToString(pub), "--audit-out", dir, "--evidence-out", dir, "--data-dir", dir)
	if exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "Business") {
		t.Errorf("import must refuse before writes: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("edition refusal touched output: %v", err)
	}
}

// DDIL air-gap transfer is a Business capability, so Community no longer runs even the
// import that carries no audit segments.
func TestCommunityDDILTrustOnlyImportRefused(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	tenant := "22222222-2222-7222-8222-222222222222"
	var b bytes.Buffer
	if err := ddil.Export(&b, ddil.ExportInput{Tenant: tenant, CreatedAt: time.Now().UTC(), Evidence: map[string][]byte{"document.txt": []byte("carried")}}, priv); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "trust.ddil")
	if err := os.WriteFile(bundle, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "evidence")
	archive := filepath.Join(t.TempDir(), "unused-archive")
	_, _, err = runDDILCommand(context.Background(), "import", "--tenant", tenant, "--bundle", bundle, "--pubkey", base64.StdEncoding.EncodeToString(pub), "--evidence-out", out, "--audit-out", archive)
	if exitcode.From(err) != exitcode.Edition || !strings.Contains(err.Error(), "Business") {
		t.Fatalf("trust-only import must be refused as a Business feature: %v", err)
	}
	for _, path := range []string{out, archive} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("the refused import touched %s: %v", path, err)
		}
	}
}
