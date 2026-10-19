// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// dataflow_test.go: the ToolDataFlow seam of the session tool server. The flow is
// a stand-in that swaps one literal for one token; the tokenizer itself is not in
// this tree (Business). What is proven here is the gate's own contract: where the
// flow runs relative to approval, evidence and relay, and that a nil flow changes
// nothing.

const (
	flowRaw   = "jane@example.com"
	flowToken = "[EMAIL_1]"
)

// swapFlow restores flowToken to flowRaw in arguments and tokenizes flowRaw to
// flowToken in results. refuse, when set, is returned by RestoreArguments.
type swapFlow struct {
	mu          sync.Mutex
	refuse      error
	tokenizeErr error
	calls       []ToolFlowCall
}

func (f *swapFlow) RestoreArguments(_ context.Context, call ToolFlowCall, args json.RawMessage) (json.RawMessage, map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.refuse != nil {
		return nil, nil, f.refuse
	}
	if !strings.Contains(string(args), flowToken) {
		return args, nil, nil
	}
	return json.RawMessage(strings.ReplaceAll(string(args), flowToken, flowRaw)), map[string]int{"email": 1}, nil
}

func (f *swapFlow) TokenizeResult(_ context.Context, call ToolFlowCall, result json.RawMessage) (json.RawMessage, map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.tokenizeErr != nil {
		return nil, nil, f.tokenizeErr
	}
	if !strings.Contains(string(result), flowRaw) {
		return result, nil, nil
	}
	return json.RawMessage(strings.ReplaceAll(string(result), flowRaw, flowToken)), map[string]int{"email": 1}, nil
}

// loggingJournal is the fake evidence journal plus what the gate wrote to it:
// every decision reason and every settled result digest.
type loggingJournal struct {
	*fakeEvidenceJournal
	mu      sync.Mutex
	reasons []string
	digests []string
}

func (j *loggingJournal) Record(ctx context.Context, dec ToolDecision, b sdk.EvidenceBinding) GateRecord {
	j.mu.Lock()
	j.reasons = append(j.reasons, dec.Reason)
	j.mu.Unlock()
	return j.fakeEvidenceJournal.Record(ctx, dec, b)
}

func (j *loggingJournal) Settle(ctx context.Context, out GateOutcome) GateSettlement {
	j.mu.Lock()
	j.digests = append(j.digests, out.ResultDigest)
	j.mu.Unlock()
	return j.fakeEvidenceJournal.Settle(ctx, out)
}

type flowRig struct {
	server  *SessionToolServer
	up      *countingUpstream
	journal *loggingJournal
	gate    *countingGate
}

