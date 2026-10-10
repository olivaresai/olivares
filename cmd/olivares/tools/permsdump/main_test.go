// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/modulespec"
)

// The inventory's failure mode is OMISSION, and omission is silent: a module
// missing from allModules() does not error, it just makes that module's
// permissions look invented to every consumer. These tests exist to make the
// silence loud.

// TestEveryModuleTypeIsInTheInventory is the one that actually closes the omission
// hole, and it exists because the import-set check below does NOT.
//
// One import can produce several api.Module VALUES: the `governance` import yields
// the base module plus the three authoring consoles, four values in the
// composition root and four here. Deleting governance.NewPolicyConsole() from
// allModules() leaves the governance import still used by the base module, so an
// import-set comparison stays green while the inventory silently loses the
// claude-policy namespace — and every console check against it starts reading as
// an invented permission. That is the exact false-positive failure this tool is
// built to avoid, hiding inside the test written to prevent it.
//
// So the anchor is the api.Module implementations themselves: every type in
// modules/ that declares APINamespace() must appear in allModules(), compared by
// concrete type, not by import path.
func TestEveryModuleTypeIsInTheInventory(t *testing.T) {
	declared := moduleTypesUnder(t, filepath.Join("..", "..", "..", "..", "modules"))
	if len(declared) == 0 {
		t.Fatal("found no APINamespace() implementations under modules/; the scan returned " +
			"nothing and every assertion below would be vacuously true")
	}
	inInventory := map[string]struct{}{}
	for _, m := range allModules() {
		rt := reflect.TypeOf(m)
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		// PkgPath's last segment is the module's directory (access-map, posture-export).
		pkg := rt.PkgPath()
		if i := strings.LastIndexByte(pkg, '/'); i >= 0 {
			pkg = pkg[i+1:]
		}
		inInventory[pkg+"."+rt.Name()] = struct{}{}
	}
	for key, where := range declared {
		if _, ok := inInventory[key]; !ok {
			t.Errorf("%s implements api.Module (%s) but no value of that type is in allModules(): "+
				"its permissions and routes are invisible to the inventory, so every console check "+
				"against them would be reported as an invented permission", key, where)
		}
	}
	for key := range inInventory {
		if _, ok := declared[key]; !ok {
			t.Errorf("allModules() contains a %s, which declares no APINamespace() under modules/", key)
		}
	}
}

