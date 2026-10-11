// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// verdictOrderRS is a server with one tool every gate can refuse: it needs the
// tools:admin scope and the admin role, is destructive, and carries the
// fs_write conditions.
func verdictOrderRS(t *testing.T) *ResourceServer {
	t.Helper()
	sg := newSigner(t)
	ts, err := NewToolset([]ToolPolicy{{Name: "guarded", RequiredScope: "tools:admin", AllowedRoles: []string{"admin"},
		Destructive: true, Conditions: fsWriteConditions, Annotations: &ToolAnnotations{Title: "Guarded"}}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		Tenant:                     "tenant-a",
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 sg.jwks,
		Toolset:                    ts,
		Upstream:                   &fakeUpstream{},
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Auditor:                    &recordingAuditor{},
		Clock:                      rsClock,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// guardedCall is a call of guarded the decision table admits (an ask).
const guardedCall = `{"name":"guarded","arguments":{"path":"/tmp/x"}}`

// verdictParams is a Tasks-declaring tools/call of tool with arguments args
// that also carries an MRTR answer for the mediator.
func verdictParams(tool, args string) json.RawMessage {
	return json.RawMessage(`{"name":"` + tool + `","arguments":` + args +
		`,"inputResponses":{"q":{"action":"accept","content":{"a":"b"}}}` +
		`,"_meta":{"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}`)
}

// TestDecideToolCallGateOrder starts from a call every gate refuses and opens
// the gates one at a time: each step must be refused by the next gate in the
// documented order, and the last step is granted.
func TestDecideToolCallGateOrder(t *testing.T) {
	rs := verdictOrderRS(t)
	store := rs.durableTasks
	rs.durableTasks = nil
	rs.pinVerifier = &fakePinVerifier{err: errors.New("pin store down")}
	rs.coazEvaluator = &stubCOAZEvaluator{decision: COAZDecision{Reason: "no"}}
	desk := &approvalDesk{}
	rs.gate = desk
	mediator := &fakeElicitationMediator{reason: "harvest"}
	rs.elicitationMediator = mediator
	tok := validatedToken{Subject: refusalSubject, ClientID: refusalClient, Issuer: rsIssuer,
		Scopes: map[string]struct{}{}, Roles: map[string]struct{}{}}
	owner := taskOwnerFromToken(rs.tenant, tok)
	var held []*taskAdmissionTicket
	for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
		ticket, _, ok := rs.taskLedger.reserveAdmission(owner)
		if !ok {
			t.Fatalf("reservation %d refused before saturation", i)
		}
		held = append(held, ticket)
	}
	params := json.RawMessage(`{"name":"guarded","name":"guarded"}`)

	steps := []struct {
		gate string
		open func()
		want string // reason fragment of the refusal; empty for the grant
	}{
		{"strict canonicalization", func() {}, "strict canonicalization"},
		{"name", func() { params = verdictParams("", `{"path":"/etc/x"}`) }, "(input error)"},
		{"tasks precondition", func() { params = verdictParams("absent", `{"path":"/etc/x"}`) }, "without a durable task store"},
		{"resolve", func() { rs.durableTasks = store }, "tool not in server toolset"},
		{"scope", func() { params = verdictParams("guarded", `{"path":"/etc/x"}`) }, "insufficient scope"},
		{"role", func() { tok.Scopes["tools:admin"] = struct{}{} }, "caller role not permitted for tool"},
		{"decision table", func() { tok.Roles["admin"] = struct{}{} }, "(row tool:guarded/condition:no-etc)"},
		{"task reservation", func() { params = verdictParams("guarded", `{"path":"/tmp/x"}`) }, "retained task inventory saturated"},
		{"pin", func() {
			for _, ticket := range held {
				ticket.release()
			}
		}, "pin verification error"},
		{"COAZ", func() { rs.pinVerifier = &fakePinVerifier{allowed: true} }, "COAZ deny"},
		{"approval", func() { rs.coazEvaluator = &stubCOAZEvaluator{decision: COAZDecision{Allow: true}} }, "destructive tool not approved (pending)"},
		{"MRTR answers", desk.approve, "denied by content mediator"},
		{"grant", func() { mediator.allow, mediator.reason = true, "" }, ""},
	}
	for _, st := range steps {
		st.open()
		grant, f := rs.decideToolCall(context.Background(), rsRequest{Params: params}, tok, "")
		got := ""
		switch {
		case f == nil:
		case f.inputError:
			got = "(input error)"
		case f.record.Decision == DecisionBlock && strings.Contains(f.record.RuleID, "/condition:"):
			got = "(row " + f.record.RuleID + ")"
		default:
			got = f.record.Reason
		}
		if (st.want == "") != (f == nil) || !strings.Contains(got, st.want) {
			t.Fatalf("gate %s: refusal %q, want %q", st.gate, got, st.want)
		}
		if f == nil {
			if grant.approval.ref != "appr-7" || grant.pin.State != "verified" || grant.coaz.State != "allow" ||
				grant.verdict.Decision != DecisionAsk || grant.canon.Name != "guarded" || grant.ticket == nil {
				t.Errorf("grant = %+v, want the approved ask with its pin and COAZ postures and a reservation", grant)
			}
			grant.ticket.release()
		}
	}
}

// TestDecideToolCallRefusalsCarryTheRow: every refusal by a gate of the
// decision table records its row, the entry's scope, the server, the OAuth
// client and the trace, as the contract requires of every decision
// (mcp-gateway-management.md, "Tool decisions"). The MRTR mediation deny
// records the elicitation channel instead (TestMediationRefusal), and the
// SEP-1303 input error records nothing.
func TestDecideToolCallRefusalsCarryTheRow(t *testing.T) {
	tok := validatedToken{Subject: refusalSubject, ClientID: refusalClient, Issuer: rsIssuer,
		Scopes: map[string]struct{}{"tools:admin": {}}, Roles: map[string]struct{}{"admin": {}}}
	block := func(rule string) toolVerdict { return toolVerdict{Decision: DecisionBlock, RuleID: rule} }
	ask := toolVerdict{Decision: DecisionAsk, RuleID: "tool:guarded"}
	cases := map[string]struct {
		params string
		setup  func(rs *ResourceServer)
		tok    func(validatedToken) validatedToken
		row    toolVerdict
		scope  string
	}{
		"canonicalization": {params: `{"name":"guarded","name":"guarded"}`, row: block("default-deny")},
		"tasks": {params: string(verdictParams("guarded", `{}`)), setup: func(rs *ResourceServer) { rs.durableTasks = nil },
			row: block("tasks:unavailable")},
		"resolve": {params: `{"name":"absent"}`, row: block("default-deny")},
		"scope": {params: guardedCall, row: block("tool:guarded"), scope: "tools:admin", tok: func(v validatedToken) validatedToken {
			v.Scopes = map[string]struct{}{}
			return v
		}},
		"role": {params: guardedCall, row: block("tool:guarded"), scope: "tools:admin", tok: func(v validatedToken) validatedToken {
			v.Roles = map[string]struct{}{}
			return v
		}},
		"table": {params: `{"name":"guarded","arguments":{"path":"/etc/x"}}`, row: block("tool:guarded/condition:no-etc"), scope: "tools:admin"},
		"reservation": {params: guardedCall, row: ask, scope: "tools:admin", setup: func(rs *ResourceServer) {
			for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
				rs.taskLedger.reserveAdmission(taskOwnerFromToken(rs.tenant, tok))
			}
		}},
		"pin": {params: guardedCall, row: ask, scope: "tools:admin",
			setup: func(rs *ResourceServer) { rs.pinVerifier = &fakePinVerifier{reason: "changed"} }},
		"COAZ": {params: guardedCall, row: ask, scope: "tools:admin",
			setup: func(rs *ResourceServer) { rs.coazEvaluator = &stubCOAZEvaluator{err: errors.New("pdp down")} }},
		"approval": {params: guardedCall, row: ask, scope: "tools:admin", setup: func(rs *ResourceServer) { rs.gate = &approvalDesk{} }},
		"round trip": {params: `{"name":"guarded","arguments":{"path":"/tmp/x"},"requestState":"` + askStatePrefix + `forged"}`,
			row: ask, scope: "tools:admin", setup: func(rs *ResourceServer) { rs.gate = &approvalDesk{} }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs := verdictOrderRS(t)
			if tc.setup != nil {
				tc.setup(rs)
			}
			caller := tok
			if tc.tok != nil {
				caller = tc.tok(tok)
			}
			_, f := rs.decideToolCall(context.Background(), rsRequest{Params: json.RawMessage(tc.params)}, caller, verdictTrace)
			if f == nil || f.inputError {
				t.Fatalf("refusal = %+v, want a recorded refusal", f)
			}
			d := f.record
			if d.Allowed || d.Decision != tc.row.Decision || d.RuleID != tc.row.RuleID || d.RequiredScope != tc.scope ||
				d.Server != rsResource || d.ClientID != refusalClient || d.Tenant != rs.tenant || d.TraceParent != verdictTrace || d.MCPTag == "" {
				t.Errorf("record = %+v, want a refusal by row %+v, scope %q, with server, client, tenant and trace", d, tc.row, tc.scope)
			}
		})
	}
}