func newFlowRig(t *testing.T, flow ToolDataFlow, destructive bool, result string) *flowRig {
	t.Helper()
	up := &countingUpstream{}
	if result != "" {
		up.result = json.RawMessage(result)
	}
	journal := &loggingJournal{fakeEvidenceJournal: &fakeEvidenceJournal{}}
	gate := &countingGate{status: StatusApproved}
	ts, err := NewToolset([]ToolPolicy{{Name: "send", RequiredScope: "tools:call", Destructive: destructive}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := SessionToolServerConfig{
		Tenant: "tenant", ServerID: "mail", Descriptor: "backend:v1", Toolset: ts,
		Upstream: up, Gate: gate, Auditor: journal,
	}
	if flow != nil { // a nil *swapFlow in the interface would not be a nil flow
		cfg.DataFlow = flow
	}
	s, err := NewSessionToolServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &flowRig{server: s, up: up, journal: journal, gate: gate}
}

func (r *flowRig) callParams(t *testing.T, params string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/session/mcp?server=mail", nil)
	r.server.CallTool(w, req, SessionToolIdentity{Subject: "session", ClientID: "launch", Scopes: []string{"tools:call"}}, json.RawMessage(`1`), json.RawMessage(params))
	return w
}

func (r *flowRig) call(t *testing.T, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": "send", "arguments": args})
	return r.callParams(t, string(params))
}

func (r *flowRig) auditText() string {
	r.journal.mu.Lock()
	defer r.journal.mu.Unlock()
	return strings.Join(r.journal.reasons, "\n")
}

func TestSessionToolFlowTokenizesTheResultBeforeSettlementAndRelay(t *testing.T) {
	flow := &swapFlow{}
	rig := newFlowRig(t, flow, false, `{"content":[{"type":"text","text":"reply to `+flowRaw+`"}]}`)

	w := rig.call(t, map[string]any{"to": "someone"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), flowRaw) || !strings.Contains(w.Body.String(), flowToken) {
		t.Fatalf("the caller must read the token, never the value: %s", w.Body)
	}
	tokenized := json.RawMessage(`{"content":[{"type":"text","text":"reply to ` + flowToken + `"}]}`)
	if len(rig.journal.digests) != 1 || rig.journal.digests[0] != resultDigest(tokenized) {
		t.Fatalf("the settlement must digest the tokenized result, got %v", rig.journal.digests)
	}
	if audit := rig.auditText(); !strings.Contains(audit, "tool result tokenized: email=1") || strings.Contains(audit, flowRaw) {
		t.Fatalf("the audit records the class and count only: %q", audit)
	}
}

func TestSessionToolFlowRestoresArgumentsUpstreamOnly(t *testing.T) {
	rig := newFlowRig(t, &swapFlow{}, false, "")
	baseline := newFlowRig(t, nil, false, "")

	w := rig.call(t, map[string]any{"to": flowToken})
	baseline.call(t, map[string]any{"to": flowToken})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	if !strings.Contains(string(rig.up.lastParams()), flowRaw) || strings.Contains(string(rig.up.lastParams()), flowToken) {
		t.Fatalf("the upstream must receive the restored value: %s", rig.up.lastParams())
	}
	if !strings.Contains(string(baseline.up.lastParams()), flowToken) {
		t.Fatalf("without a flow the token is plain text: %s", baseline.up.lastParams())
	}
	// The evidence binds the tokenized call: the value never reaches the journal key.
	if rig.journal.lastBinding().EffectDigest != baseline.journal.lastBinding().EffectDigest {
		t.Fatalf("the effect digest must bind the tokenized arguments")
	}
	if audit := rig.auditText(); !strings.Contains(audit, "tokens restored: email=1") || strings.Contains(audit, flowRaw) {
		t.Fatalf("the audit records the class and count only: %q", audit)
	}
}

func TestSessionToolFlowRefusalAsksNobodyAndClaimsNothing(t *testing.T) {
	flow := &swapFlow{refuse: &ToolFlowRefusal{Class: "email", Reason: `email tokens may not be sent to tool "send"`}}
	rig := newFlowRig(t, flow, true, "")

	w := rig.call(t, map[string]any{"to": flowToken})

	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "email") {
		t.Fatalf("want 403 naming the class, got %d %s", w.Code, w.Body)
	}
	if rig.up.count() != 0 || rig.gate.calls != 0 || rig.journal.opCount() != 0 {
		t.Fatalf("a refused restore must reach no approval, claim or upstream: upstream=%d asked=%d claims=%d", rig.up.count(), rig.gate.calls, rig.journal.opCount())
	}
	if !strings.Contains(rig.auditText(), "token restore refused: class email") {
		t.Fatalf("the refusal must be audited: %q", rig.auditText())
	}
}

func TestSessionToolFlowErrorFailsClosedWithoutItsText(t *testing.T) {
	flow := &swapFlow{refuse: errors.New("map for " + flowRaw + " exploded")}
	rig := newFlowRig(t, flow, false, "")

	w := rig.call(t, map[string]any{"to": flowToken})

	if w.Code != http.StatusForbidden || rig.up.count() != 0 {
		t.Fatalf("want a closed refusal, got %d upstream=%d", w.Code, rig.up.count())
	}
	if strings.Contains(w.Body.String(), flowRaw) || strings.Contains(rig.auditText(), flowRaw) {
		t.Fatalf("an unclassified error must not be echoed: %s / %q", w.Body, rig.auditText())
	}
}

func TestSessionToolFlowTokenizeFailureWithholdsTheResult(t *testing.T) {
	flow := &swapFlow{tokenizeErr: errors.New("token map full")}
	rig := newFlowRig(t, flow, false, `{"content":[{"type":"text","text":"reply to `+flowRaw+`"}]}`)

	w := rig.call(t, map[string]any{"to": "someone"})

	if strings.Contains(w.Body.String(), flowRaw) {
		t.Fatalf("a result that could not be tokenized must not be relayed: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatalf("want a tool error, got %s", w.Body)
	}
	if rig.up.count() != 1 {
		t.Fatalf("the upstream effect already ran once, got %d", rig.up.count())
	}
	if audit := rig.auditText(); !strings.Contains(audit, "tool result withheld") || strings.Contains(audit, flowRaw) {
		t.Fatalf("the withheld result must be audited without its content: %q", audit)
	}
}

func TestSessionToolFlowNamesTheTargetOfACallWithoutArguments(t *testing.T) {
	flow := &swapFlow{}
	rig := newFlowRig(t, flow, false, "")

	w := rig.callParams(t, `{"name":"send"}`)

	if w.Code != http.StatusOK || strings.Contains(string(rig.up.lastParams()), "arguments") {
		t.Fatalf("a call without arguments must be forwarded as it came: %d %s / %s", w.Code, w.Body, rig.up.lastParams())
	}
	want := ToolFlowCall{Server: "mail", Tool: "send"}
	if len(flow.calls) != 2 || flow.calls[0] != want || flow.calls[1] != want {
		t.Fatalf("restore and tokenize must both be told exactly what is called: %+v", flow.calls)
	}
}

// A flow that cannot run (deny-all) refuses a call without arguments too, before
// its effect: otherwise the tool would run and only its result be withheld.
func TestSessionToolFlowRefusesACallWithoutArgumentsBeforeItsEffect(t *testing.T) {
	rig := newFlowRig(t, &swapFlow{refuse: errors.New("no valid configuration")}, false, "")

	w := rig.callParams(t, `{"name":"send"}`)

	if w.Code != http.StatusForbidden || rig.up.count() != 0 {
		t.Fatalf("want a refusal before the upstream, got %d upstream=%d", w.Code, rig.up.count())
	}
}
