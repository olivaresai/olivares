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
	"strings"
	"testing"
)

// TestAuditVerifyStrictExit covers the operability fix: `audit verify`
// prints a JSON report and, by DEFAULT, exits 0 even when the chain fails to
// verify (so a naive `verify && echo OK` cron lies about a tampered ledger).
// --strict turns any failed integrity check (chain / checkpoints / event_sigs)
// into a non-zero exit so on-call automation can gate on $?.
func TestAuditVerifyStrictExit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Seed a real, signed, demo chain into the default SQLite file under dir.
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}
	if _, ok, err := eng.signer.Checkpoint(ctx, eng.store, eng.demoTenant); err != nil || !ok {
		t.Fatalf("checkpoint demo tenant: ok=%v err=%v", ok, err)
	}
	tenant := eng.demoTenant.String()
	_ = eng.Close() // release the single-writer SQLite file for the verify command

	run := func(args ...string) (string, error) {
		cmd := newAuditCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		return func() (string, error) { e := cmd.Execute(); return out.String(), e }()
	}

	// Healthy chain → --strict succeeds (exit 0) and the report is all-OK.
	out, err := run("verify", "--tenant", tenant, "--data-dir", dir, "--strict")
	if err != nil {
		t.Fatalf("verify --strict on a healthy chain returned an error: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"OK": true`) || strings.Contains(out, `"OK": false`) {
		t.Errorf("healthy chain report should be all-OK\n%s", out)
	}

	// Verifying the signed events against a WRONG external key makes event_sigs
	// fail. The bug: WITHOUT --strict the command still exits 0.
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	wrong := base64.StdEncoding.EncodeToString(wrongPub)

	out, err = run("verify", "--tenant", tenant, "--data-dir", dir, "--pubkey", wrong)
	if err != nil {
		t.Fatalf("verify WITHOUT --strict must exit 0 even on a failed check (got %v)\n%s", err, out)
	}
	if !strings.Contains(out, `"OK": false`) {
		t.Errorf("verifying against a wrong key should report a failed check (OK:false)\n%s", out)
	}

	// The fix: --strict against the same failing check returns a non-zero exit.
	if _, serr := run("verify", "--tenant", tenant, "--data-dir", dir, "--pubkey", wrong, "--strict"); serr == nil {
		t.Error("verify --strict against a wrong key must return a non-zero (error) exit")
	}
}
