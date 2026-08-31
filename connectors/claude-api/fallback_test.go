// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// boolPtr returns a pointer to b (for the tri-state fallback_has_prefill_claim).
func boolPtr(b bool) *bool { return &b }

// TestValidateFallbacks pins the published chain constraints enforced client-side
//: max 3 entries, named, distinct from each other and from the requested
// model, and mutual exclusion with a credit retry.
func TestValidateFallbacks(t *testing.T) {
	base := MessageRequest{Model: "claude-fable-5"}
	cases := []struct {
		name    string
		mutate  func(*MessageRequest)
		wantErr string
	}{
		{"none", func(r *MessageRequest) {}, ""},
		{"one", func(r *MessageRequest) { r.Fallbacks = []Fallback{{Model: "claude-opus-4-8"}} }, ""},
		{"three", func(r *MessageRequest) {
			r.Fallbacks = []Fallback{{Model: "a"}, {Model: "b"}, {Model: "c"}}
		}, ""},
		{"four", func(r *MessageRequest) {
			r.Fallbacks = []Fallback{{Model: "a"}, {Model: "b"}, {Model: "c"}, {Model: "d"}}
		}, "at most 3"},
		{"empty model", func(r *MessageRequest) { r.Fallbacks = []Fallback{{}} }, "requires a model"},
		{"equals primary", func(r *MessageRequest) {
			r.Fallbacks = []Fallback{{Model: "claude-fable-5"}}
		}, "equals the requested model"},
		{"duplicate", func(r *MessageRequest) {
			r.Fallbacks = []Fallback{{Model: "claude-opus-4-8"}, {Model: "claude-opus-4-8"}}
		}, "duplicate"},
		{"with credit token", func(r *MessageRequest) {
			r.Fallbacks = []Fallback{{Model: "claude-opus-4-8"}}
			r.FallbackCreditToken = "tok"
		}, "mutually exclusive"},
		// A credit retry WITHOUT fallbacks is the documented shape — valid.
		{"credit only", func(r *MessageRequest) { r.FallbackCreditToken = "tok" }, ""},
	}
	for _, c := range cases {
		req := base
		c.mutate(&req)
		err := validateFallbacks(req)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

// TestBetaHeaders_Fallbacks pins the request introspection: a fallbacks chain
// adds exactly the server-side beta, a credit token exactly the credit beta, and a
// request with neither stays byte-compatible (no header).
func TestBetaHeaders_Fallbacks(t *testing.T) {
	plain := MessageRequest{Model: "claude-fable-5", Messages: []Message{{Role: roleUser, Content: []ContentBlock{TextBlock("hi")}}}}
	if hs := plain.BetaHeaders(); len(hs) != 0 {
		t.Fatalf("plain request betas = %v, want none (byte-compat invariant)", hs)
	}
	chain := plain
	chain.Fallbacks = []Fallback{{Model: "claude-opus-4-8"}}
	if hs := chain.BetaHeaders(); len(hs) != 1 || hs[0] != BetaServerSideFallback {
		t.Fatalf("fallbacks betas = %v, want exactly [%s]", chain.BetaHeaders(), BetaServerSideFallback)
	}
	retry := plain
	retry.FallbackCreditToken = "tok"
	if hs := retry.BetaHeaders(); len(hs) != 1 || hs[0] != BetaFallbackCredit {
		t.Fatalf("credit betas = %v, want exactly [%s]", retry.BetaHeaders(), BetaFallbackCredit)
	}
}

// TestRefusalSignal_StopDetails pins the extended stop_details handling:
// reasoning_extraction grades Medium (cyber/bio stay High), the unstable explanation
// rides ONLY in the redacted hash (never the title), and a fallback credit token is
// recorded ONLY as its presence — the token value must appear NOWHERE in the finding.
func TestRefusalSignal_StopDetails(t *testing.T) {
	const token = "ctk_SECRET_OPAQUE_VALUE"
	const explanation = "This request asks the model to reproduce its internal reasoning."
	r := MessageResponse{
		ID: "msg_re", Model: "claude-fable-5", StopReason: "refusal",
		StopDetails: &StopDetails{
			Type: "refusal", Category: "reasoning_extraction", Explanation: explanation,
			FallbackCreditToken: token, FallbackHasPrefillClaim: boolPtr(true),
		},
	}
	f, ok := r.RefusalSignal("sess", atTime())
	if !ok {
		t.Fatal("refusal yielded no signal")
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("reasoning_extraction severity = %s, want medium (cyber/bio are the High classes)", f.Severity)
	}
	if !strings.Contains(f.Title, "reasoning_extraction") || !strings.Contains(f.Title, "fallback credit available") {
		t.Errorf("title = %q, want the category and the credit PRESENCE marker", f.Title)
	}
	if strings.Contains(f.Title, token) || strings.Contains(f.Title, explanation) {
		t.Fatalf("title leaks the token or the unstable explanation: %q", f.Title)
	}
	// The hash tuple carries the explanation and credit_available — NEVER the token.
	wantHash := redact.Hash("refusal category=reasoning_extraction model=claude-fable-5 credit_available=true explanation=" +
		explanation + "; pre-output refusal not billed (ANT2-15)")
	if f.DetailHash != wantHash {
		t.Errorf("detail hash mismatch — the hashed tuple must be stable and token-free")
	}
	// Cyber keeps High.
	r.StopDetails = &StopDetails{Type: "refusal", Category: "cyber"}
	if f, _ := r.RefusalSignal("sess", atTime()); f.Severity != model.SeverityHigh {
		t.Errorf("cyber severity = %s, want high", f.Severity)
	}
}

// TestRuntimeObservations_FallbackChain pins the per-attempt attribution: a
// chain where Fable 5 declined pre-output and Opus 4.8 served yields EXACTLY one
// cost line — on the SERVING model at ITS rates (412*5 + 264*25 = 8660 µUSD), never
// on the requested id — plus the absorbed-refusal evidence finding. The declined
// 0-output attempt costs nothing.
func TestRuntimeObservations_FallbackChain(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-fable-5", Gateway: model.GatewayDirect})
	r := MessageResponse{
		ID: "msg_fb", Model: "claude-opus-4-8", StopReason: "end_turn",
		Content: []ContentBlock{TextBlock("served")},
		Usage: MessageUsage{
			InputTokens: 412, OutputTokens: 264,
			Iterations: []MessageIteration{
				{Type: "message", Model: "claude-fable-5", InputTokens: 535, OutputTokens: 0},
				{Type: "fallback_message", Model: "claude-opus-4-8", InputTokens: 412, OutputTokens: 264},
			},
		},
	}
	samples, findings := inf.RuntimeObservations(r, "sess_fb", atTime(), false)
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1 (declined pre-output attempt costs nothing)", len(samples))
	}
	cs := samples[0]
	if cs.ModelRef != "claude-opus-4-8" || cs.CostType != "" {
		t.Fatalf("serving line = model %q cost_type %q, want claude-opus-4-8 with normal cost type", cs.ModelRef, cs.CostType)
	}
	if cs.CostMicroUSD != 8660 { // 412*$5 + 264*$25 per MTok at the SERVING model's rates
		t.Fatalf("serving cost = %d µUSD, want 8660 (Opus rates, never Fable's)", cs.CostMicroUSD)
	}
	var fb int
	for _, f := range findings {
		if f.SubjectKind == subjectRefusal {
			fb++
			if !strings.Contains(f.Title, "claude-fable-5") || !strings.Contains(f.Title, "claude-opus-4-8") {
				t.Errorf("evidence title = %q, want both chain models", f.Title)
			}
		}
	}
	if fb != 1 {
		t.Fatalf("absorbed-refusal findings = %d, want exactly 1", fb)
	}
}

// TestRuntimeObservations_MidStreamDecline pins the billed mid-stream decline: the
// declining model streamed output before its classifier fired, so its attempt IS
// billed as a distinct fallback_attempt line at the declining model's rates
// (535*$10 + 100*$50 = 10350 µUSD on Fable 5).
func TestRuntimeObservations_MidStreamDecline(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-fable-5", Gateway: model.GatewayDirect})
	r := MessageResponse{
		ID: "msg_ms", Model: "claude-opus-4-8", StopReason: "end_turn",
		Usage: MessageUsage{
			InputTokens: 412, OutputTokens: 264,
			Iterations: []MessageIteration{
				{Type: "message", Model: "claude-fable-5", InputTokens: 535, OutputTokens: 100},
				{Type: "fallback_message", Model: "claude-opus-4-8", InputTokens: 412, OutputTokens: 264},
			},
		},
	}
	samples, _ := inf.RuntimeObservations(r, "sess_ms", atTime(), false)
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2 (billed mid-stream attempt + serving line)", len(samples))
	}
	var attempt, serving *model.CostSample
	for i := range samples {
		switch samples[i].CostType {
		case "fallback_attempt":
			attempt = &samples[i]
		case "":
			serving = &samples[i]
		}
	}
	if attempt == nil || serving == nil {
		t.Fatalf("cost-type split missing: %+v", samples)
	}
	if attempt.ModelRef != "claude-fable-5" || attempt.CostMicroUSD != 10350 {
		t.Errorf("attempt line = %q %d µUSD, want claude-fable-5 / 10350 (Fable rates)", attempt.ModelRef, attempt.CostMicroUSD)
	}
	if serving.ModelRef != "claude-opus-4-8" || serving.CostMicroUSD != 8660 {
		t.Errorf("serving line = %q %d µUSD, want claude-opus-4-8 / 8660", serving.ModelRef, serving.CostMicroUSD)
	}
}

