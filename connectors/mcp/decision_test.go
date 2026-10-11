// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fsWriteConditions blocks writes under /etc and asks a human for recursive writes.
const fsWriteConditions = `
@id("no-etc")
forbid(principal, action, resource) when { context.arguments.path like "/etc/*" };
@id("recursive")
@decision("ask")
forbid(principal, action, resource) when { context.arguments has recursive && context.arguments.recursive };
`

// countingGate records every consultation and answers with a fixed status.
type countingGate struct {
	status GateStatus
	calls  int
	last   ToolApprovalRequest
}

func (g *countingGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	g.calls++
	g.last = req
	return GateDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: req.PlanHash}, nil
}

func newDecisionRS(t *testing.T, jwks []byte, gate ApprovalGate, up Upstream, aud GateAuditor, policies ...ToolPolicy) *ResourceServer {
	t.Helper()
	ts, err := NewToolset(policies)
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       gate,
		Upstream:                   up,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    aud,
		Clock:                      rsClock,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

func lastDecision(t *testing.T, aud *capturingAuditor) ToolDecision {
	t.Helper()
	if len(aud.decisions) == 0 {
		t.Fatal("no decision was audited")
	}
	return aud.decisions[len(aud.decisions)-1]
}

// TestDecisionTableConditions drives each row of the table through the live
// tools/call gate: a Cedar forbid blocks, an @decision("ask") forbid consults
// the approval gate, a block wins over an ask, and no match allows.
func TestDecisionTableConditions(t *testing.T) {
	const askRule = "tool:fs_write/condition:recursive"
	cases := []struct {
		name       string
		params     string
		gate       GateStatus
		wantStatus int
		wantGate   int
		wantCalled bool
		decision   Decision
		rule       string
		message    string
	}{
		{"no match allows", `{"path":"/tmp/a"}`, StatusPending, http.StatusOK, 0, true, DecisionAllow, "tool:fs_write", ""},
		{"forbid blocks", `{"path":"/etc/passwd"}`, StatusPending, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:no-etc", "tool call not permitted by server policy"},
		{"ask forbid asks", `{"path":"/tmp","recursive":true}`, StatusPending, http.StatusForbidden, 1, false, DecisionAsk, askRule, "these tool arguments require human approval (pending)"},
		{"approved ask runs", `{"path":"/tmp","recursive":true}`, StatusApproved, http.StatusOK, 1, true, DecisionAsk, askRule, ""},
		{"block wins over ask", `{"path":"/etc/x","recursive":true}`, StatusPending, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:no-etc", ""},
		{"unreadable condition blocks", `{"recursive":false}`, StatusPending, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:no-etc", ""},
		{"array arguments block", `[]`, StatusApproved, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:arguments", ""},
		{"string arguments block", `"x"`, StatusApproved, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:arguments", ""},
		{"null arguments block", `null`, StatusApproved, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:arguments", ""},
		{"omitted arguments block", "", StatusApproved, http.StatusForbidden, 0, false, DecisionBlock, "tool:fs_write/condition:no-etc", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sg := newSigner(t)
			up := &fakeUpstream{}
			aud := &capturingAuditor{}
			gate := &countingGate{status: tc.gate}
			rs := newDecisionRS(t, sg.jwks, gate, up, aud,
				ToolPolicy{Name: "fs_write", RequiredScope: "tools:write", Conditions: fsWriteConditions})
			params := `{"name":"fs_write"}`
			if tc.params != "" {
				params = `{"name":"fs_write","arguments":` + tc.params + `}`
			}
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, customToolsCallReq(sg.mint(t, rsResource, "tools:write", validExp()), params))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.message != "" && !strings.Contains(w.Body.String(), tc.message) {
				t.Errorf("refusal = %s, want %q", w.Body.String(), tc.message)
			}
			if gate.calls != tc.wantGate {
				t.Errorf("approval gate consulted %d times, want %d", gate.calls, tc.wantGate)
			}
			if up.called != tc.wantCalled {
				t.Errorf("upstream called = %v, want %v", up.called, tc.wantCalled)
			}
			d := lastDecision(t, aud)
			if d.Decision != tc.decision || d.RuleID != tc.rule {
				t.Errorf("audited row = %s %q, want %s %q", d.Decision, d.RuleID, tc.decision, tc.rule)
			}
			if d.Server != rsResource {
				t.Errorf("audited server = %q, want %q", d.Server, rsResource)
			}
		})
	}
}

// TestDecisionTableConditionsOnlyTighten: a destructive tool whose conditions
// match nothing still asks; conditions can never lower an entry to allow.
func TestDecisionTableConditionsOnlyTighten(t *testing.T) {
	sg := newSigner(t)
	up := &fakeUpstream{}
	gate := &countingGate{status: StatusPending}
	rs := newDecisionRS(t, sg.jwks, gate, up, &capturingAuditor{},
		ToolPolicy{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true, Conditions: fsWriteConditions})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, customToolsCallReq(sg.mint(t, rsResource, "tools:admin", validExp()),
		`{"name":"delete_db","arguments":{"path":"/tmp/a"}}`))
	if w.Code != http.StatusForbidden || gate.calls != 1 || up.called {
		t.Fatalf("destructive call: status %d, gate calls %d, upstream %v; want 403, 1, false", w.Code, gate.calls, up.called)
	}
	if !strings.Contains(w.Body.String(), "destructive tool requires human approval (pending)") {
		t.Errorf("the published destructive refusal changed: %s", w.Body.String())
	}

	// A matching forbid on a destructive tool blocks before any approval is asked.
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, customToolsCallReq(sg.mint(t, rsResource, "tools:admin", validExp()),
		`{"name":"delete_db","arguments":{"path":"/etc/db"}}`))
	if w.Code != http.StatusForbidden || gate.calls != 1 || up.called {
		t.Fatalf("blocked destructive call: status %d, gate calls %d, upstream %v; want 403, still 1, false", w.Code, gate.calls, up.called)
	}
}

// countingPinVerifier counts every pin consultation.
type countingPinVerifier struct{ calls int }

func (v *countingPinVerifier) Verify(context.Context, string, string, string) (bool, string, error) {
	v.calls++
	return true, "", nil
}

func (v *countingPinVerifier) RecordPin(context.Context, string, string, string) error { return nil }

// TestDecisionTableBlocksBeforeSideEffects: a blocked call is refused before the
// pin store is read; an allowed call still is.
func TestDecisionTableBlocksBeforeSideEffects(t *testing.T) {
	sg := newSigner(t)
	ts, err := NewToolset([]ToolPolicy{{Name: "fs_write", RequiredScope: "tools:write", Conditions: fsWriteConditions}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	pins := &countingPinVerifier{}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer}, Issuer: rsIssuer, IssuerJWKS: sg.jwks,
		Toolset: ts, Gate: &countingGate{status: StatusApproved}, Upstream: &fakeUpstream{},
		DurableTaskStore: newMemoryDurableTaskStore(), Auditor: &capturingAuditor{}, Clock: rsClock,
		PinVerifier: pins, DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	token := sg.mint(t, rsResource, "tools:write", validExp())
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, customToolsCallReq(token, `{"name":"fs_write","arguments":{"path":"/etc/passwd"}}`))
	if w.Code != http.StatusForbidden || pins.calls != 0 {
		t.Fatalf("blocked call: status %d, pin store read %d times; want 403 before any read", w.Code, pins.calls)
	}
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, customToolsCallReq(token, `{"name":"fs_write","arguments":{"path":"/tmp/a"}}`))
	if w.Code != http.StatusOK || pins.calls != 1 {
		t.Fatalf("allowed call: status %d, pin store read %d times; want 200 after one read", w.Code, pins.calls)
	}
}

// TestDecisionTableDefaultDenyRule names the row that refuses an unknown tool.
func TestDecisionTableDefaultDenyRule(t *testing.T) {
	sg := newSigner(t)
	aud := &capturingAuditor{}
	rs := newDecisionRS(t, sg.jwks, &countingGate{status: StatusApproved}, &fakeUpstream{}, aud,
		ToolPolicy{Name: "search", RequiredScope: "tools:read"},
		ToolPolicy{Name: "wipe", Deny: true})
	for tool, rule := range map[string]string{"absent": "default-deny", "wipe": "tool:wipe"} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, customToolsCallReq(sg.mint(t, rsResource, "tools:read", validExp()),
			`{"name":"`+tool+`","arguments":{}}`))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", tool, w.Code)
		}
		if d := lastDecision(t, aud); d.Decision != DecisionBlock || d.RuleID != rule {
			t.Errorf("%s: audited row = %s %q, want block %q", tool, d.Decision, d.RuleID, rule)
		}
	}
}

