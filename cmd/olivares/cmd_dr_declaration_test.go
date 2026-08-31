// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The console advertises a two-person restore. The CLI restore had NO identity
// anywhere on its path (grep -cE 'authz|Authenticate|Principal|PersonRef|dual'
// over cmd_dr.go: 0) — and for a Postgres estate the console REFUSES the restore
// and points at that very CLI (dr_handler.go: "use CLI for postgres"). So on the
// default production engine the two-person control did not exist at all, and
// --in-place replaced a live estate that had it armed, silently and rc 0.
//
// This comment used to declare the two other closures IMPOSSIBLE. Neither is, an
// external contrast measured both claims down, and the corrected reasoning — why
// authenticating against a HISTORICAL identity is a decision against, not an
// impossibility, and why a Postgres restore from the API is a packaging limit
// rather than a false dilemma — is in cmd/olivares/dr_declaration.go's header. It
// is written once, there, so the two cannot drift apart again.
//
// The closure is the third one: a restore that REPLACES an existing estate must
// carry an explicit declaration — who is doing it and why — and that declaration is
// written to the restored estate's own signed ledger.
//
// What this is NOT, stated here so no reader mistakes it: it is a DECLARATION, not
// authentication, and there is no second person anywhere on this path. The CLI's
// real trust boundary is filesystem + KEK access, and whoever holds both can
// destroy the estate by other means. What it buys is that the honest operator
// following the runbook can no longer bypass a control the estate opted into
// WITHOUT KNOWING, and that the act stops being silent.

// drDeclarationFixture seeds an estate, backs it up, and returns (dataDir,
// bundle, passphraseFile).
func drDeclarationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--engine", "sqlite",
		"--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	return src, bundle, pf
}

// restoreDeclarations collects the dr.restore.cli events recorded in a restored
// data dir's ledger, across every tenant.
func restoreDeclarations(t *testing.T, dataDir string) []model.AuditEvent {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("boot restored estate: %v", err)
	}
	defer func() { _ = eng.Close() }()

	// Every tenant, counted ONCE: ListOrgs already returns the system org, so
	// appending SystemTenantID unconditionally walked the same chain twice and
	// reported one recorded event as two.
	seen := map[model.TenantID]bool{model.SystemTenantID: true}
	tenants := []model.TenantID{model.SystemTenantID}
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		orgs, e := sys.ListOrgs(ctx)
		for _, o := range orgs {
			if !seen[o.TenantID] {
				seen[o.TenantID] = true
				tenants = append(tenants, o.TenantID)
			}
		}
		return e
	}); err != nil {
		t.Fatalf("list orgs: %v", err)
	}

	var found []model.AuditEvent
	for _, tid := range tenants {
		if err := eng.store.View(ctx, tid, func(sc store.Scope) error {
			return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
				if ev.Action == drRestoreDeclarationAction {
					found = append(found, ev)
				}
				return nil
			})
		}); err != nil {
			t.Fatalf("walk tenant %s: %v", tid, err)
		}
	}
	return found
}

