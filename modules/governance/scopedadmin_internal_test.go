// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// --- catalog + perm parsing ---------------------------------------------------------

func TestSplitPermAndCatalog(t *testing.T) {
	cases := []struct {
		p          string
		kind, verb string
		ok, cat    bool
	}{
		{"agent:write", "agent", "write", true, true},
		{"model:admin", "model", "admin", true, true},
		{"resource:read", "resource", "read", true, true},
		{"agent:delete", "agent", "delete", true, false}, // bad verb
		{"unknownkind:read", "unknownkind", "read", true, false},
		{"user:write", "user", "write", true, false}, // IAM kind not scopeable
		{"agent", "", "", false, false},
		{":read", "", "", false, false},
		{"agent:", "", "", false, false},
		{"ns:res:write", "ns:res", "write", true, false}, // module-namespaced not scopeable
	}
	for _, c := range cases {
		k, v, ok := splitPerm(c.p)
		if ok != c.ok || (ok && (k != c.kind || v != c.verb)) {
			t.Errorf("splitPerm(%q) = (%q,%q,%v), want (%q,%q,%v)", c.p, k, v, ok, c.kind, c.verb, c.ok)
		}
		if got := isCatalogPerm(c.p); got != c.cat {
			t.Errorf("isCatalogPerm(%q) = %v, want %v", c.p, got, c.cat)
		}
	}
}

// --- effective perms ----------------------------------------------------------------

func TestEffectivePermsBuiltin(t *testing.T) {
	roles := map[string]customRole{}
	groups := map[string]permGroup{}

	admin := effectivePermsOf(auth.RoleAdmin, false, "", roles, groups)
	if !admin["agent:write"] {
		t.Error("built-in admin must confer agent:write")
	}
	if admin["agent:admin"] {
		t.Error("built-in admin must NOT confer the resource admin verb (only owner does)")
	}
	if admin["user:write"] {
		t.Error("IAM permissions must not be scope-projected")
	}
	owner := effectivePermsOf(auth.RoleOwner, false, "", roles, groups)
	if !owner["agent:admin"] {
		t.Error("built-in owner must confer agent:admin")
	}
	viewer := effectivePermsOf(auth.RoleViewer, false, "", roles, groups)
	if viewer["agent:write"] || !viewer["agent:read"] {
		t.Errorf("viewer must confer agent:read only, got %v", viewer)
	}
}

func TestEffectivePermsCustomAndClass(t *testing.T) {
	groups := map[string]permGroup{
		"billing": {Name: "billing", Perms: []string{"cost:read"}},
	}
	roles := map[string]customRole{
		"auditor": {Name: "auditor", Perms: []string{"finding:read", "model:write", "bogus:nope"}, Groups: []string{"billing"}},
	}
	// Full bundle: direct perms (valid ones) ∪ group perms; the invalid "bogus:nope" is dropped.
	got := effectivePermsOf("auditor", true, "", roles, groups)
	want := map[string]bool{"finding:read": true, "model:write": true, "cost:read": true}
	if len(got) != len(want) {
		t.Fatalf("effective perms = %v, want %v", got, want)
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing %q in effective perms %v", p, got)
		}
	}
	// Class filter keeps only the matching kind.
	classed := effectivePermsOf("auditor", true, "model", roles, groups)
	if len(classed) != 1 || !classed["model:write"] {
		t.Errorf("class=model must keep only model:write, got %v", classed)
	}
	// Unknown custom role confers nothing (deny-closed).
	if got := effectivePermsOf("ghost", true, "", roles, groups); len(got) != 0 {
		t.Errorf("unknown custom role must confer nothing, got %v", got)
	}
}

