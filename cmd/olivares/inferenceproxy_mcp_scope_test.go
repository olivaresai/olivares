// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_mcp_scope_test.go holds the corrective real-proxy controls of the bounded r2
// correction (c8-mcp-enforcement-r2/ROOT-ADJUDICATION.md): the legacy approval of a request
// that also declares MCP (R-MCP-1), a second declared destination (Spec MAJOR-1), a changed
// tenant (Spec MINOR-2) and the validated legacy deny status (Standards E-2). They drive the
// real decider through ServeHTTP; the gates are fakes, the engine is real where named.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// A second declared destination, and the independent known answers for its approval identity
// and for a second tenant (evidence kat_r2.py).
const (
	mcpSecondName    = "acme-CANARY-NAME-2"
	mcpSecondURL     = "https://second.example.com/" + mcpCanaryPath + "-2?q=" + mcpCanaryQuery
	mcpSecondOrigin  = "https://second.example.com:443"
	mcpSecondSubject = "mcp-origin:0094820f40217c0d32dcb8a199e1ae3f0e30947f5facf421794607bc766698a9"
	mcpPlanU1Second  = "e9fbe74ad9f253ffecd3dafee122928d28304d9e9c6a2774291c821482952b24"
	mcpPlanGlobexU1  = "e6f4a85feee80b303c19820b4deb9b1c0245be1bd518b90bfba4436b68ca34c1"
)

func webSearchParamTool() map[string]any {
	return map[string]any{"type": "web_search_20260209", "name": "web_search"}
}

// twoServerParams declares two servers with a web-search tool between their toolsets.
func twoServerParams() map[string]any {
	p := mcpParams(false)
	p["tools"] = []any{mcpToolsetMap(mcpCanaryName), webSearchParamTool(), mcpToolsetMap(mcpSecondName)}
	p["mcp_servers"] = []any{mcpServerMap(mcpCanaryName, mcpTestURL), mcpServerMap(mcpSecondName, mcpSecondURL)}
	return p
}

// TestProxyMCPEgressApprovalPrivacyMixedLegacyNotification is Root's R-MCP-1 control against
// the REAL engine: a request that declares MCP and is denied by a legacy family (no MCP
// code) keeps its deny, sends nothing upstream, consumes no break-glass, and still opens the
// legitimate legacy notification with the adapter's identifiers and plan binding — but the
// adapter's free-form Reason is never persisted, published, logged, audited or returned, and
// the adapter's own intent object is not mutated. Expected at bf54a94bc0: exit 1 by the
// persisted-reason canary assertion.
func TestProxyMCPEgressApprovalPrivacyMixedLegacyNotification(t *testing.T) {
	const adapterCanary = "ADAPTER-LEGACY-REASON-CANARY"
	canaries := append(mcpWireCanaries(), adapterCanary)
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	grant := h.activateBreakGlassE2E(t, "", "r2 control: a mixed legacy notification is not an authorization")

	d, inf, up := mcpProxyDecider(true)
	d.authr = fakeProxyAuthr{p: auth.ScopedPrincipal(model.ID("u1"), "user one", tid, "editor")}
	d.approvals = br
	bus := &fakeObservationBus{}
	d.bus = bus
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapterReason := "grant web search " + adapterCanary + " " + strings.Join(mcpWireCanaries(), " ")
	intent := &claudeapi.ServerToolEgressApprovalIntent{
		Action: "inference.servertool.egress", Family: "web_search", ToolType: "web_search_20260209",
		Subject: "web_search", PlanHash: "plan-r2-mixed-legacy", Reason: adapterReason,
	}
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
		Forward: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: adapterCanary, ApprovalIntent: intent,
	}}
	aud := &recordingAuditor{}
	body := mcpParams(false)
	body["tools"] = []any{webSearchParamTool(), mcpToolsetMap(mcpCanaryName)}

	rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, aud, nil), "/v1/messages", body)
	requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "server-tool egress denied by policy")
	requireNoCanary(t, "HTTP refusal", rec.Body.Bytes(), canaries)
	up.requireNone(t)
	requireBreakGlassUses(t, h, grant, 0)

	list := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?status=pending&action=inference.servertool.egress&limit=200")
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("pending legacy notifications = %d, want 1 (the notification is still owed)", len(items))
	}
	item, _ := items[0].(map[string]any)
	if got, _ := item["subject_ref"].(string); got != "web_search"+planBindingMarker+"plan-r2-mixed-legacy" {
		t.Errorf("legacy identifiers and plan binding must be kept: subject_ref = %q", got)
	}
	reason, _ := item["reason"].(string)
	requireNoCanary(t, "persisted approval reason", []byte(reason), canaries)
	if !strings.Contains(reason, "also declares MCP") {
		t.Errorf("persisted reason = %q, want the Community fixed reason for a mixed request", reason)
	}
	if intent.Reason != adapterReason {
		t.Error("the adapter's own ApprovalIntent was mutated")
	}
	for _, e := range bus.events {
		if f, ok := event.FindingOf(e); ok {
			blob, _ := json.Marshal(f)
			requireNoCanary(t, "published finding", blob, canaries)
		}
	}
	requireNoCanary(t, "decider log", logs.Bytes(), canaries)
	requireAuditClean(t, aud, canaries)
}

