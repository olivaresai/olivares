// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package listprice prices token usage at the declared list prices of the
// release-embedded genai-prices dataset (connectors/internal/pricing). It is the
// door for callers outside connectors: the composition root uses it to price an
// operated session's turn when the tool reported tokens and no money.
package listprice

import (
	"github.com/olivaresai/olivares/connectors/internal/pricing"
	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// CostMicroUSD is u at provider's list price for model, in micro-USD. ok is false
// when the dataset has no price for that provider and model: never a guess, never
// a zero that would read as free.
func CostMicroUSD(provider, model string, u modelprovider.Usage) (int64, bool) {
	p, ok := pricing.For(provider, model)
	if !ok {
		return 0, false
	}
	return p.DeriveCostMicroUSD(u), true
}
