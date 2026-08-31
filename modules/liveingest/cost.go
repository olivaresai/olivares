// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package liveingest

import (
	"context"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// This file is the in-process producer for the RUNTIME cost/forensic of governed
// Claude calls (CLA-15/ANT2-15). The Claude inference client (connectors/claude-api)
// runs IN-PROCESS for the evals Judge and the models routing-execute path and returns
// a MessageResponse whose token/advisor/thinking/refusal detail the verdict alone
// discards. The connector holds NO sink (it returns the cost/forensic data to the
// caller, by design); only a module with Host.Publish can lift a sealed
// model.Observation onto the bus. So — exactly like the voice probe and the
// guardrail tap — the composition-root adapter computes the observations from the
// response (connectors/claude-api.RuntimeObservations) and hands them here to
// publish. FinOps (module XI onCost) then ingests the cost lines, and the ledger
// records the findings.
//
// Both methods are FAIL-OPEN and nil-safe: with no host (before Init) or on a
// publish hiccup they never fail the originating call. They publish the sealed wire
// types (CostSample/FindingReport) — there is no raw prompt/completion on the bus
// (docs/SECURITY-HARDENING.md); the connector already redacted every finding's detail to a hash.

// PublishCostSample publishes one runtime CostSample as a cost.sampled event so
// FinOps ingests it. The dedup natural key includes CostType and the nanosecond
// instant, so a SEPARATE advisor sub-line (CostType="advisor") and the top-level
// line never collapse, and two genuine calls never double-count (modules/finops/
// ingest.go naturalKey). A sample with no provider AND no model is dropped (nothing
// to attribute).
func (m *Module) PublishCostSample(ctx context.Context, tenant string, cs model.CostSample) error {
	if m.host == nil {
		return nil
	}
	if cs.ProviderRef == "" && cs.ModelRef == "" {
		return nil
	}
	return m.host.Publish(ctx, event.FromObservation(tenant, "module:"+Namespace, cs))
}

// PublishFinding publishes one runtime FindingReport as a finding.reported event
// (the advisor-ran / extended-thinking-billed / programmatic-under-count forensic
// signals, and the cyber/bio refusal security signal). A finding with no Kind is
// dropped.
func (m *Module) PublishFinding(ctx context.Context, tenant string, f model.FindingReport) error {
	if m.host == nil {
		return nil
	}
	if f.Kind == "" {
		return nil
	}
	return m.host.Publish(ctx, event.FromObservation(tenant, "module:"+Namespace, f))
}

// PublishEdge publishes one EdgeObservation as an edge.observed event. It is the
// in-process producer for an edge a governed ACTUATION made visible — concretely an A2A
// delegation that left this plane (the scheduled-fire A2A route): a governed delegation
// is also a COMMUNICATION fact module IV must graph (G14). The connector
// returns the wire-shape EdgeObservation (a connector holds no Host); the composition
// root hands it here, exactly as it does the cost/forensic of a Claude call. Module IV
// then derives the agent↔agent relation from the SAME edge.observed stream it already
// consumes — no new ingest path. FAIL-OPEN and nil-safe: before Init, or on a publish
// hiccup, it never fails the originating fire. An edge with no origin AND no resource is
// dropped (nothing to attribute); minimal data — the connector carries refs only, never
// a payload (docs/SECURITY-HARDENING.md).
func (m *Module) PublishEdge(ctx context.Context, tenant string, e model.EdgeObservation) error {
	if m.host == nil {
		return nil
	}
	if e.OriginRef == "" && e.ResourceRef == "" {
		return nil
	}
	return m.host.Publish(ctx, event.FromObservation(tenant, "module:"+Namespace, e))
}
