// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// --- unit: chain value + policy -------------------------------------------------

func TestChainEncodeDecodeRoundTrip(t *testing.T) {
	c := DelegationChain{Root: "root-1", Hops: []string{"alpha", "beta"}}
	enc := c.encode()
	if enc == "" {
		t.Fatal("a non-empty chain must encode to a non-empty header value")
	}
	got := decodeChain(enc)
	if !reflect.DeepEqual(got, c) {
		t.Errorf("round trip mismatch: got %+v want %+v", got, c)
	}
	if (DelegationChain{}).encode() != "" {
		t.Error("an empty chain must encode to the empty string (no header)")
	}
}

func TestDecodeChainDefensive(t *testing.T) {
	if d := decodeChain("!!!not base64!!!"); d.Depth() != 0 || d.Root != "" {
		t.Errorf("garbage must decode to an empty chain, got %+v", d)
	}
	if d := decodeChain(strings.Repeat("A", maxChainHeaderLen+1)); d.Depth() != 0 {
		t.Error("an over-long header must decode to an empty chain")
	}
	// A chain with too many hops is rejected wholesale (treated as absent).
	huge := DelegationChain{Hops: make([]string, maxChainHops+1)}
	for i := range huge.Hops {
		huge.Hops[i] = "a"
	}
	if d := decodeChain(huge.encode()); d.Depth() != 0 {
		t.Error("a chain exceeding maxChainHops must decode to empty")
	}
}

// TestChainClaimsWireShape: the propagated header JSON is the RFC 8693 §4.1 claims
// shape — top-level `sub` = the ORIGINAL principal, nested `act` objects with the
// MOST RECENT actor outermost ("the least recent actor is the most deeply nested"),
// and the private `olivares.root` correlation claim.
func TestChainClaimsWireShape(t *testing.T) {
	c := DelegationChain{Root: "task-1", Principal: "user@example.com", Hops: []string{"alpha", "beta"}}
	raw, err := base64.RawURLEncoding.DecodeString(c.encode())
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var claims struct {
		Sub  string `json:"sub"`
		Root string `json:"olivares.root"`
		Act  struct {
			Sub string `json:"sub"`
			Act *struct {
				Sub string `json:"sub"`
				Act *json.RawMessage
			} `json:"act"`
		} `json:"act"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims unmarshal: %v", err)
	}
	if claims.Sub != "user@example.com" || claims.Root != "task-1" {
		t.Errorf("sub/root = %q/%q, want principal + correlation root", claims.Sub, claims.Root)
	}
	// beta is the MOST RECENT hop → outermost act; alpha nested inside.
	if claims.Act.Sub != "beta" || claims.Act.Act == nil || claims.Act.Act.Sub != "alpha" {
		t.Errorf("act nesting wrong: outermost %q inner %+v (want beta then alpha)", claims.Act.Sub, claims.Act.Act)
	}
}

// TestChainClaimsRoundTripWithPrincipal: encode/decode preserves principal + hops.
func TestChainClaimsRoundTripWithPrincipal(t *testing.T) {
	c := DelegationChain{Root: "r1", Principal: "svc:reporting", Hops: []string{"a", "b", "c"}}
	got := decodeChain(c.encode())
	if !reflect.DeepEqual(got, c) {
		t.Errorf("claims round trip mismatch: got %+v want %+v", got, c)
	}
}

// TestDecodeChainLegacyShape: a not-yet-upgraded cooperating plane still sends the
// {root, hops} JSON — it must decode (no flag day across a mixed fleet).
func TestDecodeChainLegacyShape(t *testing.T) {
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`{"root":"task-9","hops":["alpha","beta"]}`))
	got := decodeChain(legacy)
	if got.Root != "task-9" || !reflect.DeepEqual(got.Hops, []string{"alpha", "beta"}) || got.Principal != "" {
		t.Errorf("legacy chain decode = %+v", got)
	}
}

// TestDecodeActChainBounded: an act nesting deeper than maxChainHops is rejected
// wholesale (treated as absent) — a hostile header cannot exhaust the decoder.
func TestDecodeActChainBounded(t *testing.T) {
	inner := `{"sub":"x"}`
	for i := 0; i < maxChainHops+1; i++ {
		inner = `{"sub":"x","act":` + inner + `}`
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"p","act":` + inner + `}`))
	if d := decodeChain(enc); d.Depth() != 0 || d.Principal != "" {
		t.Errorf("an over-deep act nesting must decode to an empty chain, got %+v", d)
	}
}

