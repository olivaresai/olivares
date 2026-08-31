// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// --- local helpers ---------------------------------------------------------------

func (h *harness) authzMember(admin, email, pass, role string, tenant model.TenantID) (string, string) {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); g.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	return uid, lr.body["token"].(string)
}

func (h *harness) mkAgent(token string, tenant model.TenantID, name string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/agents", token, map[string]any{"name": name, "kind": "claude-code"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create agent %s = %d %s", name, r.code, r.raw)
	}
	return r.body["id"].(string)
}

func resultIDs(r resp) []string {
	var ids []string
	rs, _ := r.body["results"].([]any)
	for _, it := range rs {
		if m, ok := it.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func resultNames(r resp) []string {
	var names []string
	rs, _ := r.body["results"].([]any)
	for _, it := range rs {
		if m, ok := it.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// --- discovery -------------------------------------------------------------------

// The PDP metadata document is public and works before setup (RootEnginePaths).
func TestAuthZenDiscovery(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/.well-known/authzen-configuration", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("discovery (pre-setup, no auth) = %d %s", r.code, r.raw)
	}
	ev, _ := r.body["access_evaluation_endpoint"].(string)
	if !strings.HasSuffix(ev, "/access/v1/evaluation") {
		t.Errorf("access_evaluation_endpoint = %q", ev)
	}
	if pdp, _ := r.body["policy_decision_point"].(string); pdp == "" {
		t.Error("policy_decision_point is required and must be non-empty")
	}
	if ss, _ := r.body["search_subject_endpoint"].(string); !strings.HasSuffix(ss, "/access/v1/search/subject") {
		t.Errorf("search_subject_endpoint = %q", ss)
	}
}

// --- evaluation ------------------------------------------------------------------

func TestAuthZenEvaluation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	viewerID, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)

	eval := func(subjID, action string, props map[string]any) resp {
		subj := map[string]any{"type": "user", "id": subjID}
		if props != nil {
			subj["properties"] = props
		}
		return h.do("POST", "/access/v1/evaluation", admin, map[string]any{
			"subject": subj, "action": map[string]any{"name": action}, "resource": map[string]any{"type": "agent"},
		}, tenantHdr(tenant))
	}

	if r := eval(editorID, "agent:write", nil); r.code != http.StatusOK || r.body["decision"] != true {
		t.Errorf("editor agent:write = %d decision=%v, want 200/true", r.code, r.body["decision"])
	}
	if r := eval(viewerID, "agent:write", nil); r.code != http.StatusOK || r.body["decision"] != false {
		t.Errorf("viewer agent:write = %d decision=%v, want 200/false", r.code, r.body["decision"])
	}
	if r := eval(viewerID, "agent:read", nil); r.body["decision"] != true {
		t.Errorf("viewer agent:read decision = %v, want true", r.body["decision"])
	}

	// HONESTY: caller-asserted subject properties (a forged role) are IGNORED — the
	// decision is computed from the stored identity, so the viewer still cannot write.
	if r := eval(viewerID, "agent:write", map[string]any{"role": "owner"}); r.body["decision"] != false {
		t.Errorf("forged subject.role must be ignored; decision = %v, want false", r.body["decision"])
	}

	// An unknown subject denies (deny-closed), not 500.
	if r := eval(model.NewID().String(), "agent:read", nil); r.code != http.StatusOK || r.body["decision"] != false {
		t.Errorf("unknown subject = %d decision=%v, want 200/false", r.code, r.body["decision"])
	}
}

// The evaluation/search surface is gated on authz:read (editor and up, never viewer)
// and requires authentication.
func TestAuthZenEvaluationGating(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewerTok := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	_, editorTok := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)

	body := map[string]any{"subject": map[string]any{"type": "user", "id": "x"}, "action": map[string]any{"name": "agent:read"}, "resource": map[string]any{"type": "agent"}}

	if r := h.do("POST", "/access/v1/evaluation", viewerTok, body, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer caller = %d, want 403 (authz:read is editor+)", r.code)
	}
	if r := h.do("POST", "/access/v1/evaluation", editorTok, body, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("editor caller = %d, want 200 (holds authz:read)", r.code)
	}
	if r := h.do("POST", "/access/v1/evaluation", "", body, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous caller = %d, want 401", r.code)
	}
}

// --- batch -----------------------------------------------------------------------

