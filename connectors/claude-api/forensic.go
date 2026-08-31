// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file extracts the COST/FORENSIC BLIND-SPOTS of the Claude runtime (ANT2-15) a
// naive usage read misses. It returns observations to the CALLER (the composition
// root / agentic runtime), which emits them to the ledger — the inference client
// itself holds no sink and persists nothing. Four blind-spots:
//
//   - advisor (advisor_20260301): a SECOND server-side inference over the full
//     transcript, billed in usage.iterations[].advisor_message APART from the
//     top-level usage. Exposed as a SEPARATE cost line (never invisible) AND a forensic
//     signal that the whole transcript was read. ZDR-eligible; may run a separate tier.
//   - programmatic tool calling (code_execution_20260120 caller): intermediate results
//     land OUTSIDE the context AND OUTSIDE usage → FinOps/forensic UNDER-count. The
//     caveat is surfaced explicitly (never a claim of total tool-call coverage), and
//     allowed_callers is NOT a security boundary.
//   - refusal (stop_reason="refusal", stop_details.category ∈ {cyber, bio}): a security
//     signal — and NOT billed since 2026-06-02, so it must not be counted as cost.
//   - extended thinking (output_tokens_details.thinking_tokens, display:omitted):
//     thinking tokens are BILLED even with no visible content, and the content is
//     ALWAYS redacted (the OBS-10 matrix has no flag for it).
//
// Authority (verbatim, jun-2026): …/tool-use/{advisor-tool,programmatic-tool-calling};
// …/handling-stop-reasons; …/extended-thinking (ANT2-15).
package claudeapi

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// Forensic finding subjects.
const (
	subjectAdvisor      = "anthropic.advisor"
	subjectProgrammatic = "anthropic.programmatic_tool_calling"
	subjectRefusal      = "anthropic.refusal"
	subjectThinking     = "anthropic.extended_thinking"
)

// IsRefusal reports whether the response is a model refusal. A refusal before any
// output is NOT billed (ANT2-15, since 2026-06-02), so a caller must not emit a cost
// sample for it.
func (r MessageResponse) IsRefusal() bool {
	return r.StopReason == "refusal" || (r.StopDetails != nil && r.StopDetails.Type == "refusal")
}

// IsBillable reports whether the response should incur a cost sample. A refusal
// BEFORE any output is non-billable (token counts appear in usage but are not
// charged, and the request does not count against rate limits); a MID-STREAM refusal
// — output already streamed — bills the input and that partial output (refined by
// the Fable 5 refusals-and-fallback page, verified 2026-06-09). Non-refusals always
// bill.
func (r MessageResponse) IsBillable() bool {
	return !r.IsRefusal() || r.Usage.OutputTokens > 0
}

// RefusalSignal returns a security FindingReport when the response is a refusal
// (ANT2-15). The category is the classifier's attribution — cyber/bio grade
// High (a safety-class refusal is a stronger signal); reasoning_extraction and an
// uncategorized refusal grade Medium. The unstable explanation text travels ONLY in
// the redacted hash, never the title; a fallback credit token is recorded ONLY as
// its presence (credit_available) — the token itself is a secret and never leaves
// the response. ok is false when the response is not a refusal.
func (r MessageResponse) RefusalSignal(sessionRef string, at time.Time) (model.FindingReport, bool) {
	if !r.IsRefusal() {
		return model.FindingReport{}, false
	}
	cat, explanation, credit := "unspecified", "", false
	if d := r.StopDetails; d != nil {
		if d.Category != "" {
			cat = d.Category
		}
		explanation = d.Explanation
		credit = d.FallbackCreditToken != ""
	}
	sev := model.SeverityMedium
	if cat == "cyber" || cat == "bio" {
		sev = model.SeverityHigh // a safety-class refusal is a stronger signal
	}
	title := "Model refusal (category " + cat + ")"
	if credit {
		title += " — fallback credit available"
	}
	return model.FindingReport{
		Kind:        "security",
		Severity:    sev,
		SubjectKind: subjectRefusal,
		SubjectRef:  refOrSession(sessionRef, r.ID),
		Title:       title,
		DetailHash: redact.Hash(fmt.Sprintf(
			"refusal category=%s model=%s credit_available=%t explanation=%s; pre-output refusal not billed (ANT2-15)",
			cat, r.Model, credit, explanation)),
		OccurredAt: at,
	}, true
}

// ThinkingTokens returns the billed-but-hidden extended-thinking token count (ANT2-15).
func (r MessageResponse) ThinkingTokens() int64 {
	if r.Usage.OutputTokensDetails == nil {
		return 0
	}
	return r.Usage.OutputTokensDetails.ThinkingTokens
}

