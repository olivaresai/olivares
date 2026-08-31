// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the Claude INFERENCE client — the runtime half of "Claude is
// first-class" (CLA-17). Where claude.go reads the Admin API (cost/catalog,
// GET-only), this invokes the model: POST /v1/messages, the Message Batches API,
// and the Files API, over a minimal own HTTP client (modelprovider.InferenceClient)
// — no third-party SDK, to keep the Apache/AGPL frontier clean (same rationale as
// the connector's own MCP client). It carries prompt caching (cache_control) and accounts
// the cache-read / per-TTL cache-creation tokens the response reports, so a caller
// can derive a cost sample with the realized cache savings.
//
// Minimal-data (docs/SECURITY-HARDENING.md): this client handles prompts and completions in flight,
// but it never persists or logs them; what it returns to its caller is the model
// output and token COUNTS. The module above it (the evals Judge / knowledge
// Embedder adapter) decides what, if anything, reaches the ledger — always a
// verdict/score or a redacted hash, never the raw prompt or completion.
package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Inference endpoint paths and headers.
const (
	messagesPath       = "/v1/messages"
	batchesPath        = "/v1/messages/batches"
	filesPath          = "/v1/files"
	filesBetaHeader    = "files-api-2025-04-14" // anthropic-beta value for the Files API (verified jun-2026)
	betaHeaderKey      = "anthropic-beta"
	cacheTypeEphemeral = "ephemeral" // the only supported cache_control type

	// CacheControlTTL1h is the extended 1-hour prompt-cache TTL (priced at 2.0x base
	// input on write; verified jun-2026). Pass it to CachedTextBlock; "" selects the
	// default 5-minute cache. No beta header is required for the 1-hour TTL.
	CacheControlTTL1h = "1h"
)

// InferenceConfig configures the Claude inference client. The key here is an
// inference (workspace) API key — distinct from the Admin key the cost/catalog
// Source uses; the Admin key cannot call /v1/messages.
type InferenceConfig struct {
	// BaseURL is the Messages API root for the resolved gateway (direct ->
	// https://api.anthropic.com; bedrock-mantle/vertex/foundry -> their endpoints).
	BaseURL string
	// APIKey is the inference credential reference (held in memory only).
	APIKey string
	// AnthropicVersion is the anthropic-version header value.
	AnthropicVersion string
	// Gateway is the deployment surface this client talks to; it is stamped on every
	// Usage this client produces so cost/governance is never blind to the surface.
	Gateway model.Gateway
	// DefaultModel is the model id used when a request does not name one (e.g. the
	// judge model). On a third-party gateway this is the pinned id (see ANTHROPIC_
	// DEFAULT_*_MODEL); on direct it is a bare model id like "claude-opus-4-8".
	DefaultModel string
	// Doer is the HTTP transport (injected in tests; nil => default).
	Doer modelprovider.Doer
}

// Inference is the Claude Messages/Batches/Files client.
type Inference struct {
	client       *modelprovider.InferenceClient
	gateway      model.Gateway
	defaultModel string
	now          func() time.Time
}

// NewInference builds an inference client from cfg. With an empty APIKey the client
// is still constructed (the provider returns 401 on call); the caller (composition
// root) should not wire a Judge/Embedder unless a credential is present.
func NewInference(cfg InferenceConfig) *Inference {
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	ver := cfg.AnthropicVersion
	if ver == "" {
		ver = defaultAnthropicVersion
	}
	gw := cfg.Gateway
	if gw == "" {
		gw = model.GatewayDirect
	}
	return &Inference{
		client:       modelprovider.NewInferenceClient(base, cfg.Doer, modelprovider.AuthAnthropicKey, cfg.APIKey, map[string]string{"anthropic-version": ver}),
		gateway:      gw,
		defaultModel: cfg.DefaultModel,
	}
}

// clock returns the client's time source (injectable for tests).
func (inf *Inference) clock() time.Time {
	if inf.now != nil {
		return inf.now()
	}
	return time.Now()
}

// ---- Messages API ----------------------------------------------------------------

// CacheControl marks a content block (or system block) as cacheable. TTL is "" for
// the default 5-minute cache, or "1h" for the extended 1-hour cache (no beta header
// required as of jun-2026). Type is always "ephemeral".
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// ContentBlock is one block of message (or system) content. The text block carries
// the typed fields the judge/embedder need (a cache_control marks a prompt-cache
// breakpoint); richer 2026 blocks (search_result, the server-side compaction summary,
// server_tool_use) are carried losslessly in raw — see the custom (Un)MarshalJSON in
// primitives.go — so a server-authored block round-trips byte-identically when
// appended back into messages[] (the compaction round-trip, D5).
type ContentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// raw holds the verbatim bytes of a block DECODED from a response, so a block the
	// connector does not model field-by-field re-serializes exactly. Set only on
	// unmarshal (and by SearchResultBlock); a constructor-built block leaves it nil and
	// marshals from the typed fields, keeping the pre-2026 wire output unchanged.
	raw json.RawMessage
}

