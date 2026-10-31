// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/store"
)

// drillPostgres is the PostgreSQL half of `dr drill`: the same round trip as the
// SQLite drill — seed a signed, checkpointed ledger, back it up, destroy the estate,
// restore into a clean target, re-verify — with throwaway databases (the source and
// the target) created through the maintenance DSN and removed afterwards. Without a
// maintenance DSN it says that PostgreSQL was not measured, rather than stay silent.
//
// The RTO covers what an operator waits for once the empty target exists: open and
// authenticate the bundle, install the keys, pg_restore, close the restored
// authority, boot, and re-verify the whole ledger. The target is provisioned before
// the clock starts, as `olivares db init` provisions it before a real restore.
func drillPostgres(cmd *cobra.Command, scratchRef string, events int, pgDumpPath, pgRestorePath string) (err error) {
	w := cmd.OutOrStdout()
	if scratchRef == "" {
		drNote(cmd, "postgres: not measured (pass --scratch-dsn, a file:/env: reference to a maintenance DSN, to run the same drill on PostgreSQL)")
		return nil
	}
	// An interrupt cancels the work and lets the deferred drops run: without a
	// signal context the process would die with a database and roles left behind.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	maintenance, err := resolveScratchDSN(ctx, "--scratch-dsn", scratchRef)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "olivares-dr-drill-pg-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	estateDir := filepath.Join(root, "estate")
	if err := os.MkdirAll(estateDir, 0o700); err != nil {
		return err
	}
	bundle := filepath.Join(root, "drill.drbundle")

	// 1) Seed a throwaway PostgreSQL estate with a real signed, checkpointed ledger.
	source, err := provisionScratchPostgres(ctx, maintenance)
	if err != nil {
		return err
	}
	// drop is idempotent: the explicit drop below is the simulated disaster, this
	// one is the guarantee that a failed drill leaves nothing behind.
	defer func() { err = errors.Join(err, source.drop(ctx)) }()
	srcFlags := source.flags
	srcFlags.dataDir = estateDir
	seed, err := seedDrillEstate(ctx, bootConfig{
		DataDir: estateDir, Engine: string(store.EnginePostgres),
		DSN: srcFlags.dsn, OwnerDSN: srcFlags.ownerDSN, AdminDSN: srcFlags.adminDSN,
		Version: version, Logger: slog.Default(), storeEngineExplicit: true, NoIngest: true,
	}, events)
	if err != nil {
		return fmt.Errorf("postgres drill seed: %w", err)
	}
	fmt.Fprintf(w, "postgres: seeded a throwaway database: %d ledger events, %d tenant(s)\n", seed.businessEvents, seed.tenants)

	// 2) Backup with an ephemeral in-memory passphrase, exactly as `dr backup` does.
	pass := make([]byte, 32)
	if _, err := rand.Read(pass); err != nil {
		return err
	}
	cipher, err := dr.NewPassphraseCipher(pass)
	if err != nil {
		return err
	}
	sealed, refs, _, err := sealSigningKeys(estateDir, cipher)
	if err != nil {
		return err
	}
	backupWork, err := os.MkdirTemp(root, "backup-")
	if err != nil {
		return err
	}
	m, snapshotPath, err := backupPostgres(ctx, srcFlags, backupWork, refs, "dr drill", time.Now(), pgOpts{pgDumpPath: pgDumpPath})
	if err != nil {
		return fmt.Errorf("postgres drill backup: %w", err)
	}
	if err := writeDrillBundle(bundle, m, snapshotPath, sealed, cipher); err != nil {
		return err
	}
	fmt.Fprintf(w, "postgres: backed up: %s (%d bytes)\n", filepath.Base(bundle), fileSizeOr0(bundle))

	// 3) DISASTER: drop the live database and its custody. Everything now depends on the bundle.
	if err := source.drop(ctx); err != nil {
		return fmt.Errorf("postgres drill destroy estate: %w", err)
	}
	if err := os.RemoveAll(estateDir); err != nil {
		return fmt.Errorf("postgres drill destroy estate: %w", err)
	}
	fmt.Fprintln(w, "postgres: destroyed the live estate (simulated total loss of the database and the host)")

	// 4) RESTORE + VERIFY into a clean target, measured.
	target, err := provisionScratchPostgres(ctx, maintenance)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.drop(ctx)) }()
	t0 := time.Now()
	restoreWork, err := os.MkdirTemp(root, "restore-")
	if err != nil {
		return err
	}
	rm, kek, err := openAndCheckBundle(bundle, restoreWork)
	if err != nil {
		return fmt.Errorf("postgres drill open bundle: %w", err)
	}
	rcipher, err := dr.OpenCipher(pass, kek)
	if err != nil {
		return err
	}
	if err := dr.VerifyBundleIntegrity(restoreWork, rm, kek, rcipher, false); err != nil {
		return fmt.Errorf("postgres drill verify bundle authentication: %w", err)
	}
	payload, err := classifyDRPayload(ctx, rm.EngineKind, rm.Store.Method, filepath.Join(restoreWork, rm.Store.File), pgRestorePath)
	if err != nil {
		return err
	}
	rep, err := restoreVerifyScratch(ctx, restoreWork, rm, rcipher, target, pgRestorePath, payload)
	if err != nil {
		return fmt.Errorf("postgres drill restore: %w", err)
	}
	rto := time.Since(t0)

	// 5) Report. The PostgreSQL manifest is TipAdvisory because a live node's
	// tips may trail its dump; this drill's source is quiescent, so every tenant's
	// restored tip MUST equal the seeded one, and a ledger that lost its tail fails
	// here instead of passing on a chain that still verifies.
	var restoredEvents int64
	for _, tv := range rep.Tenants {
		restoredEvents += tv.RestoredSeq
		if tv.RestoredSeq != tv.ManifestSeq {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s restored to seq %d, the drill seeded %d", short(tv.Tenant), tv.RestoredSeq, tv.ManifestSeq))
		}
	}
	if err := printReport(cmd, rep); err != nil {
		return fmt.Errorf("DR drill could not be reported: %w", err)
	}
	if !rep.OK {
		return fmt.Errorf("DR drill FAILED (postgres): bundle does not restore to a continuity-safe ledger: %s", strings.Join(rep.Problems, "; "))
	}
	// Both throwaway databases are gone before a success line is printed, so a
	// "PASSED" is never followed by a cleanup failure.
	if err := errors.Join(target.drop(ctx), source.drop(ctx)); err != nil {
		return err
	}
	return reportDRDrillPostgres(w, restoredEvents, rto)
}

func reportDRDrillPostgres(w io.Writer, restoredEvents int64, rto time.Duration) error {
	if restoredEvents <= 0 {
		return fmt.Errorf("DR drill FAILED (postgres): restored ledger is empty; nothing to verify")
	}
	fmt.Fprintf(w, "DR drill PASSED (postgres) — restored %d ledger events; chain + per-event signatures + checkpoints all verified\n", restoredEvents)
	fmt.Fprintf(w, "measured RTO (postgres: restore + boot + verify): %s\n", rto.Round(time.Millisecond))
	fmt.Fprintln(w, "(the empty target database was provisioned beforehand, as olivares db init does; that time is not in the RTO)")
	return nil
}
