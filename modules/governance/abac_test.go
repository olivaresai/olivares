// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// authorPolicy POSTs an abac policy and fails the test if not 201.
func (h *harness) authorPolicy(token string, tenant model.TenantID, name string, spec map[string]any) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/governance/policies", token, map[string]any{"name": name, "kind": "abac", "enabled": true, "spec": spec}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("author policy = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func req(tenant model.TenantID, perm string) auth.Request {
	return auth.Request{Principal: auth.Principal{Kind: auth.KindUser}, Permission: auth.Permission(perm), Tenant: tenant}
}

func (h *harness) authorizationEpoch(tenant model.TenantID) store.AuthorizationFactRef {
	h.t.Helper()
	var out store.AuthorizationFactRef
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			h.t.Fatal("tenant scope lacks authorization epoch reader")
		}
		var err error
		out, err = reader.ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		h.t.Fatalf("read authorization epoch: %v", err)
	}
	if out.Kind != model.AuthorizationEpochKind || out.ID != model.ID(tenant) || out.Version < 1 {
		h.t.Fatalf("authorization epoch is not an exact tenant witness: %+v", out)
	}
	return out
}

func (h *harness) storedPolicy(tenant model.TenantID, id model.ID) model.Policy {
	h.t.Helper()
	var out model.Policy
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		out, err = sc.Policies().Get(context.Background(), id)
		return err
	}); err != nil {
		h.t.Fatalf("read policy %s: %v", id, err)
	}
	return out
}

func TestABACAuthorizationEpochMutationMatrix(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "epoch-matrix")
	version := h.authorizationEpoch(tenant).Version

	assertDelta := func(label string, before, delta int64) int64 {
		t.Helper()
		after := h.authorizationEpoch(tenant).Version
		if after != before+delta {
			t.Fatalf("%s epoch = %d, want %d (delta %+d)", label, after, before+delta, delta)
		}
		return after
	}
	request := func(method, path string, body map[string]any, want int) resp {
		t.Helper()
		r := h.do(method, path, admin, body, tenantHdr(tenant))
		if r.code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, r.code, r.raw, want)
		}
		return r
	}
	abacSpec := func(permission string) map[string]any {
		return map[string]any{"rules": []any{map[string]any{"permission": permission, "deny": true}}}
	}

	// Disabled ABAC policy authoring cannot change an authorization decision.
	disabled := request("POST", "/v1/m/governance/policies", map[string]any{
		"name": "disabled", "kind": "abac", "enabled": false, "spec": abacSpec("agent:write"),
	}, http.StatusCreated)
	disabledID := model.ID(disabled.body["id"].(string))
	version = assertDelta("create disabled ABAC", version, 0)

	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "disabled", "kind": "abac", "enabled": false, "spec": abacSpec("agent:read"),
	}, http.StatusOK)
	version = assertDelta("disabled-to-disabled canonical spec change", version, 0)

	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "disabled", "kind": "abac", "enabled": true, "spec": abacSpec("agent:read"),
	}, http.StatusOK)
	version = assertDelta("disabled-to-enabled toggle", version, 1)

	beforeName := h.storedPolicy(tenant, disabledID)
	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "renamed", "kind": "abac", "enabled": true, "spec": abacSpec("agent:read"),
	}, http.StatusOK)
	version = assertDelta("enabled name-only update", version, 0)
	afterName := h.storedPolicy(tenant, disabledID)
	if afterName.Version != beforeName.Version+1 {
		t.Fatalf("name-only update did not persist exactly once: version %d -> %d", beforeName.Version, afterName.Version)
	}

	policyEvents := len(h.host.ofType(event.TypePolicyChanged))
	beforeReplay := h.storedPolicy(tenant, disabledID)
	// Whitespace is normalized before comparison, so this is the same canonical
	// policy even though the incoming match string is not byte-identical.
	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "renamed", "kind": "abac", "enabled": true, "spec": abacSpec("  agent:read  "),
	}, http.StatusOK)
	version = assertDelta("exact canonical replay", version, 0)
	afterReplay := h.storedPolicy(tenant, disabledID)
	if afterReplay.Version != beforeReplay.Version {
		t.Fatalf("exact replay wrote the policy row: version %d -> %d", beforeReplay.Version, afterReplay.Version)
	}
	if got := len(h.host.ofType(event.TypePolicyChanged)); got != policyEvents {
		t.Fatalf("exact replay emitted policy.changed: events %d -> %d", policyEvents, got)
	}

	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "renamed", "kind": "abac", "enabled": true, "spec": abacSpec("model:read"),
	}, http.StatusOK)
	version = assertDelta("enabled canonical spec change", version, 1)

	request("PUT", "/v1/m/governance/policies/"+disabledID.String(), map[string]any{
		"name": "renamed", "kind": "abac", "enabled": false, "spec": abacSpec("model:read"),
	}, http.StatusOK)
	version = assertDelta("enabled-to-disabled toggle", version, 1)

	request("DELETE", "/v1/m/governance/policies/"+disabledID.String(), nil, http.StatusNoContent)
	version = assertDelta("delete disabled ABAC", version, 0)

	enabled := request("POST", "/v1/m/governance/policies", map[string]any{
		"name": "enabled", "kind": "abac", "enabled": true, "spec": abacSpec("tool:read"),
	}, http.StatusCreated)
	enabledID := model.ID(enabled.body["id"].(string))
	version = assertDelta("create enabled ABAC", version, 1)
	request("DELETE", "/v1/m/governance/policies/"+enabledID.String(), nil, http.StatusNoContent)
	version = assertDelta("delete enabled ABAC", version, 1)

	// Approval authoring is deliberately outside this epoch cut, regardless of
	// enabled state, spec changes, toggles, or deletion.
	approval := request("POST", "/v1/m/governance/policies", map[string]any{
		"name": "approval", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": "deploy"}},
	}, http.StatusCreated)
	approvalID := model.ID(approval.body["id"].(string))
	version = assertDelta("create enabled approval", version, 0)
	request("PUT", "/v1/m/governance/policies/"+approvalID.String(), map[string]any{
		"name": "approval-v2", "kind": "approval", "enabled": false,
		"spec": map[string]any{"required_approvals": 2, "match": map[string]any{"action": "deploy"}},
	}, http.StatusOK)
	version = assertDelta("update approval", version, 0)
	request("DELETE", "/v1/m/governance/policies/"+approvalID.String(), nil, http.StatusNoContent)
	assertDelta("delete disabled approval", version, 0)
}