// panickingPin is a pin verifier that panics, standing in for any gate that does.
type panickingPin struct{}

func (panickingPin) Verify(context.Context, string, string, string) (bool, string, error) {
	panic("pin store bug")
}
func (panickingPin) RecordPin(context.Context, string, string, string) error { return nil }

// TestDecideToolCallReservation: a refusal by any gate after the retained-task
// reservation, a panic in one included, gives the slot back before
// decideToolCall returns; a grant holds the slot until the dispatch releases it.
func TestDecideToolCallReservation(t *testing.T) {
	tok := validatedToken{Subject: refusalSubject, ClientID: refusalClient, Issuer: rsIssuer,
		Scopes: map[string]struct{}{"tools:admin": {}}, Roles: map[string]struct{}{"admin": {}}}
	refusals := map[string]struct {
		params string
		setup  func(rs *ResourceServer)
		panics bool
	}{
		"pin":       {params: guardedCall, setup: func(rs *ResourceServer) { rs.pinVerifier = &fakePinVerifier{err: errors.New("pin store down")} }},
		"pin panic": {params: guardedCall, setup: func(rs *ResourceServer) { rs.pinVerifier = panickingPin{} }, panics: true},
		"COAZ": {params: guardedCall,
			setup: func(rs *ResourceServer) { rs.coazEvaluator = &stubCOAZEvaluator{decision: COAZDecision{Reason: "no"}} }},
		"approval":   {params: guardedCall, setup: func(rs *ResourceServer) { rs.gate = &approvalDesk{} }},
		"round trip": {params: `{"name":"guarded","arguments":{"path":"/tmp/x"},"requestState":"` + askStatePrefix + `forged"}`},
		"MRTR answers": {params: string(verdictParams("guarded", `{"path":"/tmp/x"}`)),
			setup: func(rs *ResourceServer) { rs.elicitationMediator = &fakeElicitationMediator{reason: "harvest"} }},
	}
	for name, tc := range refusals {
		t.Run(name, func(t *testing.T) {
			rs := verdictOrderRS(t)
			rs.gate = approvedGate{}
			if tc.setup != nil {
				tc.setup(rs)
			}
			var f *refusal
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_, f = rs.decideToolCall(context.Background(), rsRequest{Params: json.RawMessage(tc.params)}, tok, "")
			}()
			if tc.panics != (recovered != nil) || (!tc.panics && f == nil) {
				t.Fatalf("refusal %v, panic %v; want a refusal or the panic", f, recovered)
			}
			if n := rs.taskLedger.admitted[taskOwnerFromToken(rs.tenant, tok)]; n != 0 {
				t.Fatalf("refused call holds %d reservations, want 0", n)
			}
		})
	}

	t.Run("grant", func(t *testing.T) {
		rs := verdictOrderRS(t)
		rs.gate = approvedGate{}
		owner := taskOwnerFromToken(rs.tenant, tok)
		grant, f := rs.decideToolCall(context.Background(), rsRequest{Params: json.RawMessage(guardedCall)}, tok, "")
		if f != nil {
			t.Fatalf("refused: %+v", f.record)
		}
		if n := rs.taskLedger.admitted[owner]; n != 1 {
			t.Fatalf("grant holds %d reservations, want 1", n)
		}
		grant.ticket.release()
		if n := rs.taskLedger.admitted[owner]; n != 0 {
			t.Fatalf("released grant holds %d reservations, want 0", n)
		}
	})
}