// TestProxyMCPEgressSecondDestinationGoverned proves the SECOND declared server is governed
// on its own: with only the first origin granted the request is refused before count_tokens
// and the notification names the second origin; with both granted both declarations are
// forwarded in their positions. Mutants: first origin reused, first destination only,
// positional tool pairing, first approval target reused.
func TestProxyMCPEgressSecondDestinationGoverned(t *testing.T) {
	t.Run("second origin not granted", func(t *testing.T) {
		gov := &fakeGovernance{}
		d, inf, up := mcpProxyDecider(true)
		d.approvals = fakeBridge(gov, proxyTestTenant, 3600, notifyNow)
		gate := grantMCPTestOrigin()
		d.egress = gate
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", twoServerParams())
		requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "mcp_origin_not_granted")
		up.requireNone(t)
		ins := gate.inputs()
		if len(ins) != 1 || ins[0].MCP == nil {
			t.Fatalf("gate inputs = %d with MCP=%v, want one MCP input", len(ins), len(ins) == 1 && ins[0].MCP != nil)
		}
		want := []claudeapi.MCPDestination{
			{ServerIndex: 0, ToolIndex: 0, Origin: mcpTestOrigin},
			{ServerIndex: 1, ToolIndex: 2, Origin: mcpSecondOrigin},
		}
		if !reflect.DeepEqual(ins[0].MCP.Destinations, want) {
			t.Fatalf("destinations = %#v, want %#v", ins[0].MCP.Destinations, want)
		}
		if creates, consumes := gov.counts(); creates != 1 || consumes != 0 {
			t.Fatalf("creates=%d consumes=%d, want one notification and no break-glass", creates, consumes)
		}
		a := gov.approval(0)
		if a.SubjectRef != mcpSecondSubject+planBindingMarker+mcpPlanU1Second {
			t.Errorf("notification subject_ref = %q, want the second origin's subject and plan", a.SubjectRef)
		}
		if !strings.Contains(a.Reason, mcpSecondOrigin) || strings.Contains(a.Reason, mcpTestOrigin) {
			t.Errorf("notification reason = %q, want the second origin only", a.Reason)
		}
	})
	t.Run("both origins granted", func(t *testing.T) {
		d, inf, up := mcpProxyDecider(true)
		d.egress = &originGate{granted: map[string]bool{mcpTestOrigin: true, mcpSecondOrigin: true}}
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", twoServerParams())
		if rec.Code != http.StatusOK {
			t.Fatalf("both origins granted but refused: %d %s", rec.Code, rec.Body.String())
		}
		calls := up.recorded()
		if len(calls) != 2 {
			t.Fatalf("upstream calls = %d, want count_tokens and messages", len(calls))
		}
		wantTools := []any{mcpToolsetMap(mcpCanaryName), webSearchParamTool(), mcpToolsetMap(mcpSecondName)}
		wantServers := []any{mcpServerMap(mcpCanaryName, mcpTestURL), mcpServerMap(mcpSecondName, mcpSecondURL)}
		for _, c := range calls {
			requireMCPBetaOnce(t, c)
			var sent struct {
				Tools      []any `json:"tools"`
				MCPServers []any `json:"mcp_servers"`
			}
			if err := json.Unmarshal(c.body, &sent); err != nil {
				t.Fatalf("%s body: %v", c.path, err)
			}
			if !reflect.DeepEqual(sent.Tools, fixtureJSON(t, wantTools)) || !reflect.DeepEqual(sent.MCPServers, fixtureJSON(t, wantServers)) {
				t.Errorf("%s forwarded tools=%#v servers=%#v, want the declared positions", c.path, sent.Tools, sent.MCPServers)
			}
		}
	})
}