// moduleTypesUnder returns "<dir>.<Type>" -> "file:line" for every
// `func (x *T) APINamespace() string` under root, excluding tests.
func moduleTypesUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "APINamespace" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			typ := fn.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			id, ok := typ.(*ast.Ident)
			if !ok {
				continue
			}
			dir := filepath.Base(filepath.Dir(p))
			out[dir+"."+id.Name] = fmt.Sprintf("%s:%d", p, fset.Position(fn.Pos()).Line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// The inventory and boot read the same spec. Compare actual namespaces, not
// imports: one implementation package can own several API namespaces.
func TestModuleListMatchesCompositionRoot(t *testing.T) {
	got := map[string]bool{}
	for _, m := range allModules() {
		if got[m.APINamespace()] {
			t.Fatalf("duplicate namespace %q", m.APINamespace())
		}
		got[m.APINamespace()] = true
	}
	for _, spec := range modulespec.All() {
		if !got[spec.Namespace] {
			t.Errorf("spec module %q missing from permission inventory", spec.Namespace)
		}
		delete(got, spec.Namespace)
	}
	if len(got) != 0 {
		t.Errorf("inventory contains namespaces outside spec: %v", got)
	}
}

// TestEveryRoutePermissionIsDeclared is the api.Module contract, enforced:
// Permissions() is documented as "the permissions the module's routes require", so
// a route requiring something the module never declares is a declaration gap.
//
// It costs nothing at runtime today — RoleGrants grants a module permission by verb
// tier and never consults Permissions() — and that is exactly why the gap could
// open unnoticed: governance mounted five agent-risk routes whose three permissions
// Permissions() omitted. An inventory that reads only declarations cannot see them.
func TestEveryRoutePermissionIsDeclared(t *testing.T) {
	for _, m := range allModules() {
		ns := m.APINamespace()
		declared := map[auth.Permission]struct{}{}
		for _, p := range m.Permissions() {
			declared[p] = struct{}{}
		}
		var routes []Route
		m.APIRoutes(recordingRegistrar{out: &routes})
		for _, rt := range routes {
			if _, ok := declared[auth.Permission(rt.Perm)]; !ok {
				t.Errorf("module %q: route %s %s requires %q, which Permissions() does not declare",
					ns, rt.Method, rt.Pattern, rt.Perm)
			}
		}
	}
}

// nonRoutePermissions are permissions a module may DECLARE without any mounted module
// route requiring them — for enforcement outside the module router, such as the
// eventing module's per-event RBAC filter or the apps gateway's admission seam.
// An entry added here must say what surface outside the module router
// enforces it, because an entry with no enforcement anywhere is the hole.
//
// It held exactly ONE entry when this tool could compile again: the map was written
// empty and stayed empty only because nothing had run this invariant since added
// the case it was predicting. The other permission this test named — the identity
// console declaring governance:identity:read with no route of its own requiring it —
// is NOT here on purpose: nothing enforced it, so it was a hole and it was removed at
// the declaration instead of blessed at the exception. That is the distinction this
// map exists to force; an exception is for enforcement elsewhere, never for none.
var nonRoutePermissions = map[auth.Permission]string{
	// Gates DELIVERY of guardrail.observed events over the bus, enforced by the
	// eventing per-event RBAC filter (modules/eventing/catalog.go:104), not by a route:
	// it is the redacted observed-agent-text stream, the most content-like fact on the
	// bus, which is why core/auth privilegedReadPerms holds it at editor and above.
	// modules/security declares it because that module owns the "security" namespace —
	// eventing declaring it would let one module widen another's.
	"security:observed:read": "eventing per-event RBAC filter (modules/eventing/catalog.go)",
	// The apps gateway asks the admission seam before POST/DELETE spend-limit
	// mutations. These HTTP handlers are outside the module route registrar.
	"finops:budget:admin": "appsGatewayHandler.spendLimitAdminAllowed admission seam (cmd/olivares/appsgateway.go)",
	// ⛔ ESTA ENTRADA NO ES «ENFORCEMENT EN OTRO SITIO» EN EL SENTIDO HTTP, y por eso lleva su
	// razon entera: el placeholder del session cockpit monta CERO rutas a proposito (su ruta 501
	// se retiro el 2026-09-02 porque lint:public-counts exige que las rutas de modulo igualen las
	// operaciones del openapi y ese carril no podia tocar web/). La declaracion se queda porque
	// el literal es PORTANTE para un gate: scripts/check-community-cockpit-strings.sh lo usa como
	// CONTROL POSITIVO — sin el en el binario de community, ese gate no distingue «no hay traza
	// inesperada» de «este binario nunca llevo el placeholder», y su verde seria cierto por vacio.
	// Ademas lo nombran el contrato de la edicion (docs/contracts/COCKPIT-07-edition-cut.md), la
	// allowlist scripts/community-session-cockpit-strings.allow y el test del propio modulo.
	//
	// Se intento retirarla en la declaracion siguiendo el precedente de arriba y NO valia: aquel
	// caso se quito porque NADA lo usaba; aqui lo usan ocho sitios, uno de ellos un control.
	"session-cockpit:availability:read": "control positivo de check-community-cockpit-strings.sh y literal del contrato COCKPIT-07",
}

// pendingK3CommunicationRoutePermissions is deliberately NOT part of
// nonRoutePermissions: these permissions have no alternate enforcement surface.
// They are the K3 communication REST contract, declared before public route
// activation while PostgreSQL migration 0018 and the readiness/composition gates
// remain deny-closed.
//
// The companion invariant below makes every entry self-expiring. A permission
// must be declared exactly once by sessions, must still have no mounted route and
// must carry a reason. Mounting its route or removing its declaration therefore
// turns this temporary catalog red until the stale entry is deleted.
const pendingK3CommunicationRouteReason = "K3 communication REST remains deny-closed pending PostgreSQL 0018 and readiness/composition gates"

var pendingK3CommunicationRoutePermissions = map[auth.Permission]string{
	"sessions:message:write":          pendingK3CommunicationRouteReason,
	"sessions:message:admin":          pendingK3CommunicationRouteReason,
	"sessions:delivery:admin":         pendingK3CommunicationRouteReason,
	"sessions:decision-request:read":  pendingK3CommunicationRouteReason,
	"sessions:decision-request:admin": pendingK3CommunicationRouteReason,
	"sessions:handoff:read":           pendingK3CommunicationRouteReason,
	"sessions:handoff:write":          pendingK3CommunicationRouteReason,
	"sessions:handoff:admin":          pendingK3CommunicationRouteReason,
	"sessions:route:read":             pendingK3CommunicationRouteReason,
	"sessions:route:write":            pendingK3CommunicationRouteReason,
	"sessions:route:admin":            pendingK3CommunicationRouteReason,
	"sessions:subscription:read":      pendingK3CommunicationRouteReason,
	"sessions:subscription:write":     pendingK3CommunicationRouteReason,
	"sessions:subscription:admin":     pendingK3CommunicationRouteReason,
	"sessions:endpoint:read":          pendingK3CommunicationRouteReason,
	"sessions:endpoint:write":         pendingK3CommunicationRouteReason,
	"sessions:endpoint:admin":         pendingK3CommunicationRouteReason,
}

func TestPendingK3CommunicationRoutePermissionsAreCurrent(t *testing.T) {
	declaredBy := map[auth.Permission]map[string]int{}
	mountedBy := map[auth.Permission][]string{}
	for _, m := range allModules() {
		ns := m.APINamespace()
		for _, permission := range m.Permissions() {
			if declaredBy[permission] == nil {
				declaredBy[permission] = map[string]int{}
			}
			declaredBy[permission][ns]++
		}
		var routes []Route
		m.APIRoutes(recordingRegistrar{out: &routes})
		for _, route := range routes {
			permission := auth.Permission(route.Perm)
			mountedBy[permission] = append(mountedBy[permission],
				fmt.Sprintf("%s %s %s", ns, route.Method, route.Pattern))
		}
	}

	for permission, reason := range pendingK3CommunicationRoutePermissions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("temporary K3 communication route entry %q has an empty reason", permission)
		}
		if _, overlaps := nonRoutePermissions[permission]; overlaps {
			t.Errorf("temporary K3 communication route entry %q is also in nonRoutePermissions; "+
				"temporary route activation and alternate non-HTTP enforcement are distinct states", permission)
		}

		declarations := declaredBy[permission]
		if len(declarations) == 0 {
			t.Errorf("stale temporary K3 communication route entry %q: no module declares it", permission)
			continue
		}
		if got := declarations["sessions"]; got != 1 || len(declarations) != 1 {
			t.Errorf("temporary K3 communication route entry %q must be declared exactly once by "+
				"sessions, declarations = %v", permission, declarations)
		}
		if routes := mountedBy[permission]; len(routes) != 0 {
			t.Errorf("stale temporary K3 communication route entry %q now has mounted route(s) %v; "+
				"remove it from pendingK3CommunicationRoutePermissions", permission, routes)
		}
	}
}

