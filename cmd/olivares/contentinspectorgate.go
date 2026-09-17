// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strconv"
	"time"

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

// inspectionSubject is the ONLY thing that differs between two governed callers of the
// same inspector: what the published observations are ABOUT. Messages names the Anthropic
// inference surface; the D01-C2B governed Chat operation names its own execution subject
// and its profile's provider and surface. Everything else — the input shape, the ordering,
// the detail-hash preimage, the meter arithmetic, the approval behavior — is identical by
// construction, because there is now one implementation of it.
type inspectionSubject struct {
	Kind        string
	ProviderRef string
	Surface     sdkmodel.Gateway
}

// contentInspectionService is the shared invocation seam. It is NOT a policy: it decides
// nothing, it cannot turn a deny into an allow, and a nil inspector or empty content is a
// no-op that returns the connector's clean-pass zero decision.
//
// ⛔ IT IS SHARED SO THE SECOND CALLER CANNOT DRIFT, NOT TO SAVE LINES. A Chat path that
// re-implemented "inspect, publish findings, meter, open the held approval" would be one
// forgotten step away from inspecting content and never recording that it did — and the
// step most easily forgotten is the metering, which is the billable quantity. The
// alternative that looks cheaper — faking a Messages request so the existing method could
// be reused — would have put a synthetic MessageRequest through a path that authenticates
// and normalizes real ones, which is a much worse trade than one struct.
type contentInspectionService struct {
	inspector contentInspector
	publish   func(context.Context, model.TenantID, sdkmodel.Observation)
	approve   func(context.Context, model.TenantID, string, string,
		*claudeapi.ContentInspectionApprovalIntent)
	clock func() time.Time
}

// Inspect runs one direction's collected content through the firewall, publishes its
// findings and its metering, opens an approval for a held detection, and returns the
// verdict unchanged. The ordering is the caller-visible contract: findings and meter are
// emitted for EVERY verdict, including a deny, so a blocked request is still accounted.
func (s contentInspectionService) Inspect(
	ctx context.Context, subject inspectionSubject,
	tenant model.TenantID, actor, actorRef string, unbindableAgent bool,
	direction, modelRef string, content claudeapi.CollectedContent,
) claudeapi.ContentInspectionDecision {
	if s.inspector == nil || len(content.Channels) == 0 {
		return claudeapi.ContentInspectionDecision{Forward: true}
	}
	dec := s.inspector.Inspect(ctx, claudeapi.ContentInspectionInput{
		Tenant: tenant.String(), ActorRef: actorRef, UnbindableAgent: unbindableAgent, Direction: direction,
		Model: modelRef, Channels: content.Channels, Unscanned: content.Unscanned,
	})
	s.publishInspectionFindings(ctx, subject, tenant, modelRef, direction, dec.Findings)
	s.emitInspectionMeter(ctx, subject, tenant, modelRef, direction, dec.Meter)
	if (direction == claudeapi.InspectDirectionRequest && !dec.Forward) ||
		(direction == claudeapi.InspectDirectionResponse && dec.Block) {
		if s.approve != nil {
			s.approve(ctx, tenant, actor, direction, dec.ApprovalIntent)
		}
	}
	return dec
}

// runContentInspector is the Messages wrapper, unchanged in signature and behavior: it
// names the Anthropic inference subject, this proxy's configured surface and the existing
// approval callback, and delegates. It is still the single entry point both Authorize
// (request) and Finalize (response) call.
func (d *inferenceProxyDecider) runContentInspector(ctx context.Context, tenant model.TenantID, actor, direction, modelRef string, collected claudeapi.CollectedContent, actorRef string, unbindableAgent bool) claudeapi.ContentInspectionDecision {
	return d.contentInspection().Inspect(ctx,
		inspectionSubject{Kind: "anthropic.inference", ProviderRef: "anthropic", Surface: d.surface},
		tenant, actor, actorRef, unbindableAgent, direction, modelRef, collected)
}

// contentInspection binds this decider's inspector, bus and approval bridge into the
// shared service. The method values are the decider's own, so a nil bus still no-ops
// exactly as it did and the approval bridge is the same instance.
func (d *inferenceProxyDecider) contentInspection() contentInspectionService {
	return contentInspectionService{
		inspector: d.inspector, publish: d.publish,
		approve: d.openInspectionApproval, clock: d.clock,
	}
}

// publishInspectionFindings turns the firewall's posture/forensic findings into bus
// FindingReports. Minimal data: the service hashes Detail into DetailHash; no prompt or
// matched value is stored. A nil bus (publish) makes this a no-op.
func (s contentInspectionService) publishInspectionFindings(ctx context.Context, subject inspectionSubject, tenant model.TenantID, modelRef, direction string, fs []claudeapi.ContentInspectionFinding) {
	if s.publish == nil {
		return
	}
	for _, f := range fs {
		s.publish(ctx, tenant, sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "content_firewall"),
			Severity:    inspectionSeverity(f.Severity),
			SubjectKind: subject.Kind,
			SubjectRef:  firstNonEmpty(f.Detector, modelRef),
			Title:       f.Title,
			DetailHash:  hexSHA(modelRef + "|" + direction + "|" + f.Channel + "|" + f.Detail),
			OccurredAt:  s.clock().UTC(),
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
//
// ⛔ THIS UNPRICED METER IS NOT A FREE MODEL CALL. Its zero CostMicroUSD says the INSPECTION
// add-on has no price applied here; it says nothing about what the model cost. Reading it as
// a zero-cost inference sample would be exactly the fabricated monetary zero this slice
// refuses to emit anywhere.
func (s contentInspectionService) emitInspectionMeter(ctx context.Context, subject inspectionSubject, tenant model.TenantID, modelRef, direction string, m claudeapi.ContentInspectionMeter) {
	if m.Inspections <= 0 || s.publish == nil {
		return
	}
	s.publish(ctx, tenant, sdkmodel.CostSample{
		ProviderRef:  subject.ProviderRef,
		ModelRef:     modelRef,
		SessionRef:   "",
		CostType:     "content_inspection",
		CostMicroUSD: 0, // unpriced here; the metered unit is the sample count
		Gateway:      subject.Surface,
		Provenance:   sdkmodel.ProvenanceEstimated,
		OccurredAt:   s.clock().UTC(),
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