func TestAuthZenEvaluationsBatch(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	viewerID, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)

	// execute_all: defaults (action+resource) shared, per-item subject overrides;
	// results are in request order [editor=allow, viewer=deny].
	r := h.do("POST", "/access/v1/evaluations", admin, map[string]any{
		"action": map[string]any{"name": "agent:write"}, "resource": map[string]any{"type": "agent"},
		"evaluations": []any{
			map[string]any{"subject": map[string]any{"type": "user", "id": editorID}},
			map[string]any{"subject": map[string]any{"type": "user", "id": viewerID}},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("batch = %d %s", r.code, r.raw)
	}
	evs, _ := r.body["evaluations"].([]any)
	if len(evs) != 2 {
		t.Fatalf("execute_all returned %d evaluations, want 2", len(evs))
	}
	if d0 := evs[0].(map[string]any)["decision"]; d0 != true {
		t.Errorf("[0] editor decision = %v, want true", d0)
	}
	if d1 := evs[1].(map[string]any)["decision"]; d1 != false {
		t.Errorf("[1] viewer decision = %v, want false", d1)
	}

	// deny_on_first_deny short-circuits: [viewer(deny), editor] → 1 result.
	r = h.do("POST", "/access/v1/evaluations", admin, map[string]any{
		"action": map[string]any{"name": "agent:write"}, "resource": map[string]any{"type": "agent"},
		"options": map[string]any{"evaluations_semantic": "deny_on_first_deny"},
		"evaluations": []any{
			map[string]any{"subject": map[string]any{"type": "user", "id": viewerID}},
			map[string]any{"subject": map[string]any{"type": "user", "id": editorID}},
		},
	}, tenantHdr(tenant))
	evs, _ = r.body["evaluations"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["decision"] != false {
		t.Errorf("deny_on_first_deny = %v, want a single false", evs)
	}

	// An empty evaluations array is a 400.
	if r := h.do("POST", "/access/v1/evaluations", admin, map[string]any{"evaluations": []any{}}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("empty batch = %d, want 400", r.code)
	}
}

// --- search: subject ("who can do A on R?") --------------------------------------

func TestAuthZenSearchSubject(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	viewerID, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)

	r := h.do("POST", "/access/v1/search/subject", admin, map[string]any{
		"subject":  map[string]any{"type": "user"},
		"action":   map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent"},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search subject = %d %s", r.code, r.raw)
	}
	ids := resultIDs(r)
	if !has(ids, editorID) {
		t.Errorf("editor (can agent:write) missing from results %v", ids)
	}
	if has(ids, viewerID) {
		t.Errorf("viewer (cannot agent:write) must NOT be in results %v", ids)
	}
	// The population is surfaced (no implicit completeness claim).
	if ctx, _ := r.body["context"].(map[string]any); ctx["population"] == nil {
		t.Error("response context must surface the enumerated population")
	}
}

// --- search: resource ("what can S do A on?") ------------------------------------

func TestAuthZenSearchResource(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	viewerID, _ := h.authzMember(admin, "vw@acme.io", "viewerpass1", auth.RoleViewer, tenant)
	a1 := h.mkAgent(admin, tenant, "bot-1")
	a2 := h.mkAgent(admin, tenant, "bot-2")

	// Editor can agent:write both agents (tenant-wide RBAC).
	r := h.do("POST", "/access/v1/search/resource", admin, map[string]any{
		"subject":  map[string]any{"type": "user", "id": editorID},
		"action":   map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent"},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search resource = %d %s", r.code, r.raw)
	}
	ids := resultIDs(r)
	if !has(ids, a1) || !has(ids, a2) {
		t.Errorf("editor should reach both agents; results %v (want %s,%s)", ids, a1, a2)
	}

	// Viewer can write none.
	r = h.do("POST", "/access/v1/search/resource", admin, map[string]any{
		"subject":  map[string]any{"type": "user", "id": viewerID},
		"action":   map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent"},
	}, tenantHdr(tenant))
	if ids := resultIDs(r); len(ids) != 0 {
		t.Errorf("viewer agent:write resource search = %v, want empty", ids)
	}
}

// --- search: action ("what can S do on R?") --------------------------------------

func TestAuthZenSearchAction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	a1 := h.mkAgent(admin, tenant, "bot-1")

	r := h.do("POST", "/access/v1/search/action", admin, map[string]any{
		"subject":  map[string]any{"type": "user", "id": editorID},
		"resource": map[string]any{"type": "agent", "id": a1},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search action = %d %s", r.code, r.raw)
	}
	names := resultNames(r)
	if !has(names, "agent:read") || !has(names, "agent:write") {
		t.Errorf("editor should have agent:read+write on the agent; got %v", names)
	}
	if has(names, "agent:admin") {
		t.Errorf("editor should NOT have agent:admin; got %v", names)
	}
}

// --- exposure configuration ------------------------------------------------------

func evalBody(subjID, action string) map[string]any {
	return map[string]any{
		"subject": map[string]any{"type": "user", "id": subjID}, "action": map[string]any{"name": action},
		"resource": map[string]any{"type": "agent"},
	}
}

// The whole surface can be disabled (routes + discovery answer 404, as if unmounted).
func TestAuthZenExposureDisabled(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &api.AuthZenConfig{Disabled: true} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if r := h.do("POST", "/access/v1/evaluation", admin, evalBody("x", "agent:read"), tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("disabled evaluation = %d, want 404", r.code)
	}
	if r := h.do("GET", "/.well-known/authzen-configuration", "", nil, nil); r.code != http.StatusNotFound {
		t.Errorf("disabled discovery = %d, want 404", r.code)
	}
}

// Only the searches can be disabled; evaluation stays on and discovery stops
// advertising the search endpoints.
func TestAuthZenExposureSearchDisabled(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &api.AuthZenConfig{SearchDisabled: true} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, _ := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)

	if r := h.do("POST", "/access/v1/evaluation", admin, evalBody(editorID, "agent:read"), tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("evaluation with searches disabled = %d, want 200", r.code)
	}
	if r := h.do("POST", "/access/v1/search/subject", admin, map[string]any{
		"subject": map[string]any{"type": "user"}, "action": map[string]any{"name": "agent:read"}, "resource": map[string]any{"type": "agent"},
	}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("disabled search = %d, want 404", r.code)
	}
	r := h.do("GET", "/.well-known/authzen-configuration", "", nil, nil)
	if _, ok := r.body["search_subject_endpoint"]; ok {
		t.Error("discovery must NOT advertise search endpoints when searches are disabled")
	}
	if _, ok := r.body["access_evaluation_endpoint"]; !ok {
		t.Error("discovery must still advertise the evaluation endpoint")
	}
}