// TestRuntimeObservations_StickyServed pins the sticky-routing turn: only a
// fallback_message entry (no declining attempt, no message entry) — one serving cost
// line plus the sticky-served evidence finding.
func TestRuntimeObservations_StickyServed(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-fable-5", Gateway: model.GatewayDirect})
	r := MessageResponse{
		ID: "msg_st", Model: "claude-opus-4-8", StopReason: "end_turn",
		Usage: MessageUsage{
			InputTokens: 100, OutputTokens: 10,
			Iterations: []MessageIteration{
				{Type: "fallback_message", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 10},
			},
		},
	}
	samples, findings := inf.RuntimeObservations(r, "sess_st", atTime(), false)
	if len(samples) != 1 || samples[0].ModelRef != "claude-opus-4-8" {
		t.Fatalf("sticky samples = %+v, want one serving line on claude-opus-4-8", samples)
	}
	var sticky bool
	for _, f := range findings {
		if f.SubjectKind == subjectRefusal && strings.Contains(f.Title, "sticky") {
			sticky = true
		}
	}
	if !sticky {
		t.Fatal("sticky-served turn must carry the sticky evidence finding")
	}
}

// TestRuntimeObservations_MidStreamRefusalBilled pins the refusal-billing refinement
// (the Fable 5 refusals page): a PRE-output refusal stays cost-free, but a MID-STREAM
// refusal (output already streamed) bills the partial output — one cost line plus the
// security finding.
func TestRuntimeObservations_MidStreamRefusalBilled(t *testing.T) {
	inf := NewInference(InferenceConfig{APIKey: "k", DefaultModel: "claude-fable-5", Gateway: model.GatewayDirect})
	mid := MessageResponse{
		ID: "msg_mid", Model: "claude-fable-5", StopReason: "refusal",
		StopDetails: &StopDetails{Type: "refusal", Category: "cyber"},
		Usage:       MessageUsage{InputTokens: 200, OutputTokens: 50},
	}
	samples, findings := inf.RuntimeObservations(mid, "sess_mid", atTime(), false)
	if len(samples) != 1 {
		t.Fatalf("mid-stream refusal samples = %d, want 1 (input + streamed output ARE billed)", len(samples))
	}
	if want := int64(200*10 + 50*50); samples[0].CostMicroUSD != want {
		t.Errorf("mid-stream refusal cost = %d µUSD, want %d", samples[0].CostMicroUSD, want)
	}
	if len(findings) != 1 || findings[0].SubjectKind != subjectRefusal {
		t.Fatalf("mid-stream refusal findings = %+v, want exactly the security signal", findings)
	}
}

