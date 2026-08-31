// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fal

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// catalog.go holds the declared fal.ai model catalog (a representative subset of the
// 600+ models) + per-OUTPUT pricing, and the Snapshot that exposes it (and the key
// inventory) to module X through CatalogProvider.
//
// fal bills PAY-PER-OUTPUT (per image / megapixel / second / audio-minute), NOT per
// token — so the catalog deliberately carries NO token-based modelprovider.ModelPricing
// (it would be a category error); cost is metered from the per-output table here by the
// Meter helper. Prices are declared list values to VERIFY against fal.ai/pricing
// (stamped AsOf), never fabricated telemetry. The model list is a documented SUBSET:
// fal's full registry (600+) is browsable but has no verified public list API, so the
// connector ships a representative, operator-extensible set rather than a fake 600.

// falPricingAsOf stamps the declared per-output prices.
const falPricingAsOf = "2026-06-01"

// Billable unit kinds fal uses (the CostType a metered sample carries).
const (
	unitSecond    = "second"       // compute-second (the queue's inference_time signal)
	unitImage     = "image"        // per generated image
	unitMegapixel = "megapixel"    // per output megapixel
	unitAudioMin  = "audio_minute" // per minute of audio (e.g. transcription/TTS)
	unitVideoSec  = "video_second" // per second of generated video
)

// falModel is one declared fal model: its billable unit + per-unit USD price (list,
// VERIFY). perUnitUSD is 0 for a model whose price is not declared (cost stays 0).
type falModel struct {
	ref         string
	displayName string
	unit        string
	perUnitUSD  float64
	capability  modelprovider.Capability // the closest cross-vendor capability flag, if any
}

// falModels is the representative declared catalog (subset of 600+). Prices are list,
// AsOf falPricingAsOf — VERIFY against fal.ai/pricing.
var falModels = []falModel{
	{"fal-ai/flux/schnell", "FLUX.1 [schnell]", unitImage, 0.003, modelprovider.CapVision},
	{"fal-ai/flux/dev", "FLUX.1 [dev]", unitMegapixel, 0.025, modelprovider.CapVision},
	{"fal-ai/flux-pro/v1.1", "FLUX1.1 [pro]", unitImage, 0.040, modelprovider.CapVision},
	{"fal-ai/stable-diffusion-v35-large", "Stable Diffusion 3.5 Large", unitImage, 0.065, modelprovider.CapVision},
	{"fal-ai/fast-sdxl", "Fast SDXL", unitSecond, 0.005, modelprovider.CapVision},
	{"fal-ai/whisper", "Whisper (transcription)", unitAudioMin, 0.0035, ""},
	{"fal-ai/wan-t2v", "Wan text-to-video", unitVideoSec, 0.080, ""},
}

// falPricingFor returns the billable unit + per-unit USD price for a model id, matched
// by longest declared prefix (so a versioned id inherits its family). ok is false when
// no declared model matches.
func falPricingFor(modelID string) (string, float64, bool) {
	best := -1
	for i, m := range falModels {
		if hasPrefix(modelID, m.ref) {
			if best < 0 || len(m.ref) > len(falModels[best].ref) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", 0, false
	}
	return falModels[best].unit, falModels[best].perUnitUSD, true
}

// hasPrefix is strings.HasPrefix without importing strings into this file.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// declaredCatalogModels builds the offline model list. Each carries the provider ref
// and (where one applies) a cross-vendor capability flag; Pricing stays nil — fal is
// not token-billed, so a token ModelPricing would misrepresent it. CapabilitySource is
// "declared".
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(falModels))
	for _, m := range falModels {
		mdl := modelprovider.Model{
			ProviderRef:      modelprovider.ProviderFal,
			Ref:              m.ref,
			DisplayName:      m.displayName,
			CapabilitySource: "declared",
		}
		if m.capability != "" {
			mdl.Capabilities = []modelprovider.Capability{m.capability}
		}
		out = append(out, mdl)
	}
	return out
}

// Snapshot returns the fal catalog: the declared model subset (always) and, with a
// credential and manage_keys, the API-key inventory (metadata only — the masked
// partial, never the secret). On an unavailable key-management surface the key
// inventory is empty (honest degrade), never an error.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderFal, Kind: modelprovider.KindHostedAPI,
			Title: "fal.ai", BaseURL: s.queueBaseURL,
		},
		Models:     declaredCatalogModels(),
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.keysClient == nil || !s.manageKeys {
		return cat, nil
	}
	keys, err := s.keyInventory(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Keys = keys
	return cat, nil
}
