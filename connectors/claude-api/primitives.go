// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the net-new Claude Messages-API primitives of 2026 (D1-D8) on the REQUEST/RESPONSE shape the inference client sends and reads — the
// half claude.go (Admin/observability) and forensic.go (response cost/forensics) do
// not cover. It keeps the catalog "current, not 2025": output_config (effort, the
// FinOps task_budget cost lever, structured-outputs format), server-side compaction
// (context_management) with a lossless block round-trip, search-result content
// blocks, and the verified service-tier dimension. Every beta-header value and dated
// identifier below is verified against the live platform docs (jun-2026), never
// recalled — the bar set.
//
// Governance framing: two of these are levers, not mere features — Task Budgets (D4)
// is a FinOps cost cap (reinforces) and mid-conversation system messages (D3, in
// opchannel.go) are the non-spoofable operator channel (reinforces). The rest
// change request/response handling and must be modeled faithfully so the product
// GOVERNS the live API surface, not a stale one.
//
// Authority (verbatim, jun-2026): …/build-with-claude/{structured-outputs,compaction,
// search-results}; …/api/service-tiers; …/about-claude/models/whats-new-claude-4-7
// (Task Budgets); …/build-with-claude/prompt-caching (mid-conversation system msgs).
package claudeapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 2026 beta-header VALUES (the anthropic-beta header). These are the literal header
// strings — NOT the dated tool-TYPE identifiers (advisor_20260301, compact_20260112,
// …), which are a separate concept (the value of a tool's "type" / a context edit's
// "type"). Verified jun-2026; structured outputs is GA and needs NO header.
const (
	// BetaMidConversationSystem gates a role:"system" message inside messages[] (D3).
	BetaMidConversationSystem = "mid-conversation-system-2026-04-07"
	// BetaTaskBudgets gates output_config.task_budget (D4).
	BetaTaskBudgets = "task-budgets-2026-03-13"
	// BetaCompaction gates context_management server-side compaction (D5).
	BetaCompaction = "compact-2026-01-12"
	// BetaAdvisorTool gates the advisor server tool (D1).
	BetaAdvisorTool = "advisor-tool-2026-03-01"
	// BetaContextManagement gates the context-editing strategies (clear_tool_uses /
	// clear_thinking). Distinct from BetaCompaction (compaction is its own beta).
	BetaContextManagement = "context-management-2025-06-27"
	// BetaServerSideFallback gates the fallbacks request param. The date must
	// be EXACTLY 2026-06-01: any other server-side-fallback-* value is rejected
	// upstream with a 400 (verified 2026-06-09 against the refusals-and-fallback
	// page). It also grants the credit fields on stop_details, so BetaFallbackCredit
	// is NOT auto-added alongside it.
	BetaServerSideFallback = "server-side-fallback-2026-06-01"
	// BetaFallbackCredit gates fallback_credit_token redemption (and the credit
	// fields on a refusal's stop_details) for a manual retry.
	BetaFallbackCredit = "fallback-credit-2026-06-01"
	// NB: the fast-mode header value (fast-mode-2026-02-01) is defined as fastModeBeta in
	// claude.go (where the usage-report speed split uses it); the knownBetaHeaders
	// inventory references that const rather than redeclaring the value here.
)

// Conversation roles. The connector previously used string literals; the operator
// channel (D3) makes the system role load-bearing, so they are named here.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleSystem    = "system"
)

// Content block / context-edit / tool dated identifiers (the "type" VALUES, not
// headers). Verified jun-2026.
const (
	blockText          = "text"
	blockSearchResult  = "search_result"
	blockCompaction    = "compaction"
	compactionEditType = "compact_20260112" // context_management edit type (D5)
)

// ---- D4 + D6: output_config (effort, task budget, structured outputs) -------------

// Effort levels (GA, no beta header). max is Opus-tier only; xhigh is Opus 4.7+.
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortXHigh  = "xhigh"
	EffortMax    = "max"
)

// OutputConfig is the GA output-control object (D4/D6): Effort (thinking/spend depth),
// the FinOps TaskBudget cost lever, and the structured-outputs Format. All fields are
// omitempty so a request that sets none serializes byte-identically to the pre-2026
// shape (and adds no beta header).
type OutputConfig struct {
	Effort     string        `json:"effort,omitempty"`
	TaskBudget *TaskBudget   `json:"task_budget,omitempty"`
	Format     *OutputFormat `json:"format,omitempty"`
}

// taskBudgetTypeTokens is the only modeled task_budget type.
const taskBudgetTypeTokens = "tokens"

