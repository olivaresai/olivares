// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// routine governance policy CRUD lifecycle, validation, uniqueness and
// posture endpoint.

func TestRoutinePolicyCRUDLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Create a tenant-scoped policy.
	r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "default-limits", "scope_kind": "tenant",
		"max_cadence_seconds": 300, "max_active_routines": 10,
		"require_approval":      true,
		"allowed_cron_patterns": []string{"0 * * * *", "*/30 * * * *"},
		"blocked_environments":  []string{"env_prod_1"},
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["name"] != "default-limits" || r.body["scope_kind"] != "tenant" {
		t.Fatalf("created policy = %v", r.body)
	}
	if r.body["max_cadence_seconds"].(float64) != 300 {
		t.Fatalf("max_cadence_seconds = %v, want 300", r.body["max_cadence_seconds"])
	}
	if r.body["require_approval"] != true {
		t.Fatalf("require_approval = %v, want true", r.body["require_approval"])
	}

	// List returns the policy.
	r = h.do("GET", "/v1/m/governance/routine-policies", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if len(items(r)) != 1 {
		t.Fatalf("list items = %d, want 1", len(items(r)))
	}

	// Get by ID.
	r = h.do("GET", "/v1/m/governance/routine-policies/"+id, admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["id"] != id || r.body["name"] != "default-limits" {
		t.Fatalf("get = %v", r.body)
	}

	// Update: change cadence and disable.
	r = h.do("PUT", "/v1/m/governance/routine-policies/"+id, admin, map[string]any{
		"enabled": false, "max_cadence_seconds": 600,
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}
	if r.body["enabled"] != false {
		t.Fatalf("updated enabled = %v, want false", r.body["enabled"])
	}
	if r.body["max_cadence_seconds"].(float64) != 600 {
		t.Fatalf("updated max_cadence_seconds = %v, want 600", r.body["max_cadence_seconds"])
	}

	// Delete.
	r = h.do("DELETE", "/v1/m/governance/routine-policies/"+id, admin, nil, hdr)
	if r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}

	// Get after delete returns 404.
	r = h.do("GET", "/v1/m/governance/routine-policies/"+id, admin, nil, hdr)
	if r.code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", r.code)
	}

	// List after delete returns empty.
	r = h.do("GET", "/v1/m/governance/routine-policies", admin, nil, hdr)
	if r.code != http.StatusOK || len(items(r)) != 0 {
		t.Fatalf("list after delete = %d items=%d", r.code, len(items(r)))
	}

	// Verify audit trail.
	actions := h.auditActions(tenant)
	if !contains(actions, "governance.routine_policy.create") {
		t.Fatalf("missing create audit: %v", actions)
	}
	if !contains(actions, "governance.routine_policy.update") {
		t.Fatalf("missing update audit: %v", actions)
	}
	if !contains(actions, "governance.routine_policy.delete") {
		t.Fatalf("missing delete audit: %v", actions)
	}
}

func TestRoutinePolicyValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "empty name",
			body: map[string]any{"name": "", "scope_kind": "tenant"},
		},
		{
			name: "invalid scope_kind",
			body: map[string]any{"name": "p1", "scope_kind": "org"},
		},
		{
			name: "tenant scope with scope_ref",
			body: map[string]any{"name": "p1", "scope_kind": "tenant", "scope_ref": "ws1"},
		},
		{
			name: "workspace scope without scope_ref",
			body: map[string]any{"name": "p1", "scope_kind": "workspace"},
		},
		{
			name: "user scope without scope_ref",
			body: map[string]any{"name": "p1", "scope_kind": "user"},
		},
		{
			name: "cadence too low",
			body: map[string]any{"name": "p1", "scope_kind": "tenant", "max_cadence_seconds": 30},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/governance/routine-policies", admin, tt.body, hdr)
			if r.code != http.StatusBadRequest {
				t.Fatalf("%s: create = %d %s, want 400", tt.name, r.code, r.raw)
			}
		})
	}
}