// ThinkingSignal returns an informational finding when the response billed extended-
// thinking tokens (ANT2-15): they are charged even when display:omitted, and the
// content is ALWAYS redacted (no OBS-10 flag opts it in). ok is false when none.
func (r MessageResponse) ThinkingSignal(sessionRef string, at time.Time) (model.FindingReport, bool) {
	tt := r.ThinkingTokens()
	if tt == 0 {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "forensic",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectThinking,
		SubjectRef:  refOrSession(sessionRef, r.ID),
		Title:       fmt.Sprintf("Extended-thinking billed %d token(s) (content always redacted)", tt),
		DetailHash:  redact.Hash(fmt.Sprintf("thinking_tokens=%d billed even if display:omitted; content always redacted (ANT2-15/OBS-10)", tt)),
		OccurredAt:  at,
	}, true
}

// AdvisorCostSamples returns the advisor sub-inference(s) as SEPARATE cost lines
// (ANT2-15): one CostSample per iteration that carried an advisor_message, tagged
// CostType="advisor" so FinOps shows the advisor spend distinctly from the top-level
// usage (no double-count — it IS separate spend). Cost is derived from list pricing
// (estimated); an unknown advisor model leaves cost 0 rather than guessing.
func (inf *Inference) AdvisorCostSamples(r MessageResponse, sessionRef string, at time.Time) []model.CostSample {
	if at.IsZero() {
		at = inf.clock().UTC()
	}
	var out []model.CostSample
	for _, it := range r.Usage.Iterations {
		a := it.AdvisorMessage
		if a == nil {
			continue
		}
		mref := a.Model
		if mref == "" {
			mref = r.Model
		}
		cs := model.CostSample{
			ProviderRef:  modelprovider.ProviderAnthropic,
			ModelRef:     mref,
			SessionRef:   sessionRef,
			InputTokens:  a.InputTokens,
			OutputTokens: a.OutputTokens,
			OccurredAt:   at,
			Gateway:      inf.gateway,
			Provenance:   model.ProvenanceEstimated,
			CostType:     "advisor", // SEPARATE line, distinct from top-level token cost
		}
		if p, _, _, ok := pricingFor(mref); ok {
			cs.CostMicroUSD = int64(math.Round(
				float64(a.InputTokens)*p.InputPerMTokUSD + float64(a.OutputTokens)*p.OutputPerMTokUSD))
		}
		out = append(out, cs)
	}
	return out
}

// hasFallbackIterations reports whether usage.iterations records a server-side
// fallback chain: at least one fallback_message attempt (a normal chain ends
// in one; a sticky-served turn carries ONLY one). Advisor-only iterations do not
// qualify.
func (r MessageResponse) hasFallbackIterations() bool {
	for _, it := range r.Usage.Iterations {
		if it.Type == iterationFallbackMessage {
			return true
		}
	}
	return false
}

// Iteration entry types of the server-side fallback chain.
const (
	iterationMessage         = "message"          // an attempt by a model that declined
	iterationFallbackMessage = "fallback_message" // the fallback model that served
)

// FallbackCostSamples returns the PER-ATTEMPT cost lines of a server-side fallback
// chain: usage.iterations is the per-attempt billing record — each attempt is
// billed separately at ITS model's rates, and the top-level usage describes only the
// serving attempt — so when a chain ran, attribution must come from here, never from
// the top level (which would bill the serving tokens onto the REQUESTED model id).
// Per the published billing rules: an attempt with NO output tokens cost nothing
// (declined before output — not billed, no rate-limit draw) and yields no line; a
// declined attempt with streamed output (mid-stream decline) IS billed and is tagged
// CostType "fallback_attempt"; the serving fallback_message line is the normal token
// cost (empty CostType), attributed to the model that actually served. The serving
// line carries the top-level service tier / inference geo (the top-level usage
// describes exactly that attempt). Untiered per-attempt cache creation maps to the
// default 5m tier, as everywhere else. Unknown models stay cost-0, never guessed.
func (inf *Inference) FallbackCostSamples(r MessageResponse, sessionRef string, at time.Time) []model.CostSample {
	if at.IsZero() {
		at = inf.clock().UTC()
	}
	var out []model.CostSample
	for _, it := range r.Usage.Iterations {
		if it.Type != iterationMessage && it.Type != iterationFallbackMessage {
			continue // advisor iterations bill via AdvisorCostSamples
		}
		if it.OutputTokens == 0 {
			continue // declined before output: costs nothing, never a line
		}
		u := modelprovider.Usage{
			ProviderRef:           modelprovider.ProviderAnthropic,
			ModelRef:              it.Model,
			SessionRef:            sessionRef,
			InputTokens:           it.InputTokens,
			OutputTokens:          it.OutputTokens,
			CacheCreation5mTokens: it.CacheCreationInputTokens,
			CacheReadTokens:       it.CacheReadInputTokens,
			OccurredAt:            at,
			Gateway:               inf.gateway,
			Provenance:            model.ProvenanceEstimated,
		}
		if it.Type == iterationMessage {
			u.CostType = "fallback_attempt" // billed mid-stream decline, distinct line
		} else {
			// The serving attempt IS what the top-level usage describes.
			u.ServiceTier = r.Usage.ServiceTier
			u.InferenceGeo = r.Usage.InferenceGeo
		}
		if p, _, _, ok := pricingFor(it.Model); ok {
			out = append(out, modelprovider.ToCostSample(u, p))
		} else {
			out = append(out, modelprovider.ToCostSampleWithCost(u, 0))
		}
	}
	return out
}

