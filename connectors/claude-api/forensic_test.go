// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func atTime() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

// TestRefusalNonBillable proves a cyber/bio refusal yields a security finding and is
// flagged non-billable (ANT2-15: not charged since 2026-06-02).
func TestRefusalNonBillable(t *testing.T) {
	r := MessageResponse{
		ID: "msg_1", Model: "claude-opus-4-8", StopReason: "refusal",
		StopDetails: &StopDetails{Type: "refusal", Category: "cyber"},
		Usage:       MessageUsage{InputTokens: 100, OutputTokens: 0},
	}
	if r.IsBillable() {
		t.Error("a refusal must be non-billable (ANT2-15)")
	}
	f, ok := r.RefusalSignal("sess_1", atTime())
	if !ok {
		t.Fatal("refusal must produce a security signal")
	}
	if f.Kind != "security" || f.Severity != model.SeverityHigh || f.SubjectKind != subjectRefusal {
		t.Errorf("refusal finding = %+v, want security/high/refusal", f)
	}
	// A non-refusal yields no signal and is billable.
	ok2 := MessageResponse{StopReason: "end_turn"}.IsBillable()
	if !ok2 {
		t.Error("a normal end_turn must be billable")
	}
}

// TestAdvisorSeparateCostLine proves the advisor sub-inference is exposed as its OWN
// cost line (CostType=advisor), distinct from the top-level usage (ANT2-15), plus a
// forensic signal that the full transcript was read.
func TestAdvisorSeparateCostLine(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Gateway: model.GatewayDirect})
	r := MessageResponse{
		ID: "msg_2", Model: "claude-opus-4-8",
		Usage: MessageUsage{
			InputTokens:  1000,
			OutputTokens: 500,
			Iterations: []MessageIteration{
				{AdvisorMessage: &AdvisorUsage{Model: "claude-haiku-4-5", InputTokens: 2000, OutputTokens: 100}},
			},
		},
	}
	samples := inf.AdvisorCostSamples(r, "sess_2", atTime())
	if len(samples) != 1 {
		t.Fatalf("advisor cost samples = %d, want 1", len(samples))
	}
	cs := samples[0]
	if cs.CostType != "advisor" {
		t.Errorf("advisor cost CostType = %q, want advisor", cs.CostType)
	}
	if cs.ModelRef != "claude-haiku-4-5" || cs.InputTokens != 2000 || cs.OutputTokens != 100 {
		t.Errorf("advisor cost = %+v, want haiku 2000/100", cs)
	}
	// Haiku 4.5 = $1/$5 per MTok → 2000*1 + 100*5 = 2500 micro-USD.
	if cs.CostMicroUSD != 2500 {
		t.Errorf("advisor cost = %d µUSD, want 2500", cs.CostMicroUSD)
	}
	if f, ok := r.AdvisorForensicSignal("sess_2", atTime()); !ok || f.SubjectKind != subjectAdvisor {
		t.Errorf("advisor forensic signal missing/wrong: %+v ok=%v", f, ok)
	}
}

// TestThinkingAndProgrammaticSignals covers the remaining ANT2-15 blind-spots.
func TestThinkingAndProgrammaticSignals(t *testing.T) {
	r := MessageResponse{
		ID: "msg_3", Model: "claude-opus-4-8",
		Usage: MessageUsage{OutputTokens: 1200, OutputTokensDetails: &OutputTokensDetails{ThinkingTokens: 900}},
	}
	if r.ThinkingTokens() != 900 {
		t.Errorf("thinking tokens = %d, want 900", r.ThinkingTokens())
	}
	if f, ok := r.ThinkingSignal("sess_3", atTime()); !ok || f.SubjectKind != subjectThinking {
		t.Errorf("thinking signal missing/wrong: %+v ok=%v", f, ok)
	}
	pc := ProgrammaticToolCallingCaveat("sess_3", atTime())
	if pc.SubjectKind != subjectProgrammatic || pc.Severity != model.SeverityInfo {
		t.Errorf("programmatic caveat = %+v, want programmatic/info", pc)
	}
}

// TestUsageForPropagatesInferenceGeo proves the per-request residency proof flows onto
// the derived Usage (ANT2-17).
func TestUsageForPropagatesInferenceGeo(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-opus-4-8", Gateway: model.GatewayDirect})
	r := MessageResponse{Model: "claude-opus-4-8", Usage: MessageUsage{InputTokens: 10, OutputTokens: 5, InferenceGeo: "us"}}
	u := inf.UsageFor(r, "sess", atTime())
	if u.InferenceGeo != "us" {
		t.Errorf("UsageFor InferenceGeo = %q, want us", u.InferenceGeo)
	}
}

// bodyCapturingDoer records the request body so a test can assert what was sent.
type bodyCapturingDoer struct {
	body []byte
	resp string
}

func (d *bodyCapturingDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		d.body, _ = io.ReadAll(req.Body)
	}
	body := d.resp
	if body == "" {
		body = `{"id":"msg","model":"claude-opus-4-8","stop_reason":"end_turn","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: make(http.Header)}, nil
}

// TestCreateMessageWithholdsSamplingParams proves the ANT2-03 pre-advice in the
// runtime: a non-default temperature is NOT sent to an Opus 4.7+ model (which would
// 400), but IS sent to a model that accepts it.
func TestCreateMessageWithholdsSamplingParams(t *testing.T) {
	temp := 0.7
	for _, tc := range []struct {
		modelID  string
		wantTemp bool
	}{
		{"claude-opus-4-8", false}, // rejects → withheld
		{"claude-opus-4-6", true},  // accepts → sent
	} {
		doer := &bodyCapturingDoer{}
		inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: tc.modelID, Doer: doer})
		_, err := inf.CreateMessage(context.Background(), MessageRequest{
			MaxTokens: 16, Temperature: &temp,
			Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
		})
		if err != nil {
			t.Fatalf("CreateMessage(%s): %v", tc.modelID, err)
		}
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(doer.body, &sent); err != nil {
			t.Fatalf("decode sent body: %v", err)
		}
		_, hasTemp := sent["temperature"]
		if hasTemp != tc.wantTemp {
			t.Errorf("model %s: temperature sent=%v, want %v (body=%s)", tc.modelID, hasTemp, tc.wantTemp, strings.TrimSpace(string(doer.body)))
		}
	}
}
