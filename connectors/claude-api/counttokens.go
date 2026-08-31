// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds count_tokens (POST /v1/messages/count_tokens) — the PRE-FLIGHT half a
// conducted run needs (FASE W): size a request BEFORE incurring spend. Its two live
// consumers are the proxy's per-surface window deny (400) and the context-policy
// ceiling deny (413); the budget gate does not consume the count. The endpoint is GA,
// FREE, and ZDR-eligible (verified jun-2026, …/token-counting): no spend, no retention.
// It accepts the same prompt-shaping inputs as Messages but takes NO runtime params (no
// max_tokens/stream/output_config/…); a cache_control block may be present but is
// ignored for caching. SECURITY: the count body CARRIES the prompt (system,
// messages, tools, MCP servers) — it is real provider egress, so a governing caller must
// run it only AFTER its local content gates (DLP, firewall) have passed; the proxy sizes
// in its second phase for exactly this reason. The connector imports no module — it
// returns the count; the enforcement decision lives at the composition root, on the
// AGPL side of the license frontier.
package claudeapi

import (
	"context"
	"fmt"
)

// countTokensPath is the token-counting endpoint.
const countTokensPath = "/v1/messages/count_tokens"

// TokenCount is the count_tokens response — a single estimated input-token total. The
// count is an ESTIMATE (the API may differ by a small amount and adds non-billed
// system-optimization tokens of its own); it is suitable for budgeting and routing.
type TokenCount struct {
	InputTokens int64 `json:"input_tokens"`
}

// countTokensBody is the prompt-shaping subset count_tokens accepts. It mirrors the
// Messages body but deliberately OMITS every runtime param (max_tokens, stream,
// service_tier, output_config, context_management, metadata, sampling params,
// stop_sequences, fallbacks): those do not change the input-token count and either are
// rejected or simply ignored by the endpoint. system/messages/tools/tool_choice/thinking/
// mcp_servers DO shape the count and are forwarded.
type countTokensBody struct {
	Model      string         `json:"model"`
	System     []ContentBlock `json:"system,omitempty"`
	Messages   []Message      `json:"messages"`
	Tools      []any          `json:"tools,omitempty"`
	ToolChoice *ToolChoice    `json:"tool_choice,omitempty"`
	Thinking   *Thinking      `json:"thinking,omitempty"`
	MCPServers []any          `json:"mcp_servers,omitempty"`
}

// CountTokens estimates the input tokens of req against its model (defaulted from the
// client when empty), WITHOUT invoking the model — free, ZDR-eligible. It applies the
// same thinking normalization CreateMessage will (so the count mirrors what gets sent on
// a model that rejects the legacy budget). The current-turn thinking content counts;
// previous-turn thinking does not (Anthropic accounting). It forwards only the beta
// headers the prompt shape requires (a mid-conversation system message, an advisor
// tool) — the runtime betas (fallbacks, task budgets) do not apply here.
func (inf *Inference) CountTokens(ctx context.Context, req MessageRequest) (TokenCount, error) {
	if inf.client == nil {
		return TokenCount{}, ErrNotConfigured
	}
	modelID := req.Model
	if modelID == "" {
		modelID = inf.defaultModel
	}
	if modelID == "" {
		return TokenCount{}, fmt.Errorf("claudeapi: CountTokens: no model (set request.Model or InferenceConfig.DefaultModel)")
	}
	body := countTokensBody{
		Model:      modelID,
		System:     req.System,
		Messages:   req.Messages,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
		Thinking:   normalizeThinkingForModel(modelID, req.Thinking),
		MCPServers: req.MCPServers,
	}
	// Only the prompt-shaping betas matter to count_tokens; derive them from a request
	// carrying just the messages + tools (so fallbacks/task-budget/compaction headers,
	// which the count body never sends, are never attached).
	betas := MessageRequest{Messages: req.Messages, Tools: req.Tools}.BetaHeaders()
	var tc TokenCount
	if err := inf.client.PostJSON(ctx, countTokensPath, body, &tc, betaHeaderMap(betas)); err != nil {
		return TokenCount{}, err
	}
	return tc, nil
}
