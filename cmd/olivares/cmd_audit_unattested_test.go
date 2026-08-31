// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// TestAuditVerifyUnattestedIsNotCorrupt covers the first-boot case on the CLI.
// A freshly installed engine has no signed checkpoint until the scheduler fires,
// and `audit verify` used to call that ledger "corrupt" — an accusation of
// tampering against a healthy install, which is how the word stops meaning
// anything. The three answers must be distinguishable, and the loud one must
// still be reachable.
func TestAuditVerifyUnattestedIsNotCorrupt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A real, signed chain — with NO checkpoint ever taken.
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if eng.demoTenant.IsZero() {
		t.Fatal("no demo tenant seeded")
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
		e := cmd.Execute()
		return out.String(), e
	}

	out, err := run("verify", "--tenant", tenant, "--data-dir", dir)
	if err != nil {
		t.Fatalf("verify without --strict must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"status": "unattested"`) {
		t.Fatalf("an unattested but otherwise healthy ledger must not be reported corrupt\n%s", out)
	}
	if strings.Contains(out, `"status": "corrupt"`) {
		t.Fatalf("a healthy first-boot ledger was accused of corruption\n%s", out)
	}
	// The reason is carried, not invented.
	if !strings.Contains(out, `"Reason": "no-checkpoints"`) {
		t.Fatalf("expected the no-checkpoints reason in the report\n%s", out)
	}

	// --strict still exits non-zero — the automation gate keeps its detection
	// power — but the message must not imply tampering.
	sout, serr := run("verify", "--tenant", tenant, "--data-dir", dir, "--strict")
	if serr == nil {
		t.Fatalf("verify --strict on an unattested ledger must still exit non-zero\n%s", sout)
	}
	msg := serr.Error()
	if !strings.Contains(msg, "NOT ATTESTED") || !strings.Contains(msg, "NOT evidence of tampering") {
		t.Fatalf("--strict message must name the real reason, got: %s", msg)
	}
	if strings.Contains(msg, "integrity check FAILED") {
		t.Fatalf("--strict must not report an integrity FAILURE for an unattested ledger: %s", msg)
	}
}

// TestAuditVerifyForgedCheckpointStaysCorrupt is the control the calm word above
// has to earn: a checkpoint that EXISTS and does not verify keeps the loud verdict
// and the non-zero --strict exit.
//
// The tamper is done in the DATABASE FILE — the append-only trigger dropped, one
// byte of a real checkpoint signature flipped, the trigger restored — because that
// isolates the checkpoint verdict and nothing else. Two cheaper-looking setups do
// NOT isolate it, and a mutation run proved it: pinning a foreign ed25519
// --pubkey also pins it as an EVENT key (cmd_audit.go parses one ed25519 --pubkey
// into pinnedEventKeys too), so event_sigs fails as well and the report would read
// "corrupt" for a reason that has nothing to do with the checkpoint switch. Here
// the per-event pass is untouched: it SKIPS checkpoint events by design
// (core/audit/eventsig.go), so the checkpoint verdict is the only thing that moves.
func TestAuditVerifyForgedCheckpointStaysCorrupt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if _, ok, cerr := eng.signer.Checkpoint(ctx, eng.store, eng.demoTenant); cerr != nil || !ok {
		t.Fatalf("checkpoint demo tenant: ok=%v err=%v", ok, cerr)
	}
	tenant := eng.demoTenant.String()
	_ = eng.Close() // release the single-writer SQLite file

	tamperCheckpointSignature(t, dir, tenant)

	run := func(args ...string) (string, error) {
		cmd := newAuditCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		e := cmd.Execute()
		return out.String(), e
	}

	out, err := run("verify", "--tenant", tenant, "--data-dir", dir)
	if err != nil {
		t.Fatalf("verify without --strict must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"status": "corrupt"`) {
		t.Fatalf("a checkpoint that fails verification must stay corrupt\n%s", out)
	}
	if strings.Contains(out, `"status": "unattested"`) {
		t.Fatalf("a FAILED checkpoint was softened into the calm unattested verdict\n%s", out)
	}
	if !strings.Contains(out, `"Reason": "checkpoint-sig-invalid"`) {
		t.Fatalf("expected the checkpoint signature failure to be named\n%s", out)
	}
	// The isolation this test depends on: only the checkpoint half moved. If the
	// per-event pass ever stops skipping checkpoint events, this fails and the
	// assertion above stops proving what it claims.
	if strings.Contains(out, `"Reason": "event-sig-invalid"`) {
		t.Fatalf("per-event signatures must stay clean so the checkpoint verdict is isolated\n%s", out)
	}
	if _, serr := run("verify", "--tenant", tenant, "--data-dir", dir, "--strict"); serr == nil {
		t.Fatal("verify --strict against a tampered checkpoint must exit non-zero")
	}
}

// tamperCheckpointSignature rewrites one byte of the first checkpoint's Ed25519
// signature directly in the SQLite file, the way an attacker with the data dir
// would: the append-only trigger is dropped for the write and restored after, so
// the ledger is left looking untouched.
func tamperCheckpointSignature(t *testing.T, dir, tenant string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	var seq int64
	var sig []byte
	if err := raw.QueryRow(
		"SELECT seq, sig FROM audit_events WHERE tenant_id = ? AND action = ? ORDER BY seq LIMIT 1",
		tenant, audit.ActionCheckpoint,
	).Scan(&seq, &sig); err != nil {
		t.Fatalf("read checkpoint to tamper: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("checkpoint carries no signature to tamper with")
	}
	if _, err := raw.Exec("DROP TRIGGER IF EXISTS audit_events_no_update"); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{sig[0] ^ 0xFF}, sig[1:]...)
	if _, err := raw.Exec(
		"UPDATE audit_events SET sig = ? WHERE tenant_id = ? AND seq = ?", bad, tenant, seq,
	); err != nil {
		t.Fatalf("tamper checkpoint signature: %v", err)
	}
	if _, err := raw.Exec(
		"CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events " +
			"BEGIN SELECT RAISE(ABORT,'audit_events is append-only'); END",
	); err != nil {
		t.Fatal(err)
	}
}
