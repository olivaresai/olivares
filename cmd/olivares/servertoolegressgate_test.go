// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/sdk/event"
)

// servertoolegressgate_test.go proves the AGPL decider-side wiring of the optional
// commercial egress gate (the real gate logic is tested under -tags enterprise in
// enterprise/servertoolegress). It uses a FAKE gate so it runs in the DEFAULT build,
// exactly the build where the seam must be inert-by-nil and correct-when-present.

type fakeEgressGate struct {
	dec   claudeapi.ServerToolEgressDecision
	gotIn claudeapi.ServerToolEgressInput
	calls int
}

func (f *fakeEgressGate) GovernEgress(_ context.Context, in claudeapi.ServerToolEgressInput) claudeapi.ServerToolEgressDecision {
	f.calls++
	f.gotIn = in
	return f.dec
}

type fakeBus struct{ events []event.Event }

func (b *fakeBus) Publish(_ context.Context, e event.Event) error {
	b.events = append(b.events, e)
	return nil
}

func reqWithTools(tools ...any) claudeapi.MessageRequest {
	r := userReq("hi", false)
	r.Tools = tools
	return r
}

// a gate DENY must short-circuit the chain BEFORE the (fail-open) budget gate runs.
func TestProxyEgressDenyShortCircuitsBeforeBudget(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
		Forward: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "no egress grant",
	}}
	dec := d.Authorize(context.Background(), reqWithTools(map[string]any{"type": "web_search_20260209", "name": "web_search"}), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("an egress deny must 403; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if bg.calls != 0 {
		t.Error("egress (security) must run BEFORE budget — budget must not be consulted on a deny")
	}
}

// a gate FORWARD with a rewrite must substitute the governed tools[] into the forwarded
// request, and the request must still proceed (Allow) through budget.
func TestProxyEgressRewriteFlowsToRequest(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	original := map[string]any{"type": "web_search_20260209", "name": "web_search"}
	governed := []any{map[string]any{"type": "web_search_20260209", "name": "web_search", "allowed_domains": []any{"example.com"}, "max_uses": 5}}
	fake := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true, Rewritten: true, GovernedTools: governed}}
	d.egress = fake

	dec := d.Authorize(context.Background(), reqWithTools(original), "bearer")
	if !dec.Allow {
		t.Fatalf("a forward verdict must allow; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if !reflect.DeepEqual(dec.Request.Tools, governed) {
		t.Errorf("forwarded tools[] = %v; want the governed slice", dec.Request.Tools)
	}
	if fake.gotIn.Tenant != string(proxyTestTenant) {
		t.Errorf("gate saw tenant %q; want %q", fake.gotIn.Tenant, proxyTestTenant)
	}
	if len(fake.gotIn.Tools) != 1 || !reflect.DeepEqual(fake.gotIn.Tools[0], original) {
		t.Errorf("gate must receive the ORIGINAL tools; got %v", fake.gotIn.Tools)
	}
	if bg.calls != 1 {
		t.Error("a forward must continue into the budget gate")
	}
}

func TestProxyEgressUsesAuthenticatedAgentBinding(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	a.p = a.p.WithAgentIdentity("agent:nhi:bound")
	d := newTestDecider(a, mg, bg, kg, pol)
	fake := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
	d.egress = fake

	dec := d.Authorize(context.Background(), reqWithTools(map[string]any{"type": "web_search_20260209", "name": "web_search"}), "bearer")
	if !dec.Allow {
		t.Fatalf("bound request denied: status=%d reason=%q", dec.Status, dec.Reason)
	}
	if fake.gotIn.ActorRef != "agent:nhi:bound" {
		t.Errorf("egress actor = %q; want authenticated AgentIdentity", fake.gotIn.ActorRef)
	}
	if fake.gotIn.UnbindableAgent {
		t.Error("a token with an authenticated agent binding must be bindable")
	}
}

func TestProxyEgressMarksRawTokenUnbindable(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	fake := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
	d.egress = fake

	dec := d.Authorize(context.Background(), reqWithTools(map[string]any{"type": "web_search_20260209", "name": "web_search"}), "bearer")
	if !dec.Allow {
		t.Fatalf("fake gate forward denied: status=%d reason=%q", dec.Status, dec.Reason)
	}
	if !fake.gotIn.UnbindableAgent {
		t.Fatal("a raw API token without AgentIdentity must be marked unbindable")
	}
}

// a nil gate (the default AGPL build) must be a no-op: tools[] pass through untouched.
func TestProxyEgressNilGateIsNoOp(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol) // egress left nil
	original := []any{map[string]any{"type": "web_search_20260209", "name": "web_search"}}
	dec := d.Authorize(context.Background(), reqWithTools(original...), "bearer")
	if !dec.Allow {
		t.Fatalf("nil egress gate must not deny; got status=%d", dec.Status)
	}
	if !reflect.DeepEqual(dec.Request.Tools, original) {
		t.Errorf("nil gate must leave tools[] untouched; got %v", dec.Request.Tools)
	}
}

// a gate DENY must publish its finding(s) on the bus.
func TestProxyEgressDenyPublishesFinding(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	fb := &fakeBus{}
	d.bus = fb
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
		Forward: false, Status: http.StatusForbidden,
		Findings: []claudeapi.ServerToolEgressFinding{{Kind: "servertool_egress_blocked", Severity: "high", Title: "blocked", Family: "web_search"}},
	}}
	dec := d.Authorize(context.Background(), reqWithTools(map[string]any{"type": "web_search_20260209", "name": "web_search"}), "bearer")
	if dec.Allow {
		t.Fatal("expected deny")
	}
	if len(fb.events) == 0 {
		t.Error("an egress deny finding must be published on the bus")
	}
}