func TestRoutinePolicyUniqueName(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	body := map[string]any{"name": "unique-pol", "scope_kind": "tenant", "max_cadence_seconds": 300}
	r := h.do("POST", "/v1/m/governance/routine-policies", admin, body, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("first create = %d %s", r.code, r.raw)
	}

	// A duplicate name in the same tenant conflicts.
	r = h.do("POST", "/v1/m/governance/routine-policies", admin, body, hdr)
	if r.code != http.StatusConflict {
		t.Fatalf("duplicate create = %d %s, want 409", r.code, r.raw)
	}
}

func TestRoutinePolicyPosture(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Empty posture.
	r := h.do("GET", "/v1/m/governance/routine-policies/posture", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("posture = %d %s", r.code, r.raw)
	}
	if r.body["total_policies"].(float64) != 0 || r.body["enabled_policies"].(float64) != 0 {
		t.Fatalf("empty posture = %v", r.body)
	}

	// Create two policies: one enabled, one disabled.
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "p-enabled", "scope_kind": "tenant", "enabled": true,
		"max_cadence_seconds": 300, "max_active_routines": 5,
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "p-disabled", "scope_kind": "workspace", "scope_ref": "ws-1",
		"enabled": false, "max_cadence_seconds": 600,
	}, hdr)

	r = h.do("GET", "/v1/m/governance/routine-policies/posture", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("posture = %d %s", r.code, r.raw)
	}
	if r.body["total_policies"].(float64) != 2 {
		t.Fatalf("total_policies = %v, want 2", r.body["total_policies"])
	}
	if r.body["enabled_policies"].(float64) != 1 {
		t.Fatalf("enabled_policies = %v, want 1", r.body["enabled_policies"])
	}

	policies, ok := r.body["policies"].([]any)
	if !ok || len(policies) != 2 {
		t.Fatalf("posture policies = %v", r.body["policies"])
	}
}

