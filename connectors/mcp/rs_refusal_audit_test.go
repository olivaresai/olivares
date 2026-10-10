// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
)

// refusalPinAttestor answers the atomic pin check with a fixed attestation.
type refusalPinAttestor struct {
	va  ToolPinVerifyAttestation
	err error
}

func (v *refusalPinAttestor) Verify(context.Context, string, string, string) (bool, string, error) {
	return true, "", nil
}
func (v *refusalPinAttestor) RecordPin(context.Context, string, string, string) error { return nil }
func (v *refusalPinAttestor) VerifyAndAttest(context.Context, string, string, string) (ToolPinVerifyAttestation, error) {
	return v.va, v.err
}

// refusalCase is one tools/call refusal path and the decision-table row its
// audit record must carry (docs/contracts/mcp-gateway-management.md, "Tool
// decisions": every decision records policy_decision, policy_id, server_name
// and client_id).
type refusalCase struct {
	name     string
	reason   string // fragment of the audited refusal reason
	decision Decision
	rule     string
	tool     string
	params   string // raw tools/call params; empty means the Tasks-declaring toolsCallReq for tool
	scope    string
	result   string // upstream tools/call result
	noTasks  bool
	pin      ToolPinVerifier
	coaz     COAZEvaluator
	mediator ElicitationMediator
	saturate bool
	release  bool // refuse every response-release claim
	rc       bool // MCP 2026-07-28 request: the pre-body Mcp-Name gate decides first
}

const (
	refusalClient   = "client-a"
	refusalSubject  = "agent:claude"
	refusalMRTR     = `{"resultType":"input_required","inputRequests":{"need":{"message":"enter secret"}},"requestState":"s1"}`
	refusalBadMRTR  = `{"resultType":"input_required","inputRequests":["not-a-map"]}`
	refusalBadTask  = `{"resultType":"task"}`
	refusalTableRow = "tool:search"
)

func refusalMint(t *testing.T, sg *signer, scope string) string {
	t.Helper()
	raw, err := jwt.Signed(sg.js).Claims(jwt.Claims{
		Issuer: rsIssuer, Subject: refusalSubject, Audience: jwt.Audience{rsResource},
		IssuedAt: jwt.NewNumericDate(rsClock().Add(-time.Minute)), Expiry: jwt.NewNumericDate(validExp()),
	}).Claims(map[string]any{"scope": scope, "client_id": refusalClient}).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func refusalRS(t *testing.T, jwks []byte, tc refusalCase, aud GateAuditor) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "admin_only", RequiredScope: "tools:read", AllowedRoles: []string{"admin"}},
		{Name: "fs_write", RequiredScope: "tools:read", Conditions: fsWriteConditions},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	up := &fakeUpstream{}
	if tc.result != "" {
		up.result = json.RawMessage(tc.result)
	}
	cfg := ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Upstream:                   up,
		Auditor:                    aud,
		Clock:                      rsClock,
		PinVerifier:                tc.pin,
		COAZEvaluator:              tc.coaz,
		ElicitationMediator:        tc.mediator,
		DisableNextRevisionHeaders: !tc.rc,
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