// MinTaskBudgetTokens is the API floor for output_config.task_budget.total (verified:
// what's-new Claude 4.7). A request below it is rejected before the call.
const MinTaskBudgetTokens = 20000

// TaskBudget is the per-LOOP token budget the model is AWARE of and self-moderates
// against (D4). It is NOT max_tokens: max_tokens is an enforced per-response ceiling
// the model never sees, whereas task_budget is a budget surfaced to the model as a
// running countdown across the whole agentic loop (thinking + tool calls + output).
//
// FinOps lever (cross-walk): BudgetGate denies CUMULATIVE tenant spend
// deny-closed; a Task Budget is the complementary IN-BAND, per-loop cap that makes
// the model wrap up gracefully within an allowance — so an operator caps a runaway
// agentic loop (OWASP LLM10 Unbounded Consumption) at the API itself, not only after
// the fact at the ledger. The two are orthogonal puertas, like budget gate vs
// the approval gate.
type TaskBudget struct {
	Type  string `json:"type"`  // always "tokens"
	Total int    `json:"total"` // >= MinTaskBudgetTokens
}

// TokenTaskBudget builds a tokens task budget. It does not validate here (so a caller
// can construct then inspect); CreateMessage validates before sending.
func TokenTaskBudget(total int) *TaskBudget {
	return &TaskBudget{Type: taskBudgetTypeTokens, Total: total}
}

// validate rejects a malformed/too-small task budget BEFORE the API 400, so the lever
// fails honestly client-side. A nil budget is valid (the field is optional).
func (b *TaskBudget) validate() error {
	if b == nil {
		return nil
	}
	if b.Type != taskBudgetTypeTokens {
		return fmt.Errorf("claudeapi: task_budget.type %q unsupported (only %q)", b.Type, taskBudgetTypeTokens)
	}
	if b.Total < MinTaskBudgetTokens {
		return fmt.Errorf("claudeapi: task_budget.total %d is below the API minimum of %d tokens", b.Total, MinTaskBudgetTokens)
	}
	return nil
}

// OutputFormat is the structured-outputs GA format (D6): output_config.format with a
// JSON schema. This is the CURRENT shape — the old top-level output_format parameter
// is deprecated (kept working only for a transition window), so the connector models
// only output_config.format and never the legacy field. Structured outputs are GA
// (no beta header) but MODEL-GATED — see SupportsStructuredOutputs.
type OutputFormat struct {
	Type   string          `json:"type"`             // "json_schema"
	Schema json.RawMessage `json:"schema,omitempty"` // a JSON Schema (additionalProperties:false required)
}

// JSONSchemaFormat builds a json_schema output format from a raw JSON Schema.
func JSONSchemaFormat(schema json.RawMessage) *OutputFormat {
	return &OutputFormat{Type: "json_schema", Schema: schema}
}

// SupportsStructuredOutputs reports whether a model supports output_config.format /
// strict tool use (D6). Verified jun-2026 (…/structured-outputs): the current 4.x
// generation plus the legacy 4.5/4.1 Opus; older/3.x models 400 on output_config.
// An unknown id returns false (fail-closed: never send a knob the model may reject).
func SupportsStructuredOutputs(modelID string) bool {
	id := strings.TrimSpace(modelID)
	switch {
	case strings.HasPrefix(id, "claude-opus-4-1"), // Opus 4.1 (legacy, supported)
		strings.HasPrefix(id, "claude-opus-4-5"),
		strings.HasPrefix(id, "claude-opus-4-6"),
		strings.HasPrefix(id, "claude-opus-4-7"),
		strings.HasPrefix(id, "claude-opus-4-8"),
		strings.HasPrefix(id, "claude-sonnet-4-5"),
		strings.HasPrefix(id, "claude-sonnet-4-6"),
		strings.HasPrefix(id, "claude-haiku-4-5"),
		// Fable 5 is listed by the structured-outputs doc; Mythos 5 shares Fable 5's
		// capabilities per the 2026-06-09 launch page. The unverified mythos-preview
		// stays false (fail-closed).
		strings.HasPrefix(id, "claude-fable-5"),
		strings.HasPrefix(id, "claude-mythos-5"):
		return true
	default:
		return false
	}
}

// ---- D5: server-side compaction (context_management) ------------------------------

// ContextManagement carries server-side context edits (D5). The compaction edit
// (compact_20260112) summarizes earlier turns server-side when context approaches the
// trigger; it requires the BetaCompaction header (assembled by CreateMessage).
type ContextManagement struct {
	Edits []ContextEdit `json:"edits,omitempty"`
}