// TestRoutinePolicyPostureEffective pins the COMPOSED block the console header
// renders. The point of the block is that the console does not re-derive the
// fold: every assertion here is a composition rule that lives in
// routinepolicy_resolve.go, read back through the API.
func TestRoutinePolicyPostureEffective(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// With no policies at all the block still exists and says "nothing in force".
	r := h.do("GET", "/v1/m/governance/routine-policies/posture", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("posture = %d %s", r.code, r.raw)
	}
	eff, ok := r.body["effective"].(map[string]any)
	if !ok {
		t.Fatalf("effective block missing from empty posture: %s", r.raw)
	}
	if eff["in_force"] != false {
		t.Fatalf("empty in_force = %v, want false", eff["in_force"])
	}

	// Two ENABLED tenant policies plus a DISABLED one whose (much larger) floor
	// must NOT reach the composition.
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "loose", "scope_kind": "tenant", "enabled": true,
		"max_cadence_seconds": 300, "max_active_routines": 10,
		"require_approval":      false,
		"allowed_cron_patterns": []string{"0 * * * *", "*/30 * * * *"},
		"blocked_environments":  []string{"prod"},
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "tight", "scope_kind": "tenant", "enabled": true,
		"max_cadence_seconds": 900, "max_active_routines": 4,
		"require_approval":      true,
		"allowed_cron_patterns": []string{"0 * * * *"},
		"blocked_environments":  []string{"staging"},
	}, hdr)
	// A cap at a DIFFERENT scope must stay its own entry: caps constrain
	// different populations and collapsing them into one number is exactly the
	// mistake the vector exists to prevent. With only tenant caps a single
	// global minimum would pass the assertion below.
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "alice-cap", "scope_kind": "user", "scope_ref": "user:alice",
		"enabled": true, "max_active_routines": 2,
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "disabled-huge", "scope_kind": "tenant", "enabled": false,
		"max_cadence_seconds": 31622400, "require_approval": true,
	}, hdr)

	// Resolved FOR alice, so the user-scoped cap is in scope. Without a user_ref
	// a routine with no owning user is a definite NON-match against a
	// user-scoped policy, which is the semantics the scoped test below pins.
	r = h.do("GET", "/v1/m/governance/routine-policies/posture?user_ref=user:alice", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("posture = %d %s", r.code, r.raw)
	}
	eff, ok = r.body["effective"].(map[string]any)
	if !ok {
		t.Fatalf("effective block missing: %s", r.raw)
	}
	if eff["in_force"] != true {
		t.Fatalf("in_force = %v, want true", eff["in_force"])
	}
	// Floor takes the MAXIMUM of the enabled floors; the disabled 31622400 is
	// not one of them.
	if got := eff["min_interval_seconds"].(float64); got != 900 {
		t.Fatalf("min_interval_seconds = %v, want 900 (max of enabled floors)", got)
	}
	// Approval ORs.
	if eff["require_approval"] != true {
		t.Fatalf("require_approval = %v, want true (OR)", eff["require_approval"])
	}
	// Cron allowlists INTERSECT.
	crons := strList(t, eff["cron_allowed"])
	if len(crons) != 1 || crons[0] != "0 * * * *" {
		t.Fatalf("cron_allowed = %v, want the intersection [0 * * * *]", crons)
	}
	if eff["cron_allowlist_in_force"] != true {
		t.Fatalf("cron_allowlist_in_force = %v, want true", eff["cron_allowlist_in_force"])
	}
	// Blocked environments UNION.
	envs := strList(t, eff["blocked_environments"])
	if len(envs) != 2 || envs[0] != "prod" || envs[1] != "staging" {
		t.Fatalf("blocked_environments = %v, want the union [prod staging]", envs)
	}
	// Active caps stay a VECTOR keyed by scope: the two tenant caps fold to the
	// smaller one, and the user cap stays a SEPARATE constraint.
	caps, ok := eff["active_caps"].([]any)
	if !ok {
		t.Fatalf("active_caps = %v, want an array", eff["active_caps"])
	}
	byScope := map[string]float64{}
	for _, c := range caps {
		m := c.(map[string]any)
		byScope[m["scope_kind"].(string)+"/"+m["scope_ref"].(string)] = m["max"].(float64)
	}
	if len(byScope) != 2 {
		t.Fatalf("active_caps = %v, want two distinct scopes (tenant and user)", byScope)
	}
	if byScope["tenant/"] != 4 {
		t.Fatalf("tenant cap = %v, want 4 (smaller of 10 and 4)", byScope["tenant/"])
	}
	if byScope["user/user:alice"] != 2 {
		t.Fatalf("user cap = %v, want 2 kept as its own constraint", byScope["user/user:alice"])
	}
	// Drill-down: exactly the ENABLED policies are cited as the origin (the two
	// tenant ones plus the user one, which matches this scope).
	refs := strList(t, eff["policy_refs"])
	if len(refs) != 3 {
		t.Fatalf("policy_refs = %v, want the 3 enabled policies", refs)
	}
	if eff["digest"] == "" || eff["digest"] == nil {
		t.Fatalf("digest missing: %v", eff)
	}
	// The unfiltered counters still describe the whole set, disabled included.
	if r.body["total_policies"].(float64) != 4 || r.body["enabled_policies"].(float64) != 3 {
		t.Fatalf("counters = %v/%v, want 4/3", r.body["total_policies"], r.body["enabled_policies"])
	}
}

// TestRoutinePolicyPostureEffectiveDenyAll pins the tri-state at the COMPOSED
// level: an authored EMPTY allowlist is a deny-all the console must be able to
// show as such, and it must not read as "no allowlist".
func TestRoutinePolicyPostureEffectiveDenyAll(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "deny-every-cron", "scope_kind": "tenant", "enabled": true,
		"allowed_cron_patterns": []string{},
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	// The read surface keeps the authored empty array distinct from null.
	if raw, ok := r.body["allowed_cron_patterns"].([]any); !ok || len(raw) != 0 {
		t.Fatalf("created allowed_cron_patterns = %#v, want an empty array (not null)",
			r.body["allowed_cron_patterns"])
	}

	r = h.do("GET", "/v1/m/governance/routine-policies/posture", admin, nil, hdr)
	eff := r.body["effective"].(map[string]any)
	if eff["cron_allowlist_in_force"] != true {
		t.Fatalf("cron_allowlist_in_force = %v, want true for an authored empty allowlist",
			eff["cron_allowlist_in_force"])
	}
	if crons := strList(t, eff["cron_allowed"]); len(crons) != 0 {
		t.Fatalf("cron_allowed = %v, want empty (deny-all)", crons)
	}
}