// FallbackSignal returns the guardrail-evidence finding for a fallback-served
// response: the requested model's classifier declined and another model
// served — a refusal ABSORBED by the chain, which a monitor built on refusal stop
// reasons alone never sees (the response is a success). A sticky-served turn (no
// declining attempt recorded — the routing decision was reused) is titled as such.
// ok is false when no fallback ran.
func (r MessageResponse) FallbackSignal(sessionRef string, at time.Time) (model.FindingReport, bool) {
	if !r.hasFallbackIterations() {
		return model.FindingReport{}, false
	}
	var declined []string
	serving := r.Model
	for _, it := range r.Usage.Iterations {
		switch it.Type {
		case iterationMessage:
			declined = append(declined, it.Model)
		case iterationFallbackMessage:
			if it.Model != "" {
				serving = it.Model
			}
		}
	}
	title := "Fallback-served (sticky): " + serving
	if len(declined) > 0 {
		title = "Refusal absorbed by fallback: " + strings.Join(declined, ", ") + " -> " + serving
	}
	return model.FindingReport{
		Kind:        "security",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectRefusal,
		SubjectRef:  refOrSession(sessionRef, r.ID),
		Title:       title,
		DetailHash: redact.Hash(fmt.Sprintf(
			"fallback chain declined=%s serving=%s iterations=%d (per-attempt billing in usage.iterations)",
			strings.Join(declined, ","), serving, len(r.Usage.Iterations))),
		OccurredAt: at,
	}, true
}

// CostSample derives the TOP-LEVEL token cost line for a Messages response from
// list pricing (estimated; ANT2-15). The output-token cost it carries ALREADY
// INCLUDES the billed extended-thinking tokens — they are a SUBSET of
// usage.output_tokens, not an extra line — so thinking is accounted here exactly
// once, never double-counted as its own CostSample (ThinkingSignal records it
// forensically; it is not a separate spend). ok is false for a non-billable refusal
// (no cost line at all). A model with no price-book entry still yields a usage line
// with cost 0 — honest token attribution, never a fabricated figure.
func (inf *Inference) CostSample(r MessageResponse, sessionRef string, at time.Time) (model.CostSample, bool) {
	if !r.IsBillable() {
		return model.CostSample{}, false
	}
	if at.IsZero() {
		at = inf.clock().UTC()
	}
	u := inf.UsageFor(r, sessionRef, at)
	if p, _, _, ok := pricingFor(u.ModelRef); ok {
		return modelprovider.ToCostSample(u, p), true
	}
	return modelprovider.ToCostSampleWithCost(u, 0), true
}

// RuntimeObservations assembles the FULL set of cost + forensic observations a
// caller emits after one governed Messages call (CLA-15/ANT2-15). It RETURNS
// them: the inference client holds no sink and persists nothing (see the file
// header), so the composition root publishes these to the bus, where FinOps ingests
// the cost lines and the ledger records the findings. Contract:
//
//   - A REFUSAL emits the security finding. A pre-output refusal is NOT billed —
//     never a CostSample; a MID-STREAM refusal bills the input and the streamed
//     partial output (the Fable 5 refusals page), so it DOES carry its cost line.
//   - A server-side FALLBACK chain (usage.iterations carries a fallback_message
//     entry) replaces the top-level cost line with the PER-ATTEMPT lines of
//     FallbackCostSamples — the top level describes only the serving attempt and
//     would mis-attribute its tokens to the REQUESTED model id — plus the
//     guardrail-evidence finding (FallbackSignal). This covers the all-declined
//     chain on the refusal path too (billed mid-stream attempts keep their lines).
//   - Otherwise: the top-level token cost line (thinking tokens included exactly
//     once, see CostSample) PLUS one SEPARATE advisor sub-line per advisor iteration
//     (CostType="advisor"), so the advisor spend shows distinctly and is never a
//     second row of the same spend.
//   - Forensic findings (always redacted, informational): extended-thinking billed
//     (even when display:omitted) and the advisor having read the full transcript.
//   - When the caller enabled PROGRAMMATIC tool calling (code_execution_20260120),
//     the explicit UNDER-COUNT caveat — intermediate results are outside usage, so
//     this is never a claim of total tool-call coverage; allowed_callers is not a
//     boundary.
//
// at defaults to the client clock when zero.
// ToolSearchActive reports whether the request carried any tool with defer_loading
// set — meaning tool search may load tools post-hoc (not in the initial declared
// set). The caller needs this to emit the governance-level tool-visibility signal.
func ToolSearchActive(tools []MCPToolConfig) bool {
	for _, t := range tools {
		if t.DeferLoading {
			return true
		}
	}
	return false
}

