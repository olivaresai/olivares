// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strconv"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// contentinspectorgate.go is the AGPL composition-root glue for the OPTIONAL commercial
// content firewall (enterprise/contentfirewall, P1 — the deep inline inspection the PEP and
// the core DLP do not do). It defines the seam the inference-proxy decider consumes
// (contentInspector) and translates the firewall's verdict into the decider's existing
// primitives: a proxy deny / a withheld response, published posture findings, a per-inspection
// metering CostSample on the observation sink, and an OPENED governed approval via the
// existing bridge.
//
// The default AGPL build injects a nil inspector (wire_noenterprise.go), so this glue is
// inert: the inline proxy keeps its prior behavior, the core's text DLP and the (extended,
// build-independent) deny-closed unscanned posture keep working — NO rug-pull. Under
// `-tags enterprise` with a firewall config, wire_enterprise.go injects the real inspector
// (contentfirewall.Inspector), composed by cmd, never imported by the AGPL decider.
//
// HONESTY: verified-deployed inspection AT THE PROXY only. A caller who points
// ANTHROPIC_BASE_URL elsewhere, or whose traffic never transits this proxy, evades it
// (modules/inferenceproxy/doc.go). The firewall runs AFTER the deny-closed gates and BEFORE
// the fail-open budget gate on a request, and a clean verdict never skips the remaining
// chain. On a response a block is preventive only in buffer mode (the existing split).

// contentInspector is the narrow seam the decider depends on (Go structurally satisfies it;
// *contentfirewall.Inspector implements it under -tags enterprise).
type contentInspector interface {
	Inspect(ctx context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision
}

// runContentInspector inspects one direction's collected content, publishes the firewall's
// findings + metering, opens an approval for a held detection, and returns the decision. A
// nil inspector or empty content is a no-op (zero decision, Forward true). It is the single
// entry point both Authorize (request) and Finalize (response) call.
func (d *inferenceProxyDecider) runContentInspector(ctx context.Context, tenant model.TenantID, actor, direction, modelRef string, collected claudeapi.CollectedContent, actorRef string, unbindableAgent bool) claudeapi.ContentInspectionDecision {
	if d.inspector == nil || len(collected.Channels) == 0 {
		return claudeapi.ContentInspectionDecision{Forward: true}
	}
	dec := d.inspector.Inspect(ctx, claudeapi.ContentInspectionInput{
		Tenant: tenant.String(), ActorRef: actorRef, UnbindableAgent: unbindableAgent, Direction: direction,
		Model: modelRef, Channels: collected.Channels, Unscanned: collected.Unscanned,
	})
	d.publishInspectionFindings(ctx, tenant, modelRef, direction, dec.Findings)
	d.emitInspectionMeter(ctx, tenant, modelRef, direction, dec.Meter)
	if (direction == claudeapi.InspectDirectionRequest && !dec.Forward) ||
		(direction == claudeapi.InspectDirectionResponse && dec.Block) {
		d.openInspectionApproval(ctx, tenant, actor, direction, dec.ApprovalIntent)
	}
	return dec
}

// publishInspectionFindings turns the firewall's posture/forensic findings into bus
// FindingReports. Minimal data: the decider hashes Detail into DetailHash; no prompt or
// matched value is stored. A nil bus (d.publish) makes this a no-op.
func (d *inferenceProxyDecider) publishInspectionFindings(ctx context.Context, tenant model.TenantID, modelRef, direction string, fs []claudeapi.ContentInspectionFinding) {
	for _, f := range fs {
		d.publish(ctx, tenant, sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "content_firewall"),
			Severity:    inspectionSeverity(f.Severity),
			SubjectKind: "anthropic.inference",
			SubjectRef:  firstNonEmpty(f.Detector, modelRef),
			Title:       f.Title,
			DetailHash:  hexSHA(modelRef + "|" + direction + "|" + f.Channel + "|" + f.Detail),
			OccurredAt:  d.clock().UTC(),
			OWASPLLM:    f.OWASPLLM,
			OWASPASI:    f.OWASPASI,
		})
	}
}

// emitInspectionMeter publishes the per-inspection metering as a CostSample (the billable
// add-on quantity). CostType "content_inspection" tags it; the COUNT of these samples is the
// metered unit (per-inspection's default). CostMicroUSD is 0 — the price is applied by
// the billing system downstream, NEVER fabricated here (the per-channel/per-byte volume rides
// in the meter for a future pricing model). A zero-Inspections meter (nothing inspected) emits
// nothing. nil bus ⇒ no-op.
func (d *inferenceProxyDecider) emitInspectionMeter(ctx context.Context, tenant model.TenantID, modelRef, direction string, m claudeapi.ContentInspectionMeter) {
	if m.Inspections <= 0 {
		return
	}
	d.publish(ctx, tenant, sdkmodel.CostSample{
		ProviderRef:  "anthropic",
		ModelRef:     modelRef,
		SessionRef:   "",
		CostType:     "content_inspection",
		CostMicroUSD: 0, // unpriced here; the metered unit is the sample count
		Gateway:      d.surface,
		Provenance:   sdkmodel.ProvenanceEstimated,
		OccurredAt:   d.clock().UTC(),
		Labels: map[string]string{
			"direction": direction,
			"channels":  strconv.Itoa(m.Channels),
			"detectors": strconv.Itoa(m.Detectors),
		},
	})
}

// openInspectionApproval opens (or idempotently reuses) a governed approval for a held (hitl)
// detection via the existing bridge, so a future HITL plane can release it. Best-effort +
// nil-safe: no bridge or no intent ⇒ the deny/block stands with its finding only. The proxy
// is synchronous, so this NEVER resumes the call and never changes the verdict.
func (d *inferenceProxyDecider) openInspectionApproval(ctx context.Context, tenant model.TenantID, actor, direction string, intent *claudeapi.ContentInspectionApprovalIntent) {
	if d.approvals == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if _, _, _, err := d.approvals.gateOnce(ctx, tenant, intent.Action, "anthropic.inference", intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: content-firewall approval intent could not be opened (verdict stands)", "err", err, "direction", direction)
	}
}

// inspectionSeverity maps the firewall's severity string onto the SDK severity scale.
func inspectionSeverity(s string) sdkmodel.Severity {
	switch s {
	case "high":
		return sdkmodel.SeverityHigh
	case "medium":
		return sdkmodel.SeverityMedium
	default:
		return sdkmodel.SeverityInfo
	}
}
