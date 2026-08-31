// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// claudehookfirewall.go is the AGPL composition-root glue for the OPTIONAL commercial
// hooks-hardening DLP firewall (enterprise/hookhardening — the on-prem half of the
// content firewall applied to the Claude Code HOOKS path). It is the hook-path sibling of
// contentinspectorgate.go (which wires the same kind of inspector into the inline /v1/messages
// proxy): it reduces the unbounded tool_input to the shared content-inspection channels
// (connectors/claude.ExtractToolContent), runs the injected inspector, and translates the
// verdict into the governed PEP's primitives — a tool-call DENY, published posture findings, a
// per-inspection metering CostSample on the bus, and an OPENED governed approval via the
// existing bridge.
//
// The default AGPL build injects a nil inspector (wire_noenterprise.go), so this glue is inert:
// the governed hooks PEP keeps its exact prior behavior (allow/ask/deny disposition, PDP
// overlay, kill-switch, NHI, rewrite) — NO rug-pull. Under `-tags enterprise` with a firewall
// config, wire_enterprise.go injects the real inspector (hookhardening.Inspector), composed by
// cmd, never imported by the AGPL decider.
//
// HONESTY: verified-deployed inspection AT THE HOOK only. A session running without the managed
// PEP hook, or a tool-call that never reaches the PEP, evades it — the same caveat as the inline
// proxy (claudehookpep.go runs the firewall as a further-restrict overlay; a clean verdict never
// widens the disposition).

// hookFirewallSignalSource is the Event.Source label for findings/meter this surface emits.
const hookFirewallSignalSource = "hooks_pep"

// runHookFirewall extracts the tool_input content, inspects it via the commercial firewall,
// publishes its findings + per-inspection meter, opens a governed approval for a held detection,
// and returns the verdict. A nil inspector or empty content is a clean pass (Forward true) — so
// the default AGPL build is fully inert. It is the hook-path analog of
// inferenceProxyDecider.runContentInspector.
func (d *claudeHookDecider) runHookFirewall(ctx context.Context, tenant model.TenantID, actor string, in claude.HookDecisionInput) claudeapi.ContentInspectionDecision {
	if d.hookInspector == nil {
		return claudeapi.ContentInspectionDecision{Forward: true}
	}
	collected := claude.ExtractToolContent(in.Tool, in.RewriteBase())
	// Always consult the inspector when one is wired — even for a content-free tool-call. A
	// MISCONFIG deny-all inspector (set-but-unreadable config) must deny EVERY governed tool-call
	// until fixed, including the many hook calls that carry no inspectable arguments; short-
	// circuiting empty content here would silently exempt them from the fail-closed posture. A
	// healthy inspector returns its own no-op (Forward, zero meter) for empty content, so this
	// costs nothing in the normal case (no spurious finding or billable meter is emitted).
	dec := d.hookInspector.Inspect(ctx, claudeapi.ContentInspectionInput{
		Tenant:    tenant.String(),
		ActorRef:  strings.TrimSpace(in.Identity.Agent),
		Direction: claudeapi.InspectDirectionRequest,
		Channels:  collected.Channels,
		Unscanned: collected.Unscanned,
	})
	d.publishHookInspectionFindings(ctx, tenant, in.Tool, dec.Findings)
	d.emitHookInspectionMeter(ctx, tenant, in.Tool, dec.Meter)
	if !dec.Forward {
		d.openHookInspectionApproval(ctx, tenant, actor, dec.ApprovalIntent)
	}
	return dec
}

// publishHookInspectionFindings turns the firewall's posture findings into bus FindingReports.
// Minimal data: the decider hashes Detail into DetailHash; no argument value is stored. nil bus
// ⇒ no-op.
func (d *claudeHookDecider) publishHookInspectionFindings(ctx context.Context, tenant model.TenantID, tool string, fs []claudeapi.ContentInspectionFinding) {
	for _, f := range fs {
		d.publishHookObs(ctx, tenant, sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "hook_firewall"),
			Severity:    inspectionSeverity(f.Severity),
			SubjectKind: "claude.tool",
			SubjectRef:  firstNonEmpty(f.Detector, tool),
			Title:       f.Title,
			DetailHash:  hexSHA(tool + "|" + f.Channel + "|" + f.Detail),
			OccurredAt:  d.clock().UTC(),
			OWASPLLM:    f.OWASPLLM,
			OWASPASI:    f.OWASPASI,
		})
	}
}

// emitHookInspectionMeter publishes the per-inspection metering as a CostSample (the billable
// add-on quantity). CostType "hook_inspection" tags it; the COUNT of these samples is the
// metered unit. CostMicroUSD is 0 — the price is applied downstream, NEVER fabricated here. A
// zero-Inspections meter emits nothing. nil bus ⇒ no-op.
func (d *claudeHookDecider) emitHookInspectionMeter(ctx context.Context, tenant model.TenantID, tool string, m claudeapi.ContentInspectionMeter) {
	if m.Inspections <= 0 {
		return
	}
	d.publishHookObs(ctx, tenant, sdkmodel.CostSample{
		ProviderRef:  "anthropic",
		CostType:     "hook_inspection",
		CostMicroUSD: 0, // unpriced here; the metered unit is the sample count
		Gateway:      sdkmodel.GatewayDirect,
		Provenance:   sdkmodel.ProvenanceEstimated,
		OccurredAt:   d.clock().UTC(),
		Labels: map[string]string{
			"tool":      tool,
			"channels":  strconv.Itoa(m.Channels),
			"detectors": strconv.Itoa(m.Detectors),
		},
	})
}

// openHookInspectionApproval opens (or idempotently reuses) a governed approval for a held
// (hitl) detection via the existing bridge, so a future HITL plane can release it.
// Best-effort + nil-safe: no bridge or no intent ⇒ the deny stands with its finding only. The
// PEP is synchronous, so this NEVER changes the verdict.
func (d *claudeHookDecider) openHookInspectionApproval(ctx context.Context, tenant model.TenantID, actor string, intent *claudeapi.ContentInspectionApprovalIntent) {
	if d.bridge == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if _, _, _, err := d.bridge.gateOnce(ctx, tenant, intent.Action, "claude.tool", intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("hook-pep: content-firewall approval intent could not be opened (verdict stands)", "err", err)
	}
}

// publishHookObs publishes one observation (finding/meter) on the engine bus. nil bus ⇒ no-op.
func (d *claudeHookDecider) publishHookObs(ctx context.Context, tenant model.TenantID, obs sdkmodel.Observation) {
	if d.bus == nil {
		return
	}
	if err := d.bus.Publish(ctx, event.FromObservation(tenant.String(), hookFirewallSignalSource, obs)); err != nil && d.log != nil {
		d.log.Warn("hook-pep: firewall bus publish failed (best-effort)", "err", err)
	}
}