// TestEveryDeclaredPermissionIsRequiredByARoute is the reverse of the invariant
// above, and it is the one with teeth.
//
// Permissions() has no production consumer (see TestEveryRoutePermissionIsDeclared):
// nothing reads it, so a stale entry costs nothing at runtime and is invisible. But
// the console guard treats "the engine declares it" as proof the action exists, so
// a permission sitting in Permissions() with no route behind it would bless a
// console button that reaches no server action at all — the guard would pass a
// string precisely as broken as the ones it was written to catch.
//
// Concretely: adding "voice:ghost:read" to voice's Permissions() and no route would
// satisfy every other invariant here, be emitted as declared, be granted to viewers
// by RoleGrants (module-shaped, read verb) and by the console mirror alike, and so
// produce neither an undeclared nor a divergent finding.
func TestEveryDeclaredPermissionIsRequiredByARoute(t *testing.T) {
	for _, m := range allModules() {
		ns := m.APINamespace()
		var routes []Route
		m.APIRoutes(recordingRegistrar{out: &routes})
		required := map[auth.Permission]struct{}{}
		for _, rt := range routes {
			required[auth.Permission(rt.Perm)] = struct{}{}
		}
		for _, p := range m.Permissions() {
			if _, ok := required[p]; ok {
				continue
			}
			if why, allowed := nonRoutePermissions[p]; allowed {
				t.Logf("module %q: %q is declared without a route, allowed: %s", ns, p, why)
				continue
			}
			if why, pending := pendingK3CommunicationRoutePermissions[p]; pending {
				if strings.TrimSpace(why) == "" {
					t.Errorf("module %q: temporary K3 communication route entry %q has an empty reason", ns, p)
					continue
				}
				t.Logf("module %q: %q is declared while its K3 route activation is pending: %s", ns, p, why)
				continue
			}
			t.Errorf("module %q declares %q but no mounted route requires it. A declaration "+
				"nothing enforces reads as a real permission to every consumer while gating "+
				"nothing; if a surface outside the module router enforces it, say so in nonRoutePermissions", ns, p)
		}
	}
}