// The surface can be confined to an intra-cluster network (the harness peer is
// 10.0.0.1): an out-of-network CIDR blocks it (403), an in-network one allows it.
func TestAuthZenExposureNetwork(t *testing.T) {
	blocked := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &api.AuthZenConfig{AllowedCIDRs: []string{"192.168.0.0/16"}} })
	admin := blocked.adminLogin()
	tenant := blocked.createOrg(admin, "acme")
	if r := blocked.do("POST", "/access/v1/evaluation", admin, evalBody("x", "agent:read"), tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("out-of-network evaluation = %d, want 403", r.code)
	}

	allowed := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &api.AuthZenConfig{AllowedCIDRs: []string{"10.0.0.0/8"}} })
	admin2 := allowed.adminLogin()
	tenant2 := allowed.createOrg(admin2, "acme")
	editorID, _ := allowed.authzMember(admin2, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant2)
	if r := allowed.do("POST", "/access/v1/evaluation", admin2, evalBody(editorID, "agent:read"), tenantHdr(tenant2)); r.code != http.StatusOK {
		t.Errorf("in-network evaluation = %d, want 200", r.code)
	}
}

// --- pagination ------------------------------------------------------------------

// page.limit bounds candidates scanned; next_token continues until empty.
func TestAuthZenSearchPagination(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Several agents so the candidate set exceeds a tiny page.
	for i := 0; i < 5; i++ {
		h.mkAgent(admin, tenant, "bot")
	}
	_, editorTok := h.authzMember(admin, "ed@acme.io", "editorpass1", auth.RoleEditor, tenant)
	_ = editorTok

	seen := map[string]bool{}
	token := ""
	for pages := 0; pages < 20; pages++ {
		page := map[string]any{"limit": 2}
		if token != "" {
			page["token"] = token
		}
		r := h.do("POST", "/access/v1/search/resource", admin, map[string]any{
			"subject":  map[string]any{"type": "user", "id": "root@x.io"}, // superadmin by email reaches all
			"action":   map[string]any{"name": "agent:read"},
			"resource": map[string]any{"type": "agent"},
			"page":     page,
		}, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("page = %d %s", r.code, r.raw)
		}
		for _, id := range resultIDs(r) {
			seen[id] = true
		}
		pg, _ := r.body["page"].(map[string]any)
		token, _ = pg["next_token"].(string)
		if token == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Errorf("paged through %d distinct agents, want 5", len(seen))
	}
}
