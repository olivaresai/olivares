// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// heldByRole is the trustee matrix written out by hand, independent of the table: which
// built-in role holds which right on a tree kind. Supervisor is the owner's alone and
// Access Control starts at admin; everything the table maps to the write verb starts at
// editor.
var heldByRole = map[string]map[string]bool{
	RoleViewer: {"Browse": true, "Read": true},
	RoleEditor: {"Browse": true, "Read": true, "Write": true, "Create": true, "Erase": true, "Modify": true},
	RoleAdmin: {
		"Browse": true, "Read": true, "Write": true, "Create": true, "Erase": true, "Modify": true,
		"Access Control": true,
	},
	RoleOwner: {
		"Supervisor": true, "Browse": true, "Read": true, "Write": true, "Create": true,
		"Erase": true, "Modify": true, "Access Control": true,
	},
}

func TestTrusteeRightsAreTheEightNamedRights(t *testing.T) {
	var names []string
	for _, r := range TrusteeRights() {
		names = append(names, r.Name)
	}
	want := []string{"Supervisor", "Browse", "Read", "Write", "Create", "Erase", "Modify", "Access Control"}
	if len(names) != len(want) {
		t.Fatalf("rights = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("rights = %v, want %v", names, want)
		}
	}
	// A misspelt name in the hand-written matrix would read as "not held" and pass.
	real := map[string]bool{}
	for _, n := range names {
		real[n] = true
	}
	for role, held := range heldByRole {
		for n := range held {
			if !real[n] {
				t.Errorf("matrix for %s names %q, which is not a right", role, n)
			}
		}
	}
}

// Each right, asked through the live Authorizer for every built-in role on every tree
// kind, matches the hand-written matrix. Supervisor holding for the owner alone on EVERY
// tree kind is the invariant "Supervisor = owner at node" rests on: if a core kind ever
// gave the admin role its admin verb, this fails instead of the table silently lying.
func TestTrusteeQuestionAnswersByRole(t *testing.T) {
	az := NewAuthorizer(nil)
	ctx := context.Background()
	for _, kind := range TreeScopeableKinds() {
		for role, held := range heldByRole {
			p := ScopedPrincipal(model.ID("cred-"+role), role, grantTenant, role)
			for _, r := range TrusteeRights() {
				req, ok := r.Question(p, grantTenant, ResourceAttrs{Kind: kind, ID: "node-1"})
				if !ok {
					t.Fatalf("%s on tree kind %s: no question", r.Name, kind)
				}
				if got := az.Authorize(ctx, req).Allow; got != held[r.Name] {
					t.Errorf("%s %s on %s: allow=%v, want %v (asked %q)", role, r.Name, kind, got, held[r.Name], req.Permission)
				}
			}
		}
	}
}

// The question is the request the request path sends: the verbs below are the existing
// ones, Browse is a collection question (no entity id), the other entity rights carry the
// node, and Access Control is asked exactly as its routes ask it (ResourceFor: the "rbac"
// kind, no node).
func TestTrusteeQuestionShape(t *testing.T) {
	p := ScopedPrincipal("c", "c", grantTenant, RoleViewer)
	node := ResourceAttrs{Kind: "agent", ID: "node-1"}
	want := map[string]struct {
		perm Permission
		kind string
		id   string
	}{
		"Supervisor":     {"agent:admin", "agent", "node-1"},
		"Browse":         {"agent:read", "agent", ""},
		"Read":           {"agent:read", "agent", "node-1"},
		"Write":          {"agent:write", "agent", "node-1"},
		"Create":         {"agent:write", "agent", "node-1"},
		"Erase":          {"agent:write", "agent", "node-1"},
		"Modify":         {"agent:write", "agent", "node-1"},
		"Access Control": {"governance:rbac:admin", "rbac", ""},
	}
	for _, r := range TrusteeRights() {
		req, ok := r.Question(p, grantTenant, node)
		w, known := want[r.Name]
		if !ok || !known {
			t.Fatalf("%s: ok=%v in table=%v", r.Name, ok, known)
		}
		if req.Permission != w.perm || req.Resource.Kind != w.kind || req.Resource.ID != w.id || req.Tenant != grantTenant {
			t.Errorf("%s asked %+v, want permission %q kind %q id %q", r.Name, req, w.perm, w.kind, w.id)
		}
	}
}

