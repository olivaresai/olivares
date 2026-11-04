// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// A folder's records class is a legal hold plus a retention period (retention_days).
// Both protect the bytes of the folder: an edit that destroys them (overwriting a
// file, moving or deleting an entry) is refused with 423, and creating a file is not
// an edit of that kind.

// holdSubjectFolder is the legal-hold subject kind that names a registered folder;
// its ref is the folder's workspace_ref (docs/contracts open vocabulary).
const holdSubjectFolder = "workspace"

// maxRetentionDays bounds retention_days like the retention schedules of the
// compliance module (1 to 36500).
const maxRetentionDays = 36500

// HoldGate says whether an active legal hold covers one subject. The composition
// root adapts the compliance module's CheckHold; modules never import each other.
// An error means the hold cannot be ruled out, and the edit is refused.
type HoldGate interface {
	Check(ctx context.Context, tenant model.TenantID, subjectKind, subjectRef string) (held bool, err error)
}

// WithHoldGate wires the legal-hold gate. Without it no folder is ever held.
func WithHoldGate(g HoldGate) Option {
	return func(m *Module) { m.holdGate = g }
}

// guardRecords refuses an edit that would destroy the bytes at the given absolute
// paths while the folder is under a legal hold or inside its retention period. A
// path that does not exist yet has nothing to destroy. The hold gate calls into
// another module, so the edit has not started when it is asked.
func (m *Module) guardRecords(ctx context.Context, tenant model.TenantID, ws *resolvedWorkspace, targets ...string) error {
	var existing []string
	for _, abs := range targets {
		if pathExists(abs) {
			existing = append(existing, abs)
		}
	}
	if len(existing) == 0 {
		return nil
	}
	if m.holdGate != nil {
		held, err := m.holdGate.Check(ctx, tenant, holdSubjectFolder, ws.ref)
		if err != nil {
			m.warnf("sessions: legal-hold check failed; refusing the folder edit (fail closed)", "err", redactErr(err))
			return &codedRunErr{&runErr{http.StatusServiceUnavailable, "legal-hold check unavailable; the edit is refused"}, "legal_hold_unavailable"}
		}
		if held {
			return &codedRunErr{&runErr{http.StatusLocked, "this folder is under a legal hold; its files cannot be overwritten, moved or deleted"}, "legal_hold"}
		}
	}
	if ws.retentionDays <= 0 {
		return nil
	}
	period := time.Duration(ws.retentionDays) * 24 * time.Hour
	cutoff := m.clock.Now().Time().Add(-period)
	for _, abs := range existing {
		changed, err := lastChangeAfter(abs, cutoff)
		if err != nil {
			m.warnf("sessions: retention period check failed; refusing the folder edit (fail closed)", "err", redactErr(err))
			return &codedRunErr{&runErr{http.StatusServiceUnavailable, "retention period check failed; the edit is refused"}, "retention_period"}
		}
		if !changed.IsZero() {
			return &codedRunErr{&runErr{http.StatusLocked, fmt.Sprintf("this folder keeps its files for %d days after their last change; an entry here is kept until %s",
				ws.retentionDays, changed.Add(period).UTC().Format(time.DateOnly))}, "retention_period"}
		}
	}
	return nil
}

// lastChangeAfter returns the modification time of the first entry under abs (abs
// included, symbolic links not followed) changed after cutoff, or the zero time.
func lastChangeAfter(abs string, cutoff time.Time) (time.Time, error) {
	var found time.Time
	err := filepath.WalkDir(abs, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			found = info.ModTime()
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}