// TestToolsCallRefusalsAuditDecisionFields drives every reachable tools/call
// refusal through ServeHTTP and checks its audit record carries the
// decision-table row, the server and the OAuth client.
func TestToolsCallRefusalsAuditDecisionFields(t *testing.T) {
	cases := []refusalCase{
		{name: "strict canonicalization", reason: "strict canonicalization", params: `{"name":"search","name":"search"}`,
			decision: DecisionBlock, rule: "default-deny"},
		{name: "tasks without durable store", reason: "without a durable task store", tool: "search", noTasks: true,
			decision: DecisionBlock, rule: "tasks:unavailable"},
		{name: "tool not in toolset", reason: "tool not in server toolset", tool: "absent",
			decision: DecisionBlock, rule: "default-deny"},
		{name: "insufficient scope", reason: "insufficient scope", tool: "search", scope: "tools:other",
			decision: DecisionBlock, rule: "tool:search"},
		{name: "caller role", reason: "caller role not permitted", tool: "admin_only",
			decision: DecisionBlock, rule: "tool:admin_only"},
		{name: "condition blocks", reason: "condition", params: `{"name":"fs_write","arguments":{"path":"/etc/x"}}`,
			decision: DecisionBlock, rule: "tool:fs_write/condition:no-etc"},
		{name: "L7 tool not in toolset", reason: "deny-by-default at L7", tool: "absent", rc: true,
			decision: DecisionBlock, rule: "default-deny"},
		{name: "L7 insufficient scope", reason: "insufficient scope at L7", tool: "search", scope: "tools:other", rc: true,
			decision: DecisionBlock, rule: "tool:search"},
		{name: "L7 caller role", reason: "caller role not permitted at L7", tool: "admin_only", rc: true,
			decision: DecisionBlock, rule: "tool:admin_only"},
		{name: "task inventory saturated", reason: "retained task inventory saturated", tool: "search", saturate: true,
			decision: DecisionAllow, rule: refusalTableRow},
		{name: "pin attestation error", reason: "pin verification error", tool: "search",
			pin: &refusalPinAttestor{err: errors.New("pin store down")}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "pin attestation mismatch", reason: "pin mismatch", tool: "search",
			pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Reason: "changed"}}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "pin attestation incomplete", reason: "pin attestation incomplete", tool: "search",
			pin: &refusalPinAttestor{va: ToolPinVerifyAttestation{Allowed: true, Attested: true}}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "pin verification error", reason: "pin verification error", tool: "search",
			pin: &fakePinVerifier{err: errors.New("pin store down")}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "pin mismatch", reason: "pin mismatch", tool: "search",
			pin: &fakePinVerifier{reason: "changed"}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "COAZ error", reason: "COAZ evaluation error", tool: "search",
			coaz: &stubCOAZEvaluator{err: errors.New("pdp down")}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "COAZ deny", reason: "COAZ deny", tool: "search",
			coaz: &stubCOAZEvaluator{decision: COAZDecision{Reason: "no"}}, decision: DecisionAllow, rule: refusalTableRow},
		{name: "malformed task handle", reason: "failed strict validation", tool: "search", result: refusalBadTask,
			decision: DecisionAllow, rule: refusalTableRow},
		{name: "unreadable mediated result", reason: "cannot be projected", tool: "search", result: refusalBadMRTR,
			decision: DecisionAllow, rule: refusalTableRow},
		{name: "unreadable result release refused", reason: "withheld-release claim refused", tool: "search",
			result: refusalBadMRTR, release: true, decision: DecisionAllow, rule: refusalTableRow},
		{name: "mediated deny release refused", reason: "withheld-release claim refused", tool: "search",
			result: refusalMRTR, mediator: &fakeElicitationMediator{reason: "harvest"}, release: true,
			decision: DecisionAllow, rule: refusalTableRow},
		{name: "response release refused", reason: "response-release claim refused", tool: "search",
			result: refusalMRTR, release: true, decision: DecisionAllow, rule: refusalTableRow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sg := newSigner(t)
			aud := &recordingAuditor{}
			if tc.release {
				aud.recordFaultFn = s504RefuseReleaseClaims
			}
			rs := refusalRS(t, sg.jwks, tc, aud)
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
			case tc.rc:
				req = nextReqRaw(bearer, "tools/call", tc.tool,
					`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tc.tool+`"}}`)
			case tc.params != "":
				req = customToolsCallReq(bearer, tc.params)
			}
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, req)

			var found bool
			for _, d := range aud.decisions {
				if d.Allowed || !strings.Contains(d.Reason, tc.reason) {
					continue
				}
				found = true
				if d.Decision != tc.decision || d.RuleID != tc.rule {
					t.Errorf("%q: row = %s %q, want %s %q", d.Reason, d.Decision, d.RuleID, tc.decision, tc.rule)
				}
				if d.Server != rsResource || d.ClientID != refusalClient {
					t.Errorf("%q: server %q client %q, want %q %q", d.Reason, d.Server, d.ClientID, rsResource, refusalClient)
				}
			}
			if !found {
				t.Fatalf("no refusal audited with reason %q (status %d, body %s)", tc.reason, w.Code, w.Body.String())
			}
		})
	}
}

// TestToolsCallRefusalsUseTheVerdictRecorder covers the refusals no request can
// reach (crypto/rand never fails; a missing MRTR authority profile is an
// internal defect): the tools/call verdict, its writer and the dispatch record
// every decision through verdictRecord (auditVerdict after the dispatch), never
// through auditTraced, which drops the decision fields.
func TestToolsCallRefusalsUseTheVerdictRecorder(t *testing.T) {
	for _, site := range []struct{ file, gate string }{
		{"rs.go", "handleToolsCall"},
		{"rsnext.go", "enforceNextHeadersPreBody"},
		{"verdict.go", "decideToolCall"},
		{"verdict.go", "admitEntry"},
		{"verdict.go", "verifyPin"},
		{"verdict.go", "evaluateCOAZ"},
		{"verdict.go", "deny"},
		{"verdict.go", "refuse"},
		{"ask.go", "authorizeAsk"},
	} {
		file, gate := site.file, site.gate
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != gate {
				continue
			}
			found = true
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "auditTraced" {
					t.Errorf("%s: %s records with auditTraced; build the record with verdictRecord", fset.Position(sel.Pos()), gate)
				}
				return true
			})
		}
		if !found {
			t.Errorf("%s not found in %s", gate, file)
		}
	}
}
