// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// withModulePerms registers perms for the duration of the test and restores the empty
// catalog afterwards, so a test never depends on which other test ran first.
func withModulePerms(t *testing.T, perms ...auth.Permission) {
	t.Helper()
	auth.ResetModuleCatalog()
	if err := auth.RegisterModulePermissions(perms); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(auth.ResetModuleCatalog)
}

func has(ps []auth.Permission, want string) bool {
	for _, p := range ps {
		if string(p) == want {
			return true
		}
	}
	return false
}

// TestEmptyCatalogIsExactlyTheOldBehaviour pins the deny-closed property: an engine that
// registers no module keeps the pre catalog, so the widening can only ever be caused
// by a module actually declaring a permission.
func TestEmptyCatalogIsExactlyTheOldBehavior(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	if got := len(auth.ScopeableKinds()); got != len(auth.TreeScopeableKinds()) {
		t.Errorf("empty catalog: ScopeableKinds()=%d, want the %d tree kinds", got, len(auth.TreeScopeableKinds()))
	}
	if auth.IsScopeableKind("models:keys") {
		t.Error("an unregistered module kind must not be scopeable")
	}
	if auth.IsGrantablePermission("models:keys:write") {
		t.Error("an unregistered module permission must not be grantable")
	}
	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		for _, p := range auth.RoleResourcePerms(role) {
			if p.IsModule() {
				t.Errorf("role %s: empty catalog leaked module perm %q", role, p)
			}
		}
	}
}

// TestRegisteredModulePermIsGrantable is the defect this unit exists to close: before
// Every module permission was rejected by the catalog, so no custom role could name
// one and no scoped grant could confer one.
func TestRegisteredModulePermIsGrantable(t *testing.T) {
	withModulePerms(t, "models:keys:read", "models:keys:write", "compliance:risk:read")

	for _, p := range []auth.Permission{"models:keys:read", "models:keys:write", "compliance:risk:read"} {
		if !auth.IsGrantablePermission(p) {
			t.Errorf("registered module permission %q must be grantable", p)
		}
	}
	if !auth.IsScopeableKind("models:keys") || !auth.IsScopeableKind("compliance:risk") {
		t.Error("a registered module kind must be scopeable")
	}
	kinds := auth.ScopeableKinds()
	if !containsStr(kinds, "models:keys") || !containsStr(kinds, "compliance:risk") {
		t.Errorf("ScopeableKinds must list registered module kinds, got %v", kinds)
	}
	// The tree catalog is NOT widened: a module kind is grantable but is not an node.
	if auth.IsTreeScopeableKind("models:keys") {
		t.Error("a module kind must never become a tree-scopeable kind")
	}
	if len(auth.TreeScopeableKinds()) != 15 {
		t.Errorf("tree kinds = %d, want the 15 core kinds", len(auth.TreeScopeableKinds()))
	}
}

// TestModulePermMatchedWholeNotByKind: registering "models:keys:read" must not make
// "models:keys:admin" grantable. A permission no route checks is not a restriction, it is
// a lie in the authoring surface.
func TestModulePermMatchedWholeNotByKind(t *testing.T) {
	withModulePerms(t, "models:keys:read")

	if !auth.IsGrantablePermission("models:keys:read") {
		t.Fatal("the registered permission must be grantable")
	}
	for _, p := range []auth.Permission{"models:keys:write", "models:keys:admin"} {
		if auth.IsGrantablePermission(p) {
			t.Errorf("%q was never declared by a module: it must not be grantable", p)
		}
	}
	// A CORE kind keeps kind × verb matching: every core kind carries all three verbs.
	for _, p := range []auth.Permission{"agent:read", "agent:write", "agent:admin"} {
		if !auth.IsGrantablePermission(p) {
			t.Errorf("core permission %q must stay grantable by kind × verb", p)
		}
	}
}

// TestSplitIsLastColonNotMiddleSegment pins the trap that gave an earlier harness 15 false
// positives: "models:keys:write" has Resource()=="keys", and several core kind names
// appear as the MIDDLE segment of a real module permission.
func TestSplitIsLastColonNotMiddleSegment(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	// These are ALL 15 live module permissions whose middle segment is a core kind — the
	// complete trap set, not a sample. Reproduced from the live module registry with:
	//   go test ./cmd/olivares/ -run TestZZS584Probe -v   (MIDDLE_SEGMENT_TRAP line)
	// If a module adds a 16th, this test still passes; the probe is what re-counts them.
	for _, p := range []auth.Permission{
		"deploy:deployment:admin", "deploy:deployment:read", "deploy:deployment:write",
		"finops:cost:write",
		"governance:identity:admin", "governance:identity:read",
		"governance:policy:admin", "governance:policy:read",
		"observability:health:read",
		"recording:session:admin",
		"security:finding:read", "security:finding:write",
		"voice:policy:admin", "voice:session:admin", "voice:session:read",
	} {
		kind, verb, ok := auth.SplitPermission(p)
		if !ok || !auth.IsVerb(verb) {
			t.Fatalf("%q: split failed (kind=%q verb=%q ok=%v)", p, kind, verb, ok)
		}
		if !strings.Contains(kind, ":") {
			t.Errorf("%q: kind %q should be <ns>:<res>", p, kind)
		}
		if auth.IsTreeScopeableKind(kind) {
			t.Errorf("%q: kind %q must not be a tree kind", p, kind)
		}
		// The trap: the middle segment alone IS a tree kind, which is why the middle-segment
		// split reports a module permission as scopeable when it is not.
		if !auth.IsTreeScopeableKind(p.Resource()) {
			t.Errorf("%q: fixture is not exercising the trap — Resource()=%q is not a core kind", p, p.Resource())
		}
		// With an empty registry the whole permission is NOT grantable, despite the trap.
		if auth.IsGrantablePermission(p) {
			t.Errorf("%q must not be grantable off an unregistered kind", p)
		}
	}
}

