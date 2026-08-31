// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditArchiveExportVerifyCLI covers the CLI round trip: `audit
// archive export` writes JSONL segments + manifests + an advisory keys.json,
// and `audit archive verify` re-verifies them OFFLINE (no store boot) — first
// against the advisory keys, then failing under --strict with a pinned wrong
// key (pins REPLACE the advisory file).
func TestAuditArchiveExportVerifyCLI(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "archive")

	// Seed a real, signed, demo chain into the default SQLite file under dataDir.
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}
	tenant := eng.demoTenant.String()
	_ = eng.Close() // release the single-writer SQLite file for the export command

	run := func(args ...string) (string, error) {
		cmd := newAuditCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		return func() (string, error) { e := cmd.Execute(); return out.String(), e }()
	}

	out, err := run("archive", "export", "--tenant", tenant, "--data-dir", dataDir, "--out", outDir, "--segment-events", "10")
	if err != nil {
		t.Fatalf("archive export: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"Segments"`) {
		t.Errorf("export summary missing: %s", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "keys.json")); err != nil {
		t.Fatalf("keys.json not written: %v", err)
	}

	// Offline verify against the advisory keys.json: all checks (chain, per-event
	// and checkpoint signatures) must pass; --strict exits 0.
	out, err = run("archive", "verify", "--dir", outDir, "--strict")
	if err != nil {
		t.Fatalf("archive verify --strict on a healthy archive: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"OK": true`) || !strings.Contains(out, `"checked": true`) {
		t.Errorf("healthy archive report should be all-OK with signatures checked\n%s", out)
	}

	// A pinned WRONG key pair replaces the advisory keys.json and must fail
	// loudly under --strict (and report event-sig-invalid). Event and
	// checkpoint keys are pinned together: pinning only the event key is
	// refused (checkpoint lines would be unverifiable).
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	wrong := base64.StdEncoding.EncodeToString(wrongPub)
	out, err = run("archive", "verify", "--dir", outDir, "--event-pubkey", wrong, "--pubkey", wrong, "--strict")
	if err == nil {
		t.Errorf("verify --strict with a wrong pinned key must return a non-zero exit\n%s", out)
	}
	if !strings.Contains(out, "event-sig-invalid") {
		t.Errorf("report should name event-sig-invalid\n%s", out)
	}

	// Without --strict the exit stays 0 and the status lives in the JSON (the
	// same exit-code contract as `audit verify`).
	out, err = run("archive", "verify", "--dir", outDir, "--event-pubkey", wrong, "--pubkey", wrong)
	if err != nil {
		t.Fatalf("verify WITHOUT --strict must exit 0 even on a failed check (got %v)\n%s", err, out)
	}
	if !strings.Contains(out, `"OK": false`) {
		t.Errorf("failed check should be reported in the JSON\n%s", out)
	}

	// --event-pubkey WITHOUT --pubkey is refused up front: checkpoint lines
	// would be unverifiable (a forged archive could dress every event as a
	// checkpoint to dodge the per-event check). The error instructs to pin both.
	out, err = run("archive", "verify", "--dir", outDir, "--event-pubkey", wrong)
	if err == nil {
		t.Fatalf("verify with only --event-pubkey must refuse to run\n%s", out)
	}
	if !strings.Contains(err.Error(), "--pubkey") {
		t.Errorf("refusal should instruct to pin the checkpoint key too, got: %v", err)
	}

	// Re-exporting into the SAME --out must succeed: segments and manifests are
	// deterministic (byte-identical re-puts, absorbed by the WORM dir sink) and
	// the advisory keys.json — whose created_at differs — is warn-not-fail, the
	// loop's writeKeysOnce posture.
	out, err = run("archive", "export", "--tenant", tenant, "--data-dir", dataDir, "--out", outDir, "--segment-events", "10")
	if err != nil {
		t.Fatalf("re-export into the same --out: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"written": false`) || !strings.Contains(out, "warning: advisory keys.json not written") {
		t.Errorf("re-export should warn that keys.json was kept, not fail\n%s", out)
	}
	// And the re-exported archive still verifies clean.
	out, err = run("archive", "verify", "--dir", outDir, "--strict")
	if err != nil {
		t.Fatalf("verify after re-export: %v\n%s", err, out)
	}
}