// TextBlock builds an uncached text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

// CachedTextBlock builds a text content block marked as a prompt-cache breakpoint
// with the given TTL ("" => default 5m, "1h" => extended). Everything BEFORE this
// block (system + earlier blocks) is cached, so put the stable prefix first.
func CachedTextBlock(text, ttl string) ContentBlock {
	return ContentBlock{Type: "text", Text: text, CacheControl: &CacheControl{Type: cacheTypeEphemeral, TTL: ttl}}
}

// Message is one turn in the conversation.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Fallback is one entry of the server-side fallbacks chain (beta): the model
// the API retries on when the requested model's safety classifiers decline. An entry
// may override max_tokens for its attempt only; per-attempt thinking overrides exist
// upstream but are not modeled here (the request-level Thinking config applies to the
// whole chain).
type Fallback struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// MessageRequest is the POST /v1/messages body. Only the fields the runtime needs
// are modeled; Tools/MCPServers are carried opaque so the same client can later send
// an mcp_toolset (CLA-09) without re-modeling the Messages API.
type MessageRequest struct {
	Model       string         `json:"model"`
	MaxTokens   int            `json:"max_tokens"`
	System      []ContentBlock `json:"system,omitempty"`
	Messages    []Message      `json:"messages"`
	Tools       []any          `json:"tools,omitempty"`
	MCPServers  []any          `json:"mcp_servers,omitempty"`
	ServiceTier string         `json:"service_tier,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	// TopP/TopK are nucleus / top-k sampling. Like Temperature they are WITHHELD on the
	// models that reject sampling params (Opus 4.7+, Fable 5, Mythos 5 — see
	// RejectsSamplingParams): preflight drops all three rather than let the call 400
	// (ANT2-03; Anthropic's deprecation, not a product bug). On models that still accept
	// them they pass through unchanged.
	TopP *float64 `json:"top_p,omitempty"`
	TopK *int     `json:"top_k,omitempty"`
	// StopSequences are custom strings that end generation (stop_reason="stop_sequence").
	StopSequences []string `json:"stop_sequences,omitempty"`
	// Thinking is the extended/adaptive thinking config (agentic.go). preflight NORMALIZES
	// it to the shape the target model accepts — the legacy fixed budget is rewritten to
	// adaptive on Opus 4.7+/Fable 5/Mythos 5, and an explicit disabled is dropped on the
	// always-on Fable 5/Mythos 5 — so a conducted run streams rather than 400s.
	Thinking *Thinking `json:"thinking,omitempty"`
	// ToolChoice forces / allows (auto) / forbids tool use (agentic.go). preflight
	// validates it (type "tool" requires a name) before the call.
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
	// Stream selects server-sent-events streaming. It is set by StreamMessage, which owns
	// the SSE decode; CreateMessage REJECTS a request with Stream set (a blocking POST
	// cannot decode a stream) — the two entry points are distinct.
	Stream   bool            `json:"stream,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// OutputConfig carries the GA output controls (D4/D6): effort, the FinOps
	// task_budget cost lever, and the structured-outputs format. omitempty so a request
	// that sets none is byte-identical to the pre-2026 shape and sends no beta header.
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
	// ContextManagement carries server-side compaction (D5); its compaction edit makes
	// CreateMessage send the BetaCompaction header.
	ContextManagement *ContextManagement `json:"context_management,omitempty"`
	// Fallbacks names up to three models (ordered, distinct, each a permitted target
	// of the requested model) the API retries on when a safety classifier declines
	// (beta server-side-fallback-2026-06-01; rejected on Batches and not
	// available on Bedrock/Vertex/Foundry). Setting it makes CreateMessage send the
	// BetaServerSideFallback header. Only a classifier decline triggers the chain —
	// rate limits, overloads and server errors return as-is.
	Fallbacks []Fallback `json:"fallbacks,omitempty"`
	// FallbackCreditToken redeems a refusal's fallback credit on the manual retry
	// (beta fallback-credit-2026-06-01): the retry is repriced as though the
	// conversation had been on the retry model all along (cache writes re-read). The
	// token is an opaque 5-minute, org/workspace-scoped SECRET — held in memory for
	// the retry only, never logged, persisted or hashed into findings. A retry MUST
	// drop the fallbacks param (the fallback-credit page), so the two fields are
	// mutually exclusive — CreateMessage enforces it.
	FallbackCreditToken string `json:"fallback_credit_token,omitempty"`
}