// TestRoleResourcePermsCarriesModulePermsByRoleGrants: without this the delegation ceiling
// could never be satisfied for a module permission and the whole widening would be inert.
func TestRoleResourcePermsCarriesModulePermsByRoleGrants(t *testing.T) {
	withModulePerms(t,
		"compliance:risk:read", "compliance:risk:write", "compliance:risk:admin",
		"adoption:developer:read", // a PRIVILEGED read: editor and up, never viewer
	)

	cases := []struct {
		role string
		perm string
		want bool
	}{
		{auth.RoleViewer, "compliance:risk:read", true},
		{auth.RoleViewer, "compliance:risk:write", false},
		{auth.RoleViewer, "compliance:risk:admin", false},
		{auth.RoleEditor, "compliance:risk:write", true},
		{auth.RoleEditor, "compliance:risk:admin", false},
		{auth.RoleAdmin, "compliance:risk:admin", true},
		{auth.RoleOwner, "compliance:risk:admin", true},
		// privilegedReadPerms must keep biting through the projection.
		{auth.RoleViewer, "adoption:developer:read", false},
		{auth.RoleEditor, "adoption:developer:read", true},
	}
	for _, c := range cases {
		if got := has(auth.RoleResourcePerms(c.role), c.perm); got != c.want {
			t.Errorf("RoleResourcePerms(%s) has %q = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
	// The projection must agree with the live RBAC predicate for EVERY registered perm and
	// role — an over-grant here silently widens what a scoped grant confers.
	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner, "nope"} {
		projected := map[auth.Permission]bool{}
		for _, p := range auth.RoleResourcePerms(role) {
			projected[p] = true
		}
		for _, p := range auth.ModulePermissions() {
			if projected[p] != auth.RoleGrants(role, p) {
				t.Errorf("role %s perm %q: projected=%v RoleGrants=%v", role, p, projected[p], auth.RoleGrants(role, p))
			}
		}
	}
	// An unknown role stays empty (deny-closed).
	if got := auth.RoleResourcePerms("nope"); len(got) != 0 {
		t.Errorf("unknown role projected %d perms, want 0: %v", len(got), got)
	}
}

func TestRegisterRejectsMalformed(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	// Each case asserts WHICH rejection fired, not merely that one did. Several of these
	// strings are caught by more than one clause, so a test that only checks err != nil
	// cannot tell a working guard from a dead one — a mutant that disabled the
	// core-permission clause survived exactly that way before this assertion existed.
	bad := []struct {
		perm auth.Permission
		want string // a distinctive fragment of the message the RIGHT clause produces
		why  string
	}{
		{"agent:read", "is a CORE permission", "a CORE permission would widen the code-defined catalog"},
		{"system:admin", "is a CORE permission", "the system permission is core-form and never grantable"},
		{"nocolon", "not a valid permission", "no verb"},
		{"ns:res:", "not a valid permission", "empty verb"},
		{"ns:res:delete", "want read|write|admin", "not a permission verb"},
		{":res:read", "malformed kind", "empty namespace"},
		{"ns::read", "malformed kind", "empty resource segment"},
		{"a:b:c:read", "malformed kind", "kind must be exactly <ns>:<res>"},
	}
	for _, c := range bad {
		err := auth.RegisterModulePermissions([]auth.Permission{c.perm})
		if err == nil {
			t.Errorf("RegisterModulePermissions(%q) must fail: %s", c.perm, c.why)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("RegisterModulePermissions(%q) rejected for the wrong reason:\n got: %v\nwant a message containing %q (%s)", c.perm, err, c.want, c.why)
		}
		if auth.IsGrantablePermission(c.perm) && !auth.IsTreeScopeableKind(kindOf(c.perm)) {
			t.Errorf("%q must not have entered the catalog", c.perm)
		}
	}
	// A rejected batch must not partially register: the good entry rides with a bad one.
	if err := auth.RegisterModulePermissions([]auth.Permission{"good:thing:read", "bad:thing:delete"}); err == nil {
		t.Fatal("a batch with a malformed entry must fail")
	}
	if auth.IsGrantablePermission("good:thing:read") {
		t.Error("a rejected batch must register NOTHING — validation runs before any install")
	}
}

func TestRegisterIsAdditiveAndIdempotent(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	if err := auth.RegisterModulePermissions([]auth.Permission{"a:x:read"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.RegisterModulePermissions([]auth.Permission{"a:x:read", "b:y:write"}); err != nil {
		t.Fatal(err)
	}
	if got := auth.ModulePermissions(); len(got) != 2 {
		t.Errorf("ModulePermissions()=%v, want 2 deduplicated entries", got)
	}
	if !auth.IsGrantablePermission("a:x:read") || !auth.IsGrantablePermission("b:y:write") {
		t.Error("both registrations must survive")
	}
	auth.ResetModuleCatalog()
	if auth.IsGrantablePermission("a:x:read") {
		t.Error("ResetModuleCatalog must clear the registry")
	}
}

func kindOf(p auth.Permission) string {
	k, _, _ := auth.SplitPermission(p)
	return k
}

func containsStr(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
