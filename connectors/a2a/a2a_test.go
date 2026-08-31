// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || len(d.ConfigFields) == 0 {
		t.Errorf("descriptor = %+v", d)
	}
}

func TestOpenNothingToObserveFails(t *testing.T) {
	if err := New().Open(t.Context(), sdk.Config{}); err == nil {
		t.Error("Open with no agents and no interactions must fail")
	}
}

// openWith builds a Source with inline agents/interactions JSON and an injected
// card fetcher, ready to Gather without network.
func openWith(t *testing.T, agents []agentSpec, interactions []interactionSpec, cards map[string][]byte) *Source {
	t.Helper()
	aJSON, _ := json.Marshal(agents)
	iJSON, _ := json.Marshal(interactions)
	s := New()
	s.fetch = staticFetch(cards) // inject before Open so Open keeps it
	cfg := sdk.Config{Settings: map[string]string{
		cfgAgents:       string(aJSON),
		cfgInteractions: string(iJSON),
	}}
	if err := s.Open(t.Context(), cfg); err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

// TestGatherVerifiedAgentYieldsAttributedEdge is the headline DoD scenario: a
// non-Claude A2A agent with a VALID signed card produces a trust=verified finding
// and an `attributed` edge into the orchestration graph.
func TestGatherVerifiedAgentYieldsAttributedEdge(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	cards := map[string][]byte{
		"researcher": signedCardBytes(t, priv, "k1", baseCard("researcher")),
	}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com", TrustJWKS: jwks}}
	inter := []interactionSpec{{From: "planner", To: "researcher", TaskID: "t1", State: string(TaskStateCompleted), Skill: "summarize"}}

	s := openWith(t, agents, inter, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}

	// Trust finding: verified (Info).
	trust := sink.findingsOfKind(findingTrust)
	if len(trust) != 1 || trust[0].Severity != model.SeverityInfo {
		t.Fatalf("want one verified (Info) trust finding, got %+v", trust)
	}

	// One A2A edge planner→researcher, attributed (peer identity verified).
	edges := sink.edges()
	if len(edges) != 1 {
		t.Fatalf("want one A2A edge, got %d (%+v)", len(edges), edges)
	}
	e := edges[0]
	if e.OriginKind != originAgent || e.OriginRef != "planner" || e.ResourceKind != resourceAgent || e.ResourceRef != "researcher" {
		t.Errorf("edge endpoints wrong: %+v", e)
	}
	if e.Source != model.SignalA2A || e.Confidence != model.ConfidenceAttributed {
		t.Errorf("verified peer should yield an attributed a2a edge: %+v", e)
	}
	if e.ToolRef != "summarize" {
		t.Errorf("edge should carry the skill as tool_ref: %q", e.ToolRef)
	}
}

// TestGatherInvalidSignatureNotTrusted: a card with a BAD signature is flagged and
// its edge is NOT attributed (approximate) — a guardrail is never weakened to trust
// an unverifiable card.
func TestGatherInvalidSignatureNotTrusted(t *testing.T) {
	priv, _ := keypair(t, "k1")
	_, wrongJWKS := keypair(t, "k1") // anchor that does NOT match the signer
	cards := map[string][]byte{
		"researcher": signedCardBytes(t, priv, "k1", baseCard("researcher")),
	}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com", TrustJWKS: wrongJWKS}}
	inter := []interactionSpec{{From: "planner", To: "researcher", State: string(TaskStateCompleted)}}

	s := openWith(t, agents, inter, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	trust := sink.findingsOfKind(findingTrust)
	if len(trust) != 1 || trust[0].Severity != model.SeverityMedium {
		t.Fatalf("an unverifiable card should be a Medium (flagged) trust finding, got %+v", trust)
	}
	edges := sink.edges()
	if len(edges) != 1 || edges[0].Confidence != model.ConfidenceApproximate {
		t.Fatalf("an edge to an unverified peer must be approximate, got %+v", edges)
	}
}

func TestGatherEmitsSecuritySchemesAndTaskFindings(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	cards := map[string][]byte{"researcher": signedCardBytes(t, priv, "k1", baseCard("researcher"))}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com", TrustJWKS: jwks}}
	// A failed task is a notable lifecycle state → a finding.
	inter := []interactionSpec{{From: "planner", To: "researcher", State: string(TaskStateFailed)}}

	s := openWith(t, agents, inter, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if sec := sink.findingsOfKind(findingSecurity); len(sec) != 1 {
		t.Errorf("want one security-schemes finding, got %+v", sec)
	}
	task := sink.findingsOfKind(findingTask)
	if len(task) != 1 || task[0].Severity != model.SeverityLow {
		t.Errorf("a failed task should emit one Low task finding, got %+v", task)
	}
}

func TestGatherUnsignedPeerEdgeIsApproximate(t *testing.T) {
	cards := map[string][]byte{"researcher": mustJSON(t, baseCard("researcher"))}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com"}}
	inter := []interactionSpec{{From: "planner", To: "researcher", State: string(TaskStateWorking)}}

	s := openWith(t, agents, inter, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if trust := sink.findingsOfKind(findingTrust); len(trust) != 1 || trust[0].Severity != model.SeverityLow {
		t.Errorf("unsigned card should be a Low trust finding, got %+v", trust)
	}
	if edges := sink.edges(); len(edges) != 1 || edges[0].Confidence != model.ConfidenceApproximate {
		t.Errorf("edge to an unsigned peer must be approximate, got %+v", sink.edges())
	}
	// A working (happy-path) task emits no lifecycle finding.
	if task := sink.findingsOfKind(findingTask); len(task) != 0 {
		t.Errorf("happy-path task should emit no finding, got %+v", task)
	}
}

func TestGatherDiscoveryFailureFinding(t *testing.T) {
	agents := []agentSpec{{Name: "missing", URL: "https://missing.example.com"}}
	s := openWith(t, agents, nil, map[string][]byte{}) // fetch returns errNoCard
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather should not fail on a bad agent: %v", err)
	}
	if d := sink.findingsOfKind(findingDiscovery); len(d) != 1 || d[0].Severity != model.SeverityMedium {
		t.Errorf("a discovery failure should emit one Medium finding, got %+v", sink.findings())
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSchemeTypesV1UnionAndLegacy: the v1.0 SecurityScheme oneof members are
// recognized by JSON member name; legacy v0.x `type` strings still parse.
func TestSchemeTypesV1UnionAndLegacy(t *testing.T) {
	var card AgentCard
	if err := json.Unmarshal(mustJSON(t, baseCard("x")), &card); err != nil {
		t.Fatal(err)
	}
	got := schemeTypes(card)
	if len(got) != 2 || got[0] != "apiKey" || got[1] != "oauth2" {
		t.Errorf("v1.0 union scheme kinds = %v, want [apiKey oauth2]", got)
	}
	var legacy AgentCard
	if err := json.Unmarshal(mustJSON(t, legacyCard("x")), &legacy); err != nil {
		t.Fatal(err)
	}
	if got := schemeTypes(legacy); len(got) != 2 || got[0] != "apiKey" || got[1] != "oauth2" {
		t.Errorf("legacy scheme types = %v, want [apiKey oauth2]", got)
	}
}

// TestOAuthHygieneFinding: a card declaring the v1.0-deprecated implicit/password
// flows yields a Low a2a_oauth_hygiene finding; PKCE-not-required alone is Info; a
// clean posture (authorization_code + pkceRequired) yields none.
func TestOAuthHygieneFinding(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	mk := func(flows map[string]any) AgentCard {
		var card AgentCard
		raw := mustJSON(t, map[string]any{
			"securitySchemes": map[string]any{
				"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{"flows": flows}},
			},
		})
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		return card
	}

	// Deprecated implicit flow → Low.
	f, ok := oauthHygieneFinding("agent", mk(map[string]any{"implicit": map[string]any{"authorizationUrl": "https://a"}}), at)
	if !ok || f.Kind != findingOAuthHygiene || f.Severity != model.SeverityLow {
		t.Fatalf("implicit flow must be a Low hygiene finding, got ok=%v %+v", ok, f)
	}
	if !strings.Contains(f.Title, "implicit-flow(deprecated)") {
		t.Errorf("title must name the deprecated flow: %q", f.Title)
	}
	// PKCE not required → Info.
	f, ok = oauthHygieneFinding("agent", mk(map[string]any{"authorizationCode": map[string]any{"pkceRequired": false}}), at)
	if !ok || f.Severity != model.SeverityInfo || !strings.Contains(f.Title, "pkce-not-required") {
		t.Errorf("pkce-not-required must be an Info finding, got ok=%v %+v", ok, f)
	}
	// Clean posture → no finding.
	if _, ok := oauthHygieneFinding("agent", mk(map[string]any{"authorizationCode": map[string]any{"pkceRequired": true}}), at); ok {
		t.Error("a clean OAuth posture must not emit a hygiene finding")
	}
}

// TestGatherEmitsOAuthHygieneFinding: the Source surfaces a discovered agent's
// deprecated OAuth flow as an a2a_oauth_hygiene finding.
func TestGatherEmitsOAuthHygieneFinding(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	base := baseCard("researcher")
	base["securitySchemes"] = map[string]any{
		"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{
			"flows": map[string]any{"password": map[string]any{"tokenUrl": "https://a/t"}},
		}},
	}
	cards := map[string][]byte{"researcher": signedCardBytes(t, priv, "k1", base)}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com", TrustJWKS: jwks}}
	s := openWith(t, agents, nil, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	hyg := sink.findingsOfKind(findingOAuthHygiene)
	if len(hyg) != 1 || hyg[0].Severity != model.SeverityLow {
		t.Fatalf("want one Low a2a_oauth_hygiene finding, got %+v", hyg)
	}
	if !strings.Contains(hyg[0].Title, "password-flow(deprecated)") {
		t.Errorf("hygiene title wrong: %q", hyg[0].Title)
	}
}
