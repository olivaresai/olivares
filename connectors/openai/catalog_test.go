// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestPricingForGPT56Family(t *testing.T) {
	cases := []struct {
		id                            string
		wantInput, wantOutput         float64
		wantCacheWrite, wantCacheRead float64
	}{
		{"gpt-5.6-sol", 5.00, 30.00, 6.25, 0.50},
		{"gpt-5.6", 5.00, 30.00, 6.25, 0.50},
		{"gpt-5.6-terra", 2.50, 15.00, 3.125, 0.25},
		{"gpt-5.6-luna", 1.00, 6.00, 1.25, 0.10},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p, _, contextWindow, maxOutput, ok := pricingFor(tc.id)
			if !ok {
				t.Fatalf("pricingFor(%q) did not match", tc.id)
			}
			if p.InputPerMTokUSD != tc.wantInput || p.OutputPerMTokUSD != tc.wantOutput ||
				p.CacheWritePerMTokUSD != tc.wantCacheWrite || p.CacheReadPerMTokUSD != tc.wantCacheRead {
				t.Fatalf("pricingFor(%q) = %+v, want input/output/cache-write/cache-read %v/%v/%v/%v",
					tc.id, p, tc.wantInput, tc.wantOutput, tc.wantCacheWrite, tc.wantCacheRead)
			}
			if p.AsOf != "2026-07-15" {
				t.Fatalf("pricingFor(%q) AsOf = %q, want 2026-07-15", tc.id, p.AsOf)
			}
			if contextWindow != 1050000 || maxOutput != 128000 {
				t.Fatalf("pricingFor(%q) context/max output = %d/%d, want 1050000/128000",
					tc.id, contextWindow, maxOutput)
			}
		})
	}
}

func TestModelDeprecationsExactMatchOnly(t *testing.T) {
	deprecated := buildModel(modelprovider.ProviderOpenAI, "gpt-4-turbo", "gpt-4-turbo")
	if !deprecated.Deprecated || len(deprecated.Retirements) != 1 {
		t.Fatalf("exact gpt-4-turbo deprecation not applied: %+v", deprecated)
	}
	if deprecated.Retirements[0].Surface != model.GatewayDirect {
		t.Fatalf("surface = %q, want direct", deprecated.Retirements[0].Surface)
	}

	dated := buildModel(modelprovider.ProviderOpenAI, "gpt-4-turbo-2024-04-09", "gpt-4-turbo-2024-04-09")
	if dated.Deprecated || len(dated.Retirements) != 0 {
		t.Fatalf("dated gpt-4-turbo variant must not inherit exact-id schedule: %+v", dated)
	}
}
