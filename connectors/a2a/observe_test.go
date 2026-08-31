// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestAgentEdgeShape(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	e := AgentEdge("supervisor", "worker", "summarize", at)
	if e.OriginKind != originAgent || e.OriginRef != "supervisor" {
		t.Errorf("origin wrong: %+v", e)
	}
	if e.ResourceKind != resourceAgent || e.ResourceRef != "worker" {
		t.Errorf("resource wrong: %+v", e)
	}
	if e.Source != model.SignalA2A || e.Confidence != model.ConfidenceAttributed {
		t.Errorf("a governed delegation edge must be an attributed a2a edge: %+v", e)
	}
	if e.Mode != model.ModeUnknown || e.ToolRef != "summarize" || !e.ObservedAt.Equal(at) {
		t.Errorf("mode/tool/time wrong: %+v", e)
	}
}

func TestDelegationEdgeAllowedOnly(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	// Allowed → an edge with the supplied origin.
	e, ok := DelegationEdge(DelegationDecision{AgentName: "billing", Skill: "summarize", Allowed: true}, "planner", at)
	if !ok || e.OriginRef != "planner" || e.ResourceRef != "billing" || e.ToolRef != "summarize" {
		t.Errorf("an allowed delegation must map to an edge planner→billing: ok=%v %+v", ok, e)
	}
	// Denied → no edge (a refused delegation is not a communication that happened).
	if _, ok := DelegationEdge(DelegationDecision{AgentName: "billing", Allowed: false}, "planner", at); ok {
		t.Error("a denied delegation must NOT produce a communication edge")
	}
	// Blank origin / blank agent → no edge (not attributable).
	if _, ok := DelegationEdge(DelegationDecision{AgentName: "billing", Allowed: true}, "", at); ok {
		t.Error("a blank origin must not produce an edge")
	}
	if _, ok := DelegationEdge(DelegationDecision{AgentName: "", Allowed: true}, "planner", at); ok {
		t.Error("a blank target agent must not produce an edge")
	}
}

func TestDelegationFinding(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	allowed := DelegationFinding(DelegationDecision{
		AgentName: "billing", Skill: "summarize", Objective: "nightly-report",
		Allowed: true, PlanHash: "PLAN-SECRET", ApprovalRef: "appr-1", State: TaskStateSubmitted,
	}, at)
	if allowed.Kind != delegationFindingKind || allowed.Severity != model.SeverityInfo {
		t.Errorf("an allowed delegation finding must be Info a2a_delegation, got %+v", allowed)
	}
	if !strings.Contains(allowed.Title, "billing") || !strings.Contains(allowed.Title, "summarize") ||
		!strings.Contains(allowed.Title, "nightly-report") {
		t.Errorf("the title must carry agent/skill/objective: %q", allowed.Title)
	}
	// Minimal data: the plan hash / detail are HASHED, never in the title or detail field.
	if strings.Contains(allowed.Title, "PLAN-SECRET") || allowed.DetailHash == "" || strings.Contains(allowed.DetailHash, "PLAN-SECRET") {
		t.Errorf("detail must be hashed, not exposed: title=%q hash=%q", allowed.Title, allowed.DetailHash)
	}
	// OWASP ASI tag (Identity & Privilege Abuse) is attached for the SIEM/compliance feed.
	if len(allowed.OWASPASI) == 0 || allowed.OWASPASI[0] != "ASI03" {
		t.Errorf("a delegation finding must carry ASI03, got %v", allowed.OWASPASI)
	}
	// Denied → Low + "denied" in the title (the governance gap is surfaced).
	denied := DelegationFinding(DelegationDecision{AgentName: "billing", Skill: "deploy", Allowed: false}, at)
	if denied.Severity != model.SeverityLow || !strings.Contains(denied.Title, "denied") {
		t.Errorf("a denied delegation finding must be Low + 'denied', got %+v", denied)
	}
}

