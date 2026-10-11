// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// verdictGate counts the approval gate consultations around a fixed answer.
type verdictGate struct {
	inner     ApprovalGate
	calls     int
	consumers int
}

func (g *verdictGate) Authorize(ctx context.Context, req ToolApprovalRequest) (GateDecision, error) {
	g.calls++
	if req.ConsumerID != "" {
		g.consumers++
	}
	return g.inner.Authorize(ctx, req)
}

// fixedGate answers every consultation with one decision or error.
type fixedGate struct {
	dec GateDecision
	err error
}

func (g fixedGate) Authorize(context.Context, ToolApprovalRequest) (GateDecision, error) {
	return g.dec, g.err
}

// approvedGate approves the requested plan without spending it.
type approvedGate struct{}

func (approvedGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "appr-3", Status: StatusApproved, PlanHash: req.PlanHash}, nil
}

// verdictCase is one tools/call path, pinned byte for byte on the wire and in
// the audit trail.
type verdictCase struct {
	name     string
	tool     string // toolsCallReq tool (Tasks declared) when params, rc and l7 are empty
	params   string // a legacy request's raw params
	rc       string // a 2026-07-28 delete_db request's client capabilities (rcToolsCall)
	l7       bool   // a 2026-07-28 request the pre-body Mcp-Name gate reads
	scope    string // default tools:read
	gate     ApprovalGate
	pin      ToolPinVerifier
	coaz     COAZEvaluator
	mediator ElicitationMediator
	noTasks  bool
	saturate bool
	trace    string // the request traceparent header
	// extra returns the round-trip members of an rc request, given the server.
	extra func(t *testing.T, rs *ResourceServer) string
}

// verdictTrace is a W3C traceparent the traced cases send.
const verdictTrace = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

var sealedAskState = regexp.MustCompile(`"requestState":"` + regexp.QuoteMeta(askStatePrefix) + `[A-Za-z0-9_-]+"`)