// TestRestoreReplacesAnEstateSeesBothCustodySignals covers a branch the
// end-to-end tests could not reach, and did not: they all run on SQLite, where
// the data dir holds BOTH the signing keys and the store file, so the OR was
// always satisfied by the store file and the key check was never load-bearing. A
// mutant that blinded the key check survived every one of them.
//
// The key check is the only LOCAL one that fires for a POSTGRES estate — its store
// is a DSN, not a file in the data dir — and Postgres is the engine the console
// refuses to restore and sends to this command. So the untested branch was the one
// that mattered most.
//
// It is no longer the only one. The header used to argue that a Postgres estate
// with externally custodied keys was a NAMED residue and not a working bypass; the
// sol-max contrast measured otherwise, so the classifier now asks the target
// database as well. The cells below are the LOCAL half; the remote half
// needs a server and lives in cmd_dr_postgres_test.go.
func TestRestoreReplacesAnEstateSeesBothCustodySignals(t *testing.T) {
	writeFile := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// replaces runs the classifier for a SQLite target rooted at dir. The local
	// signals are decided before any DSN is probed, so no server is involved.
	replaces := func(t *testing.T, dir string) bool {
		t.Helper()
		got, _ := restoreTarget{engineKind: "sqlite", dataDir: dir}.replacesAnEstate(t.Context())
		return got
	}
	t.Run("keys only — a Postgres estate, whose store is a DSN", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "audit-signing.key")
		// restoreStorePath returns "" for a non-sqlite engine: nothing LOCAL but the
		// key tells this command it is about to replace a live estate.
		if got := restoreStorePath("postgres", "postgres://x/y", dir); got != "" {
			t.Fatalf("a postgres target has no local store file, got %q", got)
		}
		// The DSN is deliberately unreachable, and the key must decide BEFORE it is
		// probed: a data dir holding this estate's custody is an estate, and the
		// verdict must not depend on a server being up.
		got, why := restoreTarget{
			engineKind: "postgres", dsn: "postgres://127.0.0.1:1/nope", dataDir: dir,
		}.replacesAnEstate(t.Context())
		if !got {
			t.Fatal("a data dir holding the estate's signing keys is a live estate")
		}
		if !strings.Contains(why, "signing keys") {
			t.Fatalf("the verdict must name the LOCAL signal, not the probe: %q", why)
		}
	})
	t.Run("store only — keys externally custodied", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "olivares.db")
		if !replaces(t, dir) {
			t.Fatal("an existing store file is a live estate")
		}
	})
	t.Run("neither — the clean target the runbook prescribes", func(t *testing.T) {
		if replaces(t, t.TempDir()) {
			t.Fatal("an empty target replaces nothing and must stay unencumbered")
		}
	})
	t.Run("a directory named like the store is not a store", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "olivares.db"), 0o700); err != nil {
			t.Fatal(err)
		}
		if replaces(t, dir) {
			t.Fatal("a directory is not an estate's store file")
		}
	})
}

// TestDRRestoreOverALiveEstateRefusesWithoutADeclaration is the reproduction:
// the same command that silently replaced a live estate must now refuse.
func TestDRRestoreOverALiveEstateRefusesWithoutADeclaration(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)

	// --force over the SAME data dir: this is a live estate being replaced.
	out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force")
	if err == nil {
		t.Fatalf("BYPASS: a restore replaced a live estate with no declaration at all:\n%s", out)
	}
	msg := err.Error()
	for _, want := range []string{"--operator", "--reason"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must name what is missing (%q), got: %s", want, msg)
		}
	}

	// Half a declaration is not a declaration.
	if _, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force", "--operator", "alice@x.io"); err == nil {
		t.Fatal("a restore with --operator but no --reason was accepted")
	}
	if _, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force", "--reason", "INC-42"); err == nil {
		t.Fatal("a restore with --reason but no --operator was accepted")
	}
	// Whitespace is not an identity.
	if _, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force", "--operator", "   ", "--reason", "INC-42"); err == nil {
		t.Fatal("a blank --operator was accepted as a declaration")
	}
}

