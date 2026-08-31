// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING AROUND THE INFERENCE PATH — the honest answer to
// "usage -> CostSample" for a provider with NO public aggregate usage/billing API.
// Cohere returns per-request token counts in chat-completions responses, but exposes no
// endpoint to QUERY usage after the fact, so a read-only batch source cannot pull it.
// Instead the exported Meter helper lets the caller that DROVE the inference call (a
// gateway/runtime/proxy) price the completed call's token usage from the declared list
// pricing into a model.CostSample on the canonical "cost.sampled" path
// (provenance=estimated, CostType="cohere").
package cohere

import (
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one Cohere inference call's token usage into a CostSample, deriving the
// monetary amount from the declared list pricing (Source=PricingList -> provenance is
// ESTIMATED, never billed: Cohere has no public billing API to confirm against). promptT
// and completionT are the token counts the caller read from the response. at is the call
// time (zero -> the connector clock). The returned bool reports whether a declared price
// was found, so a caller can distinguish a priced sample from an unpriced (cost-0) record
// for an uncataloged model — never a guessed price.
func (s *Source) Meter(modelName string, promptT, completionT int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	u := modelprovider.Usage{
		ProviderRef:  modelprovider.ProviderCohere,
		ModelRef:     modelName,
		InputTokens:  maxInt64(promptT, 0),
		OutputTokens: maxInt64(completionT, 0),
		OccurredAt:   at,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceEstimated,
		CostType:     costTypeCohere,
	}
	f, ok := familyFor(modelName)
	if !ok {
		// Uncataloged model: record the usage with an underived (0) cost rather than
		// guess a price (ARCHITECTURE.md).
		return modelprovider.ToCostSampleWithCost(u, 0), false
	}
	return modelprovider.ToCostSample(u, f.pricing), true
}

// maxInt64 clamps a token count to be non-negative (a provider should never report a
// negative count, but a defensive clamp keeps a malformed value from corrupting cost).
func maxInt64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}
