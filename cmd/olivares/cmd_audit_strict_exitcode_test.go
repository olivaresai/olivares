// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// runAuditVerifyStrict drives `olivares audit verify --strict` against a real
// ledger and returns the rendered report, the code the process would exit with,
// and the operator's message.
func runAuditVerifyStrict(t *testing.T, dir, tenant string) (string, int, string) {
	t.Helper()
	cmd := newAuditCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"verify", "--tenant", tenant, "--data-dir", dir, "--strict"})
	err := cmd.Execute()
	if err == nil {
		return out.String(), exitcode.OK, ""
	}
	return out.String(), exitcode.From(err), err.Error()
}

// TestAuditVerifyStrictExitCodesAreDistinct pins QA-15.
//
// `audit verify` already told three different stories in three different
// sentences — a ledger nobody has attested YET, a recovered incident, and a
// chain that actually failed — and then collapsed all three into exit 1, the
// code whose published meaning is "generic failure with no more specific
// classification". With --checkpoint-interval defaulting to an hour, the
// unattested state is the first hour of every install, so a cron gating on $?
// could not separate a new install from a broken chain.
//
// The two existing tests around this behavior (TestAuditVerifyUnattestedIsNotCorrupt
// and TestAuditVerifyForgedCheckpointStaysCorrupt) assert only that the error is
// non-nil, which is precisely why the collapse was invisible. This test asserts
// the VALUE, and asserts the JSON status alongside it in the same run so a code
// change that also moved the wire contract cannot pass.
func TestAuditVerifyStrictExitCodesAreDistinct(t *testing.T) {
	t.Run("unattested is Indeterminate, not a generic failure", func(t *testing.T) {
		dir, tenant := seedAuditLedger(t, false)
		out, code, msg := runAuditVerifyStrict(t, dir, tenant)
		if code != exitcode.Indeterminate {
			t.Errorf("exit = %d, want %d (Indeterminate): %s", code, exitcode.Indeterminate, msg)
		}
		if !strings.Contains(out, `"status": "unattested"`) {
			t.Errorf("the JSON status must stay unattested:\n%s", out)
		}
		// The sentence is unchanged by this repair; only the code moves.
		if !strings.Contains(msg, "NOT ATTESTED") || !strings.Contains(msg, "NOT evidence of tampering") {
			t.Errorf("the unattested message changed: %q", msg)
		}
	})

	t.Run("a clean attested ledger still exits 0", func(t *testing.T) {
		dir, tenant := seedAuditLedger(t, true)
		out, code, msg := runAuditVerifyStrict(t, dir, tenant)
		if code != exitcode.OK {
			t.Errorf("exit = %d, want 0: %s\n%s", code, msg, out)
		}
		if !strings.Contains(out, `"status": "ok"`) {
			t.Errorf("the JSON status must stay ok:\n%s", out)
		}
	})

	// The control that makes the calm code above mean something. A checkpoint
	// that EXISTS and does not verify is a proven failure, and it keeps exit 1:
	// reclassifying it alongside unattested would tell a fleet sweep that a
	// tampered chain is merely "not yet answered".
	t.Run("a forged checkpoint stays a proven failure at 1", func(t *testing.T) {
		dir, tenant := seedAuditLedger(t, true)
		tamperCheckpointSignature(t, dir, tenant)
		out, code, msg := runAuditVerifyStrict(t, dir, tenant)
		if code != exitcode.Err {
			t.Errorf("exit = %d, want %d (Err): %s", code, exitcode.Err, msg)
		}
		if !strings.Contains(out, `"status": "corrupt"`) {
			t.Errorf("the JSON status must stay corrupt:\n%s", out)
		}
		if !strings.Contains(msg, "integrity check FAILED") {
			t.Errorf("the corrupt message changed: %q", msg)
		}
	})

	// The second preserved code, through the real recovery ceremony rather than
	// an approximation: a recovered incident is an established fact about a
	// chain that IS broken at genesis, so it stays 1 as well.
	t.Run("a recovered incident stays a proven failure at 1", func(t *testing.T) {
		f := newRecoveryCLIFixture(t, true, true)
		_ = completeRecoveryForAttack(t, f)
		out, err := runAuditVerifyForRecovery(t, f, true)
		if err == nil {
			t.Fatalf("verify --strict on a recovered ledger must exit non-zero\n%s", out)
		}
		if code := exitcode.From(err); code != exitcode.Err {
			t.Errorf("exit = %d, want %d (Err): %v", code, exitcode.Err, err)
		}
		if !strings.Contains(out, `"status": "recovered"`) {
			t.Errorf("the JSON status must stay recovered:\n%s", out)
		}
		if !strings.Contains(err.Error(), "RECOVERED") {
			t.Errorf("the recovered message changed: %v", err)
		}
	})
}

// seedAuditLedger boots a demo-seeded SQLite engine, optionally takes one signed
// checkpoint, and closes it so the single-writer file is free for the verify
// command. It is the same setup the two existing audit tests use, kept in one
// place here because this test needs both of their shapes.
func seedAuditLedger(t *testing.T, checkpoint bool) (dir, tenant string) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
	}
	if checkpoint {
		if _, ok, cerr := eng.signer.Checkpoint(ctx, eng.store, eng.demoTenant); cerr != nil || !ok {
			t.Fatalf("checkpoint demo tenant: ok=%v err=%v", ok, cerr)
		}
	}
	tenant = eng.demoTenant.String()
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	return dir, tenant
}