func TestIsAdminCapableGrant(t *testing.T) {
	roles := map[string]customRole{
		"wsadmin":  {Name: "wsadmin", Perms: []string{"agent:admin", "session:write"}},
		"wseditor": {Name: "wseditor", Perms: []string{"agent:write"}},
	}
	groups := map[string]permGroup{}
	tests := []struct {
		g    scopedGrant
		want bool
	}{
		{scopedGrant{Role: auth.RoleAdmin}, true},
		{scopedGrant{Role: auth.RoleOwner}, true},
		{scopedGrant{Role: auth.RoleEditor}, false},
		{scopedGrant{Role: auth.RoleViewer}, false},
		{scopedGrant{Role: "wsadmin", RoleCustom: true}, true},   // has agent:admin
		{scopedGrant{Role: "wseditor", RoleCustom: true}, false}, // no :admin
	}
	for _, tc := range tests {
		if got := isAdminCapableGrant(tc.g, roles, groups); got != tc.want {
			t.Errorf("isAdminCapableGrant(%+v) = %v, want %v", tc.g, got, tc.want)
		}
	}
}

// --- projection ---------------------------------------------------------------------

func TestProjectManagedCedar(t *testing.T) {
	roles := map[string]customRole{}
	groups := map[string]permGroup{}
	grants := []scopedGrant{
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: auth.RoleAdmin, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "payments"}},
		{SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeAgentGroup, Ref: "bots"}},
	}
	src := projectManagedCedar(grants, roles, groups)
	// Subject + scope clauses.
	if !strings.Contains(src, `principal in User::"u1"`) {
		t.Errorf("missing user subject permit:\n%s", src)
	}
	if !strings.Contains(src, `resource in Workspace::"payments"`) {
		t.Errorf("missing workspace scope clause:\n%s", src)
	}
	if !strings.Contains(src, `principal in Role::"viewer"`) {
		t.Errorf("missing role subject permit:\n%s", src)
	}
	if !strings.Contains(src, `resource in AgentGroup::"bots"`) {
		t.Errorf("missing agent-group scope clause:\n%s", src)
	}
	// admin-capable subject (u1: built-in admin) gets the delegation capability;
	// the editor-role subject does NOT.
	if !strings.Contains(src, `Action::"governance:rbac:admin"`) {
		t.Errorf("admin-capable subject must get the rbac:admin delegation permit:\n%s", src)
	}
	if strings.Count(src, `governance:rbac:admin`) != 1 {
		t.Errorf("only the admin-capable subject (u1) should get rbac:admin, once:\n%s", src)
	}
	// The projection must compile.
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("projection does not compile: %v\n%s", err, src)
	}
	// Determinism: same input → identical bytes.
	if src2 := projectManagedCedar(grants, roles, groups); src2 != src {
		t.Error("projection is not deterministic")
	}
}

// A group subject (S256) projects a `principal in Group::"<id>"` permit keyed by the group
// id. U7: an ADMIN-capable group subject IS a delegation DIRECTED at the group's
// members, so it DOES emit the tenant-wide rbac delegation permit — gated downstream by
// loadGrants (only a direct member receives the Group:: parent, deny-closed) and clamped by
// canDelegate (a sub-grant stays within the grant's scope). A ROLE subject, even admin-
// capable, stays access-only: it must not open the console to every holder of a built-in role.
func TestProjectManagedCedarGroupSubject(t *testing.T) {
	roles := map[string]customRole{}
	groups := map[string]permGroup{}
	grants := []scopedGrant{
		{SubjectKind: subjectGroup, SubjectRef: "grp-123", Role: auth.RoleAdmin, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "payments"}},
		{SubjectKind: subjectRole, SubjectRef: auth.RoleEditor, Role: auth.RoleAdmin, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "ops"}},
	}
	src := projectManagedCedar(grants, roles, groups)
	if !strings.Contains(src, `principal in Group::"grp-123"`) {
		t.Errorf("group subject must project a Group:: permit:\n%s", src)
	}
	if !strings.Contains(src, `resource in Workspace::"payments"`) {
		t.Errorf("missing scope clause for the group grant:\n%s", src)
	}
	// U7: the admin-capable GROUP subject gets the tenant-wide rbac delegation permit
	// (read+admin, no `when` clause) — the exact permit that lets a gated group member reach
	// the delegation API and sub-delegate within canDelegate's ceiling.
	if !strings.Contains(src, `principal in Group::"grp-123", action in [Action::"governance:rbac:read", Action::"governance:rbac:admin"], resource);`) {
		t.Errorf("an admin-capable group subject must get the rbac delegation permit (U7):\n%s", src)
	}
	// The admin-capable ROLE subject does NOT (access-only carve-out): exactly one delegation
	// permit exists in the projection and it is the group's, not the role's.
	if strings.Count(src, "governance:rbac:admin") != 1 {
		t.Errorf("exactly one delegation permit (the group's, not the role's) expected:\n%s", src)
	}
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("group-subject projection does not compile: %v\n%s", err, src)
	}
}

