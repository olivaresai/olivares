// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The identity inventory (identities, mirrored groups, agent bindings) is Community.
// Filling it from a directory roster is Business and is not exercised here.

// seedIdentity stores an identity directly, the way the access-map bridge and a bind
// create one, without a directory roster.
func (h *harness) seedInventoryIdentity(tenant model.TenantID, ref, name, kind, principal string) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			ExternalID: ref, Name: name, Kind: kind, Provider: "test",
			Metadata: map[string]any{"principal_type": principal, "attr_email": "must-not-surface@x.io"},
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed identity: %v", err)
	}
}

// TestBindingResolvesAccessMapDrift is the proof that delivers hard
// dependency: binding an agent to the NHI identity its credential presents makes
// access-map's permitted-vs-observed reconciliation cancel the false drift.
func TestBindingResolvesAccessMapDrift(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	am := accessmap.New()
	am.UseData(api.NewModuleData(h.st))
	ctx := context.Background()

	// PERMITTED grant (Vault policy): identity "entity:svc" may read secret/x.
	if _, err := am.Ingest(ctx, tenant.String(), sdkmodel.EdgeObservation{
		OriginKind: "identity", OriginRef: "entity:svc", ResourceKind: "vault.path", ResourceRef: "secret/x",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalPolicy, Confidence: sdkmodel.ConfidenceAttributed,
	}); err != nil {
		t.Fatalf("ingest permitted: %v", err)
	}
	// OBSERVED access by agent A on the same resource (pgAudit-style).
	agentA := h.createAgent(tenant, "agent-a", "agentA")
	if _, err := am.Ingest(ctx, tenant.String(), sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "agentA", ResourceKind: "vault.path", ResourceRef: "secret/x",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalPGAudit, Confidence: sdkmodel.ConfidenceAttributed,
	}); err != nil {
		t.Fatalf("ingest observed: %v", err)
	}

	// Before binding: the access cannot be tied to the grant — a false drift.
	diff, err := am.Diff(ctx, tenant, "user:test", "user", model.Query{})
	if err != nil {
		t.Fatalf("diff before: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 1 {
		t.Fatalf("expected 1 unexpected access before binding, got %d", len(diff.UnexpectedAccesses))
	}

	// Bind agent A to the NHI identity it runs as (unknown type since the bridge
	// created it from a raw ref → allow_unknown).
	br := h.do("POST", govPath+"/agents/"+agentA.ID.String()+"/identity", admin,
		map[string]any{"identity_ref": "entity:svc", "allow_unknown": true}, tenantHdr(tenant))
	if br.code != http.StatusOK {
		t.Fatalf("bind = %d %s", br.code, br.raw)
	}
	if h.getAgent(tenant, agentA.ID).IdentityID.IsZero() {
		t.Fatal("agent should now carry an identity binding")
	}

	// After binding: the access reconciles against the grant — no drift.
	diff, err = am.Diff(ctx, tenant, "user:test", "user", model.Query{})
	if err != nil {
		t.Fatalf("diff after: %v", err)
	}
	if len(diff.UnexpectedAccesses) != 0 {
		t.Fatalf("binding should reconcile the access; still %d unexpected", len(diff.UnexpectedAccesses))
	}
}

func TestBindingSharedIdentityFinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	a1 := h.createAgent(tenant, "a1", "ext-a1")
	a2 := h.createAgent(tenant, "a2", "ext-a2")

	if r := h.do("POST", govPath+"/agents/"+a1.ID.String()+"/identity", admin, map[string]any{"identity_ref": "shared-svc"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["shared"].(bool) {
		t.Fatalf("first bind should not be shared: %d %s", r.code, r.raw)
	}
	r := h.do("POST", govPath+"/agents/"+a2.ID.String()+"/identity", admin, map[string]any{"identity_ref": "shared-svc"}, tenantHdr(tenant))
	if r.code != http.StatusOK || !r.body["shared"].(bool) || r.body["agent_count"].(float64) != 2 {
		t.Fatalf("second bind to the same identity should be shared (count 2): %d %s", r.code, r.raw)
	}
	// Bind two more so the identity has 4 agents — the count must be exact, not
	// capped (and consistent with the /bindings listing).
	a3 := h.createAgent(tenant, "a3", "ext-a3")
	a4 := h.createAgent(tenant, "a4", "ext-a4")
	h.do("POST", govPath+"/agents/"+a3.ID.String()+"/identity", admin, map[string]any{"identity_ref": "shared-svc"}, tenantHdr(tenant))
	r4 := h.do("POST", govPath+"/agents/"+a4.ID.String()+"/identity", admin, map[string]any{"identity_ref": "shared-svc"}, tenantHdr(tenant))
	if r4.body["agent_count"].(float64) != 4 {
		t.Fatalf("agent_count must be the exact 4, not capped: %s", r4.raw)
	}
	bl := h.do("GET", govPath+"/bindings", admin, nil, tenantHdr(tenant))
	for _, it := range items(bl) {
		if b := it.(map[string]any); b["identity_ref"] == "shared-svc" && b["agent_count"].(float64) != 4 {
			t.Fatalf("/bindings agent_count must agree (4): %s", bl.raw)
		}
	}
	found := false
	for _, f := range h.host.findings() {
		if f.Kind == "governance_shared_identity" {
			found = true
			if strings.Contains(f.Title, "@") {
				t.Fatalf("shared-identity finding title must not carry PII: %q", f.Title)
			}
		}
	}
	if !found {
		t.Fatal("a shared-identity finding should have been emitted")
	}
}

func TestRBACTiers(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewer := h.roleUser(admin, tenant, "v@x.io", "viewer")
	_, editor := h.roleUser(admin, tenant, "e@x.io", "editor")
	agent := h.createAgent(tenant, "a", "ext-a")

	// viewer: can read identities, cannot bind (admin-tier) or author policy.
	if r := h.do("GET", govPath+"/identities", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer read identities = %d", r.code)
	}
	if r := h.do("POST", govPath+"/agents/"+agent.ID.String()+"/identity", viewer, map[string]any{"mint": true}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer bind should be 403, got %d", r.code)
	}
	if r := h.do("POST", govPath+"/policies", viewer, map[string]any{"name": "x", "kind": "abac", "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}}}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer author policy should be 403, got %d", r.code)
	}
	// editor: can request an approval, cannot decide (admin-tier).
	cr := h.createApproval(editor, tenant, map[string]any{"action": "deploy"})
	if cr.code != http.StatusCreated {
		t.Fatalf("editor create approval = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)
	if r := h.decide(editor, tenant, id, "approve"); r.code != http.StatusForbidden {
		t.Fatalf("editor decide should be 403, got %d", r.code)
	}
}

func TestSelfAuditAttributesToRealPrincipalNeverPII(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.createAgent(tenant, "a", "ext-a")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")

	h.do("POST", govPath+"/agents/"+agent.ID.String()+"/identity", admin, map[string]any{"mint": true}, tenantHdr(tenant))
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	h.decide(admin, tenant, id, "approve")
	// Author the policy last: a deny-write rule would otherwise block the editor's
	// approval create above (which is exactly what the ABAC engine should do).
	h.authorPolicy(admin, tenant, "p", map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}})

	actions := h.auditActions(tenant)
	for _, want := range []string{"governance.binding.bind", "governance.policy.create", "governance.approval.create", "governance.approval.decision"} {
		if !contains(actions, want) {
			t.Fatalf("audit chain missing %q; have %v", want, actions)
		}
	}
	// No audit actor is ever an email (the PII carve-out, docs/SECURITY-HARDENING.md).
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if strings.Contains(e.Actor, "@") {
				t.Fatalf("audit actor must never be an email: %q", e.Actor)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBindingRejectsHumanIdentity(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedInventoryIdentity(tenant, "cn=jane", "Jane", "user", "human")
	agent := h.createAgent(tenant, "a", "ext-a")
	// "cn=jane" is a human identity — binding an agent to it is a category error.
	if r := h.do("POST", govPath+"/agents/"+agent.ID.String()+"/identity", admin, map[string]any{"identity_ref": "cn=jane"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("binding to a human identity must be 400, got %d %s", r.code, r.raw)
	}
}

func TestIdentityInventoryListsStoredIdentitiesWithoutPII(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedInventoryIdentity(tenant, "cn=svc,ou=apps", "svc", "service_account", "nhi")
	r := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("identities = %d %s", r.code, r.raw)
	}
	got := items(r)
	if len(got) != 1 {
		t.Fatalf("identities = %s", r.raw)
	}
	row := got[0].(map[string]any)
	if row["ref"] != "cn=svc,ou=apps" || row["kind"] != "service_account" || row["principal_type"] != "nhi" || row["disabled"] != nil {
		t.Fatalf("identity row = %v", row)
	}
	if strings.Contains(r.raw, "@x.io") {
		t.Fatalf("identity listing leaked an email: %s", r.raw)
	}
	if f := h.do("GET", govPath+"/identities?kind=user", admin, nil, tenantHdr(tenant)); len(items(f)) != 0 {
		t.Fatalf("kind filter must narrow the list: %s", f.raw)
	}
	if g := h.do("GET", govPath+"/groups", admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("groups = %d %s", g.code, g.raw)
	}
	if m := h.do("GET", govPath+"/groups/grp:none/members?transitive=true", admin, nil, tenantHdr(tenant)); m.code != http.StatusOK || m.body["not_found"] != true {
		t.Fatalf("members of an unknown group = %d %s, want 200 with not_found", m.code, m.raw)
	}
}

// Reading who exists and who is bound to whom is recon: each list read self-audits.
func TestIdentityInventoryReadsSelfAudit(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedInventoryIdentity(tenant, "cn=svc,ou=apps", "svc", "service_account", "nhi")
	for _, path := range []string{"/identities", "/bindings"} {
		if r := h.do("GET", govPath+path, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
		}
	}
	got := map[string]bool{}
	for _, a := range h.auditActions(tenant) {
		got[a] = true
	}
	for _, want := range []string{"governance.identity.list", "governance.binding.list"} {
		if !got[want] {
			t.Errorf("audit chain lacks %q after the read; has %v", want, h.auditActions(tenant))
		}
	}
}

// Unbinding clears Agent.IdentityID, is audited, and a second unbind is idempotent.
func TestUnbindClearsTheIdentityAndAudits(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedInventoryIdentity(tenant, "cn=svc,ou=apps", "svc", "service_account", "nhi")
	agent := h.createAgent(tenant, "a", "ext-a")
	path := govPath + "/agents/" + agent.ID.String() + "/identity"
	if r := h.do("POST", path, admin, map[string]any{"identity_ref": "cn=svc,ou=apps"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("bind = %d %s", r.code, r.raw)
	}
	if h.getAgent(tenant, agent.ID).IdentityID.IsZero() {
		t.Fatal("bind did not set Agent.IdentityID")
	}
	for i := 0; i < 2; i++ {
		if r := h.do("DELETE", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
			t.Fatalf("unbind #%d = %d %s, want 204", i+1, r.code, r.raw)
		}
	}
	if !h.getAgent(tenant, agent.ID).IdentityID.IsZero() {
		t.Fatal("unbind did not clear Agent.IdentityID")
	}
	unbinds := 0
	for _, a := range h.auditActions(tenant) {
		if a == "governance.binding.unbind" {
			unbinds++
		}
	}
	if unbinds != 1 {
		t.Errorf("unbind audited %d times, want exactly 1 (the second call is a no-op)", unbinds)
	}
}

func TestMultiTenantIdentityInventoryIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "tenant-a")
	tenantB := h.createOrg(admin, "tenant-b")
	h.seedInventoryIdentity(tenantA, "cn=svc,ou=apps", "svc", "service_account", "nhi")
	// Positive control: tenant A does see its own identity, so B's empty list below is isolation.
	if rA := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenantA)); len(items(rA)) != 1 {
		t.Fatalf("tenant A must see its own identity: %s", rA.raw)
	}
	// Tenant B sees none of A's identities.
	r := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenantB))
	if len(items(r)) != 0 {
		t.Fatalf("tenant B must not see tenant A's identities: %s", r.raw)
	}
}