// capturingPin records what the pin gate asks and allows the call.
type capturingPin struct{ tenant, tool, fingerprint string }

func (p *capturingPin) Verify(_ context.Context, tenant, tool, fingerprint string) (bool, string, error) {
	p.tenant, p.tool, p.fingerprint = tenant, tool, fingerprint
	return true, "", nil
}
func (p *capturingPin) RecordPin(context.Context, string, string, string) error { return nil }

// capturingAttestor is capturingPin with the atomic attestation, answering a
// first use (allowed, nothing attested).
type capturingAttestor struct{ capturingPin }

func (p *capturingAttestor) VerifyAndAttest(_ context.Context, tenant, tool, fingerprint string) (ToolPinVerifyAttestation, error) {
	p.tenant, p.tool, p.fingerprint = tenant, tool, fingerprint
	return ToolPinVerifyAttestation{Allowed: true}, nil
}

// TestDecideToolCallPinAndCOAZInputs: both pin verifiers are asked about this
// tenant's tool with the fingerprint of the canonical forwarded params, and the
// PDP receives the caller, the tool, the server, the scopes, the entry's
// annotations and the tenant.
func TestDecideToolCallPinAndCOAZInputs(t *testing.T) {
	tok := validatedToken{Subject: refusalSubject, ClientID: refusalClient, Issuer: rsIssuer,
		Scopes: map[string]struct{}{"tools:admin": {}}, Roles: map[string]struct{}{"admin": {}}}
	canon, err := canonicalizeToolCallParams(json.RawMessage(guardedCall))
	if err != nil {
		t.Fatal(err)
	}
	plain, attesting := &capturingPin{}, &capturingAttestor{}
	for name, tc := range map[string]struct {
		verifier ToolPinVerifier
		asked    *capturingPin
	}{"verify": {plain, plain}, "attest": {attesting, &attesting.capturingPin}} {
		t.Run(name, func(t *testing.T) {
			rs := verdictOrderRS(t)
			rs.gate = approvedGate{}
			rs.pinVerifier = tc.verifier
			pdp := &stubCOAZEvaluator{decision: COAZDecision{Allow: true}}
			rs.coazEvaluator = pdp
			grant, f := rs.decideToolCall(context.Background(), rsRequest{Params: json.RawMessage(guardedCall)}, tok, "")
			if f != nil {
				t.Fatalf("refused: %+v", f.record)
			}
			defer grant.ticket.release()
			want := capturingPin{tenant: rs.tenant, tool: "guarded", fingerprint: toolCallFingerprint("guarded", canon.Forward)}
			if *tc.asked != want || grant.pin.State != "verified" {
				t.Errorf("pin asked %+v (posture %q), want %+v (verified)", *tc.asked, grant.pin.State, want)
			}
			wantReq := COAZRequest{Subject: refusalSubject, Issuer: rsIssuer, Tool: "guarded", ServerURI: rsResource,
				Scopes: tok.Scopes, Annotations: grant.policy.Annotations, Tenant: rs.tenant}
			if !reflect.DeepEqual(pdp.lastReq, wantReq) || grant.policy.Annotations == nil {
				t.Errorf("PDP asked %+v, want %+v", pdp.lastReq, wantReq)
			}
		})
	}
}