// TestRoutinePolicyPostureEffectiveScoped pins that the composition answers for
// the scope the CALLER names, so the console can ask "what governs this user?"
// rather than only "what exists?".
func TestRoutinePolicyPostureEffectiveScoped(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "tenant-wide", "scope_kind": "tenant", "enabled": true,
		"max_cadence_seconds": 300,
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "alice-only", "scope_kind": "user", "scope_ref": "user:alice",
		"enabled": true, "max_cadence_seconds": 3600,
	}, hdr)

	// No user named: a routine with no owning user is provably OUTSIDE a
	// user-scoped policy, so only the tenant floor applies — and the answer is
	// definite, not indeterminate.
	r := h.do("GET", "/v1/m/governance/routine-policies/posture", admin, nil, hdr)
	eff := r.body["effective"].(map[string]any)
	if got := eff["min_interval_seconds"].(float64); got != 300 {
		t.Fatalf("unscoped min_interval_seconds = %v, want 300", got)
	}
	if eff["indeterminate"] != false {
		t.Fatalf("unscoped indeterminate = %v, want false", eff["indeterminate"])
	}

	// Alice's own routines take the tighter user floor.
	r = h.do("GET", "/v1/m/governance/routine-policies/posture?user_ref=user:alice", admin, nil, hdr)
	eff = r.body["effective"].(map[string]any)
	if got := eff["min_interval_seconds"].(float64); got != 3600 {
		t.Fatalf("alice min_interval_seconds = %v, want 3600", got)
	}
	if eff["scope_user_ref"] != "user:alice" {
		t.Fatalf("scope_user_ref = %v, want the echo of what it resolved for", eff["scope_user_ref"])
	}
	if refs := strList(t, eff["policy_refs"]); len(refs) != 2 {
		t.Fatalf("alice policy_refs = %v, want both policies matched", refs)
	}
}

// strList reads a JSON array of strings out of a decoded body, failing the test
// rather than panicking on a shape change.
func strList(t *testing.T, v any) []string {
	t.Helper()
	// A JSON null must NOT read as "an empty list": the effective block emits
	// every list unconditionally, so a null here means the field went missing,
	// and silently returning nil would let a deny-all assertion pass against a
	// payload that carries no allowlist at all.
	if v == nil {
		t.Fatalf("expected a JSON array, got null")
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("value is not a JSON array: %#v", v)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("array element is not a string: %#v", item)
		}
		out = append(out, s)
	}
	return out
}

func TestRoutinePolicyListFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "tenant-pol", "scope_kind": "tenant",
		"max_cadence_seconds": 300, "enabled": true,
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "ws-pol", "scope_kind": "workspace", "scope_ref": "ws-1",
		"max_cadence_seconds": 600, "enabled": false,
	}, hdr)

	// Filter by scope_kind.
	r := h.do("GET", "/v1/m/governance/routine-policies?scope_kind=tenant", admin, nil, hdr)
	if r.code != http.StatusOK || len(items(r)) != 1 {
		t.Fatalf("filter scope_kind=tenant = %d items=%d", r.code, len(items(r)))
	}

	// Filter by enabled.
	r = h.do("GET", "/v1/m/governance/routine-policies?enabled=true", admin, nil, hdr)
	if r.code != http.StatusOK || len(items(r)) != 1 {
		t.Fatalf("filter enabled=true = %d items=%d", r.code, len(items(r)))
	}

	r = h.do("GET", "/v1/m/governance/routine-policies?enabled=false", admin, nil, hdr)
	if r.code != http.StatusOK || len(items(r)) != 1 {
		t.Fatalf("filter enabled=false = %d items=%d", r.code, len(items(r)))
	}
}