// TestDRRestoreWithoutInPlacePreservesWhatItOverwrites closes the P2 the sol-max
// contrast named: outside --in-place, everything destructive happens BEFORE the
// seal can fail. The bytes are copied over the live store, then the store is
// booted, verified and only then does the declaration get appended — so a ledger
// that refuses the append (a full audit spool in block mode is the measured way)
// ends the command non-zero with the estate already replaced and no record of it.
//
// The staged discipline that makes --in-place safe cannot be lifted here: the
// non-in-place path exists precisely for the operator restoring into a directory
// they are willing to overwrite. What it can do is stop the previous state from
// being UNRECOVERABLE, with the same automatic pre-restore copy --in-place already
// takes. Then a failure after the destructive step is a bad afternoon rather than
// a lost estate.
//
// This asserts the copy exists and still holds the PREVIOUS bytes — not merely
// that a file with the right name appeared.
func TestDRRestoreWithoutInPlacePreservesWhatItOverwrites(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)

	livePath := filepath.Join(src, "olivares.db")
	before, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	// Move the live estate on, so "preserved" and "restored" cannot be the same
	// bytes by accident.
	seedExtraTenant(t, src)
	moved, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(moved) == string(before) {
		t.Fatal("the fixture did not change the live store, so this test cannot tell the copies apart")
	}

	out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force",
		"--operator", "alice@x.io", "--reason", "INC-42")
	if err != nil {
		t.Fatalf("declared restore: %v\n%s", err, out)
	}

	matches, _ := filepath.Glob(filepath.Join(src, "olivares.db.pre-restore-*"))
	if len(matches) != 1 {
		t.Fatalf("a destructive restore left %d pre-restore copies of the store, want 1:\n%s", len(matches), out)
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != string(moved) {
		t.Fatal("the pre-restore copy does not hold the bytes the restore overwrote")
	}
	if keys, _ := filepath.Glob(filepath.Join(src, "*-signing.key.pre-restore-*")); len(keys) == 0 {
		t.Fatal("the previous signing keys were overwritten without a pre-restore copy")
	}
	if !strings.Contains(out, "pre-restore-") {
		t.Fatalf("the operator was not told the previous state was preserved:\n%s", out)
	}
}

