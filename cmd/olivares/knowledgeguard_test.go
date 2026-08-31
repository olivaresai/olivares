// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// guardFixture opens a store with the governance schema registered (for the
// collection_member edges) and seeds one tenant. It returns the store, the tenant and
// a guard already bound to the store handle.
func guardFixture(t *testing.T) (store.Store, model.TenantID, *governanceRetrievalGuard) {
	t.Helper()
	ctx := context.Background()
	gov := governance.New()
	ss := sourcescope.New()
	register := func(reg store.ExtensionRegistry) error {
		if err := gov.RegisterSchema(reg); err != nil {
			return err
		}
		return ss.RegisterSchema(reg)
	}
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, register)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	g := newGovernanceRetrievalGuard(quietLog())
	data := api.NewModuleData(st)
	ss.UseData(data)
	g.useData(data)
	g.useGuardPostureResolver(ss.Resolver())
	return st, tenant, g
}

// seedIdentity creates an identity with the given external id + metadata and returns
// its internal id.
func seedIdentity(t *testing.T, st store.Store, tenant model.TenantID, externalID string, meta map[string]any) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ident, e := sc.Identities().Create(context.Background(), model.Identity{
			Name: externalID, Kind: "agent_nhi", ExternalID: externalID, Provider: "test", Metadata: meta,
		})
		id = ident.ID
		return e
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return id
}

func seedAgent(t *testing.T, st store.Store, tenant model.TenantID, externalID string, identityID model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, e := sc.Agents().Create(context.Background(), model.Agent{
			Name: externalID, Kind: "claude-code", ExternalID: externalID, Status: model.StatusActive, IdentityID: identityID,
		})
		return e
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func seedEdge(t *testing.T, st store.Store, tenant model.TenantID, memberRef, collectionRef, memberKind string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(govMemberKind)
		if e != nil {
			return e
		}
		_, e = repo.Create(context.Background(), model.Record{
			"source": "test", govColCollectionRef: collectionRef, govColMemberRef: memberRef, "member_kind": memberKind,
		})
		return e
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

func seedGuardPublicOnly(t *testing.T, st store.Store, tenant model.TenantID, kbName string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("sourcescope.guard_posture"))
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"source_type": "knowledge", "source_ref": kbName,
			"guard_profile": sourcescope.GuardProfilePublicOnly,
			"reason":        "approved public-only downgrade",
			"updated_by":    "user:approver",
		})
		return err
	}); err != nil {
		t.Fatalf("seed guard posture: %v", err)
	}
}

func sortedGroups(g []string) []string { sort.Strings(g); return g }