func TestRoutinePolicyWorkspaceScope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "ws-limits", "scope_kind": "workspace", "scope_ref": "engineering",
		"max_cadence_seconds": 120, "max_active_routines": 3,
		"require_approval": false,
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace-scoped = %d %s", r.code, r.raw)
	}
	if r.body["scope_kind"] != "workspace" || r.body["scope_ref"] != "engineering" {
		t.Fatalf("scope = %v %v", r.body["scope_kind"], r.body["scope_ref"])
	}
}

// TestRoutinePolicyPostureMatchesEnforcementSeam is the test that would catch a
// break the HTTP-only assertions above cannot see. The console reads the
// composed posture over HTTP; orchestration reads it through the exported
// ResolveRoutinePolicy seam. They are supposed to be ONE fold, and the whole
// argument for extending the endpoint instead of composing in the browser rests
// on that. If the wrapper and the handler ever diverge, the panel would
// describe a posture the plane does not enforce — so the digest, which
// fingerprints the composed decision, has to be identical for the same scope.
func TestRoutinePolicyPostureMatchesEnforcementSeam(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "tenant-floor", "scope_kind": "tenant", "enabled": true,
		"max_cadence_seconds": 600, "max_active_routines": 7,
		"require_approval":      true,
		"allowed_cron_patterns": []string{"0 * * * *", "*/15 * * * *"},
		"blocked_environments":  []string{"prod"},
	}, hdr)
	h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "alice-floor", "scope_kind": "user", "scope_ref": "user:alice",
		"enabled": true, "max_cadence_seconds": 1800,
		"allowed_cron_patterns": []string{"0 * * * *"},
	}, hdr)

	for _, tc := range []struct {
		name  string
		query string
		scope governance.RoutineScope
	}{
		{"baseline", "", governance.RoutineScope{UserKnown: true}},
		{"user", "?user_ref=user:alice", governance.RoutineScope{UserRef: "user:alice", UserKnown: true}},
		{"unknown-user-axis", "?user_known=false", governance.RoutineScope{UserKnown: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("GET", "/v1/m/governance/routine-policies/posture"+tc.query, admin, nil, hdr)
			if r.code != http.StatusOK {
				t.Fatalf("posture = %d %s", r.code, r.raw)
			}
			eff, ok := r.body["effective"].(map[string]any)
			if !ok {
				t.Fatalf("effective block missing: %s", r.raw)
			}

			seam, err := h.gov.ResolveRoutinePolicy(context.Background(), tenant, tc.scope)
			if err != nil {
				t.Fatalf("seam resolve = %v", err)
			}
			if eff["digest"] != seam.Digest {
				t.Fatalf("digest: HTTP %v != seam %v — the console would describe a posture the plane does not enforce",
					eff["digest"], seam.Digest)
			}
			if eff["in_force"] != seam.InForce {
				t.Fatalf("in_force: HTTP %v != seam %v", eff["in_force"], seam.InForce)
			}
			if got := int64(eff["min_interval_seconds"].(float64)); got != seam.MinIntervalSec {
				t.Fatalf("floor: HTTP %d != seam %d", got, seam.MinIntervalSec)
			}
			if eff["indeterminate"] != seam.Indeterminate {
				t.Fatalf("indeterminate: HTTP %v != seam %v", eff["indeterminate"], seam.Indeterminate)
			}
		})
	}
}

// A named user AND an unanswerable user axis is a contradiction, and the
// resolver settles it silently in favor of the name — so the caller would get
// an answer composed FOR that user while believing it asked about an unknown
// one. The endpoint refuses instead.
func TestRoutinePolicyPostureRefusesContradictoryScope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.do("GET", "/v1/m/governance/routine-policies/posture?user_ref=user:alice&user_known=false", admin, nil, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("contradictory scope = %d %s, want 400", r.code, r.raw)
	}
	// Each half on its own stays valid.
	if r := h.do("GET", "/v1/m/governance/routine-policies/posture?user_known=false", admin, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("unknown axis alone = %d %s, want 200", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/governance/routine-policies/posture?user_ref=user:alice", admin, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("named user alone = %d %s, want 200", r.code, r.raw)
	}
}
