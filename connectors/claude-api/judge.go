// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// This file is the Claude-backed LLM-judge: it turns a candidate output + a rubric
// into a structured verdict by invoking the Messages API. It lives in the connector
// (not the evals module) so the prompt construction and verdict parsing are reusable
// and unit-testable on the Apache side; the AGPL composition-root adapter only maps
// the evals DTOs onto this. Minimal-data: the judge returns a score/pass/reason — it
// never returns or persists the evaluated text.

// JudgeInput is a single grading request: the candidate Output, the optional
// original Input it answered and Expected reference, and the Criterion (rubric). A
// ModelRef pins the judge model; empty uses the client's DefaultModel.
type JudgeInput struct {
	ModelRef  string
	Input     string
	Output    string
	Expected  string
	Criterion string
}

// JudgeResult is the model's verdict: a 0..1 score, a pass flag, and a short,
// non-sensitive reason.
type JudgeResult struct {
	Score  float64
	Passed bool
	Reason string
}

// judgeSystemPrompt is the stable rubric prefix — kept constant and marked as a
// prompt-cache breakpoint so repeated judging of a suite reuses the cached system
// (0.1× read cost) instead of re-billing it every case.
//
// Two bias mitigations are built into the prompt + schema (docs/EVAL-
// METHODOLOGY.md): CoT-forcing — "analysis" is the FIRST schema property and the
// prompt demands it before the verdict, so the model reasons before it scores
// (reasoning-before-verdict, Zheng et al. 2023) — and an explicit verbosity/style
// control ("length is not quality"). The analysis text is consumed in flight and
// DISCARDED by the caller (minimal-data): only score/passed/reason survive.
// NB: changing this prompt changes verdict behavior — bump evals' judgeCacheVersion
// (modules/evals/gate.go) so stale cached verdicts are not reused.
const judgeSystemPrompt = `You are an impartial evaluation judge. You are given a CRITERION, a candidate ` +
	`OUTPUT, and optionally the original INPUT and an EXPECTED reference. Decide how well the OUTPUT ` +
	`satisfies the CRITERION. Judge substance against the CRITERION only: length is not quality — never ` +
	`reward an answer for being longer, more elaborate, more confident or more polished; a short correct ` +
	`answer outranks a long padded one. Respond with ONLY a single JSON object and nothing else, of the ` +
	`exact form: {"analysis": "<your step-by-step assessment against the criterion, 2-5 sentences, written ` +
	`BEFORE you decide>", "score": <number between 0 and 1>, "passed": <true|false>, "reason": "<one short ` +
	`sentence, no sensitive detail>"}. Write "analysis" first and only then derive "score" and "passed" from ` +
	`it. "score" is your graded quality in [0,1]; "passed" is whether the OUTPUT meets the CRITERION; ` +
	`"reason" is a brief, non-sensitive justification. Do not echo the OUTPUT or any provided text verbatim.`

// judgeMaxTokens bounds the judge's response (the analysis + a small JSON verdict;
// sized so a 2-5 sentence forced analysis never truncates the trailing verdict).
const judgeMaxTokens = 2048

// judgeOutputSchema is the structured-outputs (D6) JSON schema for the verdict. On a
// model that supports output_config.format the judge constrains the response to this
// schema (guaranteed-parseable JSON), removing the prose-extraction guesswork; on an
// unsupported model the field is omitted and parseJudgeVerdict's tolerant extraction
// is the fallback (so the judge still works on every model). additionalProperties is
// false (required by structured outputs); the 0..1 range is enforced client-side
// (clamped in parseJudgeVerdict) since numeric constraints are not schema-supported.
// "analysis" is deliberately FIRST: generation is autoregressive, so ordering the
// property first makes the model produce its reasoning before the score it must
// justify (the CoT-forcing mitigation).
var judgeOutputSchema = json.RawMessage(`{` +
	`"type":"object",` +
	`"properties":{"analysis":{"type":"string"},"score":{"type":"number"},"passed":{"type":"boolean"},"reason":{"type":"string"}},` +
	`"required":["analysis","score","passed","reason"],` +
	`"additionalProperties":false}`)

// Judge invokes the Messages API to grade in and parses the structured verdict. A
// transport/HTTP fault or an unparseable response is returned as an error (the evals
// scorer records it as outcome=error, never a silent pass). Note: temperature/top_p
// are deliberately NOT sent — Opus 4.7+ rejects them with a 400 (Anthropic
// deprecation); the judge relies on the rubric + JSON-only instruction instead.
func (inf *Inference) Judge(ctx context.Context, in JudgeInput) (JudgeResult, error) {
	v, _, err := inf.JudgeWithResponse(ctx, in)
	return v, err
}

