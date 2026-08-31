// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds the CLIENT-SIDE minimum-cacheable check (FASE W gap #6): a prompt-cache
// breakpoint (cache_control) below a model's minimum cacheable prefix SILENTLY does
// nothing — the API caches nothing and returns NO error — so the caller pays full
// uncached input believing it cached. Mirroring the sampling-deprecation pre-advice
// (lifecycle.go samplingDeprecationFinding), this emits an informational finding when a
// request marks a breakpoint whose counted prefix is below the threshold, so an operator
// learns BEFORE the bill, not after. It is Anthropic platform behavior, not a product bug
//: informational, never a claim of a fix.
//
// The per-model minimums are VERIFIED against the live "Cache limitations" table
// (…/build-with-claude/prompt-caching, jun-2026) and differ by surface: the Claude API /
// Claude Platform on AWS / Vertex AI / Microsoft Foundry use the first-party numbers;
// Amazon Bedrock (bedrock-mantle) runs its OWN per-model minimums — only Fable 5 / Mythos
// 5 are published there (1024), the rest are deferred to the Bedrock docs and left
// UNKNOWN (fail-closed: never advise against an unverified threshold). NB: the newest
// models cache SMALLER prefixes — Opus 4.8 is 1024, not the 4096 of Opus 4.6/4.5.
package claudeapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// cacheMinimumAsOf stamps when the minimum-cacheable table was recorded against the
// authority (the prompt-caching "Cache limitations" section).
const cacheMinimumAsOf = "2026-06-09"

// subjectCacheMinimum is the FindingReport subject for the cache-minimum pre-advice.
const subjectCacheMinimum = "anthropic.prompt_cache_minimum"

// MinCacheablePrefixTokens returns the minimum prefix length (in tokens) a model will
// cache on the given gateway, and ok=false when that (model, gateway) threshold is NOT
// verified — in which case NO advisory should be emitted (fail-closed). Verified jun-2026
// (prompt-caching "Cache limitations"). An empty gateway is treated as the first-party
// surface. Matching is by id prefix so dated/aliased ids resolve.
func MinCacheablePrefixTokens(modelID string, gw model.Gateway) (int, bool) {
	id := strings.TrimSpace(modelID)
	fableFamily := strings.HasPrefix(id, "claude-fable-5") || strings.HasPrefix(id, "claude-mythos-5")

	// Amazon Bedrock runs its own per-model minimums; only Fable 5 / Mythos 5 are
	// published (1024), the rest defer to the Bedrock docs → unknown (fail-closed).
	if gw == model.GatewayBedrockMantle || gw == model.GatewayBedrockLegacy {
		if fableFamily {
			return 1024, true
		}
		return 0, false
	}

	// Claude API / Claude Platform on AWS / Vertex AI / Microsoft Foundry (and "" = direct).
	switch {
	case fableFamily:
		return 512, true
	case strings.HasPrefix(id, "claude-opus-4-8"):
		return 1024, true
	case strings.HasPrefix(id, "claude-opus-4-7"):
		return 2048, true
	case strings.HasPrefix(id, "claude-opus-4-6"), strings.HasPrefix(id, "claude-opus-4-5"):
		return 4096, true
	case strings.HasPrefix(id, "claude-opus-4-1"), strings.HasPrefix(id, "claude-opus-4-0"):
		return 1024, true
	case strings.HasPrefix(id, "claude-sonnet-4-6"),
		strings.HasPrefix(id, "claude-sonnet-4-5"),
		strings.HasPrefix(id, "claude-sonnet-4-0"):
		return 1024, true
	case strings.HasPrefix(id, "claude-haiku-4-5"):
		return 4096, true
	case strings.HasPrefix(id, "claude-haiku-3-5"):
		return 2048, true
	case strings.HasPrefix(id, "claude-mythos-preview"):
		return 2048, true
	default:
		return 0, false
	}
}

// hasCacheBreakpoint reports whether the request marks any prompt-cache breakpoint
// (a cache_control on a system block or a message content block).
func hasCacheBreakpoint(req MessageRequest) bool {
	for _, b := range req.System {
		if b.CacheControl != nil {
			return true
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

// CacheMinimumSignal returns an informational PRE-ADVICE finding when a cacheable prefix
// of prefixTokens sits below the model/gateway minimum — so a cache_control breakpoint
// over it would silently no-op and the full input bills uncached. ok is false when there
// is nothing to advise: the threshold is unverified for this (model, gateway), the prefix
// is non-positive, or it already meets the minimum. The (unstable) numbers travel in the
// title; the redacted hash carries the full context. at defaults to the client clock.
func (inf *Inference) CacheMinimumSignal(modelID string, prefixTokens int64, sessionRef string, at time.Time) (model.FindingReport, bool) {
	min, ok := MinCacheablePrefixTokens(modelID, inf.gateway)
	if !ok || prefixTokens <= 0 || prefixTokens >= int64(min) {
		return model.FindingReport{}, false
	}
	if at.IsZero() {
		at = inf.clock().UTC()
	}
	return model.FindingReport{
		Kind:        "configuration",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectCacheMinimum,
		SubjectRef:  refOrSession(sessionRef, modelID),
		Title:       fmt.Sprintf("Prompt-cache breakpoint below the %d-token minimum for %s — will not cache", min, modelID),
		DetailHash: redact.Hash(fmt.Sprintf(
			"cacheable prefix=%d < minimum=%d tokens for model=%s gateway=%s; cache_control silently no-ops (no error) — full input billed uncached (verified %s)",
			prefixTokens, min, modelID, inf.gateway, cacheMinimumAsOf)),
		OccurredAt: at,
	}, true
}

// CacheableAdvisory counts the request's input tokens (count_tokens — free, ZDR) and,
// when the request marks a prompt-cache breakpoint, returns the pre-advice finding if the
// counted prefix is below the model/gateway minimum. It returns (zero, false, nil) —
// WITHOUT making the count call — when the request marks no breakpoint or the threshold
// is unverified for this (model, gateway). The total input count is a sound UPPER bound
// on every breakpoint's own prefix, so a (total < minimum) result PROVES no breakpoint
// can cache; it does not detect an early breakpoint inside an otherwise-large request
// (that would need prefix truncation, which can yield an invalid intermediate message
// array — deliberately out of scope, so this never false-positives).
func (inf *Inference) CacheableAdvisory(ctx context.Context, req MessageRequest, sessionRef string, at time.Time) (model.FindingReport, bool, error) {
	if inf.client == nil {
		return model.FindingReport{}, false, ErrNotConfigured
	}
	if !hasCacheBreakpoint(req) {
		return model.FindingReport{}, false, nil
	}
	modelID := req.Model
	if modelID == "" {
		modelID = inf.defaultModel
	}
	if _, ok := MinCacheablePrefixTokens(modelID, inf.gateway); !ok {
		return model.FindingReport{}, false, nil // threshold unverified → never advise
	}
	tc, err := inf.CountTokens(ctx, req)
	if err != nil {
		return model.FindingReport{}, false, err
	}
	f, ok := inf.CacheMinimumSignal(modelID, tc.InputTokens, sessionRef, at)
	return f, ok, nil
}
