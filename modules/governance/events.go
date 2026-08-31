// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// events.go publishes the governance lifecycle events the eventing
// platform exposes to external subscribers: approval.requested when a pending
// approval is opened, approval.resolved when a pending approval reaches a
// terminal outcome, and policy.changed when a governance policy (kind
// abac/approval) mutates. They are emitted AFTER the store transaction commits
// (the module convention — a rolled-back mutation never signals) and are
// best-effort: a publish failure is debug-logged, never surfaced to the caller,
// because the durable delivery guarantee lives downstream in the eventing
// module's capture, not at this seam.
//
// Scope (deliberate, documented for catalog): policy.changed covers the
// core Policy entity only. The Cedar/OPA PDP revision publish
// (pdp_authoring.go) and the Claude managed-policy revisions (claudepolicy.go,
// a separate console module that holds no bus host) are policy-adjacent
// surfaces with different identifiers (engine/surface + revision, not a policy
// id); exposing them is a future, additive event type — not a silent overload
// of this payload.
//
// Minimal data (docs/SECURITY-HARDENING.md): approval payloads carry identifiers and decision
// parameters only — neither the requester's free-text reason, nor decision
// notes, nor the subject reference. The policy payload mirrors its audit Meta
// ({kind, enabled}) plus id and op.

// emitApprovalRequested publishes approval.requested for a just-created pending
// approval. Call it post-commit with the DTO the create handler built.
func (m *Module) emitApprovalRequested(ctx context.Context, tenant model.TenantID, out approvalDTO) {
	if m.host == nil {
		return
	}
	a := event.ApprovalRequest{
		ApprovalID:        out.ID,
		Action:            out.Action,
		SubjectKind:       out.SubjectKind,
		RiskTier:          out.RiskTier,
		RequiredApprovals: out.RequiredApprovals,
		PolicyRef:         out.PolicyRef,
	}
	if ts, err := model.ParseTimestamp(out.ExpiresAt); err == nil && out.ExpiresAt != "" {
		a.ExpiresAt = ts.Time()
	}
	if ts, err := model.ParseTimestamp(out.EscalateAt); err == nil && out.EscalateAt != "" {
		a.EscalateAt = ts.Time()
	}
	e := event.ApprovalRequested(tenant.String(), Name, m.clock.Now().Time(), a)
	if err := m.host.Publish(ctx, e); err != nil {
		m.debugf("governance: emit approval.requested failed", "err", err)
	}
}

// emitApprovalResolved publishes approval.resolved for a committed terminal
// approval transition. Call it post-commit with the DTO produced by the
// resolver.
func (m *Module) emitApprovalResolved(ctx context.Context, tenant model.TenantID, out approvalDTO) {
	if m.host == nil {
		return
	}
	a := event.ApprovalResolution{
		ApprovalID:        out.ID,
		Action:            out.Action,
		SubjectKind:       out.SubjectKind,
		RiskTier:          out.RiskTier,
		Outcome:           out.Status,
		RequiredApprovals: out.RequiredApprovals,
		ApproveCount:      out.ApproveCount,
		RejectCount:       out.RejectCount,
		PolicyRef:         out.PolicyRef,
	}
	if ts, err := model.ParseTimestamp(out.DecidedAt); err == nil && out.DecidedAt != "" {
		a.DecidedAt = ts.Time()
	}
	e := event.ApprovalResolved(tenant.String(), Name, m.clock.Now().Time(), a)
	if err := m.host.Publish(ctx, e); err != nil {
		m.debugf("governance: emit approval.resolved failed", "err", err)
	}
}

// emitPolicyChanged publishes policy.changed for a committed policy mutation.
// Call it post-commit, after the ABAC cache invalidation.
func (m *Module) emitPolicyChanged(ctx context.Context, tenant model.TenantID, id model.ID, kind, op string, enabled bool) {
	if m.host == nil {
		return
	}
	e := event.PolicyChanged(tenant.String(), Name, m.clock.Now().Time(), event.PolicyChange{
		PolicyID: id.String(),
		Kind:     kind,
		Op:       op,
		Enabled:  enabled,
	})
	if err := m.host.Publish(ctx, e); err != nil {
		m.debugf("governance: emit policy.changed failed", "err", err)
	}
}
