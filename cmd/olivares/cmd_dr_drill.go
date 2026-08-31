// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// drDrillCmd is the reproducible DR drill (docs/DR-RUNBOOK.md §8): an
// UNTESTED backup is not a backup. It runs the whole round trip in a throwaway
// scratch directory — seed a signed, checkpointed ledger → back it up → DESTROY the
// live estate → restore into a clean dir → re-verify chain + per-event signatures +
// checkpoints + tip continuity — and reports the MEASURED RTO. It never touches a
// real data dir, so it is safe to wire into CI (a clean container) and to run on a
// cadence. A non-zero exit means the backup does not restore to a continuity-safe
// ledger — a DR incident.
func drDrillCmd() *cobra.Command {
	var events int
	var keepArtifacts bool
	cmd := &cobra.Command{
		Use:   "drill",
		Short: "Full DR round-trip drill (backup→destroy→restore→verify) with a measured RTO",
		Long: "drill proves a backup is actually restorable, end to end, in a disposable\n" +
			"scratch dir: it seeds an ephemeral signed ledger, backs it up, destroys the\n" +
			"estate, restores into a clean dir and re-verifies the full chain — then prints\n" +
			"the measured RTO (restore + boot + verify). It never touches a real data dir,\n" +
			"so it is CI-safe (docs/DR-RUNBOOK.md §8).",
		Example: "  olivares dr drill --events 100",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			w := cmd.OutOrStdout()
			if events <= 0 {
				events = 1
			}

			root, err := os.MkdirTemp("", "olivares-dr-drill-")
			if err != nil {
				return err
			}
			if keepArtifacts {
				fmt.Fprintf(w, "drill artifacts kept in %s\n", root)
			} else {
				defer func() { _ = os.RemoveAll(root) }()
			}
			src := filepath.Join(root, "estate")
			restored := filepath.Join(root, "restored")
			bundle := filepath.Join(root, "drill.drbundle")

			// 1) Seed an ephemeral estate with a real signed, checkpointed ledger.
			seed, err := seedDrillEstate(ctx, src, events)
			if err != nil {
				return fmt.Errorf("drill seed: %w", err)
			}
			fmt.Fprintf(w, "seeded ephemeral estate: %d ledger events, %d tenant(s)\n", seed.businessEvents, seed.tenants)

			// 2) Backup with an ephemeral in-memory passphrase (the bundle is discarded
			// at the end — the drill proves restorability, not key custody).
			pass := make([]byte, 32)
			if _, err := rand.Read(pass); err != nil {
				return err
			}
			cipher, err := dr.NewPassphraseCipher(pass)
			if err != nil {
				return err
			}
			sealed, refs, err := sealSigningKeys(src, cipher)
			if err != nil {
				return err
			}
			backupWork, err := os.MkdirTemp(root, "backup-")
			if err != nil {
				return err
			}
			m, snapshotPath, err := backupSQLite(ctx, drFlags{dataDir: src, engineKind: string(store.EngineSQLite)}, backupWork, refs, "dr drill", time.Now())
			if err != nil {
				return fmt.Errorf("drill backup: %w", err)
			}
			bf, err := os.OpenFile(bundle, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := dr.WriteBundle(bf, dr.BundleInput{Manifest: m, KEK: cipher.Params(), SnapshotPath: snapshotPath, SealedKeys: sealed}); err != nil {
				_ = bf.Close()
				return err
			}
			if err := bf.Close(); err != nil {
				return err
			}
			bundleSize := fileSizeOr0(bundle)
			fmt.Fprintf(w, "backed up: %s (%d bytes)\n", filepath.Base(bundle), bundleSize)

			// 3) DISASTER: obliterate the live estate. Everything now depends on the bundle.
			if err := os.RemoveAll(src); err != nil {
				return fmt.Errorf("drill destroy estate: %w", err)
			}
			fmt.Fprintln(w, "destroyed the live estate (simulated total host loss)")

			// 4) RESTORE + VERIFY — measured. RTO = extract + digest + decrypt + copy +
			// boot + full ledger re-verification, exactly the production restore path.
			t0 := time.Now()
			restoreWork, err := os.MkdirTemp(root, "restore-")
			if err != nil {
				return err
			}
			rm, kek, err := openAndCheckBundle(bundle, restoreWork)
			if err != nil {
				return fmt.Errorf("drill open bundle: %w", err)
			}
			rcipher, err := dr.OpenCipher(pass, kek)
			if err != nil {
				return err
			}
			if err := restoreKeys(restoreWork, restored, rm, rcipher, true); err != nil {
				return err
			}
			if err := dr.CopyFile(filepath.Join(restoreWork, rm.Store.File), filepath.Join(restored, "olivares.db")); err != nil {
				return err
			}
			eng, err := drBoot(ctx, drFlags{dataDir: restored, engineKind: string(store.EngineSQLite)})
			if err != nil {
				return fmt.Errorf("drill boot restored: %w", err)
			}
			cpv, err := eng.signer.CheckpointVerifier(ctx)
			if err != nil {
				_ = eng.Close()
				return err
			}
			rep, err := dr.RestoreVerify(ctx, eng.store, rm, eng.signer.PublicKey(), cpv)
			_ = eng.Close()
			if err != nil {
				return err
			}
			rto := time.Since(t0)

			// 5) Report. TipExact means the restored tips MUST equal the manifest tips,
			// so rep.OK already proves the event counts are byte-identical after restore.
			var restoredEvents int64
			for _, tv := range rep.Tenants {
				restoredEvents += tv.RestoredSeq
			}
			// A drill whose report could not be rendered has not been drilled: the
			// artifact of this command IS the report, and reportDRDrillSuccess below
			// would otherwise announce a passing drill nobody can read.
			if err := printReport(cmd, rep); err != nil {
				return fmt.Errorf("DR drill could not be reported: %w", err)
			}
			if !rep.OK {
				return fmt.Errorf("DR drill FAILED: bundle does not restore to a continuity-safe ledger: %s", strings.Join(rep.Problems, "; "))
			}
			return reportDRDrillSuccess(w, restoredEvents, rto)
		},
	}
	cmd.Flags().IntVar(&events, "events", 500, "ledger events to seed in the ephemeral estate")
	cmd.Flags().BoolVar(&keepArtifacts, "keep-artifacts", false, "keep the scratch dir instead of removing it (debugging)")
	return cmd
}

