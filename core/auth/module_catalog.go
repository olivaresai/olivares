// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// the MODULE half of the scope-grantable permission catalog.
//
// Until this file existed the catalog was exactly coreReadWriteResources: the 15 engine
// kinds that live in the tree. Every module-namespaced permission
// ("<ns>:<res>:<verb>") was therefore UNGRANTABLE by a custom role or a scoped grant —
// isCatalogPerm rejected it, so an operator could not author "editor, but not
// models:keys:write" and could not delegate a module surface to a scoped admin at all.
// Measured on the live module set the day this landed: ZERO module permissions were
// delegable. The universe is 195: 194 come from the 33 mounted modules (the union of
// Module.Permissions() and the permissions the 656 routes require) and one more,
// "security:observed:read", is enforced by the eventing per-event RBAC filter
// (modules/eventing/catalog.go) rather than by a route. All of them are re-counted on
// every run by TestEveryModulePermissionIsDelegable (cmd/olivares).
//
// The fix is NOT to relax a check: the 15-kind rule is correct for the SCOPE TREE (a
// module row is not an node, see TreeScopeableKinds). It is to give the catalog the
// axis it lacked — a registry of the module permissions the running engine actually
// exposes, seeded at mount from Module.Permissions() by the composition root
// (core/api mountModules). An engine that registers nothing keeps exactly the old
// behavior, so the widening is deny-closed by construction: a permission is grantable
// only because a mounted module declared it.
//
// Why a registry and not a shape rule ("anything with two colons"): a typo'd permission
// in a custom role would then be accepted and silently grant nothing. The operator must
// get a 400 for a permission no module exposes — the catalog is an allowlist, and the
// authoring surface validates against it (modules/governance validatePerms).

// moduleCatalog is an immutable snapshot of the registered module permissions and the
// kinds they imply. It is swapped wholesale so readers never see a half-built map.
type moduleCatalog struct {
	perms map[Permission]struct{}
	kinds map[string]struct{}
}

// moduleCatalogPtr holds the live snapshot. Registration happens at boot (before the
// server serves) and reads happen on authoring/projection paths, never per request, so
// an atomic pointer with copy-on-write registration is both correct and lock-free.
var moduleCatalogPtr atomic.Pointer[moduleCatalog]

func loadModuleCatalog() *moduleCatalog {
	if c := moduleCatalogPtr.Load(); c != nil {
		return c
	}
	return &moduleCatalog{}
}

// RegisterModulePermissions adds module-namespaced permissions to the scope-grantable
// catalog. The composition root calls it once per mounted module with that module's
// Permissions(); it is additive and idempotent, so re-registering the same set is a
// no-op and two modules may legitimately declare the same permission (the route-only
// console modules reuse another module's namespace on purpose — core/api/modules.go).
//
// It is deny-closed on malformed input: every permission must be module-form
// ("<ns>:<res>:<verb>", two colons) with a read|write|admin verb and non-empty segments.
// A core-form permission is REJECTED rather than ignored — a module must never be able to
// widen the CORE catalog (which is code-defined and audited), and silently dropping the
// entry would leave a module believing a permission it declares is grantable when it is
// not. The error fails the mount, so a malformed declaration stops boot instead of
// producing a half-populated catalog.
func RegisterModulePermissions(perms []Permission) error {
	if len(perms) == 0 {
		return nil
	}
	add := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		kind, verb, ok := SplitPermission(p)
		switch {
		case !ok:
			return fmt.Errorf("auth: %q is not a valid permission (want <ns>:<resource>:<verb>)", p)
		case !IsVerb(verb):
			return fmt.Errorf("auth: permission %q has verb %q, want read|write|admin", p, verb)
		case !p.IsModule():
			return fmt.Errorf("auth: %q is a CORE permission; a module may only declare <ns>:<resource>:<verb>", p)
		case !isModulePermKind(kind):
			return fmt.Errorf("auth: permission %q has a malformed kind %q (want exactly <namespace>:<resource>, both non-empty)", p, kind)
		}
		add[p] = struct{}{}
	}
	// CAS against the EXACT pointer the snapshot was built from. Re-Loading inside the
	// compare would let a concurrent registration land between the read and the swap and
	// then be overwritten by a snapshot that never saw it (a lost update).
	for {
		cur := moduleCatalogPtr.Load()
		next := &moduleCatalog{
			perms: make(map[Permission]struct{}, len(add)),
			kinds: make(map[string]struct{}, len(add)),
		}
		if cur != nil {
			for p := range cur.perms {
				next.perms[p] = struct{}{}
			}
			for k := range cur.kinds {
				next.kinds[k] = struct{}{}
			}
		}
		for p := range add {
			next.perms[p] = struct{}{}
			if kind, _, ok := SplitPermission(p); ok {
				next.kinds[kind] = struct{}{}
			}
		}
		if moduleCatalogPtr.CompareAndSwap(cur, next) {
			return nil
		}
	}
}

