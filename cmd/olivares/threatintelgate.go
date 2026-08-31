// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/olivaresai/olivares/connectors/threatfeed"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// threatintelgate.go is the AGPL composition-root glue for the OPTIONAL commercial
// AI threat-intel add-on (enterprise/threatintel). It defines the seam the
// CLI and the bus subscription depend on (threatIntelSource), and binds the
// add-on's PURE additive producers to the event bus: it turns curated feed content
// into ADDITIVE findings on the bus and never touches the open engine's decisions.
//
// The default AGPL build injects a nil source (wire_noenterprise.go), so this glue
// is inert: no threat-intel subscription, the engine behaves exactly as before —
// NO rug-pull. Under `-tags enterprise` with a config, wire_enterprise.go injects
// the real *threatintel.Source (composed by cmd, never imported by an open module).
//
// ADDITIVE-ONLY (anti-poisoning). Every path here only PUBLISHES new findings; it
// can never clear, lower or de-flag an open-engine finding. The handler re-consumes
// finding.reported (for MCP-reputation enrichment) but the source skips this
// add-on's own findings and non-MCP subjects, so there is no feedback loop and a
// poisoned (but signed) feed can at most RAISE findings, never rescore down.

// threatIntelEventSource is the Event.Source label for findings this add-on emits.
const threatIntelEventSource = "module:threatintel"

// threatIntelSource is the narrow seam the composition root depends on.
// *enterprise/threatintel.Source satisfies it structurally under -tags enterprise;
// the default build supplies nil. It references only Apache types (threatfeed /
// sdkmodel) so it is nameable across the license boundary.
type threatIntelSource interface {
	// Admin (CLI): verify/apply/pull/sign and the status summary.
	Verify(blob []byte, now time.Time) (threatfeed.FeedStatus, error)
	Apply(blob []byte, now time.Time) (threatfeed.FeedStatus, error)
	Pull(ctx context.Context, now time.Time) (threatfeed.FeedStatus, error)
	Sign(payloadJSON []byte, privKeyB64 string) ([]byte, error)
	Status(now time.Time) threatfeed.FeedStatus
	Crosswalk() threatfeed.Crosswalk
	// StartRefresh starts the optional in-process periodic pull (no-op unless
	// configured); the goroutine stops on ctx cancellation.
	StartRefresh(ctx context.Context)
	// Engine (bus): pure additive producers.
	InspectObserved(surface, text, subjectRef string, now time.Time) []sdkmodel.FindingReport
	ScreenMCPPosture(in sdkmodel.FindingReport, now time.Time) []sdkmodel.FindingReport
	ScreenModelUse(tenant, modelRef string, now time.Time) []sdkmodel.FindingReport
}

// threatIntelPublishBuffer bounds the decoupled publish queue (see
// subscribeThreatIntel). A burst beyond it sheds ADDITIVE findings (logged), never
// blocks the bus drainer.
const threatIntelPublishBuffer = 1024

// subscribeThreatIntel wires the enterprise threat-intel source to the bus. It is
// opt-in and nil-safe: with no enterprise build or no config the source is nil and
// this is a no-op. It listens for:
//   - guardrail.observed → match agentic-attack signatures over redacted text
//   - finding.reported   → enrich MCP-server posture with curated reputation
//   - cost.sampled       → flag use of a deprecated/retired model (deduped/day)
//
// and publishes the resulting findings back on the bus (additive). A subscribe
// error leaves the add-on inactive — never a boot failure.
func subscribeThreatIntel(ctx context.Context, getenv func(string) string, bus eventbus.Bus, log *slog.Logger) {
	src := newThreatIntelSource(getenv, log)
	if src == nil {
		return
	}

	// DECOUPLE publishing from the bus drainer goroutine. The handler subscribes to
	// finding.reported AND emits finding.reported, so a SYNCHRONOUS in-handler
	// bus.Publish to its own (possibly full) subscriber queue would block the sole
	// drainer goroutine on its own channel — a self-deadlock under a finding burst.
	// Instead the handler does a NON-BLOCKING enqueue to a bounded buffer and a
	// dedicated worker calls bus.Publish; the drainer therefore never blocks. A
	// buffer overflow sheds ADDITIVE findings (logged — no silent cap), which is
	// acceptable because the open engine's own findings are unaffected.
	pending := make(chan event.Event, threatIntelPublishBuffer)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-pending:
				if err := bus.Publish(ctx, ev); err != nil && log != nil {
					log.Warn("threatintel: publishing additive finding failed", "err", err)
				}
			}
		}
	}()
	enqueue := func(tenant string, fs []sdkmodel.FindingReport) {
		for _, f := range fs {
			select {
			case pending <- event.FromObservation(tenant, threatIntelEventSource, f):
			default:
				if log != nil {
					log.Warn("threatintel: additive-finding publish buffer full; shedding finding (load)", "kind", f.Kind)
				}
			}
		}
	}

	handler := func(_ context.Context, e event.Event) error {
		now := time.Now().UTC()
		switch e.Type {
		case event.TypeGuardrailObserved:
			if ot, ok := event.ObservedTextOf(e); ok {
				enqueue(e.Tenant, src.InspectObserved(ot.Surface, ot.Text, firstNonEmpty(ot.AgentRef, ot.SessionRef, ot.ResourceRef), now))
			}
		case event.TypeFindingReported:
			if fr, ok := event.FindingOf(e); ok {
				enqueue(e.Tenant, src.ScreenMCPPosture(fr, now))
			}
		case event.TypeCostSampled:
			if cs, ok := event.CostOf(e); ok {
				enqueue(e.Tenant, src.ScreenModelUse(e.Tenant, cs.ModelRef, now))
			}
		}
		return nil
	}
	types := []event.Type{event.TypeGuardrailObserved, event.TypeFindingReported, event.TypeCostSampled}
	if _, err := subscribeClassed(bus, eventbus.ClassTelemetry, "threat-intel", types, handler); err != nil {
		if log != nil {
			log.Warn("threatintel: bus subscription failed; add-on inactive", "err", err)
		}
		return
	}
	src.StartRefresh(ctx) // optional in-process periodic pull (no-op unless configured)
	if log != nil {
		log.Info("threatintel: curated feed engine active (additive findings only)")
	}
}
