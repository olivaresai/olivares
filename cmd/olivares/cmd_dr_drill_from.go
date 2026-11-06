// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// drDrillAction is the audit action that seals a drill report in the ledger.
const drDrillAction = "dr.drill"

// drDrillReport is what `dr drill --from` measured: the restore of a real bundle
// into a throwaway directory, against the operator's targets. LedgerSeq is the
// sequence of the system-tenant audit event that seals the same facts (0 with
// --no-record); the event is signed and checkpointed like every ledger event, so
// the report is verified with the ledger.
type drDrillReport struct {
	Bundle           string   `json:"bundle"`
	BundleSHA256     string   `json:"bundle_sha256"`
	Engine           string   `json:"engine"`
	TakenAt          string   `json:"taken_at"`
	DrilledAt        string   `json:"drilled_at"`
	Network          string   `json:"network"`
	ChainOK          bool     `json:"chain_ok"`
	Tenants          int      `json:"tenants"`
	Events           int64    `json:"events"`
	RPOSeconds       int64    `json:"rpo_seconds"`
	RPOTargetSeconds int64    `json:"rpo_target_seconds"`
	RPOExceeded      bool     `json:"rpo_exceeded"`
	RTOMillis        int64    `json:"rto_ms"`
	RTOTargetMillis  int64    `json:"rto_target_ms"`
	RTOExceeded      bool     `json:"rto_exceeded"`
	LedgerSeq        int64    `json:"ledger_seq"`
	Problems         []string `json:"problems,omitempty"`
}

type drDrillFromOpts struct {
	from       string
	kf         kekFlags
	sf         drFlags
	rtoTarget  time.Duration
	rpoTarget  time.Duration
	noRecord   bool
	scratchDSN string
}

// drillFromBundle is `dr drill --from <bundle>`: it restores a real bundle into a
// throwaway directory, with no engine and no network (only the snapshot is read),
// re-verifies the whole ledger, measures the RTO and the RPO the bundle gives
// right now, and seals the result in the audit ledger of this estate.
func drillFromBundle(cmd *cobra.Command, o drDrillFromOpts) error {
	ctx := cmd.Context()
	if o.scratchDSN != "" {
		return fmt.Errorf("--from restores a SQLite bundle into a throwaway directory; --scratch-dsn drills the synthetic ledger on PostgreSQL, so use one or the other")
	}
	work, err := os.MkdirTemp("", "olivares-dr-drill-from-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// RTO = extract + authenticate + decrypt + copy + open + full ledger
	// re-verification, the same path `dr verify` and a restore take.
	t0 := time.Now()
	m, cipher, err := openVerifiedBundle(o.from, work, o.kf, false)
	if err != nil {
		return err
	}
	if m.EngineKind != string(store.EngineSQLite) || m.Store.Method == dr.MethodPITR {
		return fmt.Errorf("--from drills SQLite bundles; for a %s bundle use `dr verify --scratch-dsn`, which restores into a throwaway database", m.EngineKind)
	}
	rep, err := verifySQLiteBundle(ctx, work, m, cipher)
	if err != nil {
		return err
	}
	rto := time.Since(t0)

	takenAt, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("bundle manifest created_at %q is not RFC3339: %w", m.CreatedAt, err)
	}
	sum, _, err := dr.FileSHA256(o.from)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	report := drDrillReport{
		Bundle: filepath.Base(o.from), BundleSHA256: sum, Engine: m.EngineKind,
		TakenAt: m.CreatedAt, DrilledAt: now.Format(time.RFC3339), Network: "none",
		ChainOK: rep.OK, Tenants: len(rep.Tenants), Problems: rep.Problems,
		RPOSeconds: int64(now.Sub(takenAt) / time.Second), RPOTargetSeconds: int64(o.rpoTarget / time.Second),
		RTOMillis: rto.Milliseconds(), RTOTargetMillis: o.rtoTarget.Milliseconds(),
	}
	for _, tv := range rep.Tenants {
		report.Events += tv.RestoredSeq
	}
	report.RPOExceeded = o.rpoTarget > 0 && now.Sub(takenAt) > o.rpoTarget
	report.RTOExceeded = o.rtoTarget > 0 && rto > o.rtoTarget

	var sealErr error
	if !o.noRecord {
		seq, err := recordDrill(ctx, o.sf, report)
		if seq == 0 {
			return fmt.Errorf("the drill ran but its report could not be sealed in the ledger (use --no-record for a dry run): %w", err)
		}
		// The event is in the ledger even when its checkpoint failed: report its
		// sequence, so a retry is not mistaken for the first drill.
		report.LedgerSeq, sealErr = seq, err
	}
	if err := renderReportOut(cmd, report); err != nil {
		return fmt.Errorf("DR drill could not be reported: %w", err)
	}
	switch {
	case !report.ChainOK:
		return fmt.Errorf("DR drill FAILED: bundle does not restore to a continuity-safe ledger: %v", report.Problems)
	case sealErr != nil:
		return fmt.Errorf("DR drill report is in the ledger but not checkpointed: %w", sealErr)
	case report.RTOExceeded || report.RPOExceeded:
		var missed []string
		if report.RTOExceeded {
			missed = append(missed, fmt.Sprintf("rto %dms (target %dms)", report.RTOMillis, report.RTOTargetMillis))
		}
		if report.RPOExceeded {
			missed = append(missed, fmt.Sprintf("rpo %ds (target %ds)", report.RPOSeconds, report.RPOTargetSeconds))
		}
		return exitcode.New(exitcode.Degraded, fmt.Errorf("DR drill restored, but missed a target: %s", strings.Join(missed, ", ")))
	}
	return nil
}

// recordDrill seals the report as one audit event in the system tenant of the
// estate that ran the drill, then checkpoints so a signed checkpoint covers it.
// It runs after the isolated restore has finished: the restore itself touches
// neither this estate nor the network. A non-zero seq with an error means the
// event is in the ledger but its checkpoint failed.
func recordDrill(ctx context.Context, sf drFlags, r drDrillReport) (int64, error) {
	dataDir, err := resolveDataDir(sf.dataDir)
	if err != nil {
		return 0, err
	}
	sf.dataDir = dataDir
	if err := sf.resolveDSNRefs(ctx); err != nil {
		return 0, err
	}
	eng, err := drBoot(ctx, sf)
	if err != nil {
		return 0, err
	}
	defer func() { _ = eng.Close() }()
	var seq int64
	err = eng.store.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "dr drill", ActorKind: "system", Action: drDrillAction,
			Meta: map[string]any{
				"bundle": r.Bundle, "bundle_sha256": r.BundleSHA256, "taken_at": r.TakenAt, "drilled_at": r.DrilledAt,
				"chain_ok": r.ChainOK, "events": r.Events, "network": r.Network,
				"rpo_seconds": r.RPOSeconds, "rpo_target_seconds": r.RPOTargetSeconds, "rpo_exceeded": r.RPOExceeded,
				"rto_ms": r.RTOMillis, "rto_target_ms": r.RTOTargetMillis, "rto_exceeded": r.RTOExceeded,
			},
		})
		seq = ev.Seq
		return err
	})
	if err != nil {
		return 0, err
	}
	if seq == 0 {
		return 0, fmt.Errorf("the ledger dropped the event under its degrade policy")
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		return seq, fmt.Errorf("sealed at seq %d but not checkpointed: %w", seq, err)
	}
	return seq, nil
}
