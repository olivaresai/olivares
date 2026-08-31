// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING for OpenHands. OpenHands emits OTEL gen_ai.* spans
// with token usage attributes (gen_ai.usage.input_tokens / output_tokens). The operator's
// OTEL pipeline delivers these to the control-plane collector (ingest). This Meter
// function prices the extracted token usage from declared Claude list pricing into a
// model.CostSample (provenance=estimated, CostType="openhands").
//
// OpenHands has the best OSS OTEL gen_ai.* story: it emits the vendor-neutral gen_ai.*
// semconv profile. Cost arrives via the OTEL pipeline, not a billing API.
package openhands

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/pricing"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one OpenHands inference call's token usage into a CostSample, deriving the
// monetary amount from declared Claude list pricing (provenance=estimated — OpenHands has
// no billing API; cost arrives via OTEL gen_ai.usage.* attributes). The returned bool
// reports whether a declared price was found for the model.
func (s *Source) Meter(modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	u := modelprovider.Usage{
		ProviderRef:     modelprovider.ProviderAnthropic,
		ModelRef:        modelID,
		InputTokens:     clamp(inputTokens),
		OutputTokens:    clamp(outputTokens),
		CacheReadTokens: clamp(cacheReadTokens),
		OccurredAt:      at,
		Gateway:         model.GatewayDirect,
		Provenance:      model.ProvenanceEstimated,
		CostType:        costType,
	}
	p, ok := pricing.ClaudePricingFor(modelID)
	if !ok {
		return modelprovider.ToCostSampleWithCost(u, 0), false
	}
	return modelprovider.ToCostSample(u, p), true
}

func clamp(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
