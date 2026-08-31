// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements cost metering around the inference path. GLM exposes no public
// aggregate usage, billing or balance API, so a read-only batch Gather cannot pull
// spend. The exported Meter helper lets the caller that drove an inference call price
// its token counts from declared USD list pricing into a model.CostSample
// (provenance=estimated, CostType="glm").
package glm

import (
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one GLM inference call's token usage into a CostSample, deriving the
// monetary amount from declared list pricing. inputTokens and outputTokens are the
// ordinary prompt/completion token counts the caller read from the response.
// cacheReadTokens are prompt-cache hit tokens, priced at CacheReadPerMTokUSD. at is
// the call time (zero -> the connector clock). The returned bool reports whether a
// verified USD price was found; unpriced rows are returned with cost 0 and ok=false,
// never a guessed price.
func (s *Source) Meter(modelID string, inputTokens, outputTokens, cacheReadTokens int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	u := modelprovider.Usage{
		ProviderRef:     modelprovider.ProviderGLM,
		ModelRef:        modelID,
		InputTokens:     maxInt64(inputTokens, 0),
		OutputTokens:    maxInt64(outputTokens, 0),
		CacheReadTokens: maxInt64(cacheReadTokens, 0),
		OccurredAt:      at,
		Gateway:         model.GatewayDirect,
		Provenance:      model.ProvenanceEstimated,
		CostType:        costTypeGLM,
	}
	f, ok := familyFor(modelID)
	if !ok {
		return modelprovider.ToCostSampleWithCost(u, 0), false
	}
	return modelprovider.ToCostSample(u, *f.pricing), true
}

// maxInt64 clamps a token count to be non-negative.
func maxInt64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
