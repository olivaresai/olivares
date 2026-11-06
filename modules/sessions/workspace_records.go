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
	"github.com/olivaresai/olivares/core/store"
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
	if err := m.guardFolderRecords(ctx, tenant, ws, existing...); err != nil {
		return err
	}
	// References registered before a policy was set must not bypass it either.
	folders, err := m.recordsFolders(ctx, tenant)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if folder.ref == ws.ref {
			continue
		}
		for _, target := range existing {
			if !pathsOverlap(target, folder.rootReal) {
				continue
			}
			if pathWithin(folder.rootReal, target) {
				target = folder.rootReal
			}
			if err := m.guardFolderRecords(ctx, tenant, folder, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordsFolders reads every registration in this tenant, including disabled
// folders: disabling a reference must not disable preservation of its bytes.
// Complete the view before consulting the hold gate (another module's store).
func (m *Module) recordsFolders(ctx context.Context, tenant model.TenantID) ([]*resolvedWorkspace, error) {
	var folders []*resolvedWorkspace
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		q := model.Query{Limit: 500}
		for {
			rows, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, row := range rows {
				folders = append(folders, &resolvedWorkspace{ref: row.String(colWsRef), rootReal: row.String(colWsRootPath), retentionDays: row.Int(colWsRetentionDays)})
			}
			if !page.HasMore {
				return nil
			}
			if page.Cursor == "" || page.Cursor == q.Cursor {
				return fmt.Errorf("folder records registry pagination did not advance")
			}
			q.Cursor = page.Cursor
		}
	})
	return folders, err
}

// A registration may not grant a fresh reference to preserved bytes, or drop
// their policy. Retention must be explicitly removed before deregistration.
func (m *Module) guardRecordsRegistration(ctx context.Context, tenant model.TenantID, root string) error {
	folders, err := m.recordsFolders(ctx, tenant)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if !pathsOverlap(root, folder.rootReal) {
			continue
		}
		if err := m.guardFolderHold(ctx, tenant, folder.ref); err != nil {
			return err
		}
		if folder.retentionDays > 0 {
			return &codedRunErr{&runErr{http.StatusLocked, "this folder has a retention period; overlapping registrations cannot be changed"}, "retention_period"}
		}
	}
	return nil
}

func (m *Module) guardFolderHold(ctx context.Context, tenant model.TenantID, ref string) error {
	if m.holdGate != nil {
		held, err := m.holdGate.Check(ctx, tenant, holdSubjectFolder, ref)
		if err != nil {
			m.warnf("sessions: legal-hold check failed; refusing the folder edit (fail closed)", "err", redactErr(err))
			return &codedRunErr{&runErr{http.StatusServiceUnavailable, "legal-hold check unavailable; the edit is refused"}, "legal_hold_unavailable"}
		}
		if held {
			return &codedRunErr{&runErr{http.StatusLocked, "this folder is under a legal hold; its files and registration cannot be changed"}, "legal_hold"}
		}
	}
	return nil
}

func (m *Module) guardFolderRecords(ctx context.Context, tenant model.TenantID, ws *resolvedWorkspace, existing ...string) error {
	if err := m.guardFolderHold(ctx, tenant, ws.ref); err != nil {
		return err
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
