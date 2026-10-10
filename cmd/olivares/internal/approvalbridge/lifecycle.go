// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// --- LifecycleGate adapter over the bridge -------------------------------

// LifecycleGate returns the module's LifecycleGate backed by this bridge, or nil
// when no bridge is configured (the module keeps its deny-closed default).
func (b *Bridge) LifecycleGate() governance.LifecycleGate {
	if b == nil {
		return nil
	}
	return lifecycleGateAdapter{b: b}
}

var _ governance.LifecycleGate = lifecycleGateAdapter{}

type lifecycleGateAdapter struct{ b *Bridge }

// Authorize opens (or idempotently reuses) the governed approval for one NHI
// actuation and maps the bridge's neutral status onto the module's gate vocabulary.
// AllowBreakGlass selects GateOnce (emergency path permitted, e.g. an urgent
// rotation) vs GateOnceNoBreakGlass (irreversible finalize — no emergency skips the
// second human, the erase-gate precedent). The CRITICAL two-person floor is the
// engine's, inherited by every consumer; the bridge sends no required_approvals.
func (a lifecycleGateAdapter) Authorize(ctx context.Context, tenant model.TenantID, req governance.LifecycleGateRequest) (governance.LifecycleGateDecision, error) {
	var (
		ref, status, boundHash string
		err                    error
	)
	if req.AllowBreakGlass {
		ref, status, boundHash, err = a.b.GateOnce(ctx, tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, req.Reason, req.RequestedBy)
	} else {
		ref, status, boundHash, err = a.b.GateOnceNoBreakGlass(ctx, tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, req.Reason, req.RequestedBy)
	}
	if err != nil {
		return governance.LifecycleGateDecision{}, err
	}
	return governance.LifecycleGateDecision{
		Status: lifecycleGateStatus(status), ApprovalRef: ref, PlanHash: boundHash,
	}, nil
}

// lifecycleGateStatus maps the bridge's neutral status onto the module's exported
// gate vocabulary. Anything unexpected is a no_gate (deny-closed).
func lifecycleGateStatus(neutral string) string {
	switch neutral {
	case Approved:
		return governance.GateStatusApproved
	case BreakGlass:
		return governance.GateStatusBreakGlass
	case Pending:
		return governance.GateStatusPending
	case Rejected, Canceled:
		return governance.GateStatusRejected
	case Expired:
		return governance.GateStatusExpired
	default: // NoGate / unknown
		return governance.GateStatusNoGate
	}
}