// TestDelegationFindingObjectiveScrubbed: the objective is contractually a non-sensitive
// label, but the title is "safe to display" — a secret-shaped objective must NEVER survive
// verbatim in it (defense-in-depth; the scrubber must never under-redact).
func TestDelegationFindingObjectiveScrubbed(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	f := DelegationFinding(DelegationDecision{
		AgentName: "billing", Skill: "summarize", Allowed: true,
		Objective: "Bearer sk-live-0123456789abcdefghij",
	}, at)
	if strings.Contains(f.Title, "sk-live-0123456789abcdefghij") {
		t.Errorf("a secret-shaped objective must not survive verbatim in the title: %q", f.Title)
	}
}

// TestCapabilityFindingExtensionURIScrubbed: an extension URI comes from the UNTRUSTED
// remote card; a secret-shaped URI must not survive verbatim in the displayed title.
func TestCapabilityFindingExtensionURIScrubbed(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	card := AgentCard{
		Skills: []agentSkill{{ID: "s1", Name: "summarize"}},
		Capabilities: &AgentCapabilities{
			Extensions: []agentExtension{{URI: "Bearer sk-live-0123456789abcdefghij"}},
		},
	}
	f, ok := capabilityFinding("evil", card, trustUnsigned, at)
	if !ok {
		t.Fatal("a card with an extension must still produce a capability finding")
	}
	if strings.Contains(f.Title, "sk-live-0123456789abcdefghij") {
		t.Errorf("a secret-shaped extension URI must not survive verbatim in the title: %q", f.Title)
	}
}

func TestCapabilityFindingUnit(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	card := AgentCard{
		ProtocolVersion: "1.0",
		Skills:          []agentSkill{{ID: "s1", Name: "summarize"}, {ID: "s2", Name: "translate"}},
		Capabilities:    &AgentCapabilities{Streaming: true, PushNotifications: true},
	}
	// Verified card → "verified" trust note.
	f, ok := capabilityFinding("researcher", card, trustVerified, at)
	if !ok || f.Kind != findingCapability || f.Severity != model.SeverityInfo {
		t.Fatalf("a card with skills must yield an Info a2a_capability finding, got ok=%v %+v", ok, f)
	}
	if !strings.Contains(f.Title, "verified") || !strings.Contains(f.Title, "2 skill") ||
		!strings.Contains(f.Title, "push") || !strings.Contains(f.Title, "streaming") {
		t.Errorf("verified capability title wrong: %q", f.Title)
	}
	// Unverified card → the declarations are self-claimed (UNTRUSTED) and the title says so.
	un, ok := capabilityFinding("researcher", card, trustUnsigned, at)
	if !ok || !strings.Contains(un.Title, "UNTRUSTED") {
		t.Errorf("an unverified card's capabilities must be marked self-claimed/UNTRUSTED: %q", un.Title)
	}
	// A card with no skills and no capabilities → nothing to catalog.
	if _, ok := capabilityFinding("bare", AgentCard{}, trustVerified, at); ok {
		t.Error("a card declaring nothing must not emit a capability finding")
	}
}

// TestGatherEmitsCapabilityFinding: the Source surfaces a discovered agent's declared
// skills + capabilities as an a2a_capability finding (the discovery half of capability
// verification).
func TestGatherEmitsCapabilityFinding(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	cards := map[string][]byte{"researcher": signedCardBytes(t, priv, "k1", baseCard("researcher"))}
	agents := []agentSpec{{Name: "researcher", URL: "https://researcher.example.com", TrustJWKS: jwks}}
	s := openWith(t, agents, nil, cards)
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	caps := sink.findingsOfKind(findingCapability)
	if len(caps) != 1 {
		t.Fatalf("want one a2a_capability finding, got %d (%+v)", len(caps), caps)
	}
	if !strings.Contains(caps[0].Title, "verified") || !strings.Contains(caps[0].Title, "1 skill") {
		t.Errorf("capability finding title wrong: %q", caps[0].Title)
	}
}