func TestABACDenyRuleFurtherRestricts(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.authorPolicy(admin, tenant, "no-writes", map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}})

	eval := h.gov.Evaluator()
	if d, _ := eval.Evaluate(context.Background(), req(tenant, "agent:write")); d.Allow {
		t.Fatal("write should be denied by the abac deny rule")
	}
	if d, err := eval.Evaluate(context.Background(), req(tenant, "agent:read")); err != nil || !d.Allow {
		t.Fatalf("read should be allowed (no matching deny): allow=%v err=%v", d.Allow, err)
	}
}

func TestABACResourceAndPrincipalRules(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Deny token principals from writing the policy resource (module permission).
	h.authorPolicy(admin, tenant, "no-token-policy-writes", map[string]any{"rules": []any{
		map[string]any{"deny": true, "resource": "policy", "principal_kind": "token"},
	}})
	eval := h.gov.Evaluator()

	tokenReq := auth.Request{Principal: auth.Principal{Kind: auth.KindToken}, Permission: "governance:policy:admin", Tenant: tenant}
	if d, _ := eval.Evaluate(context.Background(), tokenReq); d.Allow {
		t.Fatal("a token writing policy should be denied")
	}
	userReq := auth.Request{Principal: auth.Principal{Kind: auth.KindUser}, Permission: "governance:policy:admin", Tenant: tenant}
	if d, _ := eval.Evaluate(context.Background(), userReq); !d.Allow {
		t.Fatal("a user is not matched by the principal_kind=token rule and should be allowed")
	}
}

func TestABACEmptySetAllowsAndSystemShortCircuit(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eval := h.gov.Evaluator()

	if d, err := eval.Evaluate(context.Background(), req(tenant, "agent:write")); err != nil || !d.Allow {
		t.Fatalf("no policies must allow (RBAC stands): allow=%v err=%v", d.Allow, err)
	}
	// System / zero tenant is never further-restricted (superadmin/system ops).
	if d, _ := eval.Evaluate(context.Background(), req(model.SystemTenantID, "agent:write")); !d.Allow {
		t.Fatal("system tenant must short-circuit to allow")
	}
	if d, _ := eval.Evaluate(context.Background(), req(model.TenantID(""), "agent:write")); !d.Allow {
		t.Fatal("zero tenant must short-circuit to allow (not a fail-closed denial)")
	}
}

