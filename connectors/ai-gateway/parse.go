// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aigateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// usageRecord is the TOLERANT subset of one Envoy AI Gateway usage record this
// connector reads from an access-log / usage export. The exact wire shape is NOT
// a single frozen standard: the gateway emits token usage two ways, and an
// operator decides which fields land in the access log, so this struct accepts
// BOTH documented surfaces (verified against envoyproxy/ai-gateway, see doc.go):
//
//  1. The OpenTelemetry GenAI access-log fields the gateway populates from its
//     own AI metadata — gen_ai.usage.{input,output,total}_tokens,
//     gen_ai.{request,response,original}.model, gen_ai.provider.name,
//     gen_ai.operation.name (verified: aigateway.envoyproxy.io observability
//     metrics + access logs).
//  2. The io.envoy.ai_gateway dynamic-metadata keys an operator references in an
//     access-log format string — llm_input_token, llm_output_token,
//     llm_total_token, response_model, model_name_override, backend_name
//     (verified: aigateway.envoyproxy.io access logs, AIGatewayRoute
//     llmRequestCosts.metadataKey).
//
// Both name aliases are decoded; resolve*() below prefer the GenAI form and fall
// back to the metadata form. Token counts and the cache split map to LLMRequestCost
// types (InputToken / OutputToken / CachedInputToken / CacheCreationInputToken /
// TotalToken) that the gateway can compute per request. Deliberately ABSENT: any
// request/response body, prompt, completion, or header value — only structural
// usage metadata is read (docs/SECURITY-HARDENING.md); a free-text field added to the struct could
// leak, so none is.
type usageRecord struct {
	// --- GenAI semantic-convention access-log fields (surface 1) ---
	GenAIInputTokens  *int64 `json:"gen_ai.usage.input_tokens"`
	GenAIOutputTokens *int64 `json:"gen_ai.usage.output_tokens"`
	GenAITotalTokens  *int64 `json:"gen_ai.usage.total_tokens"`
	// Cache split (gateway LLMRequestCostType CachedInputToken / CacheCreationInputToken).
	// Carried under the OTel-aligned attribute names the gateway emits when configured.
	GenAICachedInputTokens   *int64 `json:"gen_ai.usage.cached_input_tokens"`
	GenAICacheCreationTokens *int64 `json:"gen_ai.usage.cache_creation_input_tokens"`

	GenAIResponseModel string `json:"gen_ai.response.model"`
	GenAIRequestModel  string `json:"gen_ai.request.model"`
	GenAIOriginalModel string `json:"gen_ai.original.model"`
	GenAIProviderName  string `json:"gen_ai.provider.name"`

	// --- io.envoy.ai_gateway dynamic-metadata keys (surface 2) ---
	MetaInputToken         *int64 `json:"llm_input_token"`
	MetaOutputToken        *int64 `json:"llm_output_token"`
	MetaTotalToken         *int64 `json:"llm_total_token"`
	MetaCachedInputToken   *int64 `json:"llm_cached_input_token"`
	MetaCacheCreationToken *int64 `json:"llm_cache_creation_input_token"`
	MetaResponseModel      string `json:"response_model"`
	MetaModelOverride      string `json:"model_name_override"`
	MetaBackendName        string `json:"backend_name"`

	// --- cost (operator LLMRequestCost CEL field) + provenance ---
	// CostMicroUSD is an authoritative computed cost in micro-USD, when the operator
	// configured a CEL cost metadata key that emits it. CostUSD is the same in decimal
	// dollars. When NEITHER is present the cost is left to the engine to derive from
	// list pricing (provenance estimated); the connector never invents money.
	CostMicroUSD *int64   `json:"llm_cost_micro_usd"`
	CostUSD      *float64 `json:"llm_cost_usd"`

	// --- timestamp ---
	// The gateway/Envoy access log carries a start time; accept the common spellings.
	StartTime string `json:"start_time"`
	Timestamp string `json:"timestamp"`
	Time      string `json:"time"`
}

// recordsFromBytes extracts usage records from one file's bytes, accepting the two
// real shapes an export takes: newline-delimited JSON (the access-log default — one
// record per line), or a single JSON array. A JSON object that is a single record
// is also accepted. A line that does not parse, or that carries no usable token
// count, is skipped by the caller — never guessed.
func recordsFromBytes(data []byte) []usageRecord {
	// Try a top-level JSON array first (some exporters batch).
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []usageRecord
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			return arr
		}
	}
	// Newline-delimited JSON (the access-log default).
	var recs []usageRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r usageRecord
		if err := json.Unmarshal(line, &r); err == nil {
			recs = append(recs, r)
		}
	}
	if len(recs) > 0 {
		return recs
	}
	// Single object fallback.
	var one usageRecord
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &one); err == nil {
			return []usageRecord{one}
		}
	}
	return nil
}