// CacheCreation is the per-TTL cache-write breakdown the usage reports.
type CacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}

// MessageUsage is the token accounting a Messages response returns. InputTokens is
// the UNCACHED input; the cache fields are separate (cache_creation_input_tokens =
// sum of the per-TTL breakdown). The InferenceGeo / OutputTokensDetails / Iterations
// fields (ANT2-15/17) carry the residency proof, the (billed-but-hidden) thinking
// tokens, and the per-iteration advisor cost the top-level usage does NOT include.
type MessageUsage struct {
	InputTokens              int64          `json:"input_tokens"`
	OutputTokens             int64          `json:"output_tokens"`
	CacheCreationInputTokens int64          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64          `json:"cache_read_input_tokens"`
	CacheCreation            *CacheCreation `json:"cache_creation,omitempty"`
	ServiceTier              string         `json:"service_tier,omitempty"`
	// InferenceGeo is the data-residency region the request actually ran in (us|global);
	// it is the PROOF of residency (ANT2-17), returned only by models ≥ 4.6 (a 400 on
	// older models). Empty = not reported.
	InferenceGeo string `json:"inference_geo,omitempty"`
	// OutputTokensDetails breaks the output count down; its thinking_tokens are BILLED
	// even when extended-thinking content is display:omitted (ANT2-15) — so cost must
	// count them although there is no content (and the content is always redacted).
	OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details,omitempty"`
	// Iterations carries the per-iteration usage of server-side sub-inferences. The
	// advisor (advisor_20260301) bills here, APART from the top-level usage (ANT2-15),
	// so a FinOps view that reads only the top-level under-counts the advisor spend.
	Iterations []MessageIteration `json:"iterations,omitempty"`
}

// OutputTokensDetails is the output-token breakdown (ANT2-15). ThinkingTokens are
// billed even when the thinking block is display:omitted — they must be accounted.
type OutputTokensDetails struct {
	ThinkingTokens int64 `json:"thinking_tokens"`
}

// MessageIteration is one server-side iteration's usage. AdvisorMessage is present
// when the advisor tool ran a second inference over the full transcript (ANT2-15):
// it is billed here, separately from the top-level usage, and is a forensic signal
// (the advisor read the whole transcript). With server-side fallback the
// array is the PER-ATTEMPT billing record: a Type "message" entry is an attempt by a
// model that declined, a "fallback_message" entry is the fallback model that served
// — each attempt is billed separately at ITS model's rates, and the top-level usage
// describes only the serving attempt, so cost attribution must read this array
// (FallbackCostSamples) rather than the top level.
type MessageIteration struct {
	AdvisorMessage *AdvisorUsage `json:"advisor_message,omitempty"`
	// Type discriminates a fallback-chain attempt: "message" (declined attempt) or
	// "fallback_message" (the fallback model that served). Empty on advisor-only
	// iterations (pre-fallback wire shape).
	Type string `json:"type,omitempty"`
	// Model is the model that ran this attempt (per-attempt rates apply to it).
	Model                    string `json:"model,omitempty"`
	InputTokens              int64  `json:"input_tokens,omitempty"`
	OutputTokens             int64  `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens,omitempty"`
}

// AdvisorUsage is the advisor sub-inference's own token accounting and model. It is
// ZDR-eligible and may run on a separate (Priority) tier; the connector exposes it as
// a SEPARATE cost line so it is never invisible (ANT2-15).
type AdvisorUsage struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// MessageResponse is the POST /v1/messages response. StopDetails carries the refusal
// category (ANT2-15): when stop_reason is "refusal", stop_details.category ∈
// {cyber, bio} is a SECURITY signal — and a refusal is NOT billed since 2026-06-02,
// so it must not be counted as cost.
type MessageResponse struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Role        string         `json:"role"`
	Model       string         `json:"model"`
	StopReason  string         `json:"stop_reason"`
	StopDetails *StopDetails   `json:"stop_details,omitempty"`
	Content     []ContentBlock `json:"content"`
	Usage       MessageUsage   `json:"usage"`
}

