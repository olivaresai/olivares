// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
	"github.com/olivaresai/olivares/modules/governance"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sampleGraph is a roster with an NHI service account (carrying PII attributes
// that must be dropped), a human user, a group and a membership.
func sampleGraph() identitysource.Graph {
	return identitysource.Graph{
		Source: identitysource.SourceLDAP,
		Identities: []identitysource.Identity{
			{Ref: "cn=svc,ou=apps", Type: identitysource.PrincipalNHI, Kind: "service_account", DisplayName: "svc",
				Source: identitysource.SourceLDAP, Attributes: map[string]string{"email": "svc@x.io", "upn": "svc@corp", "ou": "apps", "clearance": "confidential", "region": "eu"}},
			{Ref: "cn=jane", Type: identitysource.PrincipalHuman, Kind: "user", DisplayName: "Jane",
				Source: identitysource.SourceLDAP, Attributes: map[string]string{"email": "jane@x.io"}},
		},
		Collections: []identitysource.Collection{
			{Ref: "grp:eng", Kind: identitysource.KindGroup, DisplayName: "Engineering", Source: identitysource.SourceLDAP},
		},
		Memberships: []identitysource.Membership{
			{MemberRef: "cn=svc,ou=apps", MemberKind: identitysource.MemberIdentity, CollectionRef: "grp:eng", Source: identitysource.SourceLDAP},
		},
	}
}

func (h *harness) identityByExternalID(tenant model.TenantID, ref string) (model.Identity, bool) {
	h.t.Helper()
	var out model.Identity
	var found bool
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: ref}}, Limit: 5})
		if err != nil {
			return err
		}
		if len(list) > 0 {
			out, found = list[0], true
		}
		return nil
	}); err != nil {
		h.t.Fatalf("identity lookup: %v", err)
	}
	return out, found
}

func TestRosterSyncReconcilesAndDropsPII(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{{Provider: &fakeProvider{graph: sampleGraph()}, TenantRef: tenant.String()}})

	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync = %d %s", r.code, r.raw)
	}
	if r.body["identities"].(float64) != 2 || r.body["collections"].(float64) != 1 || r.body["memberships"].(float64) != 1 {
		t.Fatalf("sync report wrong: %s", r.raw)
	}
	// The NHI identity is reconciled with its classification, but its PII
	// attributes (email, upn) are dropped from metadata; only the allow-listed
	// non-PII (ou) survives.
	svc, ok := h.identityByExternalID(tenant, "cn=svc,ou=apps")
	if !ok {
		t.Fatal("service identity not reconciled")
	}
	if svc.Metadata["principal_type"] != "nhi" {
		t.Fatalf("principal_type = %v, want nhi", svc.Metadata["principal_type"])
	}
	for k := range svc.Metadata {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "email") || strings.Contains(lk, "upn") || strings.Contains(lk, "mail") {
			t.Fatalf("PII attribute leaked into identity metadata: %q", k)
		}
	}
	if svc.Metadata["attr_ou"] != "apps" {
		t.Fatalf("allow-listed attribute ou should survive, got %v", svc.Metadata["attr_ou"])
	}
	// The non-PII AUTHORIZATION attributes (clearance, region) survive too — they
	// drive governed retrieval (knowledge.RetrievalGuard reads attr_clearance/
	// attr_region to filter by classification and enforce residency).
	if svc.Metadata["attr_clearance"] != "confidential" {
		t.Fatalf("allow-listed attribute clearance should survive, got %v", svc.Metadata["attr_clearance"])
	}
	if svc.Metadata["attr_region"] != "eu" {
		t.Fatalf("allow-listed attribute region should survive, got %v", svc.Metadata["attr_region"])
	}
	// The list endpoint never exposes an email either.
	lr := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenant))
	if strings.Contains(lr.raw, "@x.io") {
		t.Fatalf("identity listing leaked an email: %s", lr.raw)
	}
	// Transitive membership resolves the service account through the group.
	mr := h.do("GET", govPath+"/groups/grp:eng/members?transitive=true", admin, nil, tenantHdr(tenant))
	if len(items(mr)) != 1 {
		t.Fatalf("group members = %s", mr.raw)
	}
}

func TestRosterUpgradesBridgeIdentityWithoutDuplicating(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Simulate the access-map bridge having already created the credential identity
	// from a raw audit reference (kind "credential", no metadata).
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, e := sc.Identities().Create(context.Background(), model.Identity{Name: "entity:svc", Kind: "credential", ExternalID: "entity:svc"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	graph := identitysource.Graph{Source: identitysource.SourceVault, Identities: []identitysource.Identity{
		{Ref: "entity:svc", Type: identitysource.PrincipalNHI, Kind: "vault_entity", DisplayName: "svc", Source: identitysource.SourceVault},
	}}
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatal(err)
	}
	// A second sync must remain idempotent (no duplicate row).
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatal(err)
	}
	var n int
	var got model.Identity
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		list, _, err := sc.Identities().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: "entity:svc"}}, Limit: 10})
		n, got = len(list), firstOr(list)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one identity row for entity:svc, got %d", n)
	}
	if got.Kind != "vault_entity" || got.Metadata["principal_type"] != "nhi" {
		t.Fatalf("the bridge row should be upgraded in place, got kind=%q meta=%v", got.Kind, got.Metadata)
	}
}

func firstOr(list []model.Identity) model.Identity {
	if len(list) == 0 {
		return model.Identity{}
	}
	return list[0]
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

func TestBindingRejectsHumanIdentity(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{{Provider: &fakeProvider{graph: sampleGraph()}, TenantRef: tenant.String()}})
	if r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("sync = %d %s", r.code, r.raw)
	}
	agent := h.createAgent(tenant, "a", "ext-a")
	// "cn=jane" is a human identity — binding an agent to it is a category error.
	if r := h.do("POST", govPath+"/agents/"+agent.ID.String()+"/identity", admin, map[string]any{"identity_ref": "cn=jane"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("binding to a human identity must be 400, got %d %s", r.code, r.raw)
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

func TestMultiTenantRosterIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "tenant-a")
	tenantB := h.createOrg(admin, "tenant-b")
	if _, err := h.gov.SyncRoster(context.Background(), tenantA, sampleGraph()); err != nil {
		t.Fatal(err)
	}
	// Tenant B sees none of A's identities.
	r := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenantB))
	if len(items(r)) != 0 {
		t.Fatalf("tenant B must not see tenant A's roster: %s", r.raw)
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
