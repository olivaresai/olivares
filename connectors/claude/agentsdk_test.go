// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestPermissionModeOrdering(t *testing.T) {
	for _, m := range []PermissionMode{PMDefault, PMPlan, PMAcceptEdits, PMAuto, PMDontAsk, PMBypass} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
	if PermissionMode("nonsense").Valid() {
		t.Error("unknown mode must be invalid")
	}
	// bypass is more permissive than acceptEdits; plan is not more permissive than default.
	if !PMBypass.MorePermissiveThan(PMAcceptEdits) {
		t.Error("bypass > acceptEdits")
	}
	if PMPlan.MorePermissiveThan(PMDefault) {
		t.Error("plan is the safest; not > default")
	}
	// Unknown observed mode fails closed (treated as more permissive than any cap).
	if !PermissionMode("rogue").MorePermissiveThan(PMBypass) {
		t.Error("unknown observed mode must fail closed")
	}
	if stricter(PMBypass, PMPlan) != PMPlan {
		t.Error("stricter(bypass, plan) = plan")
	}
}

// TestPermissionModeRankDontAsk pins that dontAsk is treated as RESTRICTIVE (it runs only
// pre-approved tools and hard-denies everything else; canUseTool is never called), NOT as
// a permissive mode — the bug the adversarial review caught. Ranking it above
// default/acceptEdits/auto would mis-flag a tightened fleet as drift and discard a tighter
// managed dontAsk cap.
func TestPermissionModeRankDontAsk(t *testing.T) {
	for _, looser := range []PermissionMode{PMDefault, PMAcceptEdits, PMAuto, PMBypass} {
		if PMDontAsk.MorePermissiveThan(looser) {
			t.Errorf("dontAsk must NOT be more permissive than %s", looser)
		}
		if !looser.MorePermissiveThan(PMDontAsk) {
			t.Errorf("%s must be more permissive than dontAsk", looser)
		}
	}
	// A managed dontAsk cap is STRICTER than an acceptEdits operator policy → it must win.
	if stricter(PMAcceptEdits, PMDontAsk) != PMDontAsk {
		t.Error("stricter(acceptEdits, dontAsk) must be dontAsk (the tighter cap)")
	}
	// Observed dontAsk under an acceptEdits cap is a TIGHTENING, never drift.
	at := time.Unix(1_700_000_000, 0).UTC()
	if _, ok := verifyAgentSDKMode("dontAsk", "s1", at, AgentSDKPolicy{MaxPermissionMode: PMAcceptEdits}, nil); ok {
		t.Error("dontAsk under an acceptEdits cap must NOT be drift (it is stricter)")
	}
	// ...but a managed dontAsk cap DOES make a looser observed mode drift.
	if _, ok := verifyAgentSDKMode("acceptEdits", "s1", at, AgentSDKPolicy{MaxPermissionMode: PMAuto}, fakeManaged{mode: PMDontAsk, ok: true}); !ok {
		t.Error("acceptEdits must drift against a stricter managed dontAsk cap")
	}
}

func TestParseAgentSDKPolicy(t *testing.T) {
	if _, ok, err := parseAgentSDKPolicy(""); ok || err != nil {
		t.Errorf("empty => not set, no error")
	}
	p, ok, err := parseAgentSDKPolicy(`{"max_permission_mode":"acceptEdits"}`)
	if !ok || err != nil || p.MaxPermissionMode != PMAcceptEdits {
		t.Errorf("parse: %+v ok=%v err=%v", p, ok, err)
	}
	if _, _, err := parseAgentSDKPolicy(`{"max_permission_mode":"god"}`); err == nil {
		t.Error("invalid mode must fail")
	}
	if _, _, err := parseAgentSDKPolicy(`{not json`); err == nil {
		t.Error("malformed must fail")
	}
}

type fakeManaged struct {
	mode PermissionMode
	ok   bool
}

func (f fakeManaged) MaxPermissionMode() (PermissionMode, bool) { return f.mode, f.ok }