// TestFallbackBlockRoundTrip proves the server-authored fallback content block
// ({type:"fallback", from:{model}, to:{model}}) round-trips byte-identically through
// the lossless raw mechanism when the turn is appended back into messages[] — the
// API validates the thinking blocks around its position, so dropping or reordering
// it on the echo would reject the request.
func TestFallbackBlockRoundTrip(t *testing.T) {
	wire := `{"content":[{"type":"fallback","from":{"model":"claude-fable-5"},"to":{"model":"claude-opus-4-8"}},{"type":"text","text":"Hi!"}]}`
	var resp MessageResponse
	if err := json.Unmarshal([]byte(wire), &resp); err != nil {
		t.Fatal(err)
	}
	msgs := AppendAssistantTurn(nil, resp)
	got, err := json.Marshal(msgs[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"fallback","from":{"model":"claude-fable-5"},"to":{"model":"claude-opus-4-8"}},{"type":"text","text":"Hi!"}]`
	if string(got) != want {
		t.Fatalf("fallback block did not round-trip byte-identically:\n got %s\nwant %s", got, want)
	}
}

// TestSupportsStructuredOutputs_FableMythos pins the gate extension: Fable 5 and
// Mythos 5 support structured outputs; the unverified preview stays false.
func TestSupportsStructuredOutputs_FableMythos(t *testing.T) {
	cases := map[string]bool{
		"claude-fable-5":        true,
		"claude-mythos-5":       true,
		"claude-mythos-preview": false,
		"claude-opus-4-8":       true,
	}
	for id, want := range cases {
		if got := SupportsStructuredOutputs(id); got != want {
			t.Errorf("SupportsStructuredOutputs(%q) = %v, want %v", id, got, want)
		}
	}
}