// ContextEdit is one context-management edit. Only the dated type is modeled; the
// upstream object may carry more knobs (trigger thresholds) — left unset (defaults).
type ContextEdit struct {
	Type string `json:"type"`
}

// CompactionContextManagement returns a context_management that enables server-side
// compaction (D5). Pair it with AppendAssistantTurn so the returned compaction block
// is round-tripped — see that helper.
func CompactionContextManagement() *ContextManagement {
	return &ContextManagement{Edits: []ContextEdit{{Type: compactionEditType}}}
}

// needsCompactionBeta reports whether a context_management enables compaction (so the
// BetaCompaction header must be sent). nil-safe.
func needsCompactionBeta(cm *ContextManagement) bool {
	if cm == nil {
		return false
	}
	for _, e := range cm.Edits {
		if e.Type == compactionEditType {
			return true
		}
	}
	return false
}

// HasCompaction reports whether a response carried a server-side compaction block — a
// forensic signal that earlier turns were summarized server-side (so the captured
// trail may not reflect the full context the model saw; cf. modules/models CLA-15).
func (r MessageResponse) HasCompaction() bool {
	for _, c := range r.Content {
		if c.Type == blockCompaction {
			return true
		}
	}
	return false
}

// AppendAssistantTurn appends the assistant response as the next conversation turn,
// preserving EVERY content block verbatim — including a server-side compaction block
// (D5), which the API uses to replace the compacted history on the following request.
// This is the documented round-trip: appending only resp.Text() would silently drop
// the compaction state and the next request would re-process the full (uncompacted)
// history. The blocks re-serialize byte-identically (see ContentBlock round-trip).
func AppendAssistantTurn(messages []Message, resp MessageResponse) []Message {
	return append(messages, Message{Role: roleAssistant, Content: append([]ContentBlock(nil), resp.Content...)})
}

// ---- D2: search-result content blocks ---------------------------------------------

// SearchResultCitations toggles natural citations for a search_result block.
type searchResultWire struct {
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	Title     string                 `json:"title"`
	Content   []srTextWire           `json:"content"`
	Citations *searchResultCitations `json:"citations,omitempty"`
}

type srTextWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type searchResultCitations struct {
	Enabled bool `json:"enabled"`
}

// SearchResultBlock builds a search_result content block (D2): source-attributed
// content with optional natural citations, for RAG either as a tool return or as
// top-level user content. Required: source + title + at least one text. The exact
// wire shape is held in the block's raw bytes (see ContentBlock) so it round-trips and
// does not widen ContentBlock with fields that would mis-decode a response's citations.
func SearchResultBlock(source, title string, texts []string, citations bool) ContentBlock {
	w := searchResultWire{Type: blockSearchResult, Source: source, Title: title}
	for _, t := range texts {
		w.Content = append(w.Content, srTextWire{Type: blockText, Text: t})
	}
	if citations {
		w.Citations = &searchResultCitations{Enabled: true}
	}
	raw, _ := json.Marshal(w) // a fixed struct never fails to marshal
	return ContentBlock{Type: blockSearchResult, raw: raw}
}

// ---- ContentBlock lossless round-trip ---------------------------------------------

// contentBlockAlias breaks the recursion in ContentBlock's custom (un)marshaling: it
// has the same struct tags but no methods, so json handles it field-by-field.
type contentBlockAlias ContentBlock

// MarshalJSON emits the verbatim bytes of a DECODED block (raw set) so a server-
// authored block — a compaction summary (D5), a server_tool_use, a search_result —
// re-serializes byte-identically on the next request. A block built by a constructor
// (TextBlock/CachedTextBlock) has raw nil and marshals from its typed fields, so the
// pre-2026 wire output is unchanged. SearchResultBlock sets raw to its full shape.
func (c ContentBlock) MarshalJSON() ([]byte, error) {
	if len(c.raw) > 0 {
		return c.raw, nil
	}
	return json.Marshal(contentBlockAlias(c))
}

// UnmarshalJSON fills the typed fields the connector reads (type/text/cache_control)
// and stashes the full bytes in raw for lossless round-trip. Unknown fields (a
// compaction body, a response text block's citations array, server_tool_use fields)
// are ignored by the typed decode but preserved in raw.
func (c *ContentBlock) UnmarshalJSON(b []byte) error {
	var a contentBlockAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = ContentBlock(a)
	c.raw = append(json.RawMessage(nil), b...)
	return nil
}

// ---- D8: service / Priority tiers --------------------------------------------------