// StopDetails refines stop_reason. For a refusal the category names the policy area
// the classifier attributed the decline to — cyber | bio | reasoning_extraction as
// of the Fable 5 launch (2026-06-09), null/empty when the refusal maps to no named
// category (a normal, permanent value) — a security signal worth a finding
// (ANT2-15). stop_details itself is null for every stop reason other than
// refusal. The vocabulary is open (pass-through, never a sealed enum).
type StopDetails struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	// Explanation is the classifier's human-readable description of the decline. The
	// text is UNSTABLE upstream (display, never parse) and may be empty; the connector
	// carries it into a finding only as part of the redacted hash, never the title.
	Explanation string `json:"explanation,omitempty"`
	// RecommendedModel is the API's retry hint when the server-side fallback attempt
	// was SKIPPED (the fallback model was rate-limited/overloaded): a model to retry
	// directly. A hint, not a guarantee; empty when none is available.
	RecommendedModel string `json:"recommended_model,omitempty"`
	// FallbackCreditToken is the refusal's fallback credit (present under the
	// fallback-credit / server-side-fallback betas; empty when no credit is
	// available). It is an opaque 5-minute, org/workspace-scoped SECRET: the
	// connector holds it in memory for the caller's retry ONLY — it must never be
	// logged, persisted, or hashed into a finding (RefusalSignal records only its
	// PRESENCE as credit_available).
	FallbackCreditToken string `json:"fallback_credit_token,omitempty"`
	// FallbackHasPrefillClaim picks the credit-retry body shape (true: append the
	// refusal's content as an assistant turn and the retry model continues; false:
	// resend unchanged). Tri-state: nil means "not reported" (partner surfaces while
	// support rolls out) — treat the shape as unknown, not as false.
	FallbackHasPrefillClaim *bool `json:"fallback_has_prefill_claim,omitempty"`
}

// Text concatenates the text blocks of the response (the assistant's answer).
func (r MessageResponse) Text() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// CreateMessage invokes POST /v1/messages. It fills the model from DefaultModel when
// the request leaves it empty. It returns the decoded response (including usage);
// the caller maps it to a verdict/cost — this client persists nothing.
func (inf *Inference) CreateMessage(ctx context.Context, req MessageRequest) (MessageResponse, error) {
	if inf.client == nil {
		return MessageResponse{}, ErrNotConfigured
	}
	// A stream:true request cannot be decoded by a blocking POST — the two entry points
	// are distinct (StreamMessage owns the SSE decode).
	if req.Stream {
		return MessageResponse{}, fmt.Errorf("claudeapi: CreateMessage cannot stream; use StreamMessage for a stream:true request")
	}
	if err := inf.preflight(&req); err != nil {
		return MessageResponse{}, err
	}
	// Assemble the union of anthropic-beta headers the request's 2026 primitives require
	// (D1/D3/D4/D5); a request with none sends no beta header, exactly as before.
	var resp MessageResponse
	if err := inf.client.PostJSON(ctx, messagesPath, req, &resp, betaHeaderMap(req.BetaHeaders())); err != nil {
		return MessageResponse{}, err
	}
	return resp, nil
}

// preflight defaults the model and applies the client-side guards EVERY Messages call
// shares (CreateMessage and StreamMessage), mutating req in place. It first WITHHOLDS the
// sampling params on models that reject them and NORMALIZES the thinking config (ANT2-03;
// Anthropic deprecations, withheld/normalized so a foreseeable 400 never fails a
// conducted run), then fails honestly client-side for the published constraints that
// would otherwise surface as an opaque upstream 400: an invalid request service_tier
// (D8), a sub-floor task_budget (D4), a malformed fallbacks chain or a credit retry that
// kept the fallbacks param, a misplaced operator-channel system message (D3), a
// tool_choice:"tool" with no name, and an advisor tool with no model (D1).
func (inf *Inference) preflight(req *MessageRequest) error {
	normalized, err := NormalizeMessageRequest(*req, inf.defaultModel)
	if err != nil {
		return err
	}
	*req = normalized
	return nil
}