func TestVerifyAgentSDKMode(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	policy := AgentSDKPolicy{MaxPermissionMode: PMAcceptEdits}

	// Within policy => no finding.
	if _, ok := verifyAgentSDKMode("default", "s1", at, policy, nil); ok {
		t.Error("default is within acceptEdits cap; no drift")
	}
	// Exceeds policy => finding, high severity for bypass.
	f, ok := verifyAgentSDKMode("bypassPermissions", "s1", at, policy, nil)
	if !ok {
		t.Fatal("bypass exceeds acceptEdits cap; must flag drift")
	}
	if f.Severity != model.SeverityHigh || f.SubjectRef != "s1" || f.DetailHash == "" {
		t.Errorf("finding = %+v", f)
	}
	if wantSub := "exceeds policy cap acceptEdits"; !strings.Contains(f.Title, wantSub) {
		t.Errorf("title = %q, want it to mention %q", f.Title, wantSub)
	}
	// Managed-settings is STRICTER (plan): now even acceptEdits drifts, attributed to managed.
	f2, ok := verifyAgentSDKMode("acceptEdits", "s1", at, policy, fakeManaged{mode: PMPlan, ok: true})
	if !ok {
		t.Fatal("acceptEdits exceeds the stricter managed cap plan")
	}
	if !strings.Contains(f2.Title, "managed-settings cap plan") {
		t.Errorf("managed cross-ref title = %q", f2.Title)
	}
	// No policy => never a drift finding (transitions still observed elsewhere).
	if _, ok := verifyAgentSDKMode("bypassPermissions", "s1", at, AgentSDKPolicy{}, nil); ok {
		t.Error("no policy => no drift finding")
	}
	// An UNKNOWN managed cap must fail CLOSED: it dominates as strictest, so even a
	// within-policy observed mode is flagged (surfacing the managed-settings misconfig)
	// rather than being silently discarded (fail-open).
	if _, ok := verifyAgentSDKMode("default", "s1", at, policy, fakeManaged{mode: PermissionMode("garbage"), ok: true}); !ok {
		t.Error("unknown managed cap must fail closed (dominate as strictest)")
	}
}

func TestManagedAgentsSurface(t *testing.T) {
	s := ManagedAgents()
	if s.CatalogKind != "agent" || len(s.RESTResources) == 0 || s.AuthMatrixRef == "" {
		t.Errorf("managed agents surface = %+v", s)
	}
}

// TestResolveSDKDecision is the precedence TABLE: it pins the verified Agent SDK
// evaluation order (2026-06-19), especially the security-critical invariants — hooks run
// FIRST, and a scoped deny / explicit ask binds EVEN IN bypassPermissions.
func TestResolveSDKDecision(t *testing.T) {
	cases := []struct {
		name    string
		req     SDKToolRequest
		outcome string
		step    SDKEvalStep
	}{
		// Hooks run first: a hook deny short-circuits even under bypass.
		{"hook deny beats bypass", SDKToolRequest{Mode: PMBypass, HookDenies: true, AllowMatches: true}, permDeny, StepHooks},
		// Deny rules block even in bypassPermissions (the headline invariant).
		{"scoped deny beats bypass", SDKToolRequest{Mode: PMBypass, DenyMatches: true}, permDeny, StepDenyRules},
		// Ask rules prompt even in bypassPermissions.
		{"ask prompts under bypass", SDKToolRequest{Mode: PMBypass, AskMatches: true}, permAsk, StepAskRules},
		// ...but a matching ask in dontAsk is a DENY (that mode never prompts).
		{"ask denied under dontAsk", SDKToolRequest{Mode: PMDontAsk, AskMatches: true}, permDeny, StepAskRules},
		// bypass approves everything reaching the mode step.
		{"bypass approves", SDKToolRequest{Mode: PMBypass}, permAllow, StepPermissionMode},
		// acceptEdits auto-approves writes; a non-write falls through.
		{"acceptEdits approves writes", SDKToolRequest{Mode: PMAcceptEdits, Writes: true}, permAllow, StepPermissionMode},
		{"acceptEdits non-write falls through to allow", SDKToolRequest{Mode: PMAcceptEdits, AllowMatches: true}, permAllow, StepAllowRules},
		{"acceptEdits non-write hits canUseTool", SDKToolRequest{Mode: PMAcceptEdits, HasResolver: true}, permAsk, StepCanUseTool},
		// plan never auto-approves writes — routes them to canUseTool; reads fall through.
		{"plan routes writes to canUseTool", SDKToolRequest{Mode: PMPlan, Writes: true}, permAsk, StepPermissionMode},
		{"plan read falls through to allow", SDKToolRequest{Mode: PMPlan, AllowMatches: true}, permAllow, StepAllowRules},
		// default: allow rule approves; else canUseTool; else deny-closed.
		{"default allow rule", SDKToolRequest{Mode: PMDefault, AllowMatches: true}, permAllow, StepAllowRules},
		{"default canUseTool", SDKToolRequest{Mode: PMDefault, HasResolver: true}, permAsk, StepCanUseTool},
		{"default no resolver deny-closed", SDKToolRequest{Mode: PMDefault}, permDeny, StepCanUseTool},
		// dontAsk skips canUseTool: a non-pre-approved call is denied; an allow rule still approves.
		{"dontAsk denies fall-through", SDKToolRequest{Mode: PMDontAsk, HasResolver: true}, permDeny, StepCanUseTool},
		{"dontAsk allow rule approves", SDKToolRequest{Mode: PMDontAsk, AllowMatches: true}, permAllow, StepAllowRules},
		// auto "falls through" like default (its classifier is a separate later gate).
		{"auto falls through to allow", SDKToolRequest{Mode: PMAuto, AllowMatches: true}, permAllow, StepAllowRules},
		{"auto hits canUseTool", SDKToolRequest{Mode: PMAuto, HasResolver: true}, permAsk, StepCanUseTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSDKDecision(tc.req)
			if got.Outcome != tc.outcome || got.DecidedBy != tc.step {
				t.Errorf("ResolveSDKDecision(%+v) = {%s, %s}, want {%s, %s}",
					tc.req, got.Outcome, got.DecidedBy, tc.outcome, tc.step)
			}
		})
	}
}