// Service tiers (D8). Two DISTINCT surfaces, easy to conflate:
//   - The Messages-API REQUEST parameter `service_tier` accepts only a small CLOSED
//     set — auto|standard_only (verified jun-2026, …/api/service-tiers). Sending an
//     assigned-tier value (priority, flex, …) on the request 400s.
//   - The ASSIGNED/billing `service_tier` REPORTED on usage.service_tier (and the
//     Admin Usage & Cost dimension) is the broader SIX-value Claude billing vocabulary
//     — standard, batch, priority, priority_on_demand, flex, flex_discount — CONFIRMED
//     by ANT2-02 and modeled canonically in modules/models reference.go
//     (claudeServiceTiers). The inference client carries usage.service_tier through
//     VERBATIM (MessageUsage.ServiceTier) and does NOT restrict this set ("no hardcodees el set de tiers"); the constants below document the vocabulary,
//     they do not gate the response.
//
// Flagged "flex pricing as a distinct product" UNVERIFIED; ANT2-02
// subsequently CONFIRMED flex + flex_discount as real assigned `service_tier` values —
// so this does NOT claim flex's absence (the earlier draft of this comment did, which
// was wrong). flex is simply not a REQUEST value: it is assigned/billed, not requested.
const (
	// Request values — the closed set the service_tier request parameter accepts.
	ServiceTierAuto         = "auto"          // Priority if available, else fallback (default)
	ServiceTierStandardOnly = "standard_only" // never draw Priority capacity
	// Assigned/billing values reported on usage.service_tier — the six confirmed Claude
	// billing tiers (ANT2-02), matching modules/models reference.go claudeServiceTiers.
	ServiceTierStandard         = "standard"
	ServiceTierBatch            = "batch"
	ServiceTierPriority         = "priority"           // prioritized capacity (committed-pricing SLA)
	ServiceTierPriorityOnDemand = "priority_on_demand" // priority without a commitment
	ServiceTierFlex             = "flex"               // flexible/discounted assigned tier
	ServiceTierFlexDiscount     = "flex_discount"      // the flex discount line
)

// ValidRequestServiceTier reports whether a service_tier value is legal on a REQUEST.
// Only auto|standard_only (or empty = default) are accepted; sending an assigned-tier
// value like "priority" on the request 400s — the connector withholds it client-side.
func ValidRequestServiceTier(t string) bool {
	switch t {
	case "", ServiceTierAuto, ServiceTierStandardOnly:
		return true
	default:
		return false
	}
}

// ---- beta-header assembly ----------------------------------------------------------

// betaTagged is implemented by a tool whose presence requires a beta header (advisor).
type betaTagged interface{ requiredBeta() string }

// toolBetaHeader returns the beta header a declared tool requires, or "". It handles
// both the connector's typed tool builders (ServerTool) and a raw map[string]any a
// caller may have hand-built (matched on the dated "type").
func toolBetaHeader(t any) string {
	switch v := t.(type) {
	case betaTagged:
		return v.requiredBeta()
	case map[string]any:
		if ty, _ := v["type"].(string); ty == advisorToolType {
			return BetaAdvisorTool
		}
	}
	return ""
}

// BetaHeaders returns the set of anthropic-beta header values this request requires,
// de-duplicated and in a stable order. It inspects the request itself so a caller
// cannot forget a header: a mid-conversation system message (D3), a task_budget (D4),
// a compaction edit (D5), or an advisor tool (D1) each add their header. Structured
// outputs (D6) are GA and add none. CreateMessage joins these into one comma-separated
// anthropic-beta header.
func (req MessageRequest) BetaHeaders() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, m := range req.Messages {
		if m.Role == roleSystem {
			add(BetaMidConversationSystem)
			break
		}
	}
	if req.OutputConfig != nil && req.OutputConfig.TaskBudget != nil {
		add(BetaTaskBudgets)
	}
	if needsCompactionBeta(req.ContextManagement) {
		add(BetaCompaction)
	}
	for _, t := range req.Tools {
		add(toolBetaHeader(t))
	}
	// a fallbacks chain and a credit retry each carry their own beta. They are
	// mutually exclusive on a request (validateFallbacks), and the server-side header
	// already grants the credit stop_details fields, so neither implies the other.
	if len(req.Fallbacks) > 0 {
		add(BetaServerSideFallback)
	}
	if req.FallbackCreditToken != "" {
		add(BetaFallbackCredit)
	}
	return out
}

// betaHeaderMap renders the assembled beta headers as the per-request extra-header map
// (a single comma-separated anthropic-beta value), or nil when none are required (so
// an unchanged request sends no beta header, exactly as before).
func betaHeaderMap(betas []string) map[string]string {
	if len(betas) == 0 {
		return nil
	}
	return map[string]string{betaHeaderKey: strings.Join(betas, ",")}
}
