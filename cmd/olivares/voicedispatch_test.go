// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	"github.com/olivaresai/olivares/core/model"
	voicemod "github.com/olivaresai/olivares/modules/voice"
)

// voiceStubDoer is an offline voiceconn.Transport that routes by URL path, returning
// a canned mint response and capturing the last request + body for assertions.
type voiceStubDoer struct {
	lastReq  *http.Request
	lastBody []byte
}

func (s *voiceStubDoer) Do(req *http.Request) (*http.Response, error) {
	s.lastReq = req
	if req.Body != nil {
		s.lastBody, _ = io.ReadAll(req.Body)
	}
	var body string
	switch {
	case strings.Contains(req.URL.Path, "client_secrets"):
		body = `{"value":"ek_ephemeral_123","expires_at":1893456000,"session":{"id":"sess_1"}}`
	case strings.Contains(req.URL.Path, "auth/grant"):
		body = `{"access_token":"dg_ephemeral_123","expires_in":30}`
	default:
		body = `{}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: make(http.Header)}, nil
}

func voiceReq(agent, model, provider string) voicemod.OpenRequest {
	return voicemod.OpenRequest{Tenant: model2tenant(), SessionRef: "s1", AgentRef: agent, ModelRef: model, ProviderRef: provider, PlanHash: "ph-1"}
}

func model2tenant() model.TenantID { return model.TenantID("tenant-1") }

func TestVoiceDispatch_OpenAIFixesPolicyAndHidesMasterKey(t *testing.T) {
	doer := &voiceStubDoer{}
	prov := voiceconn.NewOpenAI(voiceconn.OpenAIConfig{APIKey: "MASTER-SECRET-KEY", BaseURL: "https://api.test", Transport: doer})
	d := &voiceDispatcher{
		providers: map[string]voiceconn.Provider{"openai": prov},
		think:     map[string]thinkDefaults{},
		policies: map[string]voiceSessionPolicy{
			"bot": {model: "gpt-realtime-2", voice: "marin", instructions: "be brief", maxDuration: 600 * time.Second},
		},
	}
	// The client REQUESTS a different (stronger) model than the policy allows — the
	// dispatcher must NOT let it escalate. The request model differs from the policy
	// model so this assertion actually discriminates precedence (anti-escalation).
	req := voiceReq("bot", "gpt-4o-realtime-CLIENT-ESCALATION", "openai")
	res, err := d.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The session config POSTed to the provider is fixed FROM THE POLICY, not the request.
	var sent struct {
		Session struct {
			Model string `json:"model"`
			Audio struct {
				Output struct {
					Voice string `json:"voice"`
				} `json:"output"`
			} `json:"audio"`
		} `json:"session"`
	}
	if err := json.Unmarshal(doer.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Session.Model != "gpt-realtime-2" {
		t.Fatalf("CLIENT ESCALATION: minted model %q is not the policy model (client overrode policy): %s", sent.Session.Model, doer.lastBody)
	}
	if sent.Session.Audio.Output.Voice != "marin" {
		t.Fatalf("policy voice not fixed into mint request: %s", doer.lastBody)
	}
	// The pseudonymous principal is the provider safety identifier.
	if got := doer.lastReq.Header.Get("OpenAI-Safety-Identifier"); got != principalID(req) {
		t.Fatalf("safety identifier mismatch: %q", got)
	}
	// The master key is used in the header server-side but NEVER returned to the client.
	if !strings.Contains(doer.lastReq.Header.Get("Authorization"), "MASTER-SECRET-KEY") {
		t.Fatal("master key should be sent in the provider request header")
	}
	if strings.Contains(res.Ref, "MASTER-SECRET-KEY") {
		t.Fatalf("MASTER KEY LEAK in OpenResult.Ref: %s", res.Ref)
	}
	if !strings.Contains(res.Ref, "ek_ephemeral_123") || !strings.Contains(res.Ref, "/v1/realtime/calls") {
		t.Fatalf("ref missing ephemeral credential/connect coords: %s", res.Ref)
	}
}

// When the operator policy leaves the model open, the documented fallback uses the
// (already policy-validated) requested model — pinning the other branch of
// firstNonEmpty(pol.model, req.ModelRef) so the precedence is fully covered.
func TestVoiceDispatch_ModelFallsBackToRequestWhenPolicyEmpty(t *testing.T) {
	doer := &voiceStubDoer{}
	prov := voiceconn.NewOpenAI(voiceconn.OpenAIConfig{APIKey: "k", BaseURL: "https://api.test", Transport: doer})
	d := &voiceDispatcher{
		providers: map[string]voiceconn.Provider{"openai": prov},
		policies:  map[string]voiceSessionPolicy{"bot": {voice: "marin"}}, // model intentionally empty
	}
	if _, err := d.Open(context.Background(), voiceReq("bot", "gpt-realtime-2", "openai")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var sent struct {
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if err := json.Unmarshal(doer.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Session.Model != "gpt-realtime-2" {
		t.Fatalf("empty-policy fallback should use the requested model, got %q", sent.Session.Model)
	}
}

func TestVoiceDispatch_DeepgramClaudeAsThinkNoKeyLeak(t *testing.T) {
	doer := &voiceStubDoer{}
	prov := voiceconn.NewDeepgram(voiceconn.DeepgramConfig{APIKey: "DG-MASTER-KEY", BaseURL: "https://api.test", Transport: doer})
	d := &voiceDispatcher{
		providers: map[string]voiceconn.Provider{"deepgram": prov},
		think: map[string]thinkDefaults{
			"deepgram": {endpointURL: "https://api.anthropic.com/v1/messages", headers: map[string]string{"x-api-key": "ANTHROPIC-SECRET", "anthropic-version": "2023-06-01"}},
		},
		policies: map[string]voiceSessionPolicy{
			"bot": {model: "claude-opus-4-8", voice: "aura-2-thalia-en", turn: voiceconn.TurnDetection{Type: "flux", EOTThreshold: 0.7}},
		},
	}
	req := voiceReq("bot", "claude-opus-4-8", "deepgram")
	res, err := d.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Master key used server-side for the grant; never in the returned ref.
	if !strings.Contains(doer.lastReq.Header.Get("Authorization"), "DG-MASTER-KEY") {
		t.Fatal("deepgram master key should be sent in the grant header")
	}
	if strings.Contains(res.Ref, "DG-MASTER-KEY") {
		t.Fatalf("DEEPGRAM MASTER KEY LEAK in ref: %s", res.Ref)
	}
	if strings.Contains(res.Ref, "ANTHROPIC-SECRET") {
		t.Fatalf("ANTHROPIC KEY LEAK in ref (Claude-as-think key must stay server-side): %s", res.Ref)
	}
	var payload sessionRefPayload
	if err := json.Unmarshal([]byte(res.Ref), &payload); err != nil {
		t.Fatalf("decode ref payload: %v", err)
	}
	if payload.Model != "claude-opus-4-8" || payload.Credential != "dg_ephemeral_123" || !payload.ServerMediated {
		t.Fatalf("unexpected session payload: %+v", payload)
	}
}

func TestVoiceDispatch_NoProviderIsError(t *testing.T) {
	d := &voiceDispatcher{providers: map[string]voiceconn.Provider{}, policies: map[string]voiceSessionPolicy{"bot": {}}}
	if _, err := d.Open(context.Background(), voiceReq("bot", "m", "openai")); err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestVoiceDispatch_NoPolicyIsDenyClosed(t *testing.T) {
	prov := voiceconn.NewOpenAI(voiceconn.OpenAIConfig{APIKey: "k", Transport: &voiceStubDoer{}})
	d := &voiceDispatcher{providers: map[string]voiceconn.Provider{"openai": prov}, policies: map[string]voiceSessionPolicy{}}
	if _, err := d.Open(context.Background(), voiceReq("unknown", "m", "openai")); err == nil {
		t.Fatal("expected deny-closed error for an agent with no session policy")
	}
}

func TestVoiceDispatch_EmptyPlanHashRefused(t *testing.T) {
	prov := voiceconn.NewOpenAI(voiceconn.OpenAIConfig{APIKey: "k", Transport: &voiceStubDoer{}})
	d := &voiceDispatcher{providers: map[string]voiceconn.Provider{"openai": prov}, policies: map[string]voiceSessionPolicy{"bot": {}}}
	req := voiceReq("bot", "m", "openai")
	req.PlanHash = ""
	if _, err := d.Open(context.Background(), req); err == nil || !strings.Contains(err.Error(), "plan_hash") {
		t.Fatalf("expected plan_hash defense, got %v", err)
	}
}

func TestNewVoiceDispatcher_DenyClosedWhenUnconfigured(t *testing.T) {
	if d := newVoiceDispatcher(voiceDispatchConfig{}, discardLogger()); d != nil {
		t.Fatal("unconfigured voice dispatcher must be nil (module keeps deny-closed unwiredDispatcher)")
	}
}