func TestSDKEvaluationOrder(t *testing.T) {
	order := SDKEvaluationOrder()
	if len(order) != 6 {
		t.Fatalf("want 6 evaluation steps, got %d", len(order))
	}
	for i, r := range order {
		if int(r.Step) != i {
			t.Errorf("step %d out of order: %s", i, r.Step)
		}
		if r.Rule == "" {
			t.Errorf("step %s has no rule text", r.Step)
		}
	}
	// The first step is hooks and the deny rule must mention bypassPermissions (the invariant).
	if order[0].Step != StepHooks {
		t.Error("hooks must be the first step")
	}
	if !strings.Contains(order[1].Rule, "bypassPermissions") {
		t.Errorf("deny-rules step must state it binds in bypassPermissions: %q", order[1].Rule)
	}
}

func TestInheritsToSubagents(t *testing.T) {
	for _, m := range []PermissionMode{PMBypass, PMAcceptEdits, PMAuto} {
		if !InheritsToSubagents(m) {
			t.Errorf("%s must inherit to subagents (non-overridable)", m)
		}
	}
	for _, m := range []PermissionMode{PMDefault, PMPlan, PMDontAsk} {
		if InheritsToSubagents(m) {
			t.Errorf("%s does not force-inherit to subagents", m)
		}
	}
}

// findingByRef indexes findings by SubjectRef for assertion.
func findingByRef(fs []model.FindingReport) map[string]model.FindingReport {
	m := map[string]model.FindingReport{}
	for _, f := range fs {
		m[f.SubjectRef] = f
	}
	return m
}

func TestSubagentInheritanceFinding(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	// bypass parent + subagents → HIGH (dominant multi-agent risk).
	c := AgentSDKConfig{PermissionMode: PMBypass, Agents: []string{"researcher", "writer"}}
	fs := c.PostureFindings("prog", at, AgentSDKPolicy{}, nil)
	f, ok := findingByRef(fs)["subagent_inheritance"]
	if !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("bypass parent + subagents must be HIGH inheritance finding: %+v", fs)
	}
	if !strings.Contains(f.Title, "cannot be overridden per subagent") {
		t.Errorf("inheritance finding must state non-overridability: %q", f.Title)
	}
	// acceptEdits / auto parent + subagents → MEDIUM (2026-06-19).
	for _, m := range []PermissionMode{PMAcceptEdits, PMAuto} {
		c := AgentSDKConfig{PermissionMode: m, Agents: []string{"x"}}
		f, ok := findingByRef(c.PostureFindings("prog", at, AgentSDKPolicy{}, nil))["subagent_inheritance"]
		if !ok || f.Severity != model.SeverityMedium {
			t.Errorf("%s parent inheritance must be MEDIUM: %+v", m, f)
		}
	}
	// A non-inheriting parent, or no subagents, emits no inheritance finding.
	if _, ok := findingByRef((AgentSDKConfig{PermissionMode: PMDefault, Agents: []string{"x"}}).PostureFindings("p", at, AgentSDKPolicy{}, nil))["subagent_inheritance"]; ok {
		t.Error("default parent must not emit an inheritance finding")
	}
	if _, ok := findingByRef((AgentSDKConfig{PermissionMode: PMBypass}).PostureFindings("p", at, AgentSDKPolicy{}, nil))["subagent_inheritance"]; ok {
		t.Error("no subagents → no inheritance finding")
	}
}