// providerRef resolves the provider/backend a record names, normalizing it to the
// engine's natural reference. The GenAI gen_ai.provider.name wins; the
// io.envoy.ai_gateway backend_name is the fallback. The Anthropic native provider
// is reported by the gateway as "anthropic" (verified: observability metrics doc);
// the value is taken VERBATIM (lower-cased only) so a Bedrock/Vertex-hosted backend
// keeps its own name and is never relabeled as direct Anthropic.
func (r usageRecord) providerRef() string {
	if p := strings.TrimSpace(r.GenAIProviderName); p != "" {
		return strings.ToLower(p)
	}
	return strings.TrimSpace(r.MetaBackendName)
}

// modelRef resolves the model a record names. Preference: the response model (what
// actually served), then the request model, then the original (pre-override) model,
// then the metadata response_model / model_name_override. Never invented.
func (r usageRecord) modelRef() string {
	for _, m := range []string{
		r.GenAIResponseModel, r.GenAIRequestModel, r.GenAIOriginalModel,
		r.MetaResponseModel, r.MetaModelOverride,
	} {
		if v := strings.TrimSpace(m); v != "" {
			return v
		}
	}
	return ""
}

// inputTokens resolves the input-token count. The GenAI field wins; the metadata
// key is the fallback. A record may carry only a total — that is handled by
// totalTokens, not synthesized here (an input value is never derived from a total).
func (r usageRecord) inputTokens() int64  { return firstNonNeg(r.GenAIInputTokens, r.MetaInputToken) }
func (r usageRecord) outputTokens() int64 { return firstNonNeg(r.GenAIOutputTokens, r.MetaOutputToken) }
func (r usageRecord) totalTokens() int64  { return firstNonNeg(r.GenAITotalTokens, r.MetaTotalToken) }

// cacheReadTokens / cacheCreationTokens resolve the cache split ONLY when the
// record carries it (0 = not reported, never invented — ARCHITECTURE.md). The gateway's
// CachedInputToken cost type is a cache-READ (hit); CacheCreationInputToken is a
// cache-WRITE. The write has no TTL split in the gateway's vocabulary, so it maps to
// the 5m (default) creation tier; the 1h tier is left 0 (not reported).
func (r usageRecord) cacheReadTokens() int64 {
	return firstNonNeg(r.GenAICachedInputTokens, r.MetaCachedInputToken)
}
func (r usageRecord) cacheCreationTokens() int64 {
	return firstNonNeg(r.GenAICacheCreationTokens, r.MetaCacheCreationToken)
}

// cost resolves an authoritative per-request cost in micro-USD and whether the
// record carried one. A direct micro-USD field wins; a decimal-USD field is
// converted (rounded). A negative/absent value yields ok=false, so the caller marks
// the sample estimated and leaves the engine to derive money from list pricing.
func (r usageRecord) cost() (micro int64, ok bool) {
	if r.CostMicroUSD != nil && *r.CostMicroUSD >= 0 {
		return *r.CostMicroUSD, true
	}
	if r.CostUSD != nil && *r.CostUSD >= 0 {
		return int64(*r.CostUSD*1_000_000 + 0.5), true
	}
	return 0, false
}

// occurredAt resolves the record's timestamp, accepting the access-log spellings
// (start_time / timestamp / time) in RFC3339(Nano) or a Unix-epoch string. It
// returns ok=false when no parseable timestamp is present, so the caller can stamp
// the connector clock rather than emit a zero time.
func (r usageRecord) occurredAt() (time.Time, bool) {
	for _, s := range []string{r.StartTime, r.Timestamp, r.Time} {
		if t, ok := parseTime(s); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// hasUsage reports whether a record carries any usable token count or an
// authoritative cost. A record with neither (e.g. a non-LLM proxy line that landed
// in the same log) is skipped, not emitted as a zero sample.
func (r usageRecord) hasUsage() bool {
	if r.inputTokens() > 0 || r.outputTokens() > 0 || r.totalTokens() > 0 {
		return true
	}
	_, ok := r.cost()
	return ok
}

// firstNonNeg returns the first non-nil, non-negative *int64 value, or 0. A
// negative count is treated as absent (the gateway never reports negative tokens).
func firstNonNeg(vals ...*int64) int64 {
	for _, v := range vals {
		if v != nil && *v >= 0 {
			return *v
		}
	}
	return 0
}

// timeLayouts are the RFC3339 forms Envoy/the gateway emits in an access-log time.
var timeLayouts = []string{time.RFC3339Nano, time.RFC3339}

// parseTime parses a record timestamp. It accepts RFC3339(Nano) and a Unix-seconds
// or Unix-millis epoch string, returning ok=false for anything else.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	// Epoch fallback: a bare integer is a Unix timestamp. 13+ digits is milliseconds,
	// otherwise seconds. Keeping the real timestamp (not the wall clock) preserves the
	// CostSample OccurredAt de-dup natural key across re-polls.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if len(s) >= 13 {
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}