// TestEveryDeclaredPermissionIsNamespaced enforces the half of the contract that
// actually holds: module permissions "must be namespaced
// (<namespace>:<resource>:<verb>)". A two-segment permission declared by a module
// is read by the engine as a CORE permission and checked against the explicit
// per-role set, where it does not appear — so it would be granted to nobody while
// looking perfectly declared.
//
// It deliberately does NOT require the prefix to be the declaring module's own
// namespace. That stricter rule looked obvious and is false here: the three
// authoring consoles (claude-policy, claude-agents, identity) are route-only
// modules that reuse governance's tables and its permissions on purpose, so six
// declarations legitimately carry the "governance:" prefix. What IS worth
// asserting is that the prefix names a namespace some module in the product
// actually serves — that still catches a typo, without outlawing the reuse.
func TestEveryDeclaredPermissionIsNamespaced(t *testing.T) {
	mods := allModules()
	known := map[string]struct{}{}
	for _, m := range mods {
		known[m.APINamespace()] = struct{}{}
	}
	for _, m := range mods {
		ns := m.APINamespace()
		for _, p := range m.Permissions() {
			if !p.IsModule() {
				t.Errorf("module %q declares %q, which is not namespaced: the engine would treat it "+
					"as a core permission and grant it to no role", ns, p)
				continue
			}
			prefix := strings.SplitN(string(p), ":", 2)[0]
			if _, ok := known[prefix]; !ok {
				t.Errorf("module %q declares %q, whose namespace %q is served by no module in the "+
					"product", ns, p, prefix)
			}
		}
	}
}

// TestPrivilegedReadPermsIsACopy guards the accessor added for this inventory: it
// must expose the set without exposing the set. A caller mutating the returned
// slice must not be able to change an authorization decision.
func TestPrivilegedReadPermsIsACopy(t *testing.T) {
	first := auth.PrivilegedReadPerms()
	if len(first) == 0 {
		t.Fatal("PrivilegedReadPerms returned nothing; every assertion here would be vacuous")
	}
	// The engine's own answer for the first entry, before anyone touches the slice.
	probe := first[0]
	before := auth.RoleGrants(auth.RoleViewer, probe)
	for i := range first {
		first[i] = "mutated:by:caller"
	}
	if got := auth.RoleGrants(auth.RoleViewer, probe); got != before {
		t.Errorf("mutating the returned slice changed RoleGrants(%q) from %v to %v: the accessor "+
			"aliases the privileged-read set instead of copying it", probe, before, got)
	}
	second := auth.PrivilegedReadPerms()
	if len(second) != len(first) {
		t.Fatalf("second call returned %d entries, first returned %d", len(second), len(first))
	}
	for i, p := range second {
		if p == "mutated:by:caller" {
			t.Errorf("entry %d survived a caller's mutation: the accessor is not returning a copy", i)
		}
	}
	// A privileged read is by definition denied to viewer and granted to editor.
	for _, p := range second {
		if auth.RoleGrants(auth.RoleViewer, p) {
			t.Errorf("%q is listed as a privileged read but viewer holds it", p)
		}
		if !auth.RoleGrants(auth.RoleEditor, p) {
			t.Errorf("%q is listed as a privileged read but editor does not hold it", p)
		}
	}
}