func verdictRS(t *testing.T, jwks []byte, tc verdictCase, aud GateAuditor, up Upstream) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "admin_only", RequiredScope: "tools:read", AllowedRoles: []string{"admin"}},
		{Name: "fs_write", RequiredScope: "tools:read", Conditions: fsWriteConditions},
		{Name: "delete_db", RequiredScope: "tools:read", Destructive: true},
		{Name: "view_only", RequiredScope: "tools:read", AppOnly: true},
		{Name: "retired", RequiredScope: "tools:read", Deny: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	cfg := ResourceServerConfig{
		Resource:             rsResource,
		AuthorizationServers: []string{rsIssuer},
		Issuer:               rsIssuer,
		IssuerJWKS:           jwks,
		Toolset:              ts,
		Gate:                 tc.gate,
		Upstream:             up,
		Auditor:              aud,
		Clock:                rsClock,
		PinVerifier:          tc.pin,
		COAZEvaluator:        tc.coaz,
		ElicitationMediator:  tc.mediator,
	}
	if tc.rc != "" || tc.l7 {
		cfg.RevisionMode = revisionModeDual
	} else {
		cfg.DisableNextRevisionHeaders = true
	}
	if !tc.noTasks {
		cfg.DurableTaskStore = newMemoryDurableTaskStore()
	}
	rs, err := NewResourceServer(cfg)
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// sealAsk seals this gateway's approval round-trip state for a delete_db call
// with rcToolsCall's arguments.
func sealAsk(t *testing.T, rs *ResourceServer) string {
	t.Helper()
	canon, err := canonicalizeToolCallParams(json.RawMessage(`{"name":"delete_db","arguments":{"db":"prod"}}`))
	if err != nil {
		t.Fatal(err)
	}
	state, err := rs.asks.seal(askBinding{
		Tenant: rs.tenant, Resource: rs.resource, Subject: refusalSubject, ClientID: refusalClient,
		Tool: "delete_db", Plan: toolCallPlanHash("delete_db", refusalSubject, hashArgs(canon.Args)),
	}, rs.clock())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func answered(action string) func(*testing.T, *ResourceServer) string {
	return func(t *testing.T, rs *ResourceServer) string { return askAnswer(sealAsk(t, rs), action) }
}

// renderDecision lists the non-zero fields of an audit record in declaration
// order. At is checked separately: every record carries the server clock.
func renderDecision(d ToolDecision) string {
	v := reflect.ValueOf(d)
	var parts []string
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "At" || v.Field(i).IsZero() {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", name, fmt.Sprint(v.Field(i).Interface())))
	}
	return strings.Join(parts, " ")
}

// renderVerdict is everything a tools/call outcome shows: the HTTP status, the
// headers a refusal or result sets, the body, every audit record, the binding
// a grant was claimed under, and whether the upstream and the approval gate
// were reached.
func renderVerdict(t *testing.T, w *httptest.ResponseRecorder, aud *recordingAuditor, up *fakeUpstream, gate *verdictGate) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "status %d\n", w.Code)
	for _, h := range []string{"Cache-Control", "Content-Type", "Retry-After", "Vary", "Www-Authenticate"} {
		if v := w.Header().Values(h); len(v) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", h, strings.Join(v, ", "))
		}
	}
	fmt.Fprintf(&b, "body %s\n", sealedAskState.ReplaceAllString(strings.TrimSpace(w.Body.String()), `"requestState":"<sealed>"`))
	for _, d := range aud.decisions {
		if !d.At.Equal(rsClock()) {
			t.Errorf("audit At = %v, want the server clock", d.At)
		}
		fmt.Fprintf(&b, "audit %s\n", renderDecision(d))
	}
	if digest := aud.lastRecorded.EffectDigest; digest != "" {
		fmt.Fprintf(&b, "claim %s\n", digest)
	}
	fmt.Fprintf(&b, "upstream %v gate %d consumer %d\n", up.called, gate.calls, gate.consumers)
	return b.String()
}

