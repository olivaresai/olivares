// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

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

	"github.com/olivaresai/olivares/core/ddil"
)

func runDDILCommand(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := newDDILCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func importDDILBundle(t *testing.T, path string, pub ed25519.PublicKey) ddil.Imported {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open DDIL bundle: %v", err)
	}
	defer func() { _ = f.Close() }()
	imported, err := ddil.Import(f, pub, time.Now().UTC())
	if err != nil {
		t.Fatalf("import DDIL bundle: %v", err)
	}
	return imported
}

// loadDDILEvidence is the evidence-name contract of `ddil export`. The Community build
// refuses the export before it runs, so the function is witnessed directly here and
// through the command in the Business build (cmd_ddil_evidence_enterprise_test.go).
func TestLoadDDILEvidenceNameValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		specs []string
		want  string
	}{
		{"no separator", []string{"proof"}, "must use name=path"},
		{"empty", []string{"=" + path}, "has an empty name"},
		{"slash", []string{"dir/proof=" + path}, "must not contain"},
		{"backslash", []string{`dir\proof=` + path}, "must not contain"},
		{"dotdot", []string{"proof..txt=" + path}, "must not contain"},
		{"duplicate", []string{"proof=" + path, "proof=" + path}, "is duplicated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := loadDDILEvidence(tc.specs)
			if err == nil || !strings.Contains(err.Error(), "--evidence") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadDDILEvidence(%q) = %v, want an --evidence refusal containing %q", tc.specs, err, tc.want)
			}
		})
	}
	t.Run("valid names come back sorted", func(t *testing.T) {
		evidence, names, err := loadDDILEvidence([]string{"b=" + path, "a=" + path})
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 2 || names[0] != "a" || names[1] != "b" || string(evidence["a"]) != "evidence" {
			t.Fatalf("names = %q, evidence = %q", names, evidence)
		}
	})
}

func TestDDILKeygen(t *testing.T) {
	asEdition(t, "enterprise")
	ctx := context.Background()
	keyPath := filepath.Join(t.TempDir(), "ddil.key")
	stdout, stderr, err := runDDILCommand(ctx, "keygen", "--out", keyPath)
	if err != nil {
		t.Fatalf("keygen --out: %v\n%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("keygen --out wrote stderr: %s", stderr)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		t.Fatalf("stdout public key is invalid: %v (%d bytes)", err, len(pubRaw))
	}
	seedText, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seedText)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("private seed is invalid: %v (%d bytes)", err, len(seed))
	}
	derivedPub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(pubRaw, derivedPub) {
		t.Fatal("public key does not match the written private seed")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private key permissions = %o, want 600", got)
	}

	stdout, stderr, err = runDDILCommand(ctx, "keygen")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if !strings.Contains(stdout, "private: ") || !strings.Contains(stdout, "public: ") {
		t.Fatalf("unlabelled keygen output: %s", stdout)
	}
	if !strings.Contains(stderr, "off the importing node") {
		t.Fatalf("keygen warning missing: %s", stderr)
	}
}