// JudgeWithResponse grades in AND returns the raw MessageResponse alongside the
// verdict, so a governed caller can emit the runtime cost/forensic the verdict alone
// hides (CLA-15/ANT2-15: the SEPARATE advisor sub-line, the billed-but-hidden
// thinking tokens, a non-billable refusal). The response is returned EVEN when the
// verdict fails to parse, because the Messages call was still made and BILLED — the
// caller must account that cost; it is suppressed only on a transport error (no
// response came back). This client persists nothing; the caller decides what reaches
// the ledger (a verdict + a redacted cost/finding, never the evaluated text).
func (inf *Inference) JudgeWithResponse(ctx context.Context, in JudgeInput) (JudgeResult, MessageResponse, error) {
	if inf.client == nil {
		return JudgeResult{}, MessageResponse{}, ErrNotConfigured
	}
	var user strings.Builder
	user.WriteString("CRITERION:\n")
	user.WriteString(strings.TrimSpace(in.Criterion))
	if s := strings.TrimSpace(in.Input); s != "" {
		user.WriteString("\n\nINPUT:\n")
		user.WriteString(s)
	}
	if s := strings.TrimSpace(in.Expected); s != "" {
		user.WriteString("\n\nEXPECTED:\n")
		user.WriteString(s)
	}
	user.WriteString("\n\nOUTPUT:\n")
	user.WriteString(in.Output)

	req := MessageRequest{
		Model:     in.ModelRef,
		MaxTokens: judgeMaxTokens,
		// The stable rubric is the cache breakpoint (default 5m TTL): repeated cases in
		// a suite reuse it at 0.1× read cost.
		System: []ContentBlock{CachedTextBlock(judgeSystemPrompt, "")},
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{TextBlock(user.String())},
		}},
	}
	// D6: constrain the verdict to the schema on a model that supports structured
	// outputs (GA, no beta header). The effective model is the request's or the client
	// default; an unsupported model would 400 on output_config.format, so it is gated —
	// parseJudgeVerdict's tolerant extraction remains the fallback either way.
	effModel := in.ModelRef
	if effModel == "" {
		effModel = inf.defaultModel
	}
	if SupportsStructuredOutputs(effModel) {
		req.OutputConfig = &OutputConfig{Format: JSONSchemaFormat(judgeOutputSchema)}
	}
	resp, err := inf.CreateMessage(ctx, req)
	if err != nil {
		return JudgeResult{}, MessageResponse{}, err
	}
	verdict, perr := parseJudgeVerdict(resp.Text())
	if perr != nil {
		// The call was billed; return resp so the caller still emits the incurred cost.
		return JudgeResult{}, resp, perr
	}
	return verdict, resp, nil
}

// parseJudgeVerdict extracts the JSON verdict object from the model's text and
// validates it. It tolerates surrounding prose by taking the outermost {...}. The
// forced "analysis" field is deliberately NOT decoded: the reasoning is consumed in
// flight and dropped here, so it can never reach a caller or the ledger
// (minimal-data — only score/passed/reason survive).
func parseJudgeVerdict(text string) (JudgeResult, error) {
	obj := extractJSONObject(text)
	if obj == "" {
		return JudgeResult{}, fmt.Errorf("claudeapi: judge returned no JSON verdict")
	}
	var raw struct {
		Score  float64 `json:"score"`
		Passed bool    `json:"passed"`
		Reason string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return JudgeResult{}, fmt.Errorf("claudeapi: judge verdict not valid JSON: %w", err)
	}
	score := raw.Score
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}
	return JudgeResult{Score: score, Passed: raw.Passed, Reason: strings.TrimSpace(raw.Reason)}, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}' (the
// outermost object), or "" if none. Tolerant of code-fence/prose wrapping.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

// ---- pairwise judging (bias-mitigated comparison) ----------------------------

// Pairwise winner labels. "first"/"second" are POSITIONS in the prompt, not variant
// identities: the caller performs the order-swap (two calls with the candidates
// exchanged) and maps positions back to its own labels, declaring a winner only when
// both orders agree (Zheng et al. 2023 — position-bias mitigation lives in the
// CALLER so any PairJudge implementation gets it).
const (
	PairWinnerFirst  = "first"
	PairWinnerSecond = "second"
	PairWinnerTie    = "tie"
)

// JudgePairInput is one ORDERED pairwise comparison: which of two candidate outputs
// better satisfies the criterion, as presented (first vs second).
type JudgePairInput struct {
	ModelRef  string
	Input     string
	Criterion string
	// OutputFirst/OutputSecond are the candidates IN PRESENTATION ORDER.
	OutputFirst  string
	OutputSecond string
}

// JudgePairResult is the model's ordered pairwise verdict: the winning POSITION
// (first|second|tie) and a short, non-sensitive reason.
type JudgePairResult struct {
	Winner string
	Reason string
}

