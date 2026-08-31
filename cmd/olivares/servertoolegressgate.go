// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// servertoolegressgate.go is the AGPL composition-root glue for the OPTIONAL commercial
// server-tool egress gate (enterprise/servertoolegress, P0 #1 — Claude's biggest
// unenforced egress hole). It defines the seam the inference-proxy decider consumes and
// translates the gate's verdict into the decider's existing primitives: a proxy deny, a
// rewritten req.Tools, a published posture finding, and (2026-06-19, D4) an OPENED
// governed approval via the existing bridge.
//
// The default AGPL build injects a nil gate (wire_noenterprise.go), so this glue is inert
// and the inline proxy keeps its prior observe-only behavior — NO rug-pull. Under
// `-tags enterprise` with an egress config, wire_enterprise.go injects the real gate
// (servertoolegress.Gate), the ONLY edge by which the AGPL root references the commercial
// module (build-tag-gated, like newFederation).
//
// HONESTY: this is verified-deployed enforcement AT THE PROXY only. A caller who points
// ANTHROPIC_BASE_URL elsewhere, or whose traffic never transits this proxy, evades it
// (modules/inferenceproxy/doc.go). The gate can only DENY or REWRITE tools; the decider
// runs it AFTER the deny-closed security gates and BEFORE the fail-open budget gate, and a
// Forward verdict never skips the remaining chain.

// serverToolEgressGate is the narrow seam the decider depends on (Go structurally satisfies
// it; *servertoolegress.Gate implements it under -tags enterprise).
type serverToolEgressGate interface {
	GovernEgress(ctx context.Context, in claudeapi.ServerToolEgressInput) claudeapi.ServerToolEgressDecision
}

// publishEgressFindings turns the gate's posture/forensic findings into bus FindingReports.
// Minimal data: the decider hashes Detail into DetailHash; no prompt or domain value is
// stored. A nil bus (d.publish) makes this a no-op.
func (d *inferenceProxyDecider) publishEgressFindings(ctx context.Context, tenant model.TenantID, modelRef string, fs []claudeapi.ServerToolEgressFinding) {
	for _, f := range fs {
		sev := sdkmodel.SeverityInfo
		if f.Severity == "high" {
			sev = sdkmodel.SeverityHigh
		}
		d.publish(ctx, tenant, sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "servertool_egress"),
			Severity:    sev,
			SubjectKind: "anthropic.server_tool",
			SubjectRef:  firstNonEmpty(f.ToolType, f.Family),
			Title:       f.Title,
			DetailHash:  hexSHA(modelRef + "|" + f.Detail),
			OccurredAt:  d.clock().UTC(),
			OWASPLLM:    f.OWASPLLM,
		})
	}
}

// openEgressApproval opens (or idempotently reuses) a governed approval for a denied egress
// via the existing bridge (D4), so a future HITL plane can grant it. Best-effort +
// nil-safe: no bridge configured ⇒ the deny stands with its HIGH finding only. The proxy is
// synchronous, so this NEVER resumes the call and never changes the deny — it only emits the
// intent (gateOnce also publishes approval.requested on the existing rail).
func (d *inferenceProxyDecider) openEgressApproval(ctx context.Context, tenant model.TenantID, actor string, intent *claudeapi.ServerToolEgressApprovalIntent) {
	if d.approvals == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if _, _, _, err := d.approvals.gateOnce(ctx, tenant, intent.Action, "anthropic.server_tool", intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: server-tool egress approval intent could not be opened (deny stands)", "err", err)
	}
}
