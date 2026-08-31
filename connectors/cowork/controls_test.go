// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// testControlPolicy is the canonical fixture: a connector with a level AND a
// "Custom"-style per-tool override set, a blocked connector, and a connector-less
// default. github is needs_approval with delete_repo blocked and read_issues
// always_allow; legacy is blocked; the default is always_allow.
func testControlPolicy() ConnectorControlPolicy {
	return ConnectorControlPolicy{
		Default: ControlAlwaysAllow,
		Connectors: map[string]ConnectorControl{
			"github": {
				Level: ControlNeedsApproval,
				Tools: map[string]ControlLevel{
					"delete_repo": ControlBlocked,
					"read_issues": ControlAlwaysAllow,
				},
			},
			"legacy": {Level: ControlBlocked},
		},
	}
}

func TestControlLevelValid(t *testing.T) {
	for _, l := range []ControlLevel{ControlAlwaysAllow, ControlNeedsApproval, ControlBlocked} {
		if !l.Valid() {
			t.Errorf("%q should be valid", l)
		}
	}
	for _, l := range []ControlLevel{"", "allow", "Blocked", "custom"} {
		if l.Valid() {
			t.Errorf("%q must be invalid", l)
		}
	}
}

// TestParseConnectorControls proves the deny-closed authoring contract: empty means
// not configured (nil error), while malformed JSON or any invalid level is a hard
// error — an authored policy never silently disappears.
func TestParseConnectorControls(t *testing.T) {
	const valid = `{
		"default": "needs_approval",
		"connectors": {
			"github": {"level": "always_allow", "tools": {"delete_repo": "blocked"}},
			"jira":   {"tools": {"create_issue": "needs_approval"}}
		}
	}`
	p, err := ParseConnectorControls(valid)
	if err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	if p.Default != ControlNeedsApproval {
		t.Errorf("Default = %q, want needs_approval", p.Default)
	}
	if len(p.Connectors) != 2 {
		t.Fatalf("Connectors = %d entries, want 2 (%+v)", len(p.Connectors), p.Connectors)
	}
	gh := p.Connectors["github"]
	if gh.Level != ControlAlwaysAllow || len(gh.Tools) != 1 || gh.Tools["delete_repo"] != ControlBlocked {
		t.Errorf("github = %+v", gh)
	}
	// jira is the "Custom" posture: no connector level, per-tool overrides only.
	jr := p.Connectors["jira"]
	if jr.Level != "" || len(jr.Tools) != 1 || jr.Tools["create_issue"] != ControlNeedsApproval {
		t.Errorf("jira = %+v", jr)
	}
	if !p.Configured() {
		t.Error("a parsed policy with connectors must be Configured")
	}

	// Empty / whitespace: not configured, NOT an error.
	for _, raw := range []string{"", "   ", "\n\t"} {
		p, err := ParseConnectorControls(raw)
		if err != nil {
			t.Errorf("empty raw %q must not error: %v", raw, err)
		}
		if p.Configured() {
			t.Errorf("empty raw %q must not be Configured: %+v", raw, p)
		}
		if p.Default != "" || p.Connectors != nil {
			t.Errorf("empty raw %q must yield the zero policy, got %+v", raw, p)
		}
	}

	// Malformed JSON / invalid levels: hard errors (deny-closed authoring).
	for name, raw := range map[string]string{
		"malformed JSON":          `{"connectors": {`,
		"invalid default":         `{"default": "allow"}`,
		"invalid connector level": `{"connectors": {"github": {"level": "open"}}}`,
		"invalid tool level":      `{"connectors": {"github": {"tools": {"x": "maybe"}}}}`,
		"empty tool level":        `{"connectors": {"github": {"tools": {"x": ""}}}}`,
	} {
		if _, err := ParseConnectorControls(raw); err == nil {
			t.Errorf("%s must be rejected: %s", name, raw)
		}
	}
}

func TestConnectorControlPolicyConfigured(t *testing.T) {
	if (ConnectorControlPolicy{}).Configured() {
		t.Error("the zero policy must not be Configured")
	}
	if !(ConnectorControlPolicy{Default: ControlBlocked}).Configured() {
		t.Error("a Default alone must be Configured")
	}
	if !(ConnectorControlPolicy{Connectors: map[string]ConnectorControl{"x": {}}}).Configured() {
		t.Error("a listed connector alone must be Configured")
	}
}