// NormalizeMessageRequest returns the EFFECTIVE request the client would send upstream: it
// defaults the model (from defaultModel when the request leaves it empty), then applies the
// deprecation-driven MUTATIONS every Messages call shares — it WITHHOLDS the sampling params
// on models that reject them and NORMALIZES the thinking config — and finally runs the PURE
// forwardability guards (ValidateForwardable). It is a pure function of (req, defaultModel):
// it never touches the client, so a governed decider can compute the effective request
// BEFORE deciding and forward exactly those bytes (F3 — decision↔bytes binding). preflight
// is the in-place adapter over it for the ordinary CreateMessage/StreamMessage paths.
//
// The MUTATIONS are idempotent (a defaulted model stays; already-nil sampling stays nil;
// normalizeThinkingForModel projects to a fixed shape), so normalizing an already-normalized
// request is a no-op — but S3 relies on FREEZING the normalized bytes, not on that idempotence.
func NormalizeMessageRequest(req MessageRequest, defaultModel string) (MessageRequest, error) {
	if req.Model == "" {
		req.Model = defaultModel
	}
	if req.Model == "" {
		return MessageRequest{}, fmt.Errorf("claudeapi: no model (set request.Model or InferenceConfig.DefaultModel)")
	}
	// ANT2-03: Opus 4.7+/Fable 5/Mythos 5 reject a non-default temperature/top_p/top_k
	// with a 400. Withhold all three rather than fail the call — it is Anthropic's
	// deprecation, not a product bug.
	if RejectsSamplingParams(req.Model) {
		req.Temperature = nil
		req.TopP = nil
		req.TopK = nil
	}
	// Normalize the thinking config to the shape the model accepts (the legacy fixed
	// budget → adaptive on Opus 4.7+/Fable/Mythos; an explicit disabled dropped on the
	// always-on Fable/Mythos), so a conducted run streams rather than 400s.
	req.Thinking = normalizeThinkingForModel(req.Model, req.Thinking)
	if err := ValidateForwardable(req); err != nil {
		return MessageRequest{}, err
	}
	return req, nil
}

// ValidateForwardable runs the PURE (non-mutating) client-side guards that reject a request
// which would otherwise surface as an opaque upstream 400: an invalid request service_tier
// (D8), a sub-floor task_budget (D4), a malformed fallbacks chain or a credit retry that kept
// the fallbacks param, a misplaced operator-channel system message (D3), a
// tool_choice:"tool" with no name, and an advisor tool with no model (D1). It mutates nothing,
// so a governed decider can RE-VALIDATE an already-normalized request after its gates rewrote
// tools/output_config, before freezing the bytes for forward (F3).
func ValidateForwardable(req MessageRequest) error {
	// D8: service_tier accepts only auto|standard_only on a REQUEST; an assigned-tier
	// value (priority|standard|batch) 400s. Reject it client-side rather than send it.
	if !ValidRequestServiceTier(req.ServiceTier) {
		return fmt.Errorf("claudeapi: service_tier %q is not a valid request value (only %q or %q)", req.ServiceTier, ServiceTierAuto, ServiceTierStandardOnly)
	}
	// D4: a task budget below the API floor 400s — fail honestly client-side.
	if oc := req.OutputConfig; oc != nil {
		if err := oc.TaskBudget.validate(); err != nil {
			return err
		}
	}
	// the server-side fallback chain has published constraints (max 3 entries,
	// distinct, none equal to the requested model) and a credit retry MUST drop the
	// fallbacks param — fail each honestly client-side, before the upstream 400.
	if err := validateFallbacks(req); err != nil {
		return err
	}
	// D3: a mid-conversation system message has placement constraints; surface a misuse
	// before the call rather than as an opaque 400.
	if err := ValidateOperatorChannel(req.Messages); err != nil {
		return err
	}
	// tool_choice "tool" must name the tool (agentic.go).
	if err := validateToolChoice(req.ToolChoice); err != nil {
		return err
	}
	// D1: the advisor tool's model is required; reject an advisor tool with no model
	// client-side (catches a hand-built tool, not only the AdvisorTool builder).
	for _, t := range req.Tools {
		if advisorToolMissingModel(t) {
			return fmt.Errorf("claudeapi: advisor tool (%s) requires a non-empty model", advisorToolType)
		}
	}
	return nil
}

// validateFallbacks enforces the published server-side-fallback constraints
// client-side: at most three entries, every model named and distinct (from each
// other and from the requested model), and mutual exclusion with a credit retry —
// "a retry must drop the fallbacks parameter" (the fallback-credit page).
func validateFallbacks(req MessageRequest) error {
	if len(req.Fallbacks) == 0 {
		return nil
	}
	if req.FallbackCreditToken != "" {
		return fmt.Errorf("claudeapi: fallbacks and fallback_credit_token are mutually exclusive (a credit retry must drop the fallbacks param)")
	}
	if len(req.Fallbacks) > 3 {
		return fmt.Errorf("claudeapi: fallbacks names %d models; the chain allows at most 3", len(req.Fallbacks))
	}
	seen := map[string]bool{}
	for _, f := range req.Fallbacks {
		m := strings.TrimSpace(f.Model)
		if m == "" {
			return fmt.Errorf("claudeapi: every fallbacks entry requires a model")
		}
		if m == req.Model {
			return fmt.Errorf("claudeapi: fallback %q equals the requested model (entries must be distinct from it)", m)
		}
		if seen[m] {
			return fmt.Errorf("claudeapi: duplicate fallback model %q (entries must be distinct)", m)
		}
		seen[m] = true
	}
	return nil
}