// An account state the source never reported is unknown, not "enabled": the row
// carries disabled=null so a reader cannot take a bridge-created identity for a
// live, enabled account.
func TestIdentityInventoryAccountStateIsTriValued(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ctx := context.Background()
	for ref, meta := range map[string]map[string]any{
		"cn=enabled":  {"principal_type": "nhi", "disabled": false},
		"cn=disabled": {"principal_type": "nhi", "disabled": true},
		"cn=reported": {"principal_type": "nhi"},
	} {
		if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Identities().Create(ctx, model.Identity{ExternalID: ref, Name: ref, Kind: "service_account", Provider: "test", Metadata: meta})
			return err
		}); err != nil {
			t.Fatalf("seed %s: %v", ref, err)
		}
	}
	r := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("identities = %d %s", r.code, r.raw)
	}
	got := map[string]any{}
	for _, it := range items(r) {
		row := it.(map[string]any)
		got[row["ref"].(string)] = row["disabled"]
	}
	if got["cn=enabled"] != false || got["cn=disabled"] != true {
		t.Errorf("reported states changed: enabled=%v disabled=%v, want false and true", got["cn=enabled"], got["cn=disabled"])
	}
	if v, present := got["cn=reported"]; !present || v != nil {
		t.Errorf("an identity whose source never reported the state has disabled=%v (present=%v), want null", v, present)
	}
}