func TestABACMalformedSpecFailsClosed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Insert a corrupt enabled abac policy directly (bypassing validation), as a DB
	// tamper would. The tenant has never been evaluated, so the first Evaluate loads
	// it cold.
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, e := sc.Policies().Create(context.Background(), model.Policy{
			Name: "tampered", Kind: "abac", Enabled: true, Spec: map[string]any{"rules": "not-an-array"},
		})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	d, err := h.gov.Evaluator().Evaluate(context.Background(), req(tenant, "agent:read"))
	if err == nil || d.Allow {
		t.Fatalf("a corrupt enabled policy must fail closed (deny): allow=%v err=%v", d.Allow, err)
	}
}

func TestABACCacheInvalidatedAfterWrite(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eval := h.gov.Evaluator()
	// Prime the cache with the empty (allow) set.
	if d, _ := eval.Evaluate(context.Background(), req(tenant, "agent:write")); !d.Allow {
		t.Fatal("precondition: empty set allows")
	}
	// Author a deny rule; the write must invalidate the cache so the new rule is in
	// force on the very next evaluate (no stale-allow window).
	h.authorPolicy(admin, tenant, "no-writes", map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}})
	if d, _ := eval.Evaluate(context.Background(), req(tenant, "agent:write")); d.Allow {
		t.Fatal("the deny rule must take effect immediately after the write (stale-allow window)")
	}
}

func TestABACDenyBlocksRealRequestEndToEnd(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Deny reads of the identity resource for this tenant.
	pid := h.authorPolicy(admin, tenant, "no-identity-reads", map[string]any{"rules": []any{
		map[string]any{"deny": true, "resource": "identity", "verb": "read"},
	}})
	// The ABAC evaluator is wired into the Authorizer, so a real GET is denied...
	if r := h.do("GET", "/v1/m/governance/identities", admin, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("identity read should be ABAC-denied: %d %s", r.code, r.raw)
	}
	// ...while an unrelated resource (policy) is unaffected.
	if r := h.do("GET", "/v1/m/governance/policies", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("policy read should be unaffected: %d %s", r.code, r.raw)
	}
	// Deleting the policy restores access (cache invalidated post-commit).
	if r := h.do("DELETE", "/v1/m/governance/policies/"+pid, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete policy = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/governance/identities", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("identity read should be restored after deleting the deny policy: %d %s", r.code, r.raw)
	}
}

func TestABACConcurrentTenantsNoLeak(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "tenant-a")
	tenantB := h.createOrg(admin, "tenant-b")
	// A denies writes; B has no policy.
	h.authorPolicy(admin, tenantA, "no-writes", map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}})

	eval := h.gov.Evaluator()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d, _ := eval.Evaluate(context.Background(), req(tenantA, "agent:write")); d.Allow {
				t.Error("tenant A must deny writes")
			}
			if d, _ := eval.Evaluate(context.Background(), req(tenantB, "agent:write")); !d.Allow {
				t.Error("tenant B must NOT inherit A's deny rule")
			}
		}()
	}
	wg.Wait()
}

func TestABACRejectsInertAndInvalidSyntax(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	bad := func(spec map[string]any) int {
		r := h.do("POST", "/v1/m/governance/policies", admin, map[string]any{"name": "x", "kind": "abac", "enabled": true, "spec": spec}, tenantHdr(tenant))
		return r.code
	}
	// A sensitivity field would be inert (no resource-attrs reach the evaluator);
	// it is rejected as an unknown field rather than shipped as silent no-op syntax.
	if code := bad(map[string]any{"rules": []any{map[string]any{"deny": true, "sensitivity": "high"}}}); code != http.StatusBadRequest {
		t.Fatalf("an unknown rule field must be rejected, got %d", code)
	}
	// A rule with no selector would deny every request — rejected.
	if code := bad(map[string]any{"rules": []any{map[string]any{"deny": true}}}); code != http.StatusBadRequest {
		t.Fatalf("a selector-less rule must be rejected, got %d", code)
	}
	// An allow rule cannot widen an RBAC grant — only deny rules are accepted.
	if code := bad(map[string]any{"rules": []any{map[string]any{"deny": false, "verb": "write"}}}); code != http.StatusBadRequest {
		t.Fatalf("a non-deny rule must be rejected, got %d", code)
	}
}