// UsageFor maps a Messages response's token accounting to a modelprovider.Usage
// stamped with this client's gateway/model and provenance=estimated, so a caller can
// derive a CostSample (pricing.ToCostSample) that reflects the realized prompt-cache
// savings. occurredAt defaults to now when zero.
func (inf *Inference) UsageFor(resp MessageResponse, sessionRef string, occurredAt time.Time) modelprovider.Usage {
	if occurredAt.IsZero() {
		occurredAt = inf.clock().UTC()
	}
	var c5m, c1h int64
	if resp.Usage.CacheCreation != nil {
		c5m = resp.Usage.CacheCreation.Ephemeral5mInputTokens
		c1h = resp.Usage.CacheCreation.Ephemeral1hInputTokens
	} else {
		// No per-TTL breakdown: attribute the untiered creation to the 5m (default) tier.
		c5m = resp.Usage.CacheCreationInputTokens
	}
	mref := resp.Model
	if mref == "" {
		mref = inf.defaultModel
	}
	return modelprovider.Usage{
		ProviderRef:           modelprovider.ProviderAnthropic,
		ModelRef:              mref,
		SessionRef:            sessionRef,
		InputTokens:           resp.Usage.InputTokens,
		OutputTokens:          resp.Usage.OutputTokens,
		CacheCreation5mTokens: c5m,
		CacheCreation1hTokens: c1h,
		CacheReadTokens:       resp.Usage.CacheReadInputTokens,
		OccurredAt:            occurredAt,
		ServiceTier:           resp.Usage.ServiceTier,
		InferenceGeo:          resp.Usage.InferenceGeo, // ANT2-17: per-request residency proof
		Gateway:               inf.gateway,
		Provenance:            model.ProvenanceEstimated,
	}
}

// ---- Message Batches API ---------------------------------------------------------

// BatchRequest is one entry in a batch submission: a caller-unique custom_id and the
// Messages params to run.
type BatchRequest struct {
	CustomID string         `json:"custom_id"`
	Params   MessageRequest `json:"params"`
}

// BatchStatus is the lifecycle status of a batch (in_progress|canceling|ended).
type BatchStatus string

const (
	BatchInProgress BatchStatus = "in_progress"
	BatchCanceling  BatchStatus = "canceling"
	BatchEnded      BatchStatus = "ended"
)

// Batch is a Message Batch resource.
type Batch struct {
	ID               string      `json:"id"`
	Type             string      `json:"type"`
	ProcessingStatus BatchStatus `json:"processing_status"`
	ResultsURL       string      `json:"results_url"`
	CreatedAt        string      `json:"created_at"`
	EndedAt          string      `json:"ended_at"`
}

