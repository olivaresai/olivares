// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// meter.go implements COST METERING AROUND THE INFERENCE PATH — the honest answer to
// "usage → CostSample" for a provider with NO public aggregate usage/billing API (the
// same situation as connectors/fal). Mistral returns per-request token counts in the
// chat-completions `usage` object (prompt_tokens / completion_tokens), but exposes no
// endpoint to QUERY usage after the fact, so a read-only batch source cannot pull it.
// Instead the exported Meter helper lets the caller that DROVE the inference call (a
// gateway/runtime/proxy — e.g. the inline inference PEP) price the completed call's
// token usage from the declared list pricing into a model.CostSample on the canonical
// "cost.sampled" path (provenance=estimated, CostType="mistral").
package mistral

import (
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Meter prices one Mistral inference call's token usage into a CostSample, deriving the
// monetary amount from the declared list pricing (Source=PricingList → provenance is
// ESTIMATED, never billed: Mistral has no public billing API to confirm against). promptT
// and completionT are the `usage.prompt_tokens` / `usage.completion_tokens` the caller
// read from the response. at is the call time (zero → the connector clock). The returned
// bool reports whether a declared price was found, so a caller can distinguish a priced
// sample from an unpriced (cost-0) record for an uncataloged model — never a guessed price.
func (s *Source) Meter(modelID string, promptT, completionT int64, at time.Time) (model.CostSample, bool) {
	if at.IsZero() {
		at = s.clock().UTC()
	}
	u := modelprovider.Usage{
		ProviderRef:  modelprovider.ProviderMistral,
		ModelRef:     modelID,
		InputTokens:  maxInt64(promptT, 0),
		OutputTokens: maxInt64(completionT, 0),
		OccurredAt:   at,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceEstimated,
		CostType:     costTypeMistral,
	}
	f, ok := familyFor(modelID)
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
