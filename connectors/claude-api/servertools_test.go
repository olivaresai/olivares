// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestServerToolBuilders proves the verified dated types/names and that only the
// advisor tool requires a beta header (D1/D7).
func TestServerToolBuilders(t *testing.T) {
	cases := []struct {
		tool ServerTool
		typ  string
		name string
		beta string
	}{
		{WebSearchTool(), "web_search_20260209", "web_search", ""},
		{WebFetchTool(), "web_fetch_20260209", "web_fetch", ""},
		{CodeExecutionTool(), "code_execution_20260120", "code_execution", ""},
		{AdvisorTool("claude-sonnet-4-6"), "advisor_20260301", "advisor", BetaAdvisorTool},
	}
	for _, c := range cases {
		if c.tool.Type != c.typ || c.tool.Name != c.name {
			t.Errorf("tool = %+v, want type %q name %q", c.tool, c.typ, c.name)
		}
		if c.tool.requiredBeta() != c.beta {
			t.Errorf("%s requiredBeta = %q, want %q", c.typ, c.tool.requiredBeta(), c.beta)
		}
	}
	if AdvisorTool("claude-sonnet-4-6").Model != "claude-sonnet-4-6" {
		t.Error("advisor tool must carry its required model")
	}
}

// TestAdvisorTool_RequiresModel proves D1's required model is enforced client-side
// (the builder always sets it; a hand-built advisor tool without a model is rejected
// before the API 400).
func TestAdvisorTool_RequiresModel(t *testing.T) {
	if !advisorToolMissingModel(AdvisorTool("")) {
		t.Error("advisor tool with empty model should be flagged")
	}
	if advisorToolMissingModel(AdvisorTool("claude-sonnet-4-6")) {
		t.Error("advisor tool with a model must not be flagged")
	}
	if advisorToolMissingModel(WebSearchTool()) {
		t.Error("a non-advisor tool must never be flagged")
	}
	if !advisorToolMissingModel(map[string]any{"type": "advisor_20260301", "name": "advisor"}) {
		t.Error("hand-built advisor tool without a model should be flagged")
	}
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	if _, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens: 8,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
		Tools:     []any{AdvisorTool("")},
	}); err == nil {
		t.Error("CreateMessage must reject an advisor tool with no model (it 400s upstream)")
	}
}

// TestServerToolGrant_PermittedEdges proves the allowlist becomes PERMITTED edges with
// the right kind/ref/family/source, with deny-wins and dedup (D7).
func TestServerToolGrant_PermittedEdges(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	g := ServerToolGrant{
		AgentRef:     "agent:research",
		AllowedTypes: []string{"web_search_20260209", "code_execution_20260120", "web_search_20260209"}, // dup
		DeniedTypes:  []string{"code_execution_20260120"},                                               // deny wins
	}
	edges := g.PermittedEdges(at)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 (web_search; code_execution denied, dup removed)", len(edges))
	}
	e := edges[0]
	if e.Source != model.SignalPolicy {
		t.Errorf("source = %q, want policy", e.Source)
	}
	if e.OriginKind != "agent" || e.OriginRef != "agent:research" {
		t.Errorf("origin = %s/%s", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != resServerTool || e.ResourceRef != "web_search_20260209" {
		t.Errorf("resource = %s/%s, want anthropic.server_tool/web_search_20260209", e.ResourceKind, e.ResourceRef)
	}
	// ToolRef is the family so a version bump still reconciles against observed use.
	if e.ToolRef != "web_search" || e.Mode != model.ModeUnknown {
		t.Errorf("edge family/mode wrong: %+v", e)
	}
	if !e.ObservedAt.Equal(at) {
		t.Errorf("observedAt = %v, want %v", e.ObservedAt, at)
	}
}

// TestServerToolGrant_Invalid proves an empty allow-set grants nothing and a missing
// agent is invalid.
func TestServerToolGrant_Invalid(t *testing.T) {
	if (ServerToolGrant{AgentRef: "a"}).Valid() {
		t.Error("no allowed types must be invalid (grants nothing)")
	}
	if (ServerToolGrant{AllowedTypes: []string{"web_search_20260209"}}).Valid() {
		t.Error("no agent_ref must be invalid")
	}
}

// TestParseServerToolGrants proves empty=nil and malformed=error (never silently
// ungoverned).
func TestParseServerToolGrants(t *testing.T) {
	if g, err := parseServerToolGrants("  "); g != nil || err != nil {
		t.Errorf("empty => nil,nil; got %+v %v", g, err)
	}
	good := `[{"agent_ref":"a","allowed_types":["web_search_20260209"]}]`
	g, err := parseServerToolGrants(good)
	if err != nil || len(g) != 1 || g[0].AgentRef != "a" {
		t.Fatalf("parse good: %+v err=%v", g, err)
	}
	if _, err := parseServerToolGrants(`{not an array}`); err == nil {
		t.Error("malformed policy must error (never silently ungoverned)")
	}
}

// TestRecognizedServerToolType proves the recognition set covers current + prior
// versions and rejects an unknown id.
func TestRecognizedServerToolType(t *testing.T) {
	for _, id := range []string{"web_search_20260209", "web_search_20250305", "code_execution_20260120", "advisor_20260301"} {
		if !RecognizedServerToolType(id) {
			t.Errorf("%s should be recognized", id)
		}
	}
	if RecognizedServerToolType("web_search_29991231") {
		t.Error("an unknown dated id must not be recognized")
	}
}

// TestGatherServerToolEdges proves the gather emits PERMITTED edges and a posture
// finding for an UNRECOGNIZED allowed type — surfaced, not silently accepted (D7).
func TestGatherServerToolEdges(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	s.serverTools = []ServerToolGrant{{
		AgentRef:     "agent:x",
		AllowedTypes: []string{"web_search_20260209", "web_search_29991231"}, // 2nd is unrecognized
	}}
	sink := &captureSink{}
	if err := s.gatherServerToolEdges(context.Background(), sink); err != nil {
		t.Fatalf("gatherServerToolEdges: %v", err)
	}
	var edges, findings int
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges++
			if v.ResourceKind != resServerTool {
				t.Errorf("edge kind = %q", v.ResourceKind)
			}
		case model.FindingReport:
			findings++
			if v.SubjectKind != subjectServerTool {
				t.Errorf("finding subject = %q", v.SubjectKind)
			}
		}
	}
	if edges != 2 {
		t.Errorf("edges = %d, want 2 (both allowed types emit a PERMITTED edge)", edges)
	}
	if findings != 1 {
		t.Errorf("findings = %d, want 1 (only the unrecognized type is flagged)", findings)
	}
}

// TestServerToolGrants_WiredThroughOpen proves the connector parses server_tool_grants
// at Open and the Gather emits the operator-declared edges even offline (no admin_key).
func TestServerToolGrants_WiredThroughOpen(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	cfg := sdk.Config{Settings: map[string]string{
		"server_tool_grants": `[{"agent_ref":"agent:x","allowed_types":["web_search_20260209"]}]`,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, o := range sink.obs {
		if e, ok := o.(model.EdgeObservation); ok && e.ResourceKind == resServerTool && e.OriginRef == "agent:x" {
			found = true
		}
	}
	if !found {
		t.Error("offline Gather should emit the operator-declared server-tool edge")
	}
}
