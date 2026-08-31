// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING AROUND THE INFERENCE PATH — the "claw tax" helper
// for OpenClaw. OpenClaw has no usage API; cost is metered around the Claude API call
// by the operator's gateway/proxy/wrapper. The exported Meter function prices the
// completed call's token usage from declared Claude list pricing into a model.CostSample
// on the canonical "cost.sampled" path (provenance=estimated, CostType="openclaw").
package openclaw

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/pricing"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one OpenClaw inference call's token usage into a CostSample, deriving the
// monetary amount from declared Claude list pricing (provenance=estimated — OpenClaw has
// no billing API). inputTokens/outputTokens/cacheReadTokens are from the Claude API
// response. at is the call time (zero → connector clock). The returned bool reports
// whether a declared price was found for the model.
func (s *Source) Meter(modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	return s.meter("", modelID, inputTokens, outputTokens, cacheReadTokens, at)
}

// MeterForAgent is Meter with per-agent attribution: the returned CostSample carries
// Actor="agent:<agentRef>" so FinOps allocation (modules/finops) and policies
// can attribute — and cap — the spend to that OpenClaw agent NHI. agentRef is the agent
// subject the connector emits on its config edges (e.g. "openclaw" or "openclaw/research").
//
// HONEST LIMIT: attribution requires a RUNTIME caller (an inference proxy in front of the
// agent) to supply which agent a call belonged to. The connector's config scan cannot know
// that on its own — so absent a proxy this stays an unattributed estimate, and per-agent
// FinOps ceilings on OpenClaw agents are only enforceable once that proxy path is wired.
func (s *Source) MeterForAgent(agentRef, modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	return s.meter(strings.TrimSpace(agentRef), modelID, inputTokens, outputTokens, cacheReadTokens, at)
}

func (s *Source) meter(agentRef, modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	providerRef, modelRef := splitModelID(modelID)
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
	if agentRef != "" {
		u.Actor = "agent:" + agentRef
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

func splitModelID(modelID string) (string, string) {
	modelID = strings.TrimSpace(modelID)
	if provider, modelRef, ok := strings.Cut(modelID, "/"); ok {
		provider = strings.TrimSpace(strings.ToLower(provider))
		modelRef = strings.TrimSpace(modelRef)
		if provider != "" && modelRef != "" {
			return provider, modelRef
		}
	}
	return modelprovider.ProviderAnthropic, modelID
}

func clamp(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