// TestToolsCallVerdictPinned pins every way an admitted tools/call ends before
// its evidence claim, and the grants that reach it, through ServeHTTP: each
// gate's refusal (status, RPC error, headers, audit record) and each grant's
// allow record and claimed effect digest. The verdict module must keep all of
// it byte for byte.
func TestToolsCallVerdictPinned(t *testing.T) {
	pending := func() ApprovalGate { return &approvalDesk{} }
	approved := func() ApprovalGate { d := &approvalDesk{}; d.approve(); return d }
	const mrtrAnswer = `{"name":"search","arguments":{},"inputResponses":{"q":{"action":"accept","content":{"a":"b"}}}}`
	cases := []verdictCase{
		{name: "strict canonicalization", params: `{"name":"search","name":"search"}`},
		{name: "missing name", params: `{"arguments":{}}`},
		{name: "blank name", params: `{"name":"  ","arguments":{}}`},
		{name: "tasks without durable store", tool: "search", noTasks: true},
		{name: "tool not in toolset", tool: "absent"},
		{name: "operator-denied entry", tool: "retired"},
		{name: "insufficient scope", tool: "search", scope: "tools:other"},
		{name: "caller role", tool: "admin_only"},
		{name: "condition blocks", params: `{"name":"fs_write","arguments":{"path":"/etc/x"}}`},
		{name: "task inventory saturated", tool: "search", saturate: true},
		{name: "pin attestation error", tool: "search", pin: &refusalPinAttestor{err: errors.New("pin store down")}},
		{name: "pin attestation mismatch", tool: "search", pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Reason: "changed"}}},
		{name: "pin attestation incomplete", tool: "search", pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true, Attested: true}}},
		{name: "pin attestation fingerprint only", tool: "search",
			pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true, Attested: true, Pin: ToolPinAttestation{Fingerprint: "fp-1"}}}},
		{name: "pin attestation version only", tool: "search",
			pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true, Attested: true, Pin: ToolPinAttestation{Version: "v1"}}}},
		{name: "pin verification error", tool: "search", pin: &fakePinVerifier{err: errors.New("pin store down")}},
		{name: "pin mismatch", tool: "search", pin: &fakePinVerifier{reason: "changed"}},
		{name: "COAZ error", tool: "search", coaz: &stubCOAZEvaluator{err: errors.New("pdp down")}},
		{name: "COAZ deny", tool: "search", coaz: &stubCOAZEvaluator{decision: COAZDecision{Reason: "no"}}},
		{name: "COAZ deny traced", tool: "search", coaz: &stubCOAZEvaluator{decision: COAZDecision{Reason: "no"}}, trace: verdictTrace},
		{name: "destructive pending", tool: "delete_db", gate: pending()},
		{name: "condition ask pending", params: `{"name":"fs_write","arguments":{"path":"/tmp","recursive":true}}`, gate: pending()},
		{name: "approval bound to no plan", tool: "delete_db", gate: fixedGate{dec: GateDecision{ApprovalRef: "appr-2", Status: StatusApproved}}},
		{name: "argument not reviewable", tool: "delete_db", gate: fixedGate{err: ErrArgumentsNotReviewable}},
		{name: "approval gate error", tool: "delete_db", gate: fixedGate{err: errors.New("bridge down")}},
		{name: "round trip offered", rc: `{"elicitation":{}}`, gate: pending()},
		{name: "round trip invalid state", rc: `{"elicitation":{}}`, gate: pending(),
			extra: func(*testing.T, *ResourceServer) string { return askAnswer(askStatePrefix+"forged", "accept") }},
		{name: "round trip ledger full", rc: `{"elicitation":{}}`, gate: pending(), extra: func(t *testing.T, rs *ResourceServer) string {
			state := sealAsk(t, rs)
			rs.asks.shares[rs.tenant+"\x00"+refusalSubject] = maxSpentAskStatesPerSubject
			return askAnswer(state, "accept")
		}},
		{name: "round trip declined", rc: `{"elicitation":{}}`, gate: pending(), extra: answered("decline")},
		{name: "round trip not spent", rc: `{"elicitation":{}}`, gate: approvedGate{}, extra: answered("accept")},
		{name: "MRTR answer needs approval", params: mrtrAnswer, gate: pending(),
			mediator: &fakeElicitationMediator{allow: true, hitl: true}},
		{name: "MRTR answer denied by content", params: mrtrAnswer, mediator: &fakeElicitationMediator{reason: "harvest"}},
		{name: "L7 tool not in toolset", tool: "absent", l7: true},
		{name: "L7 insufficient scope", tool: "search", scope: "tools:other", l7: true},
		{name: "L7 caller role", tool: "admin_only", l7: true},
		{name: "L7 tool not in toolset traced", tool: "absent", l7: true, trace: verdictTrace},
		{name: "grant", tool: "search"},
		{name: "grant app-only", tool: "view_only"},
		{name: "grant attested pin and COAZ allow", tool: "search",
			pin:  &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true, Attested: true, Pin: ToolPinAttestation{Fingerprint: "fp-1", Version: "v1"}}},
			coaz: &stubCOAZEvaluator{decision: COAZDecision{Allow: true, DecisionRef: "d-1", PolicyVersion: "p-1"}}},
		{name: "grant verified pin", tool: "search", pin: &fakePinVerifier{allowed: true}},
		{name: "grant first-use attestor", tool: "search", pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true}}},
		{name: "grant traced", tool: "search", trace: verdictTrace},
		{name: "grant destructive approved", tool: "delete_db", gate: approved()},
		{name: "grant round trip", rc: `{"elicitation":{}}`, gate: approved(), extra: answered("accept")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sg := newSigner(t)
			aud := &recordingAuditor{}
			up := &fakeUpstream{}
			inner := tc.gate
			if inner == nil {
				inner = &approvalDesk{}
			}
			gate := &verdictGate{inner: inner}
			tc.gate = gate
			rs := verdictRS(t, sg.jwks, tc, aud, up)
			scope := tc.scope
			if scope == "" {
				scope = "tools:read"
			}
			bearer := refusalMint(t, sg, scope)
			if tc.saturate {
				owner := taskOwner{Tenant: rs.tenant, Issuer: rsIssuer, Subject: refusalSubject, ClientID: refusalClient}
				for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
					if _, _, ok := rs.taskLedger.reserveAdmission(owner); !ok {
						t.Fatalf("reservation %d refused before saturation", i)
					}
				}
			}
			req := toolsCallReq(bearer, tc.tool, `{}`)
			switch {
			case tc.params != "":
				req = customToolsCallReq(bearer, tc.params)
			case tc.l7:
				req = nextReqRaw(bearer, "tools/call", tc.tool,
					`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tc.tool+`"}}`)
			case tc.rc != "":
				extra := ""
				if tc.extra != nil {
					extra = tc.extra(t, rs)
				}
				req = rcToolsCall(bearer, "delete_db", tc.rc, extra)
			}
			if tc.trace != "" {
				req.Header.Set("traceparent", tc.trace)
			}
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, req)
			if got, want := renderVerdict(t, w, aud, up, gate), verdictPins[tc.name]; got != want {
				t.Errorf("outcome changed:\n--- got\n%s--- want\n%s", got, want)
			}
		})
	}
}

