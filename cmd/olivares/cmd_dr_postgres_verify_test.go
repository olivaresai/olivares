// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// `dr verify` on SQLite restores the bundle into a throwaway directory and
// re-verifies the chain. On PostgreSQL it used to check the digest and decrypt the
// keys, and say that a chain check "requires restoring to a scratch Postgres". With
// --scratch-dsn it is that restore: a throwaway database with its own roles,
// removed when the check ends.

// scratchDSNFile writes the maintenance DSN into a 0600 file and returns the
// file: reference the command accepts (a DBA credential never goes in argv).
func scratchDSNFile(t *testing.T) string {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	if super == "" {
		t.Skipf("set %s to run the Postgres leg", pgProbeDSN)
	}
	p := filepath.Join(t.TempDir(), "scratch.dsn")
	if err := os.WriteFile(p, []byte(super+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "file:" + p
}

// leftoverScratch counts the databases and roles a scratch check left behind.
func (f *pgSplitFixture) leftoverScratch(t *testing.T) int {
	t.Helper()
	return f.superScalar(t, `SELECT (SELECT count(*) FROM pg_catalog.pg_database WHERE datname LIKE 'olvdr\_%')
	  + (SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname LIKE 'olvdr\_%')`)
}

// failingPgRestore stands in for pg_restore after the scratch database exists: the
// payload classification (--list) is answered by the real tool, any restore fails.
func failingPgRestore(t *testing.T, real string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pg_restore")
	body := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = \"--list\" ] && exec '" + real + "' \"$@\"; done\necho 'stand-in pg_restore: failing on purpose' >&2\nexit 1\n"
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDRVerifyPostgresRestoresIntoAScratchDatabase(t *testing.T) {
	stampVersion(t)
	est := newPGSplitFixture(t, "ver", true)
	dataDir := t.TempDir()
	pf := passphraseFile(t)
	scratch := scratchDSNFile(t)
	est.seed(t, dataDir)

	backup := func(t *testing.T, extra ...string) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "b.drbundle")
		a := append([]string{"backup", "--data-dir", dataDir}, est.drArgs()...)
		a = append(a, "--pg-dump", est.bin("pg_dump"), "--out", out, "--passphrase-file", pf)
		if o, err := runDR(t, append(a, extra...)...); err != nil {
			t.Fatalf("dr backup: %v\n%s", err, o)
		}
		return out
	}
	verify := func(bundle string, extra ...string) (string, error) {
		a := []string{"verify", "--in", bundle, "--passphrase-file", pf, "--pg-restore", est.bin("pg_restore")}
		return runDR(t, append(a, extra...)...)
	}
	good := backup(t)

	t.Run("a good bundle restores in the scratch database and the ledger verifies", func(t *testing.T) {
		out, err := verify(good, "--scratch-dsn", scratch)
		if err != nil {
			t.Fatalf("dr verify of a good PostgreSQL bundle failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "DR drill PASSED") {
			t.Fatalf("the verify did not report the restored ledger as safe:\n%s", out)
		}
		if n := est.leftoverScratch(t); n != 0 {
			t.Fatalf("the verify left %d scratch database(s) or role(s) behind", n)
		}
	})

	t.Run("a tampered payload under a valid digest is refused", func(t *testing.T) {
		// The digest and the keys check out: the bundle is authentic. Only a
		// restore can see that the ledger inside is not the one the manifest
		// describes. The tampered dump is bundled with the manifest of the CLEAN
		// estate, which is what a payload swapped before signing looks like.
		tampered := filepath.Join(t.TempDir(), "tampered.pgcustom")
		// The ledger's immutability trigger is put back in EXACTLY the mode it had:
		// the engine's append-only guard refuses a trigger that differs, even one
		// that is merely enabled in another way.
		enable := map[int]string{'O': "ENABLE", 'A': "ENABLE ALWAYS", 'R': "ENABLE REPLICA"}[est.superScalar(t,
			`SELECT ascii(tgenabled) FROM pg_catalog.pg_trigger
			  WHERE tgname = 'audit_events_immutable' AND tgrelid = 'public.audit_events'::regclass`)]
		if enable == "" {
			t.Fatal("fixture: the ledger's immutability trigger is not enabled in a known mode")
		}
		rewrite := func(from, to string) {
			est.superExecInDB(t, `BEGIN;
ALTER TABLE audit_events DISABLE TRIGGER audit_events_immutable;
UPDATE audit_events SET action = '`+to+`' WHERE seq = 2 AND action = '`+from+`';
ALTER TABLE audit_events `+enable+` TRIGGER audit_events_immutable;
COMMIT;`)
		}
		rewrite("agent.create", "tampered.event")
		if err := runPgDump(t.Context(), est.bin("pg_dump"), est.adminDSN, tampered); err != nil {
			t.Fatal(err)
		}
		rewrite("tampered.event", "agent.create")
		bad := backup(t, "--snapshot-file", tampered)

		// Without the scratch restore the old check still passes: that is the gap.
		if out, err := verify(bad); err != nil || !strings.Contains(out, "bundle integrity OK") {
			t.Fatalf("the digest-only verify was expected to pass the tampered bundle: %v\n%s", err, out)
		}
		out, err := verify(bad, "--scratch-dsn", scratch)
		if err == nil {
			t.Fatalf("a bundle whose ledger was tampered verified clean:\n%s", out)
		}
		if !strings.Contains(err.Error(), "DR drill FAILED") {
			t.Fatalf("the refusal must be the drill failing, got: %v", err)
		}
		// And for the right reason: a ledger event that no longer matches its chain.
		if !regexp.MustCompile(`tenant [0-9a-f]{8} (chain|per-event signatures)`).MatchString(err.Error()) {
			t.Fatalf("the refusal must name a broken chain or signature, got: %v", err)
		}
		if n := est.leftoverScratch(t); n != 0 {
			t.Fatalf("the failed verify left %d scratch database(s) or role(s) behind", n)
		}
	})

	t.Run("an inline credential is refused, the file: reference is the only way in", func(t *testing.T) {
		_, err := verify(good, "--scratch-dsn", "postgres://postgres:secret@127.0.0.1:1/postgres")
		if err == nil || !strings.Contains(err.Error(), "file: or env:") {
			t.Fatalf("an inline DBA credential was accepted: %v", err)
		}
	})

	t.Run("without --scratch-dsn the old check stands and says how to get the full one", func(t *testing.T) {
		out, err := verify(good)
		if err != nil {
			t.Fatalf("the digest-only verify must keep working: %v\n%s", err, out)
		}
		if !strings.Contains(out, "--scratch-dsn") || !strings.Contains(out, "NOT restored or re-verified") || strings.Contains(out, "DR drill PASSED") {
			t.Fatalf("the digest-only verify must say the ledger was not restored, name --scratch-dsn and not claim a pass:\n%s", out)
		}
	})

	t.Run("a restore that fails after the scratch database exists leaves nothing behind", func(t *testing.T) {
		out, err := verify(good, "--scratch-dsn", scratch, "--pg-restore", failingPgRestore(t, est.bin("pg_restore")))
		if err == nil || strings.Contains(out, "DR drill PASSED") {
			t.Fatalf("a failed restore must fail the verify, got %v\n%s", err, out)
		}
		if n := est.leftoverScratch(t); n != 0 {
			t.Fatalf("the failed restore left %d scratch database(s) or role(s) behind", n)
		}
	})
}

func TestDRDrillPostgresPublishesItsRTONextToSQLite(t *testing.T) {
	stampVersion(t)
	est := newPGSplitFixture(t, "drill", true)
	scratch := scratchDSNFile(t)
	// The SQLite line is the published one and keeps its exact shape; the
	// PostgreSQL line sits next to it under its own label.
	sqliteRTO := regexp.MustCompile(`(?m)^measured RTO \(restore \+ boot \+ verify\): \S+$`)
	postgresRTO := regexp.MustCompile(`(?m)^measured RTO \(postgres: restore \+ boot \+ verify\): \S+$`)

	out, err := runDR(t, "drill", "--events", "30", "--scratch-dsn", scratch,
		"--pg-dump", est.bin("pg_dump"), "--pg-restore", est.bin("pg_restore"))
	if err != nil {
		t.Fatalf("dr drill with a PostgreSQL scratch server failed: %v\n%s", err, out)
	}
	if !sqliteRTO.MatchString(out) || !postgresRTO.MatchString(out) {
		t.Fatalf("the drill must print one measured RTO per engine:\n%s", out)
	}
	// The PostgreSQL figure is a real duration, and the restored ledger is the seeded one.
	m := regexp.MustCompile(`(?m)^measured RTO \(postgres: restore \+ boot \+ verify\): (\S+)$`).FindStringSubmatch(out)
	if d, err := time.ParseDuration(m[1]); err != nil || d <= 0 {
		t.Fatalf("the PostgreSQL RTO %q is not a positive duration: %v", m[1], err)
	}
	seeded := regexp.MustCompile(`postgres: seeded a throwaway database: (\d+) ledger events`).FindStringSubmatch(out)
	restored := regexp.MustCompile(`DR drill PASSED \(postgres\) — restored (\d+) ledger events`).FindStringSubmatch(out)
	if seeded == nil || restored == nil {
		t.Fatalf("the drill must report the seeded and the restored event counts:\n%s", out)
	}
	if n, err := strconv.Atoi(restored[1]); err != nil || n < 30 {
		t.Fatalf("the restored ledger must hold at least the 30 seeded events, got %q:\n%s", restored[1], out)
	}
	if n := est.leftoverScratch(t); n != 0 {
		t.Fatalf("the drill left %d scratch database(s) or role(s) behind", n)
	}

	t.Run("an inline credential is refused", func(t *testing.T) {
		_, err := runDR(t, "drill", "--events", "5", "--scratch-dsn", "postgres://postgres:secret@127.0.0.1:1/postgres")
		if err == nil || !strings.Contains(err.Error(), "file: or env:") {
			t.Fatalf("an inline DBA credential was accepted: %v", err)
		}
	})

	t.Run("a restore that fails leaves nothing behind and does not pass", func(t *testing.T) {
		out, err := runDR(t, "drill", "--events", "5", "--scratch-dsn", scratch,
			"--pg-dump", est.bin("pg_dump"), "--pg-restore", failingPgRestore(t, est.bin("pg_restore")))
		if err == nil || !strings.Contains(err.Error(), "postgres drill restore") || strings.Contains(out, "DR drill PASSED (postgres)") {
			t.Fatalf("a failed PostgreSQL restore must fail the drill, got %v\n%s", err, out)
		}
		if n := est.leftoverScratch(t); n != 0 {
			t.Fatalf("the failed drill left %d scratch database(s) or role(s) behind", n)
		}
	})

	t.Run("without --scratch-dsn the PostgreSQL line says it was not measured", func(t *testing.T) {
		out, err := runDR(t, "drill", "--events", "5")
		if err != nil {
			t.Fatalf("the SQLite drill must keep working alone: %v\n%s", err, out)
		}
		if !strings.Contains(out, "postgres: not measured") || strings.Contains(out, "measured RTO (postgres") {
			t.Fatalf("a drill with no PostgreSQL server must say so rather than stay silent or invent a figure:\n%s", out)
		}
	})
}