// linkMembers records membership edges {collection, member, member kind}.
func (h *harness) linkMembers(tenant model.TenantID, links [][3]string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.collection_member")
		if err != nil {
			return err
		}
		groups, err := sc.Ext("governance.collection")
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, l := range links {
			if !seen[l[0]] {
				if _, err := groups.Create(ctx, model.Record{"source": "dir", "col_ref": l[0], "col_kind": "group"}); err != nil {
					return err
				}
				seen[l[0]] = true
			}
			if _, err := repo.Create(ctx, model.Record{"source": "dir", "collection_ref": l[0], "member_ref": l[1], "member_kind": l[2]}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		h.t.Fatalf("link members: %v", err)
	}
}

// seedChain nests collections grp:0 -> grp:1 -> ... -> grp:<depth>, with one
// identity in the deepest one and another directly in the root.
func (h *harness) seedChain(tenant model.TenantID, depth int) {
	h.t.Helper()
	name := func(i int) string { return "grp:" + strconv.Itoa(i) }
	links := [][3]string{{name(0), "user:near", "identity"}}
	for i := 0; i < depth; i++ {
		links = append(links, [3]string{name(i), name(i + 1), "collection"})
	}
	links = append(links, [3]string{name(depth), "user:deep", "identity"})
	h.linkMembers(tenant, links)
}

// The transitive walk is bounded (cyclic directories). Cutting a branch at the
// bound without saying so lists a partial membership as the whole one.
func TestGroupMembersTransitiveDepthBoundIsMarked(t *testing.T) {
	cases := []struct {
		name          string
		depth         int
		wantDeep      bool
		wantTruncated bool
	}{
		{"shallow nesting is complete", 3, true, false},
		{"the deepest expanded level is complete", 31, true, false},
		{"one level past the bound is cut and says so", 32, false, true},
		{"nesting far past the bound is cut and says so", 40, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			h.seedChain(tenant, tc.depth)
			r := h.do("GET", govPath+"/groups/grp:0/members?transitive=true", admin, nil, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("members = %d %s", r.code, r.raw)
			}
			refs := map[string]bool{}
			for _, it := range items(r) {
				refs[it.(map[string]any)["member_ref"].(string)] = true
			}
			if !refs["user:near"] {
				t.Fatalf("the root's direct identity is missing: %s", r.raw)
			}
			if refs["user:deep"] != tc.wantDeep {
				t.Errorf("deep identity listed = %v, want %v: %s", refs["user:deep"], tc.wantDeep, r.raw)
			}
			if got, _ := r.body["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v: %s", got, tc.wantTruncated, r.raw)
			}
		})
	}
}

// A cycle terminates and is not a cut: nothing was left unexpanded because of
// the depth bound, so nothing is marked.
func TestGroupMembersTransitiveCycleIsNotTruncated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.linkMembers(tenant, [][3]string{
		{"grp:a", "grp:b", "collection"},
		{"grp:b", "grp:a", "collection"},
		{"grp:b", "user:in-cycle", "identity"},
	})
	r := h.do("GET", govPath+"/groups/grp:a/members?transitive=true", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("members = %d %s", r.code, r.raw)
	}
	if got := items(r); len(got) != 1 || got[0].(map[string]any)["member_ref"] != "user:in-cycle" {
		t.Fatalf("members = %s, want user:in-cycle once", r.raw)
	}
	if got, _ := r.body["truncated"].(bool); got {
		t.Errorf("a cycle reported truncated: %s", r.raw)
	}
}

// Only the transitive walk is bounded; a direct listing never carries the marker.
func TestGroupMembersDirectListingIsNeverTruncated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedChain(tenant, 40)
	r := h.do("GET", govPath+"/groups/grp:0/members", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("members = %d %s", r.code, r.raw)
	}
	if _, present := r.body["truncated"]; present {
		t.Errorf("a direct listing carries truncated: %s", r.raw)
	}
}