func (inf *Inference) RuntimeObservations(r MessageResponse, sessionRef string, at time.Time, programmaticToolCalling bool) (samples []model.CostSample, findings []model.FindingReport) {
	if at.IsZero() {
		at = inf.clock().UTC()
	}
	fellBack := r.hasFallbackIterations()
	// Refusal: a safety signal (ANT2-15). Pre-output: never a charge. Mid-stream:
	// the streamed partial output IS billed — per-attempt when a chain ran, else the
	// top-level line.
	if r.IsRefusal() {
		if f, ok := r.RefusalSignal(sessionRef, at); ok {
			findings = append(findings, f)
		}
		if fellBack {
			samples = append(samples, inf.FallbackCostSamples(r, sessionRef, at)...)
		} else if cs, ok := inf.CostSample(r, sessionRef, at); ok {
			samples = append(samples, cs)
		}
		return samples, findings
	}
	if fellBack {
		// The chain's per-attempt record replaces the top-level line (which describes
		// only the serving attempt and carries the requested model id).
		samples = append(samples, inf.FallbackCostSamples(r, sessionRef, at)...)
		if f, ok := r.FallbackSignal(sessionRef, at); ok {
			findings = append(findings, f)
		}
	} else if cs, ok := inf.CostSample(r, sessionRef, at); ok {
		samples = append(samples, cs)
	}
	samples = append(samples, inf.AdvisorCostSamples(r, sessionRef, at)...)
	if f, ok := r.ThinkingSignal(sessionRef, at); ok {
		findings = append(findings, f)
	}
	if f, ok := r.AdvisorForensicSignal(sessionRef, at); ok {
		findings = append(findings, f)
	}
	if programmaticToolCalling {
		findings = append(findings, ProgrammaticToolCallingCaveat(sessionRef, at))
	}
	// governance-level tool-visibility signal — always emitted so the
	// governance plane has an explicit record of whether per-tool DLP/access-map
	// coverage is full or partial.
	findings = append(findings, ToolVisibilitySignal(sessionRef, at, programmaticToolCalling, false))
	return samples, findings
}

// AdvisorForensicSignal returns a forensic finding when the advisor ran (ANT2-15):
// the advisor reads the FULL transcript, so its invocation is a forensic signal worth
// recording. ok is false when no advisor iteration is present.
func (r MessageResponse) AdvisorForensicSignal(sessionRef string, at time.Time) (model.FindingReport, bool) {
	n := 0
	for _, it := range r.Usage.Iterations {
		if it.AdvisorMessage != nil {
			n++
		}
	}
	if n == 0 {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "forensic",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAdvisor,
		SubjectRef:  refOrSession(sessionRef, r.ID),
		Title:       fmt.Sprintf("Advisor ran %d server-side inference(s) over the full transcript", n),
		DetailHash:  redact.Hash(fmt.Sprintf("advisor_20260301 iterations=%d read full transcript; billed separately (ANT2-15)", n)),
		OccurredAt:  at,
	}, true
}

// ProgrammaticToolCallingCaveat returns the explicit UNDER-COUNT caveat a caller emits
// when it enabled programmatic tool calling (code_execution_20260120 caller, ANT2-15):
// intermediate tool results are OUTSIDE the context AND OUTSIDE usage, so FinOps and
// forensic under-count — surfaced explicitly, never a claim of total coverage. It also
// records that allowed_callers is NOT a security boundary.
func ProgrammaticToolCallingCaveat(sessionRef string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "forensic",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectProgrammatic,
		SubjectRef:  sessionRef,
		Title:       "Programmatic tool calling: intermediate results are outside usage (FinOps/forensic under-count)",
		DetailHash:  redact.Hash("code_execution_20260120 caller: intermediate tool results outside context AND outside usage → under-count; allowed_callers is NOT a security boundary (ANT2-15)"),
		OccurredAt:  at,
	}
}

// refOrSession prefers the session ref, falling back to a response id, so a signal is
// always attributable to something even when the caller did not thread a session.
func refOrSession(sessionRef, fallback string) string {
	if sessionRef != "" {
		return sessionRef
	}
	return fallback
}