func TestDangerousKnobFindings(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	c := AgentSDKConfig{
		SessionStore:                    true,
		AllowDangerouslySkipPermissions: true,
		Plugins:                         []string{"/opt/plugins/foo"},
		MaxBudgetUsd:                    250,
		PermissionPromptToolName:        "mcp__rogue__approve",
	}
	// No authorizing policy → every knob is HIGH.
	got := findingByRef(c.PostureFindings("prog", at, AgentSDKPolicy{}, nil))
	for _, knob := range []string{"sessionStore", "allowDangerouslySkipPermissions", "plugins", "maxBudgetUsd", "permissionPromptToolName"} {
		f, ok := got[knob]
		if !ok {
			t.Fatalf("missing finding for knob %q: %+v", knob, got)
		}
		if f.Severity != model.SeverityHigh {
			t.Errorf("knob %q must be HIGH without authorization, got %s", knob, f.Severity)
		}
		if f.SubjectKind != subjectAgentSDK {
			t.Errorf("knob %q subject kind = %q, want %q", knob, f.SubjectKind, subjectAgentSDK)
		}
	}
	// maxBudgetUsd finding must be HONEST that it is a client-side estimate, not a hard cap.
	if !strings.Contains(got["maxBudgetUsd"].Title, "CLIENT-SIDE") {
		t.Errorf("maxBudgetUsd finding must flag the client-side caveat: %q", got["maxBudgetUsd"].Title)
	}

	// Explicit authorization degrades each knob HIGH→Info (2026-06-19).
	authed := AgentSDKPolicy{
		AllowSessionStore:    true,
		AllowSkipPermissions: true,
		AllowPlugins:         true,
		AllowMaxBudget:       true,
		PermissionPromptTool: "mcp__rogue__approve", // now the sanctioned (governed) tool
	}
	got = findingByRef(c.PostureFindings("prog", at, authed, nil))
	for _, knob := range []string{"sessionStore", "allowDangerouslySkipPermissions", "plugins", "maxBudgetUsd", "permissionPromptToolName"} {
		if f := got[knob]; f.Severity != model.SeverityInfo {
			t.Errorf("authorized knob %q must degrade to Info, got %s", knob, f.Severity)
		}
	}
	// A permissionPromptToolName that does NOT match the sanctioned tool stays HIGH.
	mismatch := AgentSDKConfig{PermissionPromptToolName: "mcp__other__approve"}
	if f := findingByRef(mismatch.PostureFindings("p", at, AgentSDKPolicy{PermissionPromptTool: "mcp__governed__approve"}, nil))["permissionPromptToolName"]; f.Severity != model.SeverityHigh {
		t.Errorf("unsanctioned permissionPromptToolName must be HIGH, got %s", f.Severity)
	}
	// A clean config emits no knob findings.
	if fs := (AgentSDKConfig{PermissionMode: PMPlan}).PostureFindings("p", at, AgentSDKPolicy{}, nil); len(fs) != 0 {
		t.Errorf("clean config must emit no findings, got %+v", fs)
	}
}

// TestPostureFindingsCapDrift proves PostureFindings REUSES verifyAgentSDKMode for the
// effective-cap drift rather than re-implementing precedence.
func TestPostureFindingsCapDrift(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	c := AgentSDKConfig{PermissionMode: PMBypass}
	// Declared bypass exceeds an acceptEdits cap → a HIGH drift finding (subject = program).
	fs := c.PostureFindings("prog-7", at, AgentSDKPolicy{MaxPermissionMode: PMAcceptEdits}, nil)
	f, ok := findingByRef(fs)["prog-7"]
	if !ok || f.Severity != model.SeverityHigh || f.Kind != findingKindPolicyChange {
		t.Fatalf("declared bypass over an acceptEdits cap must drift HIGH: %+v", fs)
	}
	// The program cap-drift must carry the agent-SDK subject kind (not "session"), so the
	// whole program's posture groups under one SubjectKind in the console.
	if f.SubjectKind != subjectAgentSDK {
		t.Errorf("program cap-drift SubjectKind = %q, want %q", f.SubjectKind, subjectAgentSDK)
	}
	// No cap policy → no drift finding (knobs still apply, but here there are none).
	if _, ok := findingByRef((AgentSDKConfig{PermissionMode: PMBypass}).PostureFindings("prog-7", at, AgentSDKPolicy{}, nil))["prog-7"]; ok {
		t.Error("no cap policy → no drift finding")
	}
}

func TestParseAgentSDKConfig(t *testing.T) {
	if _, ok, err := parseAgentSDKConfig(""); ok || err != nil {
		t.Errorf("empty => not set, no error")
	}
	c, ok, err := parseAgentSDKConfig(`{"permission_mode":"bypassPermissions","session_store":true,"agents":["a"]}`)
	if !ok || err != nil || c.PermissionMode != PMBypass || !c.SessionStore || len(c.Agents) != 1 {
		t.Errorf("parse: %+v ok=%v err=%v", c, ok, err)
	}
	if _, _, err := parseAgentSDKConfig(`{"permission_mode":"god"}`); err == nil {
		t.Error("invalid mode must fail")
	}
	if _, _, err := parseAgentSDKConfig(`{not json`); err == nil {
		t.Error("malformed must fail")
	}
}