func TestEnforceChainUnit(t *testing.T) {
	// Success: append the target, lineage grows by one.
	out, err := enforceChain(ChainPolicy{MaxDepth: 4}, DelegationChain{Root: "r", Hops: []string{"a"}}, "b")
	if err != nil {
		t.Fatalf("a within-policy delegation must pass, got %v", err)
	}
	if !reflect.DeepEqual(out.Hops, []string{"a", "b"}) || out.Root != "r" {
		t.Errorf("outbound chain = %+v, want hops [a b] root r", out)
	}
	// Depth ceiling.
	_, err = enforceChain(ChainPolicy{MaxDepth: 2}, DelegationChain{Hops: []string{"a", "b"}}, "c")
	var ce *ChainError
	if !errors.As(err, &ce) || !strings.Contains(ce.Reason, "maximum depth") {
		t.Errorf("a depth breach must be a ChainError(maximum depth), got %v", err)
	}
	// Cycle (checked before depth).
	_, err = enforceChain(ChainPolicy{MaxDepth: 8}, DelegationChain{Hops: []string{"a", "b"}}, "a")
	if !errors.As(err, &ce) || !strings.Contains(ce.Reason, "cycle") {
		t.Errorf("a cycle must be a ChainError(cycle), got %v", err)
	}
}

func TestChainPolicyDefaultDepth(t *testing.T) {
	if (ChainPolicy{}).maxDepth() != defaultMaxDepth {
		t.Errorf("an unset MaxDepth must resolve to the safe default %d", defaultMaxDepth)
	}
	if (ChainPolicy{MaxDepth: -5}).maxDepth() != defaultMaxDepth {
		t.Error("a non-positive MaxDepth must resolve to the safe default (never unbounded)")
	}
}

// --- delegation-path enforcement through the PEP --------------------------------