func TestCedarSubjectExprGroup(t *testing.T) {
	if got := cedarSubjectExpr(scopedGrant{SubjectKind: subjectGroup, SubjectRef: "g1"}); got != `Group::"g1"` {
		t.Errorf("group subject expr = %q, want Group::\"g1\"", got)
	}
	// An empty group ref is unrenderable (deny-closed: skipped), like an empty user ref.
	if got := cedarSubjectExpr(scopedGrant{SubjectKind: subjectGroup, SubjectRef: ""}); got != "" {
		t.Errorf("empty group ref must be unrenderable, got %q", got)
	}
}

func TestProjectSkipsEmptyAndUnrenderable(t *testing.T) {
	roles := map[string]customRole{"empty": {Name: "empty"}}
	groups := map[string]permGroup{}
	grants := []scopedGrant{
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: "empty", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}, // no perms → skipped
		{SubjectKind: subjectRole, SubjectRef: "not-a-role", Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeTenant}},   // bad subject → skipped
	}
	if src := projectManagedCedar(grants, roles, groups); strings.TrimSpace(src) != "" {
		t.Errorf("empty/unrenderable grants must project nothing, got:\n%s", src)
	}
}

func TestCedarStrEscaping(t *testing.T) {
	if got := cedarStr(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("cedarStr escaping = %s", got)
	}
}

func TestMergeCedarSources(t *testing.T) {
	if got := mergeCedarSources("", "B"); got != "B" {
		t.Errorf("merge empty+B = %q", got)
	}
	if got := mergeCedarSources("A", ""); got != "A" {
		t.Errorf("merge A+empty = %q", got)
	}
	if got := mergeCedarSources("A", "B"); !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Errorf("merge A+B = %q", got)
	}
}

// --- ceiling (pure: tenant/workspace scopes do not touch the store) ------------------

func TestCanDelegateSuperadminRoot(t *testing.T) {
	su := auth.Principal{Superadmin: true}
	g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleOwner, Scope: scopeSpec{Tree: scopeTenant}}
	if err := canDelegate(context.Background(), nil, su, "t1", g, nil, nil, nil); err != nil {
		t.Errorf("superadmin must delegate anything, got %v", err)
	}
}

func TestCanDelegateTenantAdminCeiling(t *testing.T) {
	tenant := model.TenantID("t1")
	admin := auth.ScopedPrincipal("c", "admin", tenant, auth.RoleAdmin)
	roles := map[string]customRole{}
	groups := map[string]permGroup{}

	// A tenant admin (read+write) MAY grant an editor within a workspace.
	okGrant := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}}
	if err := canDelegate(context.Background(), nil, admin, tenant, okGrant, nil, roles, groups); err != nil {
		t.Errorf("tenant admin must delegate editor@workspace, got %v", err)
	}
	// ...but NOT an owner (owner confers the resource admin verb the actor lacks).
	ownerGrant := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleOwner, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}}
	if err := canDelegate(context.Background(), nil, admin, tenant, ownerGrant, nil, roles, groups); err == nil {
		t.Error("tenant admin must NOT delegate owner (perm ceiling)")
	} else if _, ok := asCeiling(err); !ok {
		t.Errorf("expected ceilingError, got %T %v", err, err)
	}
}

func TestCanDelegateNoAuthority(t *testing.T) {
	tenant := model.TenantID("t1")
	viewer := auth.ScopedPrincipal("c", "viewer", tenant, auth.RoleViewer)
	g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleViewer, Scope: scopeSpec{Tree: scopeTenant}}
	err := canDelegate(context.Background(), nil, viewer, tenant, g, nil, nil, nil)
	if err == nil {
		t.Fatal("a viewer has no delegation authority")
	}
	if _, ok := asCeiling(err); !ok {
		t.Errorf("expected ceilingError, got %T %v", err, err)
	}
}

