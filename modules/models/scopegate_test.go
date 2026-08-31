// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// fakeScopeGate denies the models named in deny (or every model when errOnAll is set,
// to exercise the deny-closed posture).
type fakeScopeGate struct {
	deny     map[string]bool
	errOnAll bool
}

func (g fakeScopeGate) Allowed(_ context.Context, _ model.TenantID, q ScopeQuery) (ScopeVerdict, error) {
	if g.errOnAll {
		return ScopeVerdict{}, errors.New("scope state unreadable")
	}
	if g.deny[q.ModelRef] {
		return ScopeVerdict{Allowed: false, Reason: "out of scope"}, nil
	}
	return ScopeVerdict{Allowed: true}, nil
}

func chainOf(refs ...string) decisionDTO {
	d := decisionDTO{Resolved: true, Policy: "cost", Fallbacks: []targetDTO{}, Chain: []targetDTO{}}
	for _, r := range refs {
		d.Chain = append(d.Chain, targetDTO{ProviderRef: "anthropic", ModelRef: r})
	}
	if len(d.Chain) > 0 {
		p := d.Chain[0]
		d.Primary = &p
		d.Fallbacks = d.Chain[1:]
	}
	return d
}

func newModuleWithScope(g ScopeGate) *Module {
	m := New()
	m.scopeGate = g
	return m
}

func mc() api.ModuleContext { return api.ModuleContext{Tenant: model.TenantID("t1")} }

// TestScopeGateAllowsAll: with every model in scope the chain is untouched.
func TestScopeGateAllowsAll(t *testing.T) {
	m := newModuleWithScope(fakeScopeGate{})
	dec := chainOf("a", "b", "c")
	r := httptest.NewRequest("POST", "/x", nil)
	if status, denied := m.scopeDeniesRoute(r, mc(), &dec, "sess-1"); denied || status != 0 {
		t.Fatalf("all in scope: want (0,false), got (%d,%v)", status, denied)
	}
	if len(dec.Chain) != 3 || dec.Primary == nil || dec.Primary.ModelRef != "a" {
		t.Errorf("chain must be unchanged, got %+v", dec)
	}
}

// TestScopeGateDropsOutOfScope: an out-of-scope primary is dropped and the next in-scope
// model is promoted (so it is never tried as a fallback either).
func TestScopeGateDropsOutOfScope(t *testing.T) {
	m := newModuleWithScope(fakeScopeGate{deny: map[string]bool{"a": true}})
	dec := chainOf("a", "b", "c")
	r := httptest.NewRequest("POST", "/x", nil)
	if status, denied := m.scopeDeniesRoute(r, mc(), &dec, "sess-1"); denied || status != 0 {
		t.Fatalf("partial filter: want (0,false), got (%d,%v)", status, denied)
	}
	if dec.Primary == nil || dec.Primary.ModelRef != "b" || len(dec.Chain) != 2 {
		t.Errorf("out-of-scope 'a' must be dropped and 'b' promoted, got %+v", dec)
	}
	for _, tgt := range dec.Chain {
		if tgt.ModelRef == "a" {
			t.Errorf("dropped model 'a' must not survive as a fallback")
		}
	}
}

// TestScopeGateDeniesWhenAllOutOfScope: every candidate out of scope ⇒ 403, no target.
func TestScopeGateDeniesWhenAllOutOfScope(t *testing.T) {
	m := newModuleWithScope(fakeScopeGate{deny: map[string]bool{"a": true, "b": true}})
	dec := chainOf("a", "b")
	r := httptest.NewRequest("POST", "/x", nil)
	status, denied := m.scopeDeniesRoute(r, mc(), &dec, "sess-1")
	if !denied || status != 403 {
		t.Fatalf("all out of scope: want (403,true), got (%d,%v)", status, denied)
	}
	if dec.Resolved || dec.Primary != nil || len(dec.Chain) != 0 {
		t.Errorf("denied decision must carry no usable target, got %+v", dec)
	}
}

// TestScopeGateErrorIsDenyClosed: a gate error drops the candidate (an unreadable scope
// state never authorizes a model); all-error ⇒ 403.
func TestScopeGateErrorIsDenyClosed(t *testing.T) {
	m := newModuleWithScope(fakeScopeGate{errOnAll: true})
	dec := chainOf("a", "b")
	r := httptest.NewRequest("POST", "/x", nil)
	status, denied := m.scopeDeniesRoute(r, mc(), &dec, "sess-1")
	if !denied || status != 403 {
		t.Fatalf("gate error must be deny-closed (403,true), got (%d,%v)", status, denied)
	}
}