// TestGuardResolvesRealGrants is the core proof: the guard maps an agent to its
// bound identity and returns REAL grants — transitive group memberships (for chunk
// ACL), clearance and region — not just "public".
func TestGuardResolvesRealGrants(t *testing.T) {
	st, tenant, g := guardFixture(t)
	idID := seedIdentity(t, st, tenant, "id-1", map[string]any{"attr_clearance": "confidential", "attr_region": "eu"})
	seedAgent(t, st, tenant, "agent-1", idID)
	// id-1 ∈ group:eng ∈ group:all (nested → transitive).
	seedEdge(t, st, tenant, "id-1", "group:eng", "identity")
	seedEdge(t, st, tenant, "group:eng", "group:all", "collection")

	grants, err := g.Resolve(context.Background(), tenant, "user:x", "agent-1", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Allowed {
		t.Fatal("a resolved, enabled identity must be allowed")
	}
	if got := sortedGroups(grants.Groups); len(got) != 2 || got[0] != "group:all" || got[1] != "group:eng" {
		t.Fatalf("groups = %v, want transitive [group:all group:eng]", grants.Groups)
	}
	if grants.Clearance != "confidential" {
		t.Fatalf("clearance = %q, want confidential", grants.Clearance)
	}
	if grants.Region != "eu" {
		t.Fatalf("region = %q, want eu", grants.Region)
	}
}

// TestGuardErrorDeniesFailClosed proves a store/resolution error is a DENY (the module
// then refuses the retrieval), never a degraded allow.
func TestGuardErrorDeniesFailClosed(t *testing.T) {
	g := newGovernanceRetrievalGuard(quietLog())
	g.useData(errData{})
	_, err := g.Resolve(context.Background(), model.TenantID("t"), "user:x", "agent-1", "kb")
	if err == nil {
		t.Fatal("a store error must return an error (deny-closed), never grants")
	}
}

func TestGuardPublicOnlyPostureDowngradesKB(t *testing.T) {
	st, tenant, g := guardFixture(t)
	idID := seedIdentity(t, st, tenant, "id-1", map[string]any{"attr_clearance": "secret", "attr_region": "eu"})
	seedAgent(t, st, tenant, "agent-1", idID)
	seedEdge(t, st, tenant, "id-1", "group:eng", "identity")
	seedGuardPublicOnly(t, st, tenant, "kb")

	grants, err := g.Resolve(context.Background(), tenant, "user:x", "agent-1", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Allowed || grants.Clearance != "public" || grants.Region != "" || len(grants.Groups) != 0 {
		t.Fatalf("public_only guard posture must downgrade grants to public-only, got %+v", grants)
	}
	if grants.Reason == "" || !strings.Contains(grants.Reason, "public_only") {
		t.Fatalf("public_only downgrade reason must be visible, got %q", grants.Reason)
	}
}

func TestGuardPostureReadErrorDeniesFailClosed(t *testing.T) {
	_, tenant, g := guardFixture(t)
	g.useGuardPostureResolver(sourcescope.New().Resolver()) // unbound resolver: posture read fails
	_, err := g.Resolve(context.Background(), tenant, "user:x", "agent-1", "kb")
	if err == nil {
		t.Fatal("an unreadable guard posture must deny closed")
	}
}

// TestGuardNoAgentRefIsPublicOnly: governed retrieval is agent-centric; without an
// agent subject only public, unrestricted content is retrievable (no over-grant).
func TestGuardNoAgentRefIsPublicOnly(t *testing.T) {
	_, tenant, g := guardFixture(t)
	grants, err := g.Resolve(context.Background(), tenant, "user:x", "", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Allowed || grants.Clearance != "public" || len(grants.Groups) != 0 || grants.Region != "" {
		t.Fatalf("no agent_ref must yield public-only grants, got %+v", grants)
	}
}

// TestGuardUnboundAgentIsPublicOnly: an agent with no bound identity cannot be
// authorized beyond public content.
func TestGuardUnboundAgentIsPublicOnly(t *testing.T) {
	st, tenant, g := guardFixture(t)
	seedAgent(t, st, tenant, "agent-x", model.ID("")) // zero IdentityID
	grants, err := g.Resolve(context.Background(), tenant, "user:x", "agent-x", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Allowed || grants.Clearance != "public" || len(grants.Groups) != 0 {
		t.Fatalf("unbound agent must be public-only, got %+v", grants)
	}
}

// TestGuardDisabledIdentityDenied: a disabled identity is denied at the KB level.
func TestGuardDisabledIdentityDenied(t *testing.T) {
	st, tenant, g := guardFixture(t)
	idID := seedIdentity(t, st, tenant, "id-d", map[string]any{"disabled": true, "attr_clearance": "secret"})
	seedAgent(t, st, tenant, "agent-d", idID)
	grants, err := g.Resolve(context.Background(), tenant, "user:x", "agent-d", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if grants.Allowed {
		t.Fatalf("a disabled identity must be denied, got %+v", grants)
	}
}

// TestGuardNoClearanceFailsClosedToPublic: an identity without a clearance attribute
// gets empty clearance (the module normalizes "" to public) — so a confidential chunk
// is never retrievable. Region likewise defaults to none (a region-locked KB denies).
func TestGuardNoClearanceFailsClosedToPublic(t *testing.T) {
	st, tenant, g := guardFixture(t)
	idID := seedIdentity(t, st, tenant, "id-bare", map[string]any{}) // no clearance/region
	seedAgent(t, st, tenant, "agent-bare", idID)
	grants, err := g.Resolve(context.Background(), tenant, "user:x", "agent-bare", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if grants.Clearance != "" || grants.Region != "" {
		t.Fatalf("a clearance-less identity must fail closed (empty clearance/region ⇒ public/none), got %+v", grants)
	}
}

// TestGuardMultiTenantIsolation: the agent seeded in tenant A is invisible from tenant
// B — a cross-tenant retrieval resolves to public-only (no leakage of A's grants).
func TestGuardMultiTenantIsolation(t *testing.T) {
	st, tenantA, g := guardFixture(t)
	idID := seedIdentity(t, st, tenantA, "id-a", map[string]any{"attr_clearance": "secret"})
	seedAgent(t, st, tenantA, "agent-a", idID)
	seedEdge(t, st, tenantA, "id-a", "group:secret", "identity")

	var tenantB model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: "Globex", Slug: "globex", Status: model.StatusActive})
		tenantB = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	grants, err := g.Resolve(context.Background(), tenantB, "user:x", "agent-a", "kb")
	if err != nil {
		t.Fatal(err)
	}
	if grants.Clearance == "secret" || len(grants.Groups) != 0 {
		t.Fatalf("tenant B must not see tenant A's grants, got %+v", grants)
	}
}

// errData is an api.ModuleData whose View/Mutate always fail — to prove fail-closed.
type errData struct{}

func (errData) View(_ context.Context, _ model.TenantID, _ func(store.Scope) error) error {
	return errors.New("store down")
}
func (errData) Mutate(_ context.Context, _ model.TenantID, _ func(store.Scope) error) error {
	return errors.New("store down")
}