// seedExtraTenant writes one more tenant into a live SQLite estate, so its store
// bytes differ from the bundle's.
func seedExtraTenant(t *testing.T, dataDir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS s630_after_backup (note text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO s630_after_backup VALUES ('work done AFTER the backup')`); err != nil {
		t.Fatal(err)
	}
}

// TestDRRestoreOverALiveEstateRecordsTheDeclaration proves the declaration is not
// merely friction: it lands in the restored estate's SIGNED ledger, which is the
// only place evidence of a restore survives the restore.
func TestDRRestoreOverALiveEstateRecordsTheDeclaration(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)

	out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force",
		"--operator", "alice@x.io", "--reason", "INC-42 ransomware recovery")
	if err != nil {
		t.Fatalf("declared restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restore verified") {
		t.Fatalf("restore did not complete:\n%s", out)
	}

	evs := restoreDeclarations(t, src)
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 recorded restore declaration, got %d", len(evs))
	}
	ev := evs[0]
	if !strings.Contains(ev.Actor, "alice@x.io") {
		t.Fatalf("the recorded actor does not name the declared operator: %q", ev.Actor)
	}
	if len(ev.Hash) == 0 {
		t.Fatal("the declaration was not sealed into the chain")
	}
	// The metadata was committed into the chain. This alone does NOT prove the
	// reason reached the row — commitments are blinded, so it would be non-empty
	// whatever was stored; TestDRRestoreDeclarationReasonReachesTheStoredRow reads
	// the stored column for that.
	if len(ev.MetaCommitment) == 0 {
		t.Fatal("the declaration's metadata was not committed into the event")
	}

	// The operator-facing output must state the reason and must not let anyone
	// mistake a typed-in name for an authenticated one.
	for _, want := range []string{"INC-42 ransomware recovery", "alice@x.io", "authenticated by nothing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the restore output does not state %q:\n%s", want, out)
		}
	}
}

// TestDRRestoreDeclarationReasonReachesTheStoredRow is the discriminator for the
// assertion above: the reason has to reach the sealed row, not merely be printed
// on the way past. It cannot be checked through Walk (Meta is nil on every read
// path) and it cannot be checked through MetaCommitment either — commitments are
// BLINDED, so identical metadata commits differently on every run and any
// comparison of them would pass whatever was stored, including nothing.
//
// So this reads the stored column. It is the only measurement that distinguishes
// "recorded" from "printed", and a restore record nobody can read back is not
// evidence.
func TestDRRestoreDeclarationReasonReachesTheStoredRow(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)
	const reason = "INC-42 ransomware recovery"
	if out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--force", "--operator", "alice@x.io", "--reason", reason); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}

	db, err := sql.Open("sqlite", filepath.Join(src, "olivares.db"))
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer func() { _ = db.Close() }()

	var meta, actor string
	err = db.QueryRow(`SELECT meta, actor FROM audit_events WHERE action = ?`,
		drRestoreDeclarationAction).Scan(&meta, &actor)
	if err != nil {
		t.Fatalf("read the stored declaration row: %v", err)
	}
	if !strings.Contains(actor, "alice@x.io") {
		t.Fatalf("the stored actor does not name the declared operator: %q", actor)
	}
	for _, want := range []string{reason, `"identity":"declared"`, `"second_party":"none"`} {
		if !strings.Contains(meta, want) {
			t.Fatalf("the stored declaration does not carry %q:\n%s", want, meta)
		}
	}
	// The gate is on the record NAMED FOR WHAT IT IS. The field used to be
	// console_dual_control_restore and was described as saying "whether a two-person
	// control was in force when one person acted"; what it actually reads is the
	// policy inside the RESTORED COPY, and the estate this replaced is gone by then.
	// A copy taken while the gate was off, restored over an estate that armed it
	// afterwards, signed "off" about a control that was on.
	//
	// The old name is asserted ABSENT, not merely the new one present: a rename that
	// left the misleading key in place alongside would satisfy a presence check.
	if !strings.Contains(meta, "bundle_dual_control_restore") {
		t.Fatalf("the stored declaration does not say what the RESTORED COPY's gate said:\n%s", meta)
	}
	if strings.Contains(meta, "console_dual_control_restore") {
		t.Fatalf("the stored declaration still carries the key that named a question it cannot answer:\n%s", meta)
	}
	// And the classifier's verdict, so a reader can tell a measured "this target
	// holds an estate" from a fail-closed "I could not look".
	if !strings.Contains(meta, `"target_verdict"`) {
		t.Fatalf("the stored declaration does not say WHY this target counted as an estate:\n%s", meta)
	}
}

// TestDRRestoreIntoAnEmptyTargetNeedsNoDeclaration is the discriminator: the
// documented disaster path — restore into a clean target — destroys nothing and
// must not gain a single flag. A control that also fires when there is nothing to
// protect is friction an operator will route around in an outage.
func TestDRRestoreIntoAnEmptyTargetNeedsNoDeclaration(t *testing.T) {
	_, bundle, pf := drDeclarationFixture(t)
	dst := t.TempDir()

	out, err := runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite",
		"--passphrase-file", pf)
	if err != nil {
		t.Fatalf("clean-target restore must stay unencumbered: %v\n%s", err, out)
	}
	if evs := restoreDeclarations(t, dst); len(evs) != 0 {
		t.Fatalf("a clean-target restore recorded %d bypass declarations; it bypassed nothing", len(evs))
	}
}

// TestDRRestoreInPlaceRequiresAndRecordsTheDeclaration covers the other
// live-replacement path, the one measured replacing a real estate: --in-place
// always targets a live data dir, so it always has something to destroy.
func TestDRRestoreInPlaceRequiresAndRecordsTheDeclaration(t *testing.T) {
	src, bundle, pf := drDeclarationFixture(t)

	out, err := runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--in-place")
	if err == nil {
		t.Fatalf("BYPASS: --in-place replaced a live estate with no declaration:\n%s", out)
	}

	out, err = runDR("restore", "--in", bundle, "--data-dir", src, "--engine", "sqlite",
		"--passphrase-file", pf, "--in-place",
		"--operator", "bob@x.io", "--reason", "INC-43 verified rollback")
	if err != nil {
		t.Fatalf("declared in-place restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "promoted in place") {
		t.Fatalf("in-place restore did not promote:\n%s", out)
	}
	evs := restoreDeclarations(t, src)
	if len(evs) != 1 {
		t.Fatalf("want 1 recorded declaration after an in-place restore, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Actor, "bob@x.io") {
		t.Fatalf("recorded actor %q", evs[0].Actor)
	}
}