// fixtureJSON is the test's own expectation of what a fixture serializes to.
func fixtureJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fixture marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	return out
}

// TestProxyMCPEgressApprovalTenantIsolation proves a changed tenant is a different
// notification identity, opened with that tenant's own service credential and plan, and that
// each tenant still deduplicates its own pending notification (Root m3, Spec MINOR-2).
func TestProxyMCPEgressApprovalTenantIsolation(t *testing.T) {
	const globex = model.TenantID("globex")
	gov := &fakeGovernance{}
	bridge := fakeBridgeFor(gov, 3600, notifyNow, proxyTestTenant, globex)
	gate := &originGate{granted: map[string]bool{}}
	run := func(p auth.Principal) {
		t.Helper()
		d, inf, up := mcpProxyDecider(true)
		d.authr = fakeProxyAuthr{p: p}
		d.approvals, d.egress = bridge, gate
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
		requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "mcp_origin_not_granted")
		up.requireNone(t)
	}
	run(proxyTestPrincipal())
	run(auth.ScopedPrincipal(model.ID("u1"), "user one", globex, "editor"))
	run(proxyTestPrincipal())
	if creates, consumes := gov.counts(); creates != 2 || consumes != 0 {
		t.Fatalf("creates=%d consumes=%d, want one notification per tenant", creates, consumes)
	}
	acme, other := gov.approval(0), gov.approval(1)
	if acme.Tenant != proxyTestTenant.String() || acme.Token != "svc-"+proxyTestTenant.String() ||
		acme.SubjectRef != mcpApprovalSubject+planBindingMarker+mcpPlanU1 {
		t.Errorf("first tenant notification = %s/%s %s", acme.Tenant, acme.Token, acme.SubjectRef)
	}
	if other.Tenant != globex.String() || other.Token != "svc-"+globex.String() ||
		other.SubjectRef != mcpApprovalSubject+planBindingMarker+mcpPlanGlobexU1 {
		t.Errorf("second tenant notification = %s/%s %s", other.Tenant, other.Token, other.SubjectRef)
	}
}

// TestProxyMCPEgressLegacyDenyStatusValidated proves ONE validated mapping for a legacy-family
// deny, with and without MCP (Standards E-2): a non-error status becomes 403, an error type
// outside the closed vocabulary takes the status default, and valid 402/429 classes stand.
// Without MCP the adapter reason is still relayed; with MCP it is the fixed reason. Expected
// at bf54a94bc0: exit 1 on the rows without MCP that relay 200, 302 or an unknown type.
func TestProxyMCPEgressLegacyDenyStatusValidated(t *testing.T) {
	rows := []struct {
		name       string
		status     int
		errType    string
		wantStatus int
		wantType   string
	}{
		{name: "success status", status: http.StatusOK, errType: "permission_error", wantStatus: http.StatusForbidden, wantType: "permission_error"},
		{name: "redirect status", status: http.StatusFound, wantStatus: http.StatusForbidden, wantType: "permission_error"},
		{name: "unknown error type", status: http.StatusForbidden, errType: "weird_error", wantStatus: http.StatusForbidden, wantType: "permission_error"},
		{name: "zero status", wantStatus: http.StatusForbidden, wantType: "permission_error"},
		{name: "payment required", status: http.StatusPaymentRequired, errType: "billing_error", wantStatus: http.StatusPaymentRequired, wantType: "billing_error"},
		{name: "rate limited", status: http.StatusTooManyRequests, errType: "rate_limit_error", wantStatus: http.StatusTooManyRequests, wantType: "rate_limit_error"},
	}
	for _, r := range rows {
		for _, declareMCP := range []bool{false, true} {
			name := r.name + " without MCP"
			body := plainParams(false)
			body["tools"] = []any{webSearchParamTool()}
			message := "no egress grant"
			if declareMCP {
				name = r.name + " with MCP"
				body = mcpParams(false)
				body["tools"] = []any{webSearchParamTool(), mcpToolsetMap(mcpCanaryName)}
				message = "server-tool egress denied by policy"
			}
			t.Run(name, func(t *testing.T) {
				d, inf, up := mcpProxyDecider(true)
				d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
					Forward: false, Status: r.status, ErrorType: r.errType, Reason: "no egress grant",
				}}
				rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", body)
				requireProxyRefusal(t, rec, r.wantStatus, r.wantType, message)
				up.requireNone(t)
			})
		}
	}
}
