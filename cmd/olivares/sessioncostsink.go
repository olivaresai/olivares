// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sessionCostSink maps governed turn usage to the owning session observation
// port. That port updates the managed row and publishes the canonical cost event
// once; a separate unmarked publication would create a legacy duplicate.
type sessionCostSink struct {
	credentials *auth.SessionCredentials
	sessions    *sessions.Module
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
	if s == nil || s.credentials == nil || s.sessions == nil {
		return errors.New("managed session cost publication is unavailable")
	}
	principal, scope, err := s.credentials.ResolveRun(ctx, tenant, sample.RunRef)
	if err != nil {
		return err
	}
	if scope.TenantID != tenant || scope.RunRef != sample.RunRef || sample.SessionRef != "" && sample.SessionRef != scope.SessionRef {
		return auth.ErrUnauthenticated
	}
	cs := sdkmodel.CostSample{
		ProviderRef:           sample.ProviderRef,
		ModelRef:              sample.ModelRef,
		SessionRef:            scope.SessionRef,
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
	return s.sessions.RecordSessionObservation(ctx, principal, cs)
}