// CreateBatch submits a batch (50% price, results within 24h). It defaults each
// entry's model from DefaultModel.
func (inf *Inference) CreateBatch(ctx context.Context, requests []BatchRequest) (Batch, error) {
	if inf.client == nil {
		return Batch{}, ErrNotConfigured
	}
	for i := range requests {
		if requests[i].Params.Model == "" {
			requests[i].Params.Model = inf.defaultModel
		}
	}
	body := map[string]any{"requests": requests}
	var b Batch
	if err := inf.client.PostJSON(ctx, batchesPath, body, &b, nil); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// CreateBatchRaw submits a batch and returns BOTH the decoded Batch (governance/audit
// metadata) and the RAW upstream response bytes, so the inline PEP can relay the upstream
// response VERBATIM — preserving fields this connector does not model (request_counts,
// expires_at, cancel_initiated_at). It defaults each entry's model from DefaultModel exactly
// like CreateBatch. The decode is best-effort: the relay always uses raw, the Batch is only
// for the audit record.
func (inf *Inference) CreateBatchRaw(ctx context.Context, requests []BatchRequest) (Batch, []byte, error) {
	if inf.client == nil {
		return Batch{}, nil, ErrNotConfigured
	}
	for i := range requests {
		if requests[i].Params.Model == "" {
			requests[i].Params.Model = inf.defaultModel
		}
	}
	body := map[string]any{"requests": requests}
	raw, err := inf.client.PostJSONRaw(ctx, batchesPath, body, nil)
	if err != nil {
		return Batch{}, nil, err
	}
	var b Batch
	_ = json.Unmarshal(raw, &b) // best-effort: the relay uses raw; b carries only audit metadata
	return b, raw, nil
}

// GetBatch polls a batch's status by id.
func (inf *Inference) GetBatch(ctx context.Context, id string) (Batch, error) {
	if inf.client == nil {
		return Batch{}, ErrNotConfigured
	}
	var b Batch
	if err := inf.client.GetJSON(ctx, batchesPath+"/"+id, nil, &b, nil); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// BatchResultEntry is one line of the results JSONL: the custom_id and the per-entry
// result (type succeeded|errored|canceled|expired). The succeeded message is left
// raw so a caller decodes only what it needs.
type BatchResultEntry struct {
	CustomID string `json:"custom_id"`
	Result   struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message,omitempty"`
		Error   json.RawMessage `json:"error,omitempty"`
	} `json:"result"`
}

// BatchResults downloads and parses the results JSONL for an ended batch. It returns
// an error if the batch has no results_url yet (not ended).
func (inf *Inference) BatchResults(ctx context.Context, b Batch) ([]BatchResultEntry, error) {
	if inf.client == nil {
		return nil, ErrNotConfigured
	}
	if b.ResultsURL == "" {
		return nil, fmt.Errorf("claudeapi: batch %s has no results_url (status=%s)", b.ID, b.ProcessingStatus)
	}
	data, err := inf.client.GetBytes(ctx, b.ResultsURL, nil)
	if err != nil {
		return nil, err
	}
	var out []BatchResultEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e BatchResultEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("claudeapi: decode batch result line: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

// ---- Files API -------------------------------------------------------------------
//
// The Files store this client reads/writes is workspace-scoped, SHARED across the
// workspace's API keys, PERSISTENT ("retained until explicitly deleted") and NOT
// zero-data-retention by default (verified jun-2026, platform.claude.com/docs/en/api/
// files-list, files-delete). The connector is a SHELL: it lists/gets/deletes what it is
// told to. The GOVERNED decisions over this store — the observe inventory + posture, the
// legal-hold-gated, risk-tiered, dual-control DELETE, and the deletion receipt — live in
// the compliance/composition-root plane the connector may not import (the open-core
// boundary, LICENSING.md).

// FileScope is the optional scope of a Files object. Session-scoped files (managed-agent /
// code-execution outputs) carry {id, type:"session"}; a plain upload has none. A pointer so
// an absent scope round-trips as null rather than an empty object.
type FileScope struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "session"
}

// FileMetadata is a Files API object (id has a file_ prefix). Downloadable is true only for
// skill / code-execution outputs (a user upload is false) and Scope is set only on
// session-scoped files — both modeled as pointers so "not reported" stays distinct from a
// zero value. The object carries NO data-subject metadata: there is no way to map a file to
// a person from the store alone (a constraint the RTBF integration models honestly).
type FileMetadata struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"` // "file"
	Filename     string     `json:"filename"`
	MimeType     string     `json:"mime_type"`
	SizeBytes    int64      `json:"size_bytes"`
	CreatedAt    string     `json:"created_at"`
	Downloadable *bool      `json:"downloadable,omitempty"`
	Scope        *FileScope `json:"scope,omitempty"`
}

// FileDeletion is the DELETE /v1/files/{file_id} confirmation ({"id":…,"type":"file_deleted"}).
type FileDeletion struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "file_deleted"
}

// ListFilesParams are the cursor-pagination + filter inputs for GET /v1/files. Limit is
// clamped to the API's 1..1000 range (the API defaults to 20 when unset); AfterID/BeforeID
// are the opaque cursors a prior page reports; ScopeID filters to one scope (e.g. a session).
type ListFilesParams struct {
	Limit    int
	AfterID  string
	BeforeID string
	ScopeID  string
}

// FilesPage is one page of GET /v1/files. HasMore + LastID drive forward pagination (feed
// LastID back as the next AfterID). The pagination fields are typed optional upstream.
type FilesPage struct {
	Data    []FileMetadata `json:"data"`
	FirstID string         `json:"first_id"`
	LastID  string         `json:"last_id"`
	HasMore bool           `json:"has_more"`
}

// filesListMaxLimit is the API's maximum page size for GET /v1/files (default is 20).
const filesListMaxLimit = 1000

// filesListPageHardCap bounds the pages ListAllFiles will follow, so a runaway or hostile
// has_more loop cannot spin forever (defense in depth — the store is large but finite).
const filesListPageHardCap = 10000