// isModulePermKind reports whether kind is exactly "<namespace>:<resource>" with both
// segments non-empty. It rejects "ns:" and ":res" (which an empty-segment substring test
// misses) and any deeper nesting, so the registry can never carry a kind the authoring
// surface could not round-trip.
func isModulePermKind(kind string) bool {
	i := strings.IndexByte(kind, ':')
	if i <= 0 || i == len(kind)-1 {
		return false
	}
	return !strings.Contains(kind[i+1:], ":")
}

// ResetModuleCatalog clears the registry. It exists for TESTS and for a composition root
// that rebuilds a server in-process: without it a second mount would union its module set
// with the first, and a test asserting the catalog's contents would depend on which other
// test ran before it. It is never called on a serving path.
func ResetModuleCatalog() { moduleCatalogPtr.Store(&moduleCatalog{}) }

// ModulePermissions returns the registered module permissions, sorted. It is the
// authoring catalog a console offers for a custom role, and it is empty until a module
// registers — never a shape-derived guess.
func ModulePermissions() []Permission {
	c := loadModuleCatalog()
	out := make([]Permission, 0, len(c.perms))
	for p := range c.perms {
		out = append(out, p)
	}
	sortPerms(out)
	return out
}

// ModuleScopeableKinds returns the registered module permission KINDS ("<ns>:<res>"),
// sorted. These are grantable but are NOT tree nodes: see TreeScopeableKinds.
func ModuleScopeableKinds() []string {
	c := loadModuleCatalog()
	out := make([]string, 0, len(c.kinds))
	for k := range c.kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isRegisteredModuleKind reports whether kind is a registered module permission kind.
func isRegisteredModuleKind(kind string) bool {
	_, ok := loadModuleCatalog().kinds[kind]
	return ok
}

// isRegisteredModulePerm reports whether p is a registered module permission.
func isRegisteredModulePerm(p Permission) bool {
	_, ok := loadModuleCatalog().perms[p]
	return ok
}

// SplitPermission parses a permission into its KIND and VERB at the LAST colon:
// "agent:read" → ("agent", "read"); "models:keys:write" → ("models:keys", "write"). ok is
// false for a string with no colon, an empty kind or an empty verb.
//
// The last colon is not a detail. Splitting on the segment BEFORE the verb (what
// Permission.Resource returns) makes a module permission look like a core kind, and is how
// an earlier harness produced 15 false positives. On the live module set exactly 15
// permissions are trapped that way, spanning SEVEN core kinds as their middle segment:
// deployment, cost, identity, policy, health, session and finding (e.g.
// "deploy:deployment:read", "voice:session:admin", "security:finding:write"). Every caller
// that asks "is this kind grantable" MUST use this split. The count and the list are
// re-measured on every run by TestMiddleSegmentTrapIsNotHowTheCatalogDecides
// (cmd/olivares), which fails if the live set stops exercising the trap.
func SplitPermission(p Permission) (kind, verb string, ok bool) {
	s := string(p)
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// IsVerb reports whether v is a permission verb (read|write|admin).
func IsVerb(v string) bool {
	return v == VerbRead || v == VerbWrite || v == VerbAdmin
}

// IsGrantablePermission reports whether p may appear in a custom role, a permission-group
// or the projection of a scoped grant.
//
// The two halves of the catalog are matched DIFFERENTLY, on purpose:
//
//   - a CORE kind is matched by kind × verb, because the core catalog is code-defined and
//     every kind carries all three verbs by construction (buildCoreRolePerms);
//   - a MODULE permission is matched WHOLE against the registry, because a module declares
//     the exact permissions it enforces. "models:keys:read" and "models:keys:write" exist;
//     "models:keys:admin" does not, and accepting it by kind would let an operator author a
//     role carrying a permission no route will ever check — a rule that looks like a
//     restriction and enforces nothing. The authoring surface returns 400 instead.
func IsGrantablePermission(p Permission) bool {
	kind, verb, ok := SplitPermission(p)
	if !ok || !IsVerb(verb) {
		return false
	}
	if IsTreeScopeableKind(kind) {
		return true
	}
	return isRegisteredModulePerm(p)
}