// There is no honest question for a kind the catalog does not know, for an entity right
// without a node, or for Supervisor on a non-tree kind (where the admin role holds the
// admin verb too, so the question would not mean "owner"). The answer is "no question",
// not a request: a superadmin would pass any permission, including a malformed one.
func TestTrusteeQuestionRefusesWhatItCannotAsk(t *testing.T) {
	p := ScopedPrincipal("c", "c", grantTenant, RoleOwner)
	cases := []struct {
		name  string
		right TrusteeRight
		node  ResourceAttrs
	}{
		{"empty kind", TrusteeRead, ResourceAttrs{ID: "n"}},
		{"unknown kind", TrusteeBrowse, ResourceAttrs{Kind: "bogus"}},
		{"colon smuggles a module permission", TrusteeSupervisor, ResourceAttrs{Kind: "governance:rbac", ID: "n"}},
		{"colon on a read", TrusteeBrowse, ResourceAttrs{Kind: "session-cockpit:transcript"}},
		{"IAM kind is not a tree kind", TrusteeSupervisor, ResourceAttrs{Kind: "user", ID: "n"}},
		{"entity right without a node", TrusteeWrite, ResourceAttrs{Kind: "agent"}},
		{"zero right", TrusteeRight{}, ResourceAttrs{Kind: "agent", ID: "n"}},
	}
	for _, c := range cases {
		if req, ok := c.right.Question(p, grantTenant, c.node); ok {
			t.Errorf("%s: got a question %+v", c.name, req)
		}
	}
}

// A positive scoped grant at one node answers the right there and nowhere else, and the
// batch of all rights is exactly the per-right Authorize: no second algebra.
func TestTrusteeQuestionHonorsScopedGrantAtNode(t *testing.T) {
	az := NewAuthorizer(nil, WithScopedGrants(scopedFunc(func(_ context.Context, req Request) (ScopedDecision, error) {
		if req.Resource.ID == "node-1" && req.Permission == "agent:write" {
			return ScopedDecision{Effect: EffectGrant}, nil
		}
		return ScopedDecision{Effect: EffectAbstain}, nil
	})))
	p := ScopedPrincipal("c", "c", grantTenant, RoleViewer)
	var reqs []Request
	for _, r := range TrusteeRights() {
		req, ok := r.Question(p, grantTenant, ResourceAttrs{Kind: "agent", ID: "node-1"})
		if !ok {
			t.Fatalf("%s: no question", r.Name)
		}
		reqs = append(reqs, req)
	}
	got, err := az.AuthorizeBatch(context.Background(), reqs)
	if err != nil {
		t.Fatalf("AuthorizeBatch: %v", err)
	}
	for i, r := range TrusteeRights() {
		// A viewer holds Browse and Read by RBAC; the grant adds the write verb at node-1 only.
		want := r.Name == "Browse" || r.Name == "Read" || r.Verb == VerbWrite
		if got[i].Allow != want {
			t.Errorf("viewer %s at node-1: allow=%v, want %v", r.Name, got[i].Allow, want)
		}
		if one := az.Authorize(context.Background(), reqs[i]); one.Allow != got[i].Allow || one.Reason != got[i].Reason {
			t.Errorf("%s: batch %+v differs from Authorize %+v", r.Name, got[i], one)
		}
	}
	other, _ := TrusteeWrite.Question(p, grantTenant, ResourceAttrs{Kind: "agent", ID: "node-2"})
	if az.Authorize(context.Background(), other).Allow {
		t.Error("write granted at node-1 leaked to node-2")
	}
}

// On a registered module kind the admin verb is held by the admin role as well as the
// owner (roleGrantsVerb), so Supervisor would no longer mean "owner": it has no question
// there. The ordinary rights still ask, and ask with the Resource.Kind the request path
// derives from the permission.
func TestTrusteeModuleKind(t *testing.T) {
	ResetModuleCatalog()
	t.Cleanup(ResetModuleCatalog)
	if err := RegisterModulePermissions([]Permission{"trusteemod:thing:read", "trusteemod:thing:admin"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	node := ResourceAttrs{Kind: "trusteemod:thing", ID: "n"}
	p := ScopedPrincipal("c", "c", grantTenant, RoleAdmin)
	if req, ok := TrusteeSupervisor.Question(p, grantTenant, node); ok {
		t.Fatalf("Supervisor asked on a module kind: %+v", req)
	}
	req, ok := TrusteeRead.Question(p, grantTenant, node)
	if !ok || req.Permission != "trusteemod:thing:read" || req.Resource.Kind != "thing" || req.Resource.ID != "n" {
		t.Fatalf("Read on a module kind = %+v, ok=%v", req, ok)
	}
	if !NewAuthorizer(nil).Authorize(context.Background(), req).Allow {
		t.Error("admin role does not read a module kind it holds by verb tier")
	}
}
