// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// the routine-policy list fields are TRI-STATE. Enforcement treats an
// authored empty allowlist as a deny-all and a null as "any", so the API must
// let an operator write, read back and CLEAR each of those states. Before
// `omitempty` erased the difference on read and an update could set a list
// but never clear it.

func TestRoutinePolicyListFieldsAreTriState(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Authored EMPTY list: it must come back as [], not disappear.
	r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "deny-all-cron", "scope_kind": "tenant",
		"allowed_cron_patterns": []string{},
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	got, ok := r.body["allowed_cron_patterns"].([]any)
	if !ok || got == nil || len(got) != 0 {
		t.Fatalf("authored [] read back as %#v; want an empty array (an operator must see their deny-all)", r.body["allowed_cron_patterns"])
	}

	// A policy with NO list must read back as null, distinguishable from [].
	r = h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "no-cron-list", "scope_kind": "tenant",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if v, present := r.body["allowed_cron_patterns"]; !present || v != nil {
		t.Fatalf("absent list read back as %#v; want null", v)
	}

	// Update must be able to CLEAR an authored list back to null.
	r = h.do("PUT", "/v1/m/governance/routine-policies/"+id, admin, map[string]any{
		"allowed_cron_patterns": nil,
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("clear = %d %s", r.code, r.raw)
	}
	if v := r.body["allowed_cron_patterns"]; v != nil {
		t.Fatalf("clearing the allowlist left %#v; want null", v)
	}

	// And an omitted field must leave the stored value alone.
	r = h.do("PUT", "/v1/m/governance/routine-policies/"+id, admin, map[string]any{
		"blocked_environments": []string{"prod"},
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("set blocked = %d %s", r.code, r.raw)
	}
	r = h.do("PUT", "/v1/m/governance/routine-policies/"+id, admin, map[string]any{
		"enabled": false,
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("unrelated update = %d %s", r.code, r.raw)
	}
	blocked, ok := r.body["blocked_environments"].([]any)
	if !ok || len(blocked) != 1 || blocked[0] != "prod" {
		t.Fatalf("an omitted list field was clobbered: %#v", r.body["blocked_environments"])
	}
}

// A malformed list is a 400, never a silently dropped control.
func TestRoutinePolicyRejectsMalformedList(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "p", "scope_kind": "tenant",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	if r := h.do("PUT", "/v1/m/governance/routine-policies/"+id, admin, map[string]any{
		"allowed_cron_patterns": map[string]any{"not": "an array"},
	}, hdr); r.code != http.StatusBadRequest {
		t.Fatalf("malformed allowlist update = %d %s, want 400", r.code, r.raw)
	}
}

// (round-1 review finding) — a workspace-scoped policy authored with the
// operator-facing SLUG must resolve. The resolver maps the routine's workspace
// id to its slug so that spelling matches; if that lookup ever fails it must
// propagate, because returning "no slug" would silently stop every
// slug-authored workspace policy from matching — a fail-open with nothing
// marked indeterminate.
func TestResolveRoutinePolicyMatchesWorkspaceBySlug(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	ws := h.do("GET", "/v1/workspaces", admin, nil, hdr)
	if ws.code != http.StatusOK {
		t.Skipf("workspaces surface unavailable in this harness: %d %s", ws.code, ws.raw)
	}
	items, _ := ws.body["items"].([]any)
	if len(items) == 0 {
		t.Skip("no default workspace materialized in this harness")
	}
	first := items[0].(map[string]any)
	wsID, _ := first["id"].(string)
	wsSlug, _ := first["slug"].(string)
	if wsID == "" || wsSlug == "" {
		t.Skipf("workspace row has no id/slug: %#v", first)
	}

	// Author the policy by SLUG.
	if r := h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
		"name": "by-slug", "scope_kind": "workspace", "scope_ref": wsSlug,
		"max_cadence_seconds": 3600,
	}, hdr); r.code != http.StatusCreated {
		t.Fatalf("create policy by slug = %d %s", r.code, r.raw)
	}

	got, err := h.gov.ResolveRoutinePolicy(context.Background(), tenant, governance.RoutineScope{WorkspaceRef: wsID})
	if err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if !got.InForce || got.MinIntervalSec != 3600 {
		t.Fatalf("slug-authored workspace policy did not match a routine in that workspace: %+v", got)
	}
}

// Round-2 MF-4 — max_cadence_seconds is an operator-supplied int64, and a
// large-but-previously-valid value multiplied into a time.Duration overflows
// its nanosecond range and wraps NEGATIVE, so every elapsed time compared
// "greater" and the cadence floor silently ALLOWED. Enforcement now compares in
// seconds, and the policy surface refuses a floor no admissible routine could
// ever satisfy (module IV caps a schedule's own interval at ~366 days).
func TestRoutinePolicyRejectsAnUnsatisfiableCadenceFloor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	create := func(name string, floor int64) int {
		return h.do("POST", "/v1/m/governance/routine-policies", admin, map[string]any{
			"name": name, "scope_kind": "tenant", "max_cadence_seconds": floor,
		}, hdr).code
	}
	for i, v := range []int64{1 << 62, 9223372036854, 31622401} {
		if code := create(fmt.Sprintf("absurd-%d", i), v); code != http.StatusBadRequest {
			t.Errorf("max_cadence_seconds=%d accepted with %d, want 400", v, code)
		}
	}
	if code := create("at-ceiling", 31622400); code != http.StatusCreated {
		t.Errorf("the ceiling itself = %d, want 201", code)
	}
	if code := create("no-floor", 0); code != http.StatusCreated {
		t.Errorf("0 (no floor) = %d, want 201", code)
	}
}