func reportDRDrillSuccess(w io.Writer, restoredEvents int64, rto time.Duration) error {
	if restoredEvents <= 0 {
		return fmt.Errorf("DR drill FAILED: restored ledger is empty; nothing to verify")
	}
	fmt.Fprintf(w, "DR drill PASSED — restored %d ledger events; chain + per-event signatures + checkpoints + tips all verified\n", restoredEvents)
	fmt.Fprintf(w, "measured RTO (restore + boot + verify): %s\n", rto.Round(time.Millisecond))
	return nil
}

// drillSeed reports what a drill seeded, for the human summary.
type drillSeed struct {
	businessEvents int
	tenants        int
}

// seedDrillEstate boots a fresh engine in dir, creates a tenant, appends events to
// its ledger and checkpoints, then closes cleanly — leaving a data dir (store +
// signing keys) exactly like a live estate a backup would capture.
func seedDrillEstate(ctx context.Context, dir string, events int) (drillSeed, error) {
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: string(store.EngineSQLite), Version: version})
	if err != nil {
		return drillSeed{}, err
	}
	var tid model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "drill", Slug: "drill", Status: model.StatusActive})
		tid = o.TenantID
		return e
	}); err != nil {
		_ = eng.Close()
		return drillSeed{}, fmt.Errorf("create org: %w", err)
	}
	if err := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		for i := 0; i < events; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "drill", ActorKind: "system", Action: "agent.create", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = eng.Close()
		return drillSeed{}, fmt.Errorf("append events: %w", err)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		_ = eng.Close()
		return drillSeed{}, fmt.Errorf("checkpoint: %w", err)
	}
	if err := eng.Close(); err != nil {
		return drillSeed{}, err
	}
	return drillSeed{businessEvents: events, tenants: 1}, nil
}

func fileSizeOr0(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}
