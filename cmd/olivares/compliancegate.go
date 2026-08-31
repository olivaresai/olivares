// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
)

// compliancegate.go wires the records-management dual-control seam
// (compliance.ApprovalGate) to the approval bridge, for the module's two
// DANGEROUS verbs: "compliance.retention.enable" (turning a purge schedule on)
// and "compliance.hold.release" (lifting a legal preservation order).
//
// The flow: Authorize opens (or idempotently finds, reusing an approved grant
// within its time-box — the module re-checks the gate on every PUT/release
// attempt, like the hooks PEP) a governed approval bound to the exact
// PlanHash the approvers see (anti-TOCTOU: the canonical schedule/hold string
// the module hashes — a changed scope is a new question). hold.release is in
// the default CRITICAL set (modules/governance/risktier.go), so the engine
// floors its threshold at TWO distinct human approvers; retention.enable is
// HIGH by default (≥1 approver + SoD), raisable per tenant by approval policy.
// On an approved status the adapter reads the approval's immutable decision
// trail and returns the distinct approving principals as GateDecision.Approvers
// — the quorum evidence the module independently re-verifies (≥2 for release,
// ≥1 for enable; a gate that reports approved without it is DENIED, defense in
// depth, the erase-gate pattern).
//
// NO BREAK-GLASS PATH, deliberately, for BOTH verbs: enabling destruction and
// releasing a preservation are irreversible-direction operations — there is no
// emergency that justifies skipping the human quorum (contract §4/§6; the
// erasegate.go precedent). The adapter only ever calls gateOnceNoBreakGlass,
// and even if an emergency grant leaked through, a break-glass authorization
// carries no approvers, so the module's own quorum re-check could never pass.
// Deny-closed twice.
//
// An unconfigured tenant denies exactly like the module's own denyApprovalGate
// (gate_no_gate + "no-gate:<hash>"): an ungoverned deployment over-preserves,
// never silently destroys.

// complianceGate returns the compliance.ApprovalGate adapter over this bridge
// (wired by buildModules when the bridge exists; without it the module keeps
// its deny-closed denyApprovalGate default).
func (b *approvalBridge) complianceGate() compliance.ApprovalGate {
	return complianceApprovalAdapter{b: b}
}

var _ compliance.ApprovalGate = complianceApprovalAdapter{}

type complianceApprovalAdapter struct{ b *approvalBridge }

// Authorize opens/finds the governed approval for this exact schedule/hold and
// reports the decision with its quorum evidence. The returned PlanHash is the
// hash the approval is BOUND to (read from the bridge, never echoed from the
// caller), so the module's plan-hash match denies a re-scoped, un-approved
// change. Deny-closed at every edge: any bridge error surfaces to the module
// (which answers 500 and persists nothing enabled), and the zero GateDecision
// already reads as a deny.
func (a complianceApprovalAdapter) Authorize(ctx context.Context, tenant model.TenantID, req compliance.GateRequest) (compliance.GateDecision, error) {
	ref, status, boundHash, err := a.b.gateOnceNoBreakGlass(ctx, tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, req.Reason, req.RequestedBy)
	if err != nil {
		return compliance.GateDecision{}, err
	}
	dec := compliance.GateDecision{Status: complianceGateStatus(status), ApprovalRef: ref, PlanHash: boundHash}
	if status == nbApproved {
		// The quorum evidence, read from the approval's immutable decision trail: the
		// credentials for provenance and the distinct PEOPLE the module's quorum
		// re-check counts, plus any approval it could not attribute to a person. A read
		// failure degrades to zero of all three — the module's quorum re-check then
		// denies (deny-closed, never a fabricated approver).
		if cred, ok := a.b.cred(tenant); ok {
			ev := a.b.approvalApproverEvidence(ctx, cred, ref)
			dec.Approvers, dec.ApproverPersons, dec.UnattributedApprovals = ev.Actors, ev.Persons, ev.Unattributed
		}
	}
	return dec, nil
}

// complianceGateStatus maps the bridge's neutral status onto the compliance
// module's gate vocabulary; every non-approved value is a deny. nbBreakGlass is
// unreachable here (this gate never consults the emergency path) but maps to
// pending defensively — NEVER to approved.
func complianceGateStatus(neutral string) string {
	switch neutral {
	case nbApproved:
		return compliance.GateStatusApproved
	case nbPending, nbBreakGlass:
		return compliance.GateStatusPending
	case nbRejected, nbCanceled:
		return compliance.GateStatusRejected
	case nbExpired:
		return compliance.GateStatusExpired
	default:
		return compliance.GateStatusNoGate
	}
}
