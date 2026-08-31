// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/ddil"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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

func TestDDILExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot seed engine: %v", err)
	}

	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("create tenant: %v", err)
	}
	const appendedEvents = 4
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < appendedEvents; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:test", ActorKind: "user", Action: "agent.update", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("append audit events: %v", err)
	}
	var n int64
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(ctx)
		if err == nil && ok {
			n = head.Seq
		}
		return err
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("read audit head: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close seed engine: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	evidenceBody := []byte{0x00, 0x01, 0xfe, 0xff, '\n'}
	evidencePath := filepath.Join(t.TempDir(), "evidence.bin")
	if err := os.WriteFile(evidencePath, evidenceBody, 0o600); err != nil {
		t.Fatal(err)
	}

	export := func(out string) ddil.Imported {
		t.Helper()
		stdout, stderr, err := runDDILCommand(ctx,
			"export", "--data-dir", dataDir, "--tenant", tenant.String(),
			"--out", out, "--sign-key", signKey, "--segment-events", "2",
			"--evidence", "proof.bin="+evidencePath, "--notes", "ddil bundle round trip")
		if err != nil {
			t.Fatalf("ddil export: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var report ddilExportReport
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("decode export report: %v\n%s", err, stdout)
		}
		if report.Tenant != tenant.String() || report.Events != n || report.FromSeq != 1 || report.ToSeq != n {
			t.Fatalf("unexpected export report: %+v, want tenant=%s events/range=%d/1..%d", report, tenant, n, n)
		}
		if report.Policy.Included {
			t.Fatalf("policy unexpectedly included: %+v", report.Policy)
		}
		if len(report.Evidence) != 1 || report.Evidence[0] != "proof.bin" {
			t.Fatalf("evidence report = %v, want [proof.bin]", report.Evidence)
		}
		info, err := os.Stat(out)
		if err != nil {
			t.Fatalf("stat bundle: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("bundle permissions = %o, want 600", got)
		}
		return importDDILBundle(t, out, pub)
	}

	first := export(filepath.Join(t.TempDir(), "first.ddil"))
	if first.Index.Tenant != tenant.String() {
		t.Fatalf("index tenant = %q, want %q", first.Index.Tenant, tenant)
	}
	if len(first.Index.Segments) == 0 {
		t.Fatal("exported bundle has no audit segments")
	}
	next := int64(1)
	for _, segment := range first.Index.Segments {
		if segment.FromSeq != next {
			t.Fatalf("segment starts at %d, want %d", segment.FromSeq, next)
		}
		next = segment.ToSeq + 1
	}
	if next != n+1 {
		t.Fatalf("segments end at %d, want %d", next-1, n)
	}
	plan := first.Reconcile(0, "")
	if plan.GapBeforeApply {
		t.Fatal("fresh-node reconcile unexpectedly reports a gap")
	}
	if len(plan.NewSegments) != len(first.Index.Segments) || len(plan.SkippedSegments) != 0 {
		t.Fatalf("reconcile = %+v, want every segment new and none skipped", plan)
	}
	if len(first.Index.Evidence) != 1 {
		t.Fatalf("index evidence = %v, want one entry", first.Index.Evidence)
	}
	if got := first.Payloads[first.Index.Evidence[0]]; !bytes.Equal(got, evidenceBody) {
		t.Fatalf("evidence payload = %x, want %x", got, evidenceBody)
	}

	// A second bundle from the unchanged store must independently verify and import.
	second := export(filepath.Join(t.TempDir(), "second.ddil"))
	if second.Index.Tenant != tenant.String() || len(second.Index.Segments) != len(first.Index.Segments) {
		t.Fatalf("re-export index mismatch: first=%+v second=%+v", first.Index, second.Index)
	}

	staleOut := filepath.Join(t.TempDir(), "stale.ddil")
	stdout, stderr, err := runDDILCommand(ctx,
		"export", "--data-dir", dataDir, "--tenant", tenant.String(), "--out", staleOut,
		"--sign-key", signKey, "--max-staleness", "1h")
	if err == nil || !strings.Contains(err.Error(), "requires an included active policy snapshot") {
		t.Fatalf("--max-staleness without policy = %v, want refusal\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, statErr := os.Stat(staleOut); !os.IsNotExist(statErr) {
		t.Fatalf("refused export left an output file: %v", statErr)
	}
}

func TestDDILEvidenceNameValidation(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signKey := base64.StdEncoding.EncodeToString(priv.Seed())
	tenant := model.NewTenantID().String()
	evidencePath := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(evidencePath, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		specs []string
	}{
		{name: "empty", specs: []string{"=" + evidencePath}},
		{name: "slash", specs: []string{"dir/proof=" + evidencePath}},
		{name: "backslash", specs: []string{`dir\proof=` + evidencePath}},
		{name: "dotdot", specs: []string{"proof..txt=" + evidencePath}},
		{name: "duplicate", specs: []string{"proof=" + evidencePath, "proof=" + evidencePath}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{
				"export", "--data-dir", t.TempDir(), "--tenant", tenant,
				"--out", filepath.Join(t.TempDir(), "invalid.ddil"), "--sign-key", signKey,
			}
			for _, spec := range tc.specs {
				args = append(args, "--evidence", spec)
			}
			_, _, err := runDDILCommand(ctx, args...)
			if err == nil || !strings.Contains(err.Error(), "--evidence") {
				t.Fatalf("invalid evidence names %q returned %v, want --evidence refusal", tc.specs, err)
			}
		})
	}
}

func TestDDILKeygen(t *testing.T) {
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