// verdictPins is each case's outcome as measured on origin/main 67ce5c22 before
// the verdict module (renderVerdict format; a sealed round-trip state is
// shown as <sealed>).
var verdictPins = map[string]string{
	"strict canonicalization": `status 400
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-32602,"message":"malformed tools/call params (strict decoding refused)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Reason="tools/call params refused by strict canonicalization (dup/case-alias/malformed keys)" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="default-deny" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"missing name": `status 200
Cache-Control: no-store
Content-Type: application/json
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"text":"tools/call requires a string 'name'","type":"text"}],"isError":true}}
upstream false gate 0 consumer 0
`,
	"tasks without durable store": `status 503
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31010,"message":"MCP Tasks is unavailable because durable task persistence is not configured"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" Reason="MCP Tasks requested without a durable task store; request refused before upstream dispatch" MCPTag="MCP07" TokenBinding="bearer" Decision="block" RuleID="tasks:unavailable" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"tool not in toolset": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by server policy"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="absent" Reason="deny-by-default (tool not in server toolset)" MCPTag="MCP01" TokenBinding="bearer" Decision="block" RuleID="default-deny" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"operator-denied entry": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by server policy"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="retired" Reason="deny-by-default (tool not in server toolset)" MCPTag="MCP01" TokenBinding="bearer" Decision="block" RuleID="tool:retired" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"insufficient scope": `status 403
Cache-Control: no-store
Content-Type: application/json
Www-Authenticate: Bearer error="insufficient_scope", scope="tools:read", resource_metadata="https://mcp.olivares.example/.well-known/oauth-protected-resource"
body {"error":{"code":-31001,"message":"insufficient scope for tool (step up required)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="insufficient scope" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"caller role": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted for caller role"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="admin_only" RequiredScope="tools:read" Reason="caller role not permitted for tool" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="tool:admin_only" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"condition blocks": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool call not permitted by server policy"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="fs_write" RequiredScope="tools:read" Reason="a condition forbids these arguments" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="tool:fs_write/condition:no-etc" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"task inventory saturated": `status 429
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"the retained task inventory is full; reconcile the retained tasks before issuing more calls"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="retained task inventory saturated (owner 64/64, gateway 64/65536); task-producing forwards refused until reconciled" MCPTag="MCP07" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin attestation error": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool pin verification unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin verification error (fail-closed)" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin attestation mismatch": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool definition changed since approval (rug-pull detected)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin mismatch: changed" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin attestation incomplete": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool pin attestation unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin attestation incomplete (fail-closed)" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin verification error": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool pin verification unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin verification error (fail-closed)" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin mismatch": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool definition changed since approval (rug-pull detected)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin mismatch: changed" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"COAZ error": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool authorization evaluation unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="COAZ evaluation error (fail-closed)" MCPTag="MCP02" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"COAZ deny": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by authorization policy"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="COAZ deny: no" MCPTag="MCP02" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"destructive pending": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"destructive tool requires human approval (pending)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="destructive tool not approved (pending)" ApprovalRef="appr-7" MCPTag="MCP02" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"condition ask pending": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"these tool arguments require human approval (pending)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="fs_write" RequiredScope="tools:read" Reason="conditional tool call not approved (pending)" ApprovalRef="appr-7" MCPTag="MCP02" TokenBinding="bearer" Decision="ask" RuleID="tool:fs_write/condition:recursive" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"approval bound to no plan": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"destructive tool requires human approval (approved)"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="destructive tool not approved (approved)" ApprovalRef="appr-2" MCPTag="MCP02" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"argument not reviewable": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"argument not reviewable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="argument not reviewable" MCPTag="MCP07" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"approval gate error": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"approval gate error"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="gate error (fail-closed)" MCPTag="MCP07" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"round trip offered": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":9,"jsonrpc":"2.0","result":{"content":[],"requestState":"<sealed>","resultType":"input_required","inputRequests":{"olivares.approval":{"method":"elicitation/create","params":{"mode":"form","message":"Olivares needs a human approval before it runs tool \"delete_db\". Approve request appr-7 in the Olivares console, then accept here.","requestedSchema":{"properties":{},"type":"object"}}}}}}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="destructive tool not approved (pending); approval round trip offered" ApprovalRef="appr-7" MCPTag="MCP02" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 0
`,
	"round trip invalid state": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"the approval round trip is invalid, expired or already used; call the tool again"},"id":9,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="approval round trip refused: not a state this gateway issued" MCPTag="MCP07" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"round trip ledger full": `status 429
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"too many approval round trips are in flight; call the tool again later"},"id":9,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="approval round trip refused: too many approval round trips in flight" MCPTag="MCP07" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"round trip declined": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"human approval was not confirmed in the client"},"id":9,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="approval round trip not accepted by the client user (decline)" MCPTag="MCP02" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"round trip not spent": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"approval gate error"},"id":9,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Reason="approval gate did not spend the approval for the round trip (fail-closed)" ApprovalRef="appr-3" MCPTag="MCP07" TokenBinding="bearer" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 1 consumer 1
`,
	"MRTR answer needs approval": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"mcp_mrtr_input_response requires human approval"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="mcp_mrtr_input_response" Reason="mcp_mrtr_input_response denied by HITL gate ()" MCPTag="MCP10" TokenBinding="bearer"
upstream false gate 1 consumer 0
`,
	"MRTR answer denied by content": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"mcp_mrtr_input_response denied by content mediator"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="mcp_mrtr_input_response" Reason="mcp_mrtr_input_response denied by content mediator: harvest" MCPTag="MCP10" TokenBinding="bearer"
