// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestRuntimeObservationsBillable proves the live cost/forensic assembly (CLA-15/
// ANT2-15): a billable response yields the top-level token line PLUS one SEPARATE
// advisor sub-line (CostType=advisor), the thinking tokens are accounted INSIDE the
// top-level output cost (never a second line), and the thinking/advisor/programmatic
// forensic findings ride alongside.
func TestRuntimeObservationsBillable(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Gateway: model.GatewayDirect})
	r := MessageResponse{
		ID: "msg_rt", Model: "claude-opus-4-8", StopReason: "end_turn",
		Usage: MessageUsage{
			InputTokens:         1000,
			OutputTokens:        500, // INCLUDES the 300 thinking tokens (a subset, not extra)
			OutputTokensDetails: &OutputTokensDetails{ThinkingTokens: 300},
			Iterations: []MessageIteration{
				{AdvisorMessage: &AdvisorUsage{Model: "claude-haiku-4-5", InputTokens: 100, OutputTokens: 50}},
			},
		},
	}
	samples, findings := inf.RuntimeObservations(r, "sess_rt", atTime(), true)

	// Exactly two cost lines: the top-level base line + the advisor sub-line. NO third
	// "thinking" line — thinking is already in the top-level output cost.
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2 (top-level + advisor; thinking is NOT a separate line)", len(samples))
	}
	var top, advisor int
	for _, cs := range samples {
		switch cs.CostType {
		case "advisor":
			advisor++
			if cs.ModelRef != "claude-haiku-4-5" || cs.InputTokens != 100 || cs.OutputTokens != 50 {
				t.Errorf("advisor sub-line = %+v, want haiku 100/50", cs)
			}
		case "":
			top++
			if cs.OutputTokens != 500 {
				t.Errorf("top-level OutputTokens = %d, want 500 (the 300 thinking tokens accounted here, once)", cs.OutputTokens)
			}
			if cs.Provenance != model.ProvenanceEstimated {
				t.Errorf("top-level Provenance = %q, want estimated (reconciles vs billed)", cs.Provenance)
			}
		default:
			t.Errorf("unexpected CostType %q", cs.CostType)
		}
	}
	if top != 1 || advisor != 1 {
		t.Errorf("CostType split = top %d / advisor %d, want 1/1", top, advisor)
	}

	// Forensic findings: thinking billed + advisor ran + the programmatic under-count
	// caveat (flag=true). Each is informational and redacted.
	subj := map[string]int{}
	for _, f := range findings {
		subj[f.SubjectKind]++
	}
	if subj[subjectThinking] != 1 || subj[subjectAdvisor] != 1 || subj[subjectProgrammatic] != 1 {
		t.Errorf("finding subjects = %v, want thinking+advisor+programmatic each once", subj)
	}
}

// TestRuntimeObservationsRefusalNoCost proves a refusal emits the security finding and
// NO cost line at all (ANT2-15: not billed since 2026-06-02).
func TestRuntimeObservationsRefusalNoCost(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Gateway: model.GatewayDirect})
	ref := MessageResponse{
		ID: "msg_ref", Model: "claude-opus-4-8", StopReason: "refusal",
		StopDetails: &StopDetails{Type: "refusal", Category: "bio"},
		Usage:       MessageUsage{InputTokens: 80, OutputTokens: 0},
	}
	samples, findings := inf.RuntimeObservations(ref, "sess_ref", atTime(), false)
	if len(samples) != 0 {
		t.Errorf("a refusal emitted %d cost samples, want 0 (not billed)", len(samples))
	}
	if len(findings) != 1 || findings[0].SubjectKind != subjectRefusal {
		t.Errorf("refusal findings = %+v, want exactly one refusal security signal", findings)
	}
}

// TestRuntimeObservationsNoProgrammaticByDefault proves the under-count caveat is NOT
// emitted unless the caller actually enabled programmatic tool calling.
func TestRuntimeObservationsNoProgrammaticByDefault(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Gateway: model.GatewayDirect})
	r := MessageResponse{ID: "m", Model: "claude-opus-4-8", StopReason: "end_turn", Usage: MessageUsage{InputTokens: 1, OutputTokens: 1}}
	_, findings := inf.RuntimeObservations(r, "s", atTime(), false)
	for _, f := range findings {
		if f.SubjectKind == subjectProgrammatic {
			t.Error("programmatic under-count caveat emitted without the caller enabling it")
		}
	}
}

// TestJudgeWithResponseReturnsRawResponse proves the judge path can hand its caller the
// raw MessageResponse (so the verdict no longer hides the runtime cost), and that a
// non-zero response is returned even though only the verdict was needed before.
func TestJudgeWithResponseReturnsRawResponse(t *testing.T) {
	doer := &bodyCapturingDoer{resp: `{"id":"msg_j","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"{\"score\":0.9,\"passed\":true,\"reason\":\"ok\"}"}],"usage":{"input_tokens":10,"output_tokens":20}}`}
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Doer: doer})
	v, resp, err := inf.JudgeWithResponse(context.Background(), JudgeInput{Output: "x", Criterion: "y"})
	if err != nil {
		t.Fatalf("JudgeWithResponse: %v", err)
	}
	if !v.Passed || v.Score != 0.9 {
		t.Errorf("verdict = %+v, want passed/0.9", v)
	}
	if resp.ID != "msg_j" || resp.Usage.OutputTokens != 20 {
		t.Errorf("raw response not surfaced: %+v", resp)
	}
	// And the same response feeds the live emitter (a top-level cost line).
	samples, _ := inf.RuntimeObservations(resp, "sess_j", atTime(), false)
	if len(samples) != 1 || samples[0].OutputTokens != 20 {
		t.Errorf("emitter from judge response = %+v, want one line with 20 output tokens", samples)
	}
}
