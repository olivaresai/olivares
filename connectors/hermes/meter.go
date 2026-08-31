// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING AROUND THE INFERENCE PATH — the "claw tax" helper
// for Hermes. Hermes has no usage API; cost is metered around the model call by the
// operator's gateway/proxy/wrapper. The exported Meter function prices completed token
// usage from declared Claude list pricing when the provider is Anthropic and returns a
// zero-cost estimated sample for other providers.
package hermes

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/pricing"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one Hermes inference call's token usage into a CostSample. The provider
// comes from model.provider learned during Gather; a provider/model modelID also works as
// an explicit override. The returned bool reports whether a declared price was found.
func (s *Source) Meter(modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	providerRef, modelRef := s.splitMeterModel(modelID)
	u := modelprovider.Usage{
		ProviderRef:     providerRef,
		ModelRef:        modelRef,
		InputTokens:     clamp(inputTokens),
		OutputTokens:    clamp(outputTokens),
		CacheReadTokens: clamp(cacheReadTokens),
		OccurredAt:      at,
		Gateway:         model.GatewayDirect,
		Provenance:      model.ProvenanceEstimated,
		CostType:        CostType,
	}
	if providerRef != modelprovider.ProviderAnthropic {
		return modelprovider.ToCostSampleWithCost(u, 0), false
	}
	p, ok := pricing.ClaudePricingFor(modelRef)
	if !ok {
		return modelprovider.ToCostSampleWithCost(u, 0), false
	}
	return modelprovider.ToCostSample(u, p), true
}

func (s *Source) splitMeterModel(modelID string) (string, string) {
	modelID = strings.TrimSpace(modelID)
	if provider, modelRef, ok := strings.Cut(modelID, "/"); ok {
		provider = strings.TrimSpace(strings.ToLower(provider))
		modelRef = strings.TrimSpace(modelRef)
		if provider != "" && modelRef != "" {
			return provider, modelRef
		}
	}
	provider := strings.TrimSpace(strings.ToLower(s.meterProvider))
	if provider == "" {
		provider = modelprovider.ProviderAnthropic
	}
	return provider, modelID
}

func clamp(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
