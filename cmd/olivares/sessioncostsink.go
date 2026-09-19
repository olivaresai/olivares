// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sessioncostsink.go is the composition-root adapter that puts what a GOVERNED
// SESSION cost onto the same estate cost bus the in-process inference client
// already feeds.
//
// ⛔ WHY IT EXISTS, MEASURED 2026-09-18. A
// real governed turn answered, the driver reported `total_cost_usd` and the full
// token usage on its own result frame, and `finops spend summary` over the same
// window returned `samples 0`. The producers of cost.sampled were the three
// adapters around the in-process inference client (claude_inference.go,
// modelsactuate.go, recording.go) and NOTHING in the sessions plane: a FinOps
// product could not price the sessions it governs. This adapter is the missing
// producer, and it is a composition-root adapter for the usual reason — the
// module owns no observation vocabulary and must not import one.
type sessionCostSink struct {
	sink runtimeObservationSink
	log  *slog.Logger
}

var _ sessions.SessionCostSink = (*sessionCostSink)(nil)

// PublishSessionCost maps one governed turn's metering onto the estate's cost
// sample and publishes it. The mapping is deliberate field by field:
//
//   - CostType "session_usage": these samples are the cost of an OPERATED
//     session, which is a different subject from a routed inference call, and the
//     FinOps reader can separate them without guessing from the model name.
//   - Provenance ESTIMATED, always, and that is not a hedge. `billed` is reserved
//     for a figure the provider's own cost API reported and finance can reconcile
//     against an invoice; an official CLI's `total_cost_usd` is the client's own
//     arithmetic. Labelling it billed would present a client-side number as the
//     invoice, which is the one thing this vocabulary exists to prevent.
//   - Gateway is left empty (consumers read that as direct): a governed session
//     runs the vendor's own CLI against the vendor's own endpoint.
func (s *sessionCostSink) PublishSessionCost(
	ctx context.Context, tenant model.TenantID, sample sessions.SessionCostSample,
) error {
	if s == nil || s.sink == nil {
		return nil
	}
	cs := sdkmodel.CostSample{
		ProviderRef:           sample.ProviderRef,
		ModelRef:              sample.ModelRef,
		SessionRef:            sample.SessionRef,
		InputTokens:           sample.InputTokens,
		OutputTokens:          sample.OutputTokens,
		CostMicroUSD:          sample.CostMicroUSD,
		OccurredAt:            sample.OccurredAt,
		CacheReadTokens:       sample.CacheReadTokens,
		CacheCreation5mTokens: sample.CacheCreationTokens,
		WorkspaceRef:          sample.WorkspaceRef,
		Actor:                 sample.AgentRef,
		Provenance:            sdkmodel.ProvenanceEstimated,
		CostType:              "session_usage",
	}
	if cs.SessionRef == "" {
		// The canonical session identity is what a FinOps reader joins on; when
		// admission gave the launch none, the run reference is the only stable name
		// this turn has, and naming it is better than posting an unattributable row.
		cs.SessionRef = sample.RunRef
	}
	return s.sink.PublishCostSample(ctx, tenant.String(), cs)
}