// pairSystemPrompt mirrors judgeSystemPrompt for the pairwise comparison: forced
// reasoning-before-verdict (analysis first) plus the explicit position/verbosity
// controls. Stable + cache-marked like the pointwise rubric.
const pairSystemPrompt = `You are an impartial evaluation judge comparing two candidate responses. You are ` +
	`given a CRITERION, optionally the original INPUT, and two candidates: RESPONSE_1 and RESPONSE_2. Decide ` +
	`which response better satisfies the CRITERION. The order in which the responses are presented is ` +
	`arbitrary — it carries no information; do not favor either position. Length is not quality: never reward ` +
	`a response for being longer, more elaborate, more confident or more polished. Respond with ONLY a single ` +
	`JSON object and nothing else, of the exact form: {"analysis": "<your step-by-step comparison against the ` +
	`criterion, 2-5 sentences, written BEFORE you decide>", "winner": "<1|2|tie>", "reason": "<one short ` +
	`sentence, no sensitive detail>"}. Write "analysis" first and only then derive "winner" from it. "winner" ` +
	`is "1" if RESPONSE_1 better satisfies the CRITERION, "2" if RESPONSE_2 does, "tie" if they are equally ` +
	`good or equally bad. Do not echo the responses or any provided text verbatim.`

// pairOutputSchema constrains the pairwise verdict (analysis first — CoT-forcing,
// same rationale as judgeOutputSchema).
var pairOutputSchema = json.RawMessage(`{` +
	`"type":"object",` +
	`"properties":{"analysis":{"type":"string"},"winner":{"type":"string","enum":["1","2","tie"]},"reason":{"type":"string"}},` +
	`"required":["analysis","winner","reason"],` +
	`"additionalProperties":false}`)

// JudgePair grades one ORDERED pairwise comparison and discards the raw response.
func (inf *Inference) JudgePair(ctx context.Context, in JudgePairInput) (JudgePairResult, error) {
	v, _, err := inf.JudgePairWithResponse(ctx, in)
	return v, err
}

// JudgePairWithResponse grades one ORDERED pairwise comparison AND returns the raw
// MessageResponse so a governed caller can emit the runtime cost/forensic (the same
// contract as JudgeWithResponse: the response comes back even on a parse failure,
// because the call was billed; it is zero only on a transport error).
func (inf *Inference) JudgePairWithResponse(ctx context.Context, in JudgePairInput) (JudgePairResult, MessageResponse, error) {
	if inf.client == nil {
		return JudgePairResult{}, MessageResponse{}, ErrNotConfigured
	}
	var user strings.Builder
	user.WriteString("CRITERION:\n")
	user.WriteString(strings.TrimSpace(in.Criterion))
	if s := strings.TrimSpace(in.Input); s != "" {
		user.WriteString("\n\nINPUT:\n")
		user.WriteString(s)
	}
	user.WriteString("\n\nRESPONSE_1:\n")
	user.WriteString(in.OutputFirst)
	user.WriteString("\n\nRESPONSE_2:\n")
	user.WriteString(in.OutputSecond)

	req := MessageRequest{
		Model:     in.ModelRef,
		MaxTokens: judgeMaxTokens,
		System:    []ContentBlock{CachedTextBlock(pairSystemPrompt, "")},
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{TextBlock(user.String())},
		}},
	}
	effModel := in.ModelRef
	if effModel == "" {
		effModel = inf.defaultModel
	}
	if SupportsStructuredOutputs(effModel) {
		req.OutputConfig = &OutputConfig{Format: JSONSchemaFormat(pairOutputSchema)}
	}
	resp, err := inf.CreateMessage(ctx, req)
	if err != nil {
		return JudgePairResult{}, MessageResponse{}, err
	}
	verdict, perr := parsePairVerdict(resp.Text())
	if perr != nil {
		return JudgePairResult{}, resp, perr
	}
	return verdict, resp, nil
}

// parsePairVerdict extracts and validates the pairwise verdict. Like
// parseJudgeVerdict it drops the forced analysis (in-flight reasoning only) and
// tolerates prose wrapping; an unknown winner value is an error, never a guess.
func parsePairVerdict(text string) (JudgePairResult, error) {
	obj := extractJSONObject(text)
	if obj == "" {
		return JudgePairResult{}, fmt.Errorf("claudeapi: pair judge returned no JSON verdict")
	}
	var raw struct {
		Winner string `json:"winner"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return JudgePairResult{}, fmt.Errorf("claudeapi: pair verdict not valid JSON: %w", err)
	}
	var winner string
	switch strings.ToLower(strings.TrimSpace(raw.Winner)) {
	case "1":
		winner = PairWinnerFirst
	case "2":
		winner = PairWinnerSecond
	case "tie":
		winner = PairWinnerTie
	default:
		return JudgePairResult{}, fmt.Errorf("claudeapi: pair verdict winner %q is not 1|2|tie", raw.Winner)
	}
	return JudgePairResult{Winner: winner, Reason: strings.TrimSpace(raw.Reason)}, nil
}
