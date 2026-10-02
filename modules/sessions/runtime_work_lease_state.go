// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runWorkLeaseState projects only the control posture inside the run's existing
// authorized read scope. The client need not read the private lease surface.
// An unread, mismatched or incomplete binding stays unknown; runtime control
// separately rechecks its generation and liveness under the item lock.
func runWorkLeaseState(ctx context.Context, sc store.Scope, rec model.Record) string {
	if !runHasWorkBinding(rec) {
		return ""
	}
	stamp, err := parseRunWorkStamp(rec, rec.Int(colRunWorkLeaseFence), false)
	if err != nil {
		return "unknown"
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return "unknown"
	}
	item, err := items.Get(ctx, stamp.itemID)
	if err != nil {
		return "unknown"
	}
	workspace, err := model.ParseID(item.String(colWorkWorkspaceID))
	if err != nil {
		return "unknown"
	}
	lease, found, err := findWorkLease(ctx, sc, stamp.itemID)
	if err != nil || !found || lease.String(colWorkWorkspaceID) != workspace.String() {
		return "unknown"
	}
	state, err := workLeaseFenceState(lease)
	if err != nil || state.Fence < stamp.fence {
		return "unknown"
	}
	if state.Lifecycle == fenceActive && state.ExpiresAt.IsZero() {
		return "unknown"
	}
	now, err := observeLeaseClock(ctx, sc, workspace)
	if err != nil {
		return "unknown"
	}
	if fenceIsLive(state, now.Time()) {
		if state.Fence != stamp.fence || lease.String(colLeaseHolderRunRef) != rec.String(colRunRef) || item.Int(colWorkOwnerEpoch) != stamp.ownerEpoch {
			return "unknown"
		}
		return "active"
	}
	return "ended"
}