// TestMediationRefusal: a HITL deny asks for a human approval and any other
// deny is a content refusal; both record the elicitation channel (MCP10)
// without the decision fields, as before the verdict module.
func TestMediationRefusal(t *testing.T) {
	rs := verdictOrderRS(t)
	tok := validatedToken{Subject: refusalSubject, ClientID: refusalClient, Binding: "bearer"}
	content := struct{ reason, message string }{"ch denied by content mediator: r", "ch denied by content mediator"}
	for v, want := range map[mediationVerdict]struct{ reason, message string }{
		mediationDenyHITL:    {"ch denied by HITL gate (r)", "ch requires human approval"},
		mediationDenyContent: content,
		mediationVerdict(99): content,
	} {
		f := rs.mediationRefusal(tok, v, "r", "ch", verdictTrace)
		d := f.record
		if f.status != http.StatusForbidden || f.code != rpcAccessDenied || f.message != want.message ||
			d.Allowed || d.Tool != "ch" || d.MCPTag != "MCP10" || d.Reason != want.reason || d.TraceParent != verdictTrace ||
			d.Tenant != rs.tenant || d.Subject != refusalSubject || d.TokenBinding != "bearer" || d.Decision != "" || d.ClientID != "" {
			t.Errorf("verdict %d: refusal %+v, want %+v", v, f, want)
		}
	}
}