func TestActorDomainsFromScopedGrant(t *testing.T) {
	tenant := model.TenantID("t1")
	// The actor is only a tenant viewer (no tenant-wide delegation authority)...
	actor := auth.ScopedPrincipal("c", "u", tenant, auth.RoleViewer)
	// ...but the principal's user id is empty for a ScopedPrincipal, so a user-subject
	// grant cannot apply; use a role-subject grant matching the actor's viewer role.
	roles := map[string]customRole{}
	groups := map[string]permGroup{}
	allGrants := []scopedGrant{
		{SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: auth.RoleAdmin, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}},
	}
	domains := actorDomains(actor, tenant, allGrants, roles, groups)
	if len(domains) != 1 || domains[0].scope.Ref != "w" {
		t.Fatalf("expected one workspace domain from the admin-capable grant, got %+v", domains)
	}
	// Within that domain the actor may delegate an editor in the same workspace.
	g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}}
	if err := canDelegate(context.Background(), nil, actor, tenant, g, allGrants, roles, groups); err != nil {
		t.Errorf("scoped-admin must sub-delegate editor within its workspace, got %v", err)
	}
	// ...but not in a different workspace.
	gOut := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "other"}}
	if err := canDelegate(context.Background(), nil, actor, tenant, gOut, allGrants, roles, groups); err == nil {
		t.Error("scoped-admin must NOT delegate outside its workspace")
	}
}

func TestScopeContainsPure(t *testing.T) {
	ctx := context.Background()
	tt := []struct {
		name         string
		outer, inner scopeSpec
		want         bool
	}{
		{"tenant ⊇ workspace", scopeSpec{Tree: scopeTenant}, scopeSpec{Tree: scopeWorkspace, Ref: "w"}, true},
		{"tenant ⊇ agent_group", scopeSpec{Tree: scopeTenant}, scopeSpec{Tree: scopeAgentGroup, Ref: "g"}, true},
		{"workspace == workspace", scopeSpec{Tree: scopeWorkspace, Ref: "w"}, scopeSpec{Tree: scopeWorkspace, Ref: "w"}, true},
		{"workspace != workspace", scopeSpec{Tree: scopeWorkspace, Ref: "w"}, scopeSpec{Tree: scopeWorkspace, Ref: "x"}, false},
		{"workspace ⊉ tenant", scopeSpec{Tree: scopeWorkspace, Ref: "w"}, scopeSpec{Tree: scopeTenant}, false},
		{"agent_group == agent_group", scopeSpec{Tree: scopeAgentGroup, Ref: "g"}, scopeSpec{Tree: scopeAgentGroup, Ref: "g"}, true},
		{"agent_group ⊉ workspace", scopeSpec{Tree: scopeAgentGroup, Ref: "g"}, scopeSpec{Tree: scopeWorkspace, Ref: "w"}, false},
		{"class broader ⊇ classed", scopeSpec{Tree: scopeTenant}, scopeSpec{Tree: scopeTenant, Class: "model"}, true},
		{"class mismatch", scopeSpec{Tree: scopeTenant, Class: "agent"}, scopeSpec{Tree: scopeTenant, Class: "model"}, false},
		{"class equal", scopeSpec{Tree: scopeTenant, Class: "model"}, scopeSpec{Tree: scopeTenant, Class: "model"}, true},
	}
	for _, tc := range tt {
		got, err := scopeContains(ctx, nil, tc.outer, tc.inner)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: scopeContains = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidRBACName(t *testing.T) {
	good := []string{"billing", "team-ops", "role_1", "a.b", "X", "BillingAuditor2"}
	bad := []string{"", "team/ops", "has space", "a:b", "name!", strings.Repeat("a", 65)}
	for _, s := range good {
		if !validRBACName(s) {
			t.Errorf("want valid: %q", s)
		}
	}
	for _, s := range bad {
		if validRBACName(s) {
			t.Errorf("want invalid: %q", s)
		}
	}
}

func TestPermSubset(t *testing.T) {
	a := map[string]bool{"agent:read": true}
	b := map[string]bool{"agent:read": true, "agent:write": true}
	if !permSubset(a, b) {
		t.Error("a ⊆ b")
	}
	if permSubset(b, a) {
		t.Error("b ⊄ a")
	}
}
