// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"database/sql"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

var errPolicySnapshotEnabled = errors.New("policy snapshot: invalid enabled")

// snapshotReader returns a value copy of the tenant-pinned generic repository
// whose get and List scan policy version and enabled through
// policySnapshotScanTargets. The shared repository, and with it ordinary typed
// CRUD, keeps its established scan targets.
func (r *policyRepo) snapshotReader() *genericRepo {
	g := *r.g
	g.scanTargets = policySnapshotScanTargets
	return &g
}

// policySnapshotScanTargets returns a separate destination slice in which the
// nonnullable version and enabled targets are wrapped by scanners that report
// only a constant field error. Every other entry is the scanState's own target,
// and the wrappers write into the original targets, so scanState.record stays
// the owner of the scanned row.
func policySnapshotScanTargets(st *scanState) []any {
	dests := make([]any, len(st.dests))
	copy(dests, st.dests)
	for i, col := range st.cols {
		switch col {
		case model.ColVersion:
			if dst, ok := st.dests[i].(*int64); ok {
				dests[i] = &policySnapshotVersionScanner{dst: dst}
			}
		case "enabled":
			if dst, ok := st.dests[i].(*bool); ok {
				dests[i] = &policySnapshotEnabledScanner{dst: dst}
			}
		}
	}
	return dests
}

// policySnapshotVersionScanner performs database/sql's int64 conversion through
// sql.NullInt64. A NULL or unconvertible stored value yields
// errPolicySnapshotVersion without the driver's message, which quotes the value.
type policySnapshotVersionScanner struct{ dst *int64 }

func (s *policySnapshotVersionScanner) Scan(src any) error {
	var version sql.NullInt64
	if err := version.Scan(src); err != nil || !version.Valid {
		return errPolicySnapshotVersion
	}
	*s.dst = version.Int64
	return nil
}

// policySnapshotEnabledScanner is policySnapshotVersionScanner for the bool
// enabled column, through sql.NullBool.
type policySnapshotEnabledScanner struct{ dst *bool }

func (s *policySnapshotEnabledScanner) Scan(src any) error {
	var enabled sql.NullBool
	if err := enabled.Scan(src); err != nil || !enabled.Valid {
		return errPolicySnapshotEnabled
	}
	*s.dst = enabled.Bool
	return nil
}

// policySnapshotScanError replaces a scan failure produced by one of the
// snapshot scalar scanners with its constant field error, dropping the column
// context database/sql wraps around it. Every other error, including query,
// driver and cancellation errors, passes through unchanged.
func policySnapshotScanError(err error) error {
	switch {
	case errors.Is(err, errPolicySnapshotVersion):
		return errPolicySnapshotVersion
	case errors.Is(err, errPolicySnapshotEnabled):
		return errPolicySnapshotEnabled
	default:
		return err
	}
}