// TestEffectiveLevelPrecedence proves the console precedence (per-tool override >
// connector level > default) and the deny-closed floor: under a CONFIGURED policy
// an unlisted connector — or an empty default — resolves to blocked.
func TestEffectiveLevelPrecedence(t *testing.T) {
	p := testControlPolicy()
	cases := []struct {
		name         string
		server, tool string
		want         ControlLevel
	}{
		{"per-tool override wins over connector level", "github", "delete_repo", ControlBlocked},
		{"per-tool override (allow) wins", "github", "read_issues", ControlAlwaysAllow},
		{"unlisted tool falls to connector level", "github", "create_issue", ControlNeedsApproval},
		{"empty tool resolves the connector level", "github", "", ControlNeedsApproval},
		{"blocked connector level", "legacy", "any_tool", ControlBlocked},
		{"unlisted connector falls to default", "slack", "post_message", ControlAlwaysAllow},
	}
	for _, c := range cases {
		if got := p.EffectiveLevel(c.server, c.tool); got != c.want {
			t.Errorf("%s: EffectiveLevel(%q,%q) = %q, want %q", c.name, c.server, c.tool, got, c.want)
		}
	}

	// Custom posture: no connector level → an unlisted tool falls THROUGH to default.
	custom := ConnectorControlPolicy{
		Default:    ControlNeedsApproval,
		Connectors: map[string]ConnectorControl{"jira": {Tools: map[string]ControlLevel{"delete": ControlBlocked}}},
	}
	if got := custom.EffectiveLevel("jira", "create"); got != ControlNeedsApproval {
		t.Errorf("custom connector, unlisted tool = %q, want the default needs_approval", got)
	}

	// DENY-CLOSED: configured policy, empty default → unlisted connector is blocked.
	noDefault := ConnectorControlPolicy{Connectors: map[string]ConnectorControl{"github": {Level: ControlAlwaysAllow}}}
	if got := noDefault.EffectiveLevel("slack", "post_message"); got != ControlBlocked {
		t.Errorf("unlisted connector under an empty default = %q, want blocked (deny-closed)", got)
	}
	if got := noDefault.EffectiveLevel("slack", ""); got != ControlBlocked {
		t.Errorf("unlisted server-level lookup under an empty default = %q, want blocked", got)
	}
}