upstream false gate 0 consumer 0
`,
	"L7 tool not in toolset": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by server policy"},"id":null,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="absent" Reason="deny-by-default at L7 (Mcp-Name not in server toolset)" MCPTag="MCP01" TokenBinding="bearer" Decision="block" RuleID="default-deny" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"L7 insufficient scope": `status 403
Cache-Control: no-store
Content-Type: application/json
Www-Authenticate: Bearer error="insufficient_scope", scope="tools:read", resource_metadata="https://mcp.olivares.example/.well-known/oauth-protected-resource"
body {"error":{"code":-31001,"message":"insufficient scope for tool (step up required)"},"id":null,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="insufficient scope at L7" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"L7 caller role": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted for caller role"},"id":null,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="admin_only" RequiredScope="tools:read" Reason="caller role not permitted at L7" MCPTag="MCP02" TokenBinding="bearer" Decision="block" RuleID="tool:admin_only" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"grant": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 421f396955cdab1c0ed653ed9f44e8f96c28fe5f164a7e163277895c23441108
upstream true gate 0 consumer 0
`,
	"grant app-only": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="view_only" RequiredScope="tools:read" Allowed="true" Reason="app-only tools/call authorized (UI-originated via rendered MCP App, SEP-1865)" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="allow" RuleID="tool:view_only" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 796eebbafa2f6b3e3d4b7c4202de0e91a5bd75d9446d0805967496359f7571d3
upstream true gate 0 consumer 0
`,
	"grant attested pin and COAZ allow": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim f2832cd1f1310f858cffb07835e03e83127d41f6f4e2e39a51730dd9873dc704
upstream true gate 0 consumer 0
`,
	"grant verified pin": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 18558f9e14064a7200ede676280cd9a522fb724eb8a46fbcacee2450967305c4
upstream true gate 0 consumer 0
`,
	"grant destructive approved": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" ApprovalRef="appr-7" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 799b58100b4cae59674751415ed846b0b0844640253d5f4362933814e9f8dd46
upstream true gate 1 consumer 0
`,
	"grant round trip": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":9,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="delete_db" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" ApprovalRef="appr-7" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="ask" RuleID="tool:delete_db" Server="https://mcp.olivares.example/gw" ClientID="client-a" GrantMode="round_trip"
claim ca597675ba9fd321c2cfbbe5fe034efd94d0f6c724b7a00246f08f228f1e6abe
upstream true gate 1 consumer 1
`,
	"blank name": `status 200
Cache-Control: no-store
Content-Type: application/json
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"text":"tools/call requires a string 'name'","type":"text"}],"isError":true}}
upstream false gate 0 consumer 0
`,
	"pin attestation fingerprint only": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool pin attestation unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin attestation incomplete (fail-closed)" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"pin attestation version only": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool pin attestation unavailable"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="pin attestation incomplete (fail-closed)" MCPTag="MCP04" TokenBinding="bearer" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"COAZ deny traced": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by authorization policy"},"id":1,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Reason="COAZ deny: no" MCPTag="MCP02" TokenBinding="bearer" TraceParent="00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"L7 tool not in toolset traced": `status 403
Cache-Control: no-store
Content-Type: application/json
body {"error":{"code":-31001,"message":"tool not permitted by server policy"},"id":null,"jsonrpc":"2.0"}
audit Subject="agent:claude" Tool="absent" Reason="deny-by-default at L7 (Mcp-Name not in server toolset)" MCPTag="MCP01" TokenBinding="bearer" TraceParent="00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" Decision="block" RuleID="default-deny" Server="https://mcp.olivares.example/gw" ClientID="client-a"
upstream false gate 0 consumer 0
`,
	"grant first-use attestor": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" MCPTag="MCP07" TokenBinding="bearer" OperationIDKind="request_instance" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 18558f9e14064a7200ede676280cd9a522fb724eb8a46fbcacee2450967305c4
upstream true gate 0 consumer 0
`,
	"grant traced": `status 200
Cache-Control: no-store
Content-Type: application/json
Vary: Authorization
body {"id":1,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"ok"}]}}
audit Subject="agent:claude" Tool="search" RequiredScope="tools:read" Allowed="true" Reason="tools/call authorized" MCPTag="MCP07" TokenBinding="bearer" TraceParent="00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" OperationIDKind="request_instance" Decision="allow" RuleID="tool:search" Server="https://mcp.olivares.example/gw" ClientID="client-a"
claim 421f396955cdab1c0ed653ed9f44e8f96c28fe5f164a7e163277895c23441108
upstream true gate 0 consumer 0
`,
}
