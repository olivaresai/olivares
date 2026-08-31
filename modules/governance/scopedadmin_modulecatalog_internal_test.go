// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// a module permission must survive the WHOLE chain: validation → effective perms →
// Cedar projection → delegation ceiling. Testing only the first link would have passed
// while the feature stayed inert, because the ceiling reads a different catalog view
// (auth.RoleResourcePerms) than the authoring surface (isCatalogPerm).

// registerS584Perms installs a small module catalog for the test and restores the empty
// one afterwards. compliance:* stands for an ordinary module surface; adoption:developer
// is a PRIVILEGED read (editor and up, never viewer) and must keep behaving as one.
func registerS584Perms(t *testing.T) {
	t.Helper()
	auth.ResetModuleCatalog()
	err := auth.RegisterModulePermissions([]auth.Permission{
		"compliance:risk:read", "compliance:risk:write", "compliance:risk:admin",
		"models:keys:read", "models:keys:write",
		"adoption:developer:read",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(auth.ResetModuleCatalog)
}

// TestModulePermsAreUndelegableWithoutRegistration is the BEFORE state, kept as a
// regression: an engine with no registered module keeps rejecting module permissions, so
// the widening can only ever come from a module declaring one.
func TestModulePermsAreUndelegableWithoutRegistration(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	for _, p := range []string{"compliance:risk:read", "models:keys:write", "voice:session:admin"} {
		if isCatalogPerm(p) {
			t.Errorf("%q must not be a catalog perm with an empty registry", p)
		}
		if _, err := validatePerms([]string{p}); err == nil {
			t.Errorf("validatePerms(%q) must reject an unregistered module permission", p)
		}
	}
}

func TestValidatePermsAcceptsRegisteredModulePerms(t *testing.T) {
	registerS584Perms(t)

	got, err := validatePerms([]string{"compliance:risk:read", "models:keys:write", "agent:read"})
	if err != nil {
		t.Fatalf("validatePerms rejected a registered module permission: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("validatePerms returned %v, want the 3 inputs deduplicated and sorted", got)
	}
	// A permission whose KIND is registered but whose exact permission is not stays out.
	if _, err := validatePerms([]string{"models:keys:admin"}); err == nil {
		t.Error("models:keys:admin was never declared: validatePerms must reject it by whole permission, not by kind")
	}
}

// TestCustomRoleSubtractsAModulePermission is the operator story P-4 exists for: a role
// that is an editor over a module surface EXCEPT one write.
func TestCustomRoleSubtractsAModulePermission(t *testing.T) {
	registerS584Perms(t)

	roles := map[string]customRole{
		"compliance-editor-no-keys": {
			Name: "compliance-editor-no-keys",
			Perms: []string{
				"compliance:risk:read", "compliance:risk:write",
				"models:keys:read", // read the key REFERENCES...
				// ...and deliberately NOT models:keys:write.
			},
		},
	}
	eff := effectivePermsOf("compliance-editor-no-keys", true, "", roles, map[string]permGroup{})
	for _, want := range []string{"compliance:risk:read", "compliance:risk:write", "models:keys:read"} {
		if !eff[want] {
			t.Errorf("custom role must confer %q, got %v", want, eff)
		}
	}
	if eff["models:keys:write"] {
		t.Error("the whole point of the role is that models:keys:write is NOT in it")
	}
	// A built-in editor DOES hold models:keys:write — so the custom role is a genuine
	// restriction, not a relabelling of the same authority.
	if !auth.RoleGrants(auth.RoleEditor, "models:keys:write") {
		t.Fatal("fixture broken: a built-in editor is supposed to hold models:keys:write")
	}
}

// TestModulePermsProjectToCedarAndCompile: the grant must reach the engine as an action,
// not merely validate at the authoring surface.
func TestModulePermsProjectToCedarAndCompile(t *testing.T) {
	registerS584Perms(t)

	roles := map[string]customRole{
		"risk-reader": {Name: "risk-reader", Perms: []string{"compliance:risk:read"}},
	}
	grants := []scopedGrant{
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: "risk-reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
	}
	src := projectManagedCedar(grants, roles, map[string]permGroup{})
	if !strings.Contains(src, `Action::"compliance:risk:read"`) {
		t.Errorf("the module permission must be projected as a Cedar action:\n%s", src)
	}
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("projection does not compile: %v\n%s", err, src)
	}
	if src2 := projectManagedCedar(grants, roles, map[string]permGroup{}); src2 != src {
		t.Error("projection is not deterministic")
	}
}

// TestCeilingAdmitsAndClampsModulePerms is the link that would have made the whole unit
// inert: actorDomains builds a tenant admin's domain from auth.RoleResourcePerms, so
// unless THAT carries module permissions, permSubset rejects every module-perm grant and
// the widened authoring surface produces nothing but 403s.
func TestCeilingAdmitsAndClampsModulePerms(t *testing.T) {
	registerS584Perms(t)
	ctx, tenant := context.Background(), model.TenantID("t1")
	admin := auth.ScopedPrincipal("c", "admin", tenant, auth.RoleAdmin)
	groups := map[string]permGroup{}

	roles := map[string]customRole{
		// Within an admin's authority: admin holds module read+write by verb tier.
		"risk-editor": {Name: "risk-editor", Perms: []string{"compliance:risk:read", "compliance:risk:write"}},
		// ABOVE it: compliance:risk:admin is admin-verb, which a tenant ADMIN does hold...
		"risk-admin": {Name: "risk-admin", Perms: []string{"compliance:risk:admin"}},
		// ...and a privileged read a viewer may never receive, used below for the editor case.
		"dev-drilldown": {Name: "dev-drilldown", Perms: []string{"adoption:developer:read"}},
	}
	mk := func(role string) scopedGrant {
		return scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: role, RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}
	}

	if err := canDelegate(ctx, nil, admin, tenant, mk("risk-editor"), nil, roles, groups); err != nil {
		t.Errorf("a tenant admin must be able to delegate module read+write, got %v", err)
	}
	if err := canDelegate(ctx, nil, admin, tenant, mk("risk-admin"), nil, roles, groups); err != nil {
		t.Errorf("a tenant admin holds the module admin verb, so it must be able to delegate it, got %v", err)
	}
	// An EDITOR has no delegation authority at all — the ceiling is not widened by the
	// catalog change, only made reachable for those who already had authority.
	editor := auth.ScopedPrincipal("c", "ed", tenant, auth.RoleEditor)
	if err := canDelegate(ctx, nil, editor, tenant, mk("risk-editor"), nil, roles, groups); err == nil {
		t.Error("an editor has no delegation domain: it must not delegate a module permission")
	} else if _, ok := asCeiling(err); !ok {
		t.Errorf("expected ceilingError, got %T %v", err, err)
	}
}

// TestScopedAdminCannotEscalateBeyondItsOwnModulePerms: a scoped admin whose grant carries
// only compliance perms must not be able to sub-delegate a DIFFERENT module's perms.
func TestScopedAdminCannotEscalateBeyondItsOwnModulePerms(t *testing.T) {
	registerS584Perms(t)
	ctx, tenant := context.Background(), model.TenantID("t1")
	actor := auth.ScopedPrincipal("c", "u", tenant, auth.RoleViewer) // no tenant-wide authority
	groups := map[string]permGroup{}
	roles := map[string]customRole{
		"compliance-admin": {Name: "compliance-admin", Perms: []string{"compliance:risk:read", "compliance:risk:admin"}},
		"keys-reader":      {Name: "keys-reader", Perms: []string{"models:keys:read"}},
		"risk-reader":      {Name: "risk-reader", Perms: []string{"compliance:risk:read"}},
	}
	// The actor holds an admin-capable custom-role grant over compliance only.
	all := []scopedGrant{
		{SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: "compliance-admin", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
	}
	if ds := actorDomains(actor, tenant, all, roles, groups); len(ds) != 1 {
		t.Fatalf("expected exactly one domain from the admin-capable module grant, got %+v", ds)
	}
	within := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: "risk-reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}
	if err := canDelegate(ctx, nil, actor, tenant, within, all, roles, groups); err != nil {
		t.Errorf("a module scoped-admin must sub-delegate a subset of its own perms, got %v", err)
	}
	outside := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: "keys-reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}
	if err := canDelegate(ctx, nil, actor, tenant, outside, all, roles, groups); err == nil {
		t.Error("a compliance scoped-admin must NOT delegate another module's permission")
	} else if _, ok := asCeiling(err); !ok {
		t.Errorf("expected ceilingError, got %T %v", err, err)
	}
}

// TestValidateScopeRefsRejectsAModuleClassOnATreeScope: the module does not persist inert
// grants. A module CLASS bounded to a workspace/agent-group/folder would project a permit
// whose `resource in Workspace::…` condition no module route can satisfy, so it is
// rejected at authoring time with a message that says WHY — not accepted and silently
// dead. At TENANT scope the same class is a real filter and must be accepted.
func TestValidateScopeRefsRejectsAModuleClassOnATreeScope(t *testing.T) {
	registerS584Perms(t)
	ctx := context.Background()

	gov := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, gov.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// Every scope tree gets a REAL, resolvable ref. Without that, validateScopeRefs
	// rejects on the unknown-ref check and never reaches the class check — the test would
	// pass while the class guard was narrowed to workspaces only. A mutant that did
	// exactly that survived the first version of this test.
	var folderID string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, e := sc.Workspaces().Create(ctx, model.Workspace{Name: "Payments", Slug: "payments"}); e != nil {
			return e
		}
		if _, e := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "Bots", Slug: "bots"}); e != nil {
			return e
		}
		res, e := sc.Resources().Create(ctx, model.Resource{Name: "Vault", Kind: "folder", URI: "fs:///vault"})
		if e != nil {
			return e
		}
		folderID = res.ID.String()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	check := func(s scopeSpec) error {
		var out error
		if e := st.View(ctx, tenant, func(sc store.Scope) error {
			out = validateScopeRefs(ctx, sc, s)
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		return out
	}

	// A TREE kind on a workspace scope is unchanged: accepted.
	if err := check(scopeSpec{Tree: scopeWorkspace, Ref: "payments", Class: "agent"}); err != nil {
		t.Errorf("a tree kind on a workspace scope must stay valid, got %v", err)
	}
	// A MODULE kind on a workspace scope: rejected, and the message must explain it.
	err = check(scopeSpec{Tree: scopeWorkspace, Ref: "payments", Class: "compliance:risk"})
	if err == nil {
		t.Error("a module class on a workspace scope would be inert: it must be rejected")
	} else if !strings.Contains(err.Error(), "tenant level") {
		t.Errorf("the rejection must say why, got: %v", err)
	}
	// ...and on EVERY other tree, each with a ref that really resolves, so the rejection
	// we observe is the CLASS check and not the unknown-ref check standing in for it.
	for _, c := range []struct{ tree, ref string }{
		{scopeWorkspace, "payments"},
		{scopeAgentGroup, "bots"},
		{scopeFolder, folderID},
	} {
		// Control: the same scope with a TREE class is accepted, which proves the ref
		// resolves and the only thing the module class changes is the class check.
		treeClass := "agent"
		if err := check(scopeSpec{Tree: c.tree, Ref: c.ref, Class: treeClass}); err != nil {
			t.Fatalf("control failed for tree %q ref %q: the ref must resolve, got %v", c.tree, c.ref, err)
		}
		err := check(scopeSpec{Tree: c.tree, Ref: c.ref, Class: "compliance:risk"})
		if err == nil {
			t.Errorf("a module class on scope tree %q must not be accepted", c.tree)
			continue
		}
		if !strings.Contains(err.Error(), "tenant level") {
			t.Errorf("tree %q rejected for the wrong reason (the ref check, not the class check): %v", c.tree, err)
		}
	}
	// At TENANT scope the module class is a legitimate filter.
	if err := check(scopeSpec{Tree: scopeTenant, Class: "compliance:risk"}); err != nil {
		t.Errorf("a module class at tenant scope must be accepted, got %v", err)
	}
	// An unregistered module kind is not a class at all, at any scope.
	if err := check(scopeSpec{Tree: scopeTenant, Class: "nosuch:kind"}); err == nil {
		t.Error("an unregistered module kind must not be accepted as a scope class")
	}
}

// TestRejectInertTreeScopedGrant covers the ROLE-shaped inert grant: the class-shaped one
// is caught by validateScopeRefs, but a role whose whole permission set is module
// permissions is just as unreachable from a tree scope — and worse than a no-op when the
// role is admin-capable, because the tenant-wide delegation permit is still emitted.
func TestRejectInertTreeScopedGrant(t *testing.T) {
	registerS584Perms(t)
	moduleOnly := map[string]bool{"compliance:risk:read": true, "models:keys:read": true}
	mixed := map[string]bool{"compliance:risk:read": true, "agent:read": true}
	coreOnly := map[string]bool{"agent:read": true}

	for _, tree := range []string{scopeWorkspace, scopeAgentGroup, scopeFolder} {
		g := scopedGrant{Scope: scopeSpec{Tree: tree, Ref: "x"}}
		if err := rejectInertTreeScopedGrant(g, moduleOnly); err == nil {
			t.Errorf("tree %q: a module-only grant is unreachable there and must be rejected", tree)
		}
		// A grant with ANY tree-reachable permission is partly effective: allowed.
		if err := rejectInertTreeScopedGrant(g, mixed); err != nil {
			t.Errorf("tree %q: a mixed grant is partly effective and must be allowed, got %v", tree, err)
		}
		if err := rejectInertTreeScopedGrant(g, coreOnly); err != nil {
			t.Errorf("tree %q: a core-only grant must be allowed, got %v", tree, err)
		}
	}
	// At TENANT scope a module-only grant is exactly what this whole unit exists to make
	// possible — it must NOT be rejected.
	tenantScoped := scopedGrant{Scope: scopeSpec{Tree: scopeTenant}}
	if err := rejectInertTreeScopedGrant(tenantScoped, moduleOnly); err != nil {
		t.Errorf("a module-only grant at tenant scope is the point of this unit and must be allowed, got %v", err)
	}
}

// TestModuleKindIsGrantableButNotATreeNode pins the honest limit this step ships with: a
// module permission is grantable, but a module route does not resolve the tree yet,
// so a module CLASS on a tree scope would persist a grant that authorizes nothing.
func TestModuleKindIsGrantableButNotATreeNode(t *testing.T) {
	registerS584Perms(t)

	if !auth.IsScopeableKind("compliance:risk") {
		t.Fatal("a registered module kind must be scopeable")
	}
	if auth.IsTreeScopeableKind("compliance:risk") {
		t.Error("a module kind must never be a scope-tree kind")
	}
	// The class filter still works at TENANT scope: it narrows the role to one surface.
	roles := map[string]customRole{
		"mixed": {Name: "mixed", Perms: []string{"compliance:risk:read", "models:keys:read", "agent:read"}},
	}
	eff := effectivePermsOf("mixed", true, "compliance:risk", roles, map[string]permGroup{})
	if !eff["compliance:risk:read"] || len(eff) != 1 {
		t.Errorf("a module class must filter to that surface alone, got %v", eff)
	}
}