// UploadFile uploads a file (multipart, with the Files API beta header) and returns
// its metadata. The returned file_id can be referenced from a message content block
// across requests without re-uploading.
func (inf *Inference) UploadFile(ctx context.Context, filename string, content []byte) (FileMetadata, error) {
	if inf.client == nil {
		return FileMetadata{}, ErrNotConfigured
	}
	var fm FileMetadata
	err := inf.client.PostMultipart(ctx, filesPath, nil, "file", filename, content, &fm,
		map[string]string{betaHeaderKey: filesBetaHeader})
	if err != nil {
		return FileMetadata{}, err
	}
	return fm, nil
}

// ListFiles fetches ONE page of GET /v1/files for the given cursor/filter (beta header).
// Limit is clamped to the API ceiling; a zero limit lets the API default (20) apply.
func (inf *Inference) ListFiles(ctx context.Context, p ListFilesParams) (FilesPage, error) {
	if inf.client == nil {
		return FilesPage{}, ErrNotConfigured
	}
	q := url.Values{}
	if p.Limit > 0 {
		limit := p.Limit
		if limit > filesListMaxLimit {
			limit = filesListMaxLimit
		}
		q.Set("limit", strconv.Itoa(limit))
	}
	if p.AfterID != "" {
		q.Set("after_id", p.AfterID)
	}
	if p.BeforeID != "" {
		q.Set("before_id", p.BeforeID)
	}
	if p.ScopeID != "" {
		q.Set("scope_id", p.ScopeID)
	}
	var page FilesPage
	if err := inf.client.GetJSON(ctx, filesPath, q, &page, map[string]string{betaHeaderKey: filesBetaHeader}); err != nil {
		return FilesPage{}, err
	}
	return page, nil
}

// ListAllFiles follows the cursor (has_more / last_id) and returns the full inventory for
// the filter — the reader the governed inventory sits on (NO single-page assumption). It
// requests the max page size for fewer round-trips and is bounded by the page hard cap; on
// a mid-walk error it returns what it has plus the error.
func (inf *Inference) ListAllFiles(ctx context.Context, scopeID string) ([]FileMetadata, error) {
	if inf.client == nil {
		return nil, ErrNotConfigured
	}
	var out []FileMetadata
	after := ""
	for page := 0; page < filesListPageHardCap; page++ {
		fp, err := inf.ListFiles(ctx, ListFilesParams{Limit: filesListMaxLimit, AfterID: after, ScopeID: scopeID})
		if err != nil {
			return out, err
		}
		out = append(out, fp.Data...)
		// Terminate on the documented end (has_more=false) OR a non-advancing cursor (no
		// last_id / empty page) so a misbehaving endpoint cannot loop to the hard cap.
		if !fp.HasMore || fp.LastID == "" || len(fp.Data) == 0 {
			break
		}
		after = fp.LastID
	}
	return out, nil
}

// GetFile fetches one file's metadata (GET /v1/files/{file_id}, beta header).
func (inf *Inference) GetFile(ctx context.Context, fileID string) (FileMetadata, error) {
	if inf.client == nil {
		return FileMetadata{}, ErrNotConfigured
	}
	var fm FileMetadata
	if err := inf.client.GetJSON(ctx, filesPath+"/"+url.PathEscape(fileID), nil, &fm, map[string]string{betaHeaderKey: filesBetaHeader}); err != nil {
		return FileMetadata{}, err
	}
	return fm, nil
}

// DeleteFile deletes one file (DELETE /v1/files/{file_id}, beta header) and returns the
// confirmation. This is the ONLY way the persistent store is erased. The connector is a
// SHELL — it deletes what it is told to; the GOVERNED decision (legal-hold re-check,
// risk-tier/HITL, the compliance-emitted receipt) lives above it.
func (inf *Inference) DeleteFile(ctx context.Context, fileID string) (FileDeletion, error) {
	if inf.client == nil {
		return FileDeletion{}, ErrNotConfigured
	}
	var d FileDeletion
	if err := inf.client.DeleteJSON(ctx, filesPath+"/"+url.PathEscape(fileID), nil, &d, map[string]string{betaHeaderKey: filesBetaHeader}); err != nil {
		return FileDeletion{}, err
	}
	return d, nil
}

// ErrNotConfigured is returned when an inference call is made on a client built
// without a base/transport (it should not be wired as a Judge/Embedder).
var ErrNotConfigured = fmt.Errorf("claudeapi: inference client not configured")