// TestDelegateChainDepthExceeded: an inbound lineage already at the depth ceiling refuses
// a further delegation (deny-closed), emitting nothing — the runaway-fan-out guard.
func TestDelegateChainDepthExceeded(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	d := NewDelegator(DelegatorConfig{
		Emit:        EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist:   billingAllowlist(),
		Gate:        &fakeGate{status: StatusApproved},
		ChainPolicy: ChainPolicy{MaxDepth: 2},
	})
	spec := okSpec()
	spec.Chain = &DelegationChain{Root: "task-1", Hops: []string{"planner", "router"}} // depth 2 == ceiling
	_, err := d.Delegate(context.Background(), spec)
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("a depth-exceeded delegation must be a ChainError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a chain-depth deny must emit NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestDelegateChainCycle: delegating to an agent already in the lineage is a cycle and is
// refused (deny-closed), emitting nothing.
func TestDelegateChainCycle(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	spec := okSpec()
	spec.Chain = &DelegationChain{Root: "task-1", Hops: []string{"alpha", "billing"}} // billing recurs
	_, err := d.Delegate(context.Background(), spec)
	var ce *ChainError
	if !errors.As(err, &ce) || !strings.Contains(ce.Reason, "cycle") {
		t.Fatalf("a delegation closing a cycle must be a ChainError(cycle), got %v", err)
	}
	if doer.postCount != 0 {
		t.Errorf("a chain-cycle deny must emit NOTHING, got %d POSTs", doer.postCount)
	}
	if aud.last().Allowed {
		t.Error("a chain-cycle deny must be audited as not-allowed")
	}
}

// TestDelegateChainPropagationAndAudit: a within-policy delegation appends this hop to the
// lineage, propagates it on the out-of-band header, and records the inbound depth/root +
// objective in the audit decision (the observability data).
func TestDelegateChainPropagationAndAudit(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	spec := okSpec()
	spec.Objective = "nightly-report"
	spec.Chain = &DelegationChain{Root: "task-7", Hops: []string{"planner"}}
	if _, err := d.Delegate(context.Background(), spec); err != nil {
		t.Fatalf("a within-policy delegation must succeed, got %v", err)
	}
	// The outbound delegation-path header carries the appended lineage.
	hdr := doer.postReq.Header.Get(delegationPathHeader)
	if hdr == "" {
		t.Fatal("the outbound delegation must propagate the chain header")
	}
	out := decodeChain(hdr)
	if out.Root != "task-7" || !reflect.DeepEqual(out.Hops, []string{"planner", "billing"}) {
		t.Errorf("propagated chain = %+v, want root task-7 hops [planner billing]", out)
	}
	// The audit decision records the INBOUND depth/root + the objective.
	last := aud.last()
	if last.ChainDepth != 1 || last.ChainRoot != "task-7" {
		t.Errorf("audit chain = depth %d root %q, want depth 1 root task-7", last.ChainDepth, last.ChainRoot)
	}
	if last.Objective != "nightly-report" {
		t.Errorf("audit objective = %q, want nightly-report", last.Objective)
	}
}

// TestDelegateChainFromContext: when the spec carries no explicit chain, the inbound
// lineage is taken from the context (withChain) — so a composition root that set the
// inbound chain on ctx still gets it enforced + propagated.
func TestDelegateChainFromContext(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	ctx := withChain(context.Background(), DelegationChain{Root: "ctx-task", Hops: []string{"super"}})
	if _, err := d.Delegate(ctx, okSpec()); err != nil { // okSpec has no Chain
		t.Fatalf("delegate: %v", err)
	}
	if aud.last().ChainDepth != 1 || aud.last().ChainRoot != "ctx-task" {
		t.Errorf("inbound chain must be read from context: got depth %d root %q", aud.last().ChainDepth, aud.last().ChainRoot)
	}
	out := decodeChain(doer.postReq.Header.Get(delegationPathHeader))
	if !reflect.DeepEqual(out.Hops, []string{"super", "billing"}) {
		t.Errorf("context chain must be appended + propagated, got %+v", out.Hops)
	}
}

// TestDelegatePrincipalSeededAndPropagated: on a FRESH ROOT delegation the chain's
// principal (the RFC 8693 `sub` position) is seeded from RequestedBy, recorded in
// the audit decision, and propagated in the claims header — every downstream hop
// sees WHO the multi-agent task is ultimately for.
func TestDelegatePrincipalSeededAndPropagated(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	spec := okSpec()
	spec.RequestedBy = "user@example.com"
	if _, err := d.Delegate(context.Background(), spec); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if aud.last().Principal != "user@example.com" {
		t.Errorf("audit principal = %q, want the root RequestedBy", aud.last().Principal)
	}
	out := decodeChain(doer.postReq.Header.Get(delegationPathHeader))
	if out.Principal != "user@example.com" || !reflect.DeepEqual(out.Hops, []string{"billing"}) {
		t.Errorf("propagated chain = %+v, want principal user@example.com hops [billing]", out)
	}
}

// TestDelegateMidChainPrincipalNotFabricated: a lineage that arrived WITHOUT a
// principal keeps it empty — the current hop's requester is NOT the original
// principal, and mislabeling it would misattribute the whole chain.
func TestDelegateMidChainPrincipalNotFabricated(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	spec := okSpec()
	spec.RequestedBy = "mid-hop-operator"
	spec.Chain = &DelegationChain{Root: "task-1", Hops: []string{"planner"}} // no principal propagated
	if _, err := d.Delegate(context.Background(), spec); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if aud.last().Principal != "" {
		t.Errorf("a mid-chain delegation must not fabricate a principal, got %q", aud.last().Principal)
	}
	if out := decodeChain(doer.postReq.Header.Get(delegationPathHeader)); out.Principal != "" {
		t.Errorf("propagated principal must stay empty mid-chain, got %q", out.Principal)
	}
}

// TestDelegateRootDelegationNoChain: a root delegation (no inbound lineage) succeeds at
// depth 0 and propagates a single-hop lineage; audit records depth 0.
func TestDelegateRootDelegationNoChain(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", string(rpcTaskResp("t1", "TASK_STATE_SUBMITTED")))
	aud := &capAuditor{}
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
		Auditor:   aud,
	})
	if _, err := d.Delegate(context.Background(), okSpec()); err != nil {
		t.Fatalf("a root delegation must succeed, got %v", err)
	}
	if aud.last().ChainDepth != 0 {
		t.Errorf("a root delegation must record chain depth 0, got %d", aud.last().ChainDepth)
	}
	// A root delegation still propagates a single-hop lineage (so a downstream plane is
	// bounded from hop 1).
	out := decodeChain(doer.postReq.Header.Get(delegationPathHeader))
	if !reflect.DeepEqual(out.Hops, []string{"billing"}) {
		t.Errorf("root delegation should propagate hops [billing], got %+v", out.Hops)
	}
}