// assertControlEdge asserts every field of a permitted connector-control edge.
func assertControlEdge(t *testing.T, e model.EdgeObservation, kind, ref, toolRef string) {
	t.Helper()
	if e.OriginKind != originIdentity || e.OriginRef != "org-9" {
		t.Errorf("origin = %s/%s, want identity/org-9", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != kind || e.ResourceRef != ref {
		t.Errorf("resource = %s/%s, want %s/%s", e.ResourceKind, e.ResourceRef, kind, ref)
	}
	if e.Mode != model.ModeUnknown {
		t.Errorf("mode = %q, want unknown (a grant is not an R/W access)", e.Mode)
	}
	if e.Source != model.SignalPolicy {
		t.Errorf("source = %q, want policy (access-map derives permitted=true from it)", e.Source)
	}
	if e.Confidence != model.ConfidenceAttributed {
		t.Errorf("confidence = %q", e.Confidence)
	}
	if e.ToolRef != toolRef {
		t.Errorf("toolRef = %q, want %q", e.ToolRef, toolRef)
	}
	if !e.ObservedAt.Equal(testTime) {
		t.Errorf("observedAt = %v", e.ObservedAt)
	}
	if e.Labels != nil {
		t.Errorf("labels must stay nil (operator tags, never connector data), got %v", e.Labels)
	}
}

// TestPermittedEdges proves the PERMITTED projection: non-blocked connectors emit
// mcp.server edges, non-blocked per-tool overrides emit mcp.tool edges in the exact
// "server/tool" form mcpResourceRef produces, blocked entries emit NOTHING, and the
// order is deterministic.
func TestPermittedEdges(t *testing.T) {
	edges := testControlPolicy().PermittedEdges("org-9", testTime)
	// github (needs_approval) + github/read_issues (delete_repo is blocked; legacy is
	// blocked): exactly two edges, servers sorted, tools after their server.
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2: %+v", len(edges), edges)
	}
	assertControlEdge(t, edges[0], resMCPServer, "github", "")
	assertControlEdge(t, edges[1], resMCP, "github/read_issues", "read_issues")

	// The tool ref byte-matches the observed-edge form derived from the wire name.
	if got := mcpResourceRef("mcp__github__read_issues"); got != edges[1].ResourceRef {
		t.Errorf("permitted ref %q != observed ref %q (the diff would never reconcile)", edges[1].ResourceRef, got)
	}

	// Determinism: two runs produce identical sequences.
	again := testControlPolicy().PermittedEdges("org-9", testTime)
	for i := range edges {
		if edges[i].ResourceKind != again[i].ResourceKind || edges[i].ResourceRef != again[i].ResourceRef {
			t.Errorf("order not deterministic at %d: %+v vs %+v", i, edges[i], again[i])
		}
	}

	// A fully blocked policy emits nothing (a block is not a grant).
	blocked := ConnectorControlPolicy{Connectors: map[string]ConnectorControl{
		"legacy": {Level: ControlBlocked, Tools: map[string]ControlLevel{"x": ControlBlocked}},
	}}
	if got := blocked.PermittedEdges("org-9", testTime); got != nil {
		t.Errorf("a blocked-only policy must emit no permitted edges, got %+v", got)
	}

	// Guards: no org to attribute to / not configured → nil.
	if got := testControlPolicy().PermittedEdges("", testTime); got != nil {
		t.Errorf("an empty orgRef must emit nothing, got %+v", got)
	}
	if got := (ConnectorControlPolicy{}).PermittedEdges("org-9", testTime); got != nil {
		t.Errorf("an unconfigured policy must emit nothing, got %+v", got)
	}
}

func TestMCPServerTool(t *testing.T) {
	cases := []struct {
		in           string
		server, tool string
		ok           bool
	}{
		{"mcp__github__create_issue", "github", "create_issue", true},
		// First "__" cuts, the rest stays in the tool — mirrors mcpResourceRef.
		{"mcp__github__create__issue", "github", "create__issue", true},
		// Server-only (malformed but attributable): governed at connector level.
		{"mcp__github", "github", "", true},
		{"Write", "", "", false},
		{"mcp__", "", "", false},
		{"mcp____tool", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		server, tool, ok := mcpServerTool(c.in)
		if server != c.server || tool != c.tool || ok != c.ok {
			t.Errorf("mcpServerTool(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, server, tool, ok, c.server, c.tool, c.ok)
		}
		// Whenever ok, reassembling must byte-match mcpResourceRef's form.
		if ok && tool != "" {
			if ref := server + "/" + tool; ref != mcpResourceRef(c.in) {
				t.Errorf("(%q) reassembled %q != mcpResourceRef %q", c.in, ref, mcpResourceRef(c.in))
			}
		}
	}
}

func TestControlTarget(t *testing.T) {
	// MCP-prefixed tool name resolves server AND tool.
	ev := coworkEvent{toolName: "mcp__github__delete_repo"}
	if server, tool, ok := controlTarget(ev); !ok || server != "github" || tool != "delete_repo" {
		t.Errorf("mcp name target = (%q,%q,%v)", server, tool, ok)
	}
	// A non-MCP tool name with an mcp_server_scope resolves the server from the scope
	// and the tool from the bare name, so per-tool overrides still apply (a blocked
	// tool must not fall back fail-open to an always_allow connector level, and an
	// always_allow exception must not raise a false HIGH under a needs_approval one).
	ev = coworkEvent{toolName: "search_issues", mcpServerScope: "github"}
	if server, tool, ok := controlTarget(ev); !ok || server != "github" || tool != "search_issues" {
		t.Errorf("scope target = (%q,%q,%v)", server, tool, ok)
	}
	// A built-in tool with no scope is not governed by connector controls.
	if _, _, ok := controlTarget(coworkEvent{toolName: "Write"}); ok {
		t.Error("a built-in tool without scope must not resolve a control target")
	}
}

// TestControlTargetScopeOnlyOverrides pins the two repro scenarios behind the
// review fix: per-tool overrides resolve on scope-only events.
func TestControlTargetScopeOnlyOverrides(t *testing.T) {
	p := ConnectorControlPolicy{Connectors: map[string]ConnectorControl{
		"github": {Level: ControlAlwaysAllow, Tools: map[string]ControlLevel{"delete_repo": ControlBlocked}},
		"jira":   {Level: ControlNeedsApproval, Tools: map[string]ControlLevel{"read_issues": ControlAlwaysAllow}},
	}}

	// Scope-only execution of a BLOCKED per-tool override under an allowed connector
	// must fire the HIGH drift (previously fail-open: the override was unreachable).
	ev := coworkEvent{sessionID: "s1", toolName: "delete_repo", mcpServerScope: "github", decisionSource: srcConfig}
	server, tool, ok := controlTarget(ev)
	if !ok {
		t.Fatal("scope-only event must resolve a control target")
	}
	if f, ok := controlDriftFinding(ev, p.EffectiveLevel(server, tool), server, tool); !ok || f.Severity != model.SeverityHigh {
		t.Errorf("blocked per-tool override on a scope-only execution must be HIGH drift, got %+v ok=%v", f, ok)
	}

	// Scope-only execution of an always_allow EXCEPTION under a needs-approval
	// connector must stay silent (previously a false HIGH).
	ev = coworkEvent{sessionID: "s1", toolName: "read_issues", mcpServerScope: "jira", decisionSource: srcConfig}
	server, tool, _ = controlTarget(ev)
	if f, ok := controlDriftFinding(ev, p.EffectiveLevel(server, tool), server, tool); ok {
		t.Errorf("always_allow per-tool exception must not raise drift, got %+v", f)
	}
}

// TestControlDriftFinding proves the live OBSERVED-vs-PERMITTED drift table: a
// blocked execution and an auto-approved needs-approval execution are HIGH findings;
// a manually-gated needs-approval execution and an always-allow one are the control
// WORKING (no finding); no session → nothing to attribute.
func TestControlDriftFinding(t *testing.T) {
	ev := coworkEvent{
		name: evtToolResult, sessionID: "sess-1", toolName: "mcp__github__delete_repo",
		decisionSource: srcConfig, promptID: "turn-1", at: testTime,
	}

	// blocked + executed → HIGH drift, full field check.
	f, ok := controlDriftFinding(ev, ControlBlocked, "github", "delete_repo")
	if !ok {
		t.Fatal("blocked execution must be a finding")
	}
	if f.Kind != findingKindControlDrift || f.Severity != model.SeverityHigh {
		t.Errorf("kind/severity = %s/%s", f.Kind, f.Severity)
	}
	if f.SubjectKind != originSession || f.SubjectRef != "sess-1" {
		t.Errorf("subject = %s/%s", f.SubjectKind, f.SubjectRef)
	}
	if want := "Cowork connector control drift: blocked connector/tool executed: github/delete_repo"; f.Title != want {
		t.Errorf("title = %q, want %q", f.Title, want)
	}
	if want := redact.Hash("sess-1|github|delete_repo|blocked|config|turn-1"); f.DetailHash != want {
		t.Errorf("detailHash = %q, want hash of session|server|tool|level|source|prompt", f.DetailHash)
	}
	if !f.OccurredAt.Equal(testTime) {
		t.Errorf("occurredAt = %v", f.OccurredAt)
	}
	if len(f.OWASPASI) != 1 || f.OWASPASI[0] != "ASI02" {
		t.Errorf("OWASPASI = %v, want [ASI02] (tool misuse)", f.OWASPASI)
	}
	if len(f.OWASPLLM) != 1 || f.OWASPLLM[0] != "LLM06:2025" {
		t.Errorf("OWASPLLM = %v, want [LLM06:2025]", f.OWASPLLM)
	}

	// blocked, server-level only (no tool): the title names just the connector.
	srvEv := coworkEvent{name: evtToolResult, sessionID: "sess-1", toolName: "post", mcpServerScope: "legacy", at: testTime}
	f, ok = controlDriftFinding(srvEv, ControlBlocked, "legacy", "")
	if !ok || f.Title != "Cowork connector control drift: blocked connector/tool executed: legacy" {
		t.Errorf("server-level blocked = %+v ok=%v", f, ok)
	}

	// needs_approval + AUTOMATIC source (config and hook) → HIGH drift.
	for _, src := range []string{srcConfig, srcHook} {
		e := ev
		e.decisionSource = src
		f, ok := controlDriftFinding(e, ControlNeedsApproval, "github", "delete_repo")
		if !ok || f.Severity != model.SeverityHigh {
			t.Errorf("needs_approval + %s must be a high finding, got %+v ok=%v", src, f, ok)
		}
		want := "Cowork connector control drift: needs-approval connector/tool ran auto-approved (" + src + "): github/delete_repo"
		if f.Title != want {
			t.Errorf("title = %q, want %q", f.Title, want)
		}
	}

	// needs_approval + MANUAL human decision → the gate worked, no finding.
	for _, src := range []string{"user_permanent", "user_temporary"} {
		e := ev
		e.decisionSource = src
		if _, ok := controlDriftFinding(e, ControlNeedsApproval, "github", "delete_repo"); ok {
			t.Errorf("needs_approval + %s must NOT be a finding (the human gate happened)", src)
		}
	}

	// needs_approval + unknown/empty source → fail-safe, not claimed automatic.
	e := ev
	e.decisionSource = ""
	if _, ok := controlDriftFinding(e, ControlNeedsApproval, "github", "delete_repo"); ok {
		t.Error("an unknown decision source must NOT be treated as auto-approved drift")
	}

	// always_allow → permitted, no finding.
	if _, ok := controlDriftFinding(ev, ControlAlwaysAllow, "github", "delete_repo"); ok {
		t.Error("an always-allow execution must NOT be a finding")
	}

	// no session → nothing to attribute.
	noSess := ev
	noSess.sessionID = ""
	if _, ok := controlDriftFinding(noSess, ControlBlocked, "github", "delete_repo"); ok {
		t.Error("a finding without a session must not be built")
	}
}
