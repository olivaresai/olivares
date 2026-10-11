// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
	"github.com/spf13/cobra"
)

type drRestoreStatus struct {
	ControlPresent     *bool              `json:"control_present"`
	State              string             `json:"state"`
	OperationID        string             `json:"operation_id"`
	Revision           *int64             `json:"revision"`
	PlanSHA256         string             `json:"plan_sha256"`
	Destination        opgate.Destination `json:"destination"`
	LocalWitness       string             `json:"local_witness"`
	CustodyMatch       *bool              `json:"custody_match"`
	PublicationAllowed *bool              `json:"publication_allowed"`
	ReasonCode         string             `json:"reason_code"`
}

func drRestoreStatusCmd() *cobra.Command {
	var sf drFlags
	cmd := &cobra.Command{
		Use: "restore-status", Short: "Read the destination's restore controls without opening the engine",
		Long: "Read durable restore controls without loading signing keys or migrating the store.\n" +
			"An unenrolled destination has no verified restore completion. This observation\n" +
			"does not authorize a later boot or prove that the recorded signing keys are available.",
		Example: "  olivares dr restore-status --data-dir /var/lib/olivares -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			s, code := readDRRestoreStatus(ctx, sf)
			if err := renderStatusOut(cmd, s); err != nil {
				return err
			}
			if code != exitcode.OK {
				drNote(cmd, "Restore state does not permit startup; inspect the reported controls before recovery.")
				return exitcode.New(code, nil)
			}
			return nil
		},
	}
	addDRStoreFlags(cmd, &sf)
	return cmd
}

func readDRRestoreStatus(ctx context.Context, sf drFlags) (s drRestoreStatus, code int) {
	s = drRestoreStatus{State: "unknown", LocalWitness: "unknown", ReasonCode: "restore_state_unknown"}
	unknown := func(reason string) (drRestoreStatus, int) {
		s.State, s.ControlPresent, s.PublicationAllowed = "unknown", nil, nil
		s.Revision, s.CustodyMatch = nil, nil
		s.ReasonCode = reason
		return s, exitcode.Usage
	}
	refuse := func(reason string) (drRestoreStatus, int) {
		allowed := false
		s.PublicationAllowed = &allowed
		s.ReasonCode = reason
		return s, exitcode.Err
	}
	if sf.engineKind != string(store.EngineSQLite) && sf.engineKind != string(store.EnginePostgres) {
		return unknown("unsupported_engine")
	}
	var err error
	sf.dataDir, err = resolveDataDir(sf.dataDir)
	if err != nil || sf.resolveDSNRefs(ctx) != nil {
		return unknown("invalid_destination")
	}
	var anchors []opgate.Anchor
	dir, exists, err := opgate.AnchorForDataDir(sf.dataDir)
	if err != nil {
		return unknown("local_control_unreadable")
	}
	if exists {
		anchors = append(anchors, dir)
	}
	s.Destination.Engine = sf.engineKind
	if sf.engineKind == string(store.EngineSQLite) {
		if sf.dsn == "" {
			sf.dsn = filepath.Join(sf.dataDir, "olivares.db")
		}
		target, err := opgate.ResolveSQLiteTarget(sf.dsn)
		if err != nil || target.Memory() {
			return unknown("invalid_destination")
		}
		s.Destination.SQLiteFile = target.CanonicalPath()
		s.Destination.CanonicalPath = target.CanonicalPath()
		anchors = append(anchors, target.Anchor())
	} else if sf.dsn == "" {
		return unknown("invalid_destination")
	}
	// A status read must not provision locks. An absent lock and record is an
	// unenrolled observation; a record without its stable lock is unreadable.
	var locked []opgate.Anchor
	for _, a := range anchors {
		info, err := os.Lstat(a.LockPath())
		if errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(a.RecordPath()); !errors.Is(err, os.ErrNotExist) {
				return unknown("local_control_lock_missing")
			}
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return unknown("local_control_unreadable")
		}
		locked = append(locked, a)
	}
	var local *opgate.Record
	s.LocalWitness = "absent"
	if len(locked) > 0 {
		lease, held, err := opgate.TryAcquire(opgate.ModeShared, locked...)
		if err != nil || !held {
			return unknown("restore_coordination_unavailable")
		}
		defer func() {
			if err := lease.Release(); err != nil {
				s, code = unknown("restore_coordination_unavailable")
			}
		}()
		for _, a := range locked {
			rec, present, err := lease.Read(a)
			if err != nil {
				s.LocalWitness = "unreadable"
				return unknown("local_control_unreadable")
			}
			if !present {
				continue
			}
			s.LocalWitness = rec.State
			if rec.Destination.Engine != sf.engineKind ||
				(sf.engineKind == string(store.EngineSQLite) && rec.Destination.SQLiteFile != s.Destination.SQLiteFile) {
				return refuse("local_destination_mismatch")
			}
			if local != nil && (local.OpID != rec.OpID || local.PlanSHA256 != rec.PlanSHA256 || local.State != rec.State || local.Keyset.SHA256 != rec.Keyset.SHA256) {
				return refuse("local_controls_disagree")
			}
			local = &rec
		}
	}
	present := local != nil
	s.ControlPresent = &present
	if local != nil {
		s.State, s.OperationID, s.Revision, s.PlanSHA256 = local.State, local.OpID, &local.Revision, local.PlanSHA256
	}
	if sf.engineKind == string(store.EnginePostgres) {
		report, err := coreengine.ReadRestoreControl(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: sf.dsn, OwnerDSN: sf.ownerDSN, AdminDSN: sf.adminDSN,
		})
		if err != nil {
			s.ControlPresent = nil
			return unknown("database_control_unreadable")
		}
		s.Destination.Database, s.Destination.Schema, s.Destination.SystemIdentifier = report.Database, report.Schema, report.SystemIdentifier
		s.ControlPresent = &report.Present
		s.State, s.OperationID, s.PlanSHA256 = report.State, report.OpID, report.PlanSHA256
		s.Revision = nil
		if report.Present {
			s.Revision = &report.Revision
		}
		if local != nil {
			if local.Destination.Postgres() != s.Destination.Postgres() {
				return refuse("local_destination_mismatch")
			}
			if !report.Present {
				return refuse("enrolled_control_missing")
			}
			match := local.Keyset.SHA256 == report.KeysetSHA256
			if local.State == opgate.StateComplete && report.State == opgate.StateComplete {
				s.CustodyMatch = &match
			}
			if local.OpID != report.OpID || local.PlanSHA256 != report.PlanSHA256 || local.State != report.State || !match {
				return refuse("database_and_local_control_disagree")
			}
		}
	}
	if *s.ControlPresent && s.State != opgate.StateComplete {
		return refuse("restore_incomplete")
	}
	allowed := true
	s.PublicationAllowed = &allowed
	s.ReasonCode = "complete_control_custody_unchecked"
	if !*s.ControlPresent {
		s.State, s.ReasonCode = "legacy_or_lost_unknown", "unenrolled_legacy_or_lost_unknown"
	}
	return s, exitcode.OK
}