// TestInventoryCoversAllFourDeclarationForms is the guard on the guard. Each form
// must actually contribute permissions; a form that silently collects nothing
// (a renamed accessor, a constructor that stopped mounting routes) would quietly
// shrink the inventory and turn real console checks into reported inventions.
func TestInventoryCoversAllFourDeclarationForms(t *testing.T) {
	inv := build()
	seen := map[string]int{}
	for _, d := range inv.Declared {
		for _, f := range d.Forms {
			seen[strings.SplitN(f, ":", 2)[0]]++
		}
	}
	for _, form := range []string{"core", "privileged", "module", "route"} {
		if seen[form] == 0 {
			t.Errorf("declaration form %q contributed no permissions: the inventory is blind to "+
				"an entire form, and every permission declared only that way reads as invented", form)
		}
	}

	// The concatenated core permissions are the form a grep cannot see. Assert one
	// by name: "session:read" exists only as "session" + ":" + VerbRead.
	if _, ok := inv.Declared["session:read"]; !ok {
		t.Error(`"session:read" is absent from the inventory: the core sets are built by ` +
			`concatenation, so losing them is exactly the blindness this tool exists to avoid`)
	}
	// And a privileged read, which PermissionsForRole deliberately omits.
	if d, ok := inv.Declared["authz:read"]; !ok {
		t.Error(`"authz:read" is absent: the privileged-read form is not being collected`)
	} else if d.Grants[auth.RoleViewer] {
		t.Error(`"authz:read" is recorded as granted to viewer; it is a privileged read`)
	}
}

// TestModuleAdminVerbIsNeverGrantedToEditor pins the tier a feature test used to
// restate on the client, and it lives here because here is where the whole
// declaration inventory is in scope.
//
// web/src/features/voice/voice.test.tsx asserted `roleAllows('editor', p) === false`
// for the voice policy gate. Deleted roleAllows — correctly, because the console
// stopped modeling the engine's tiers at all: /v1/auth/whoami now hands each grant its
// effective permission set and can() is set membership. But a guarantee that only one
// feature restated, in a language that no longer holds the rule, was one deletion away
// from being lost with the mechanism that carried it. Asserted over EVERY module
// permission rather than voice's, it cannot rot when a feature is renamed or removed.
//
// The engine's rule is roleGrantsVerb: viewer→read, editor→+write, admin/owner→+admin.
// Privileged reads are checked BEFORE the module split in RoleGrants and are all :read,
// so they cannot collide with the :admin verb this walks.
func TestModuleAdminVerbIsNeverGrantedToEditor(t *testing.T) {
	inv := build()

	checked := 0
	for perm, d := range inv.Declared {
		p := auth.Permission(perm)
		if !p.IsModule() || p.Verb() != auth.VerbAdmin {
			continue
		}
		checked++
		if d.Grants[auth.RoleEditor] {
			t.Errorf("%q is an admin-verb module permission but editor holds it; the console would offer an action the engine answers 403 to", perm)
		}
		if !d.Grants[auth.RoleAdmin] {
			t.Errorf("%q is an admin-verb module permission that admin does NOT hold; the console would hide an action from the role that can take it", perm)
		}
	}

	// Vacuity control: a walk that examined nothing passes every assertion above, and
	// a shape change that emptied it would read as green.
	if checked == 0 {
		t.Fatal("no admin-verb module permission was examined: the inventory or the shape test is wrong, not the tier")
	}
	t.Logf("examined %d admin-verb module permission(s)", checked)
}

// recordingRegistrar captures what APIRoutes mounts instead of serving it. It is
// the only honest way to read the route requirement: the permission is an argument
// to Handle, so nothing short of running APIRoutes observes conditional mounts.
type recordingRegistrar struct {
	out       *[]Route
	namespace string
}

func (r recordingRegistrar) Handle(method, pattern string, perm auth.Permission, h api.ModuleHandler) {
	*r.out = append(*r.out, Route{Method: method, Pattern: pattern, Perm: string(perm)})
}

// HandleEntity records an ENTITY route exactly as Handle records a collection one.
// api.RouteRegistrar grew this second method after this tool was written, and the
// compile error it caused is the good outcome: an interface that gains a mounting
// verb MUST break every recorder, because the alternative is a recorder that keeps
// compiling and silently stops seeing a whole class of route. Ignoring the entity
// routes here would leave their permissions out of the inventory, and this tool
// exists precisely so the console cannot ask for a permission the engine does not
// mount — a hole in the inventory is that same drift with the evidence removed.
//
// The EntityRef is deliberately dropped: it declares the lineage the engine
// authorizes against, which changes WHICH rows a grant reaches, never WHICH
// permission the route requires. This inventory answers only the latter.
func (r recordingRegistrar) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

// HandleSealed records the permission of a governed route. Its authority seal
// changes how a request is admitted, not the permission declared by the module.
func (r recordingRegistrar) HandleSealed(method, pattern string, perm auth.Permission, _ api.SealedRoute, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}