// TestToolConditionsCompile refuses conditions that could not mean what they say.
func TestToolConditionsCompile(t *testing.T) {
	for name, src := range map[string]string{
		"permit rule":        `permit(principal, action, resource);`,
		"unknown decision":   `@decision("allow") forbid(principal, action, resource);`,
		"syntax error":       `forbid(principal, action, resource) when {`,
		"comment only":       `// nothing here`,
		"principal in group": `forbid(principal in Group::"contractors", action, resource);`,
		"resource is in":     `forbid(principal, action, resource is Resource in Folder::"etc");`,
	} {
		if _, err := NewToolset([]ToolPolicy{{Name: "t", Conditions: src}}); err == nil {
			t.Errorf("%s: NewToolset accepted %q", name, src)
		}
	}
	if _, err := NewToolset([]ToolPolicy{{Name: "t", Conditions: fsWriteConditions}}); err != nil {
		t.Fatalf("valid conditions refused: %v", err)
	}
}

// TestToolConditionsRequestMapping pins the Cedar request: principal, action,
// resource and context.tool. A renamed entity type would make a scoped forbid
// stop matching, so each one is read here.
func TestToolConditionsRequestMapping(t *testing.T) {
	ts, err := NewToolset([]ToolPolicy{{Name: "t", Conditions: `
@id("scoped") forbid(principal == Principal::"agent:claude", action == Action::"tools/call", resource == Resource::"t") when { context.tool == "t" };
`}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	if v := ts.decide("t", ToolPolicy{Name: "t"}, "agent:claude", []byte(`{}`)); v.Decision != DecisionBlock || v.RuleID != "tool:t/condition:scoped" {
		t.Errorf("scoped forbid for its principal = %s %q, want block", v.Decision, v.RuleID)
	}
	if v := ts.decide("t", ToolPolicy{Name: "t"}, "agent:other", []byte(`{}`)); v.Decision != DecisionAllow {
		t.Errorf("scoped forbid for another principal = %s %q, want allow", v.Decision, v.RuleID)
	}
}

// TestToolConditionsArgumentMapping pins how JSON arguments reach Cedar.
func TestToolConditionsArgumentMapping(t *testing.T) {
	ts, err := NewToolset([]ToolPolicy{{Name: "t", Conditions: `
@id("big") forbid(principal, action, resource) when { context.arguments.count > 10 };
@id("tag") forbid(principal, action, resource) when { context.arguments.tags.contains("prod") };
@id("nested") forbid(principal, action, resource) when { context.arguments.opts has force && context.arguments.opts.force };
@id("ratio") forbid(principal, action, resource) when { context.arguments has ratio && context.arguments.ratio == decimal("0.5") };
@id("mode") forbid(principal, action, resource) when { context.arguments has mode && context.arguments.mode == 1 };
@id("nullable") forbid(principal, action, resource) when { context.arguments has gone };
`}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	base := `"count":1,"tags":["dev"],"opts":{"force":false}`
	for args, want := range map[string]string{
		`{` + base + `}`:                              "",
		`{"count":11,"tags":[],"opts":{}}`:            "tool:t/condition:big",
		`{"count":1,"tags":["dev","prod"],"opts":{}}`: "tool:t/condition:tag",
		`{"count":1,"tags":[],"opts":{"force":true}}`: "tool:t/condition:nested",
		`{` + base + `,"ratio":0.5}`:                  "tool:t/condition:ratio",
		`{` + base + `,"gone":null}`:                  "",
		`{` + base + `,"ratio":5e-1}`:                 "tool:t/condition:ratio",
		`{` + base + `,"mode":1}`:                     "tool:t/condition:mode",
		`{` + base + `,"mode":1.0}`:                   "tool:t/condition:mode",
		`{` + base + `,"mode":1e0}`:                   "tool:t/condition:mode",
		`{` + base + `,"mode":2}`:                     "",
		// What a condition could misread is refused, never passed through.
		`{` + base + `,"x":0.12345}`:               "tool:t/condition:arguments",
		`{` + base + `,"x":9223372036854775808}`:   "tool:t/condition:arguments",
		`{` + base + `,"x":1e999999999}`:           "tool:t/condition:arguments",
		`{` + base + `,"x":[1,0.00001]}`:           "tool:t/condition:arguments",
		`{` + base + `,"Count":11}`:                "tool:t/condition:arguments",
		`{` + base + `,"x":{"force":1,"FORCE":2}}`: "tool:t/condition:arguments",
		`{` + base + `,"ſtate":1,"state":2}`:       "tool:t/condition:arguments",
		`{` + base + `,"x":{"k":1,"\u212a":2}}`:    "tool:t/condition:arguments",
	} {
		v := ts.decide("t", ToolPolicy{Name: "t"}, "agent:claude", []byte(args))
		got := ""
		if v.Decision != DecisionAllow {
			got = v.RuleID
		}
		if got != want {
			t.Errorf("args %s: rule %q (%s), want %q", args, got, v.Decision, want)
		}
	}
}

// TestPolicyDigestBindsConditions: conditions are part of the authorizing policy,
// and an entry without them keeps the digest it had before conditions existed.
func TestPolicyDigestBindsConditions(t *testing.T) {
	plain := ToolPolicy{Name: "t", RequiredScope: "tools:read"}
	conditional := plain
	conditional.Conditions = fsWriteConditions
	pin, coaz := pinBinding{State: "unwired"}, coazBinding{State: "unwired"}
	if toolCallPolicyDigest(plain, pin, coaz) == toolCallPolicyDigest(conditional, pin, coaz) {
		t.Fatal("conditions are not bound into the policy digest")
	}
	blank := plain
	blank.Conditions = " \n\t"
	if toolCallPolicyDigest(blank, pin, coaz) != toolCallPolicyDigest(plain, pin, coaz) {
		t.Error("whitespace-only conditions compile to nothing but changed the digest")
	}
	// Measured with the same call on origin/main ede66ce5, before this change.
	const mainDigest = "9e5be4b1ccb139dd9be6749247f39ef7d0b5ed065acb4b5c5306d08acec3abc9"
	if got := toolCallPolicyDigest(plain, pin, coaz); got != mainDigest {
		t.Errorf("digest of an entry without conditions changed: %s", got)
	}
}
