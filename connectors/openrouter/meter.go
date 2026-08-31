// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST + POLICY-DRIFT metering around the inference path. Unlike
// providers with no billing API (deepseek/mistral), OpenRouter reports the ACTUAL cost
// of each generation in its response (usage.cost when usage accounting is requested, or
// GET /api/v1/generation total_cost), so MeterCall records that authoritative BILLED
// figure rather than deriving an estimate. It also returns the model's policy verdict so
// the caller that drove the call (a gateway / the inline inference PEP) can gate or
// alert on a denied/unapproved model at the point of use — the honest per-call drift.
package openrouter

import (
	"math"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// MeterCall records one OpenRouter generation's actual cost into a CostSample and
// returns the model's policy verdict. modelID is the OpenRouter model id (e.g.
// "anthropic/claude-sonnet-4"); actor is the caller identity for per-user attribution
// (e.g. "user:alice"), empty if unknown; inputTokens/outputTokens are the reported token
// counts; costUSD is the cost OpenRouter reported for the call (a billed amount — 0 when
// unknown, never guessed); at is the call time (zero => connector clock).
func (s *Source) MeterCall(modelID, actor string, inputTokens, outputTokens int64, costUSD float64, at time.Time) (model.CostSample, PolicyVerdict) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	u := modelprovider.Usage{
		ProviderRef:  modelprovider.ProviderOpenRouter,
		ModelRef:     modelID,
		InputTokens:  clampNonNeg(inputTokens),
		OutputTokens: clampNonNeg(outputTokens),
		OccurredAt:   at,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceBilled,
		CostType:     costTypeOpenRouter,
	}
	if actor != "" {
		u.Actor = actor
	}
	costMicro := int64(0)
	if costUSD > 0 {
		costMicro = int64(math.Round(costUSD * 1e6))
	}
	return modelprovider.ToCostSampleWithCost(u, costMicro), s.policy.evaluate(modelID)
}

func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
