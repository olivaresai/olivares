// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command permsdump emits the control plane's PERMISSION INVENTORY: every
// permission the engine declares, in which FORM it is declared, and — for each
// built-in role — whether the engine actually grants it. The output is the source
// of truth the console's permission STRINGS are checked against (reshaped by
// there is no console-side RBAC mirror left to compare against).
//
// It RUNS the engine rather than reading it. That distinction is the whole point:
//
//   - Core permissions are not written down anywhere. They are CONCATENATED at
//     init — "session:read" is `"session" + ":" + VerbRead` (auth.permission.go
//     buildCoreRolePerms). A grep for the literal finds nothing and concludes the
//     permission is invented; an earlier cross-check of the console against
//     grep-able declaration forms reported 48 candidates, of which 45 were core
//     permissions that exist perfectly well. A method that cannot see a
//     declaration form reports the permissions using it as fabricated, and a
//     guard with false positives gets switched off within a week.
//   - The grant decision itself is a function, not a table: RoleGrants dispatches
//     on privileged-read membership, then module-vs-core shape. Re-implementing
//     that dispatch anywhere else creates a second copy that drifts — which is
//     precisely the defect this inventory exists to detect in the console.
//
// FOUR declaration forms are collected, because any one of them missing puts real
// permissions in the "undeclared" bucket:
//
//	core        the per-role explicit sets (auth.PermissionsForRole over every role)
//	privileged  auth.PrivilegedReadPerms — gated above the read tier, deliberately
//	            absent from the per-role core sets
//	module      what each module's Permissions() DECLARES
//	route       what a mounted route actually REQUIRES — the form that produces the
//	            403, captured through a recording api.RouteRegistrar
//
// The module and route forms are distinct facts and are reported separately. A
// permission a route demands but Permissions() omits is a real declaration bug
// (main_test.go asserts the two are equal, in both directions), and collapsing the
// two forms into one bucket would hide it.
//
// It no longer emits a TypeScript RBAC table for the console. It used to, and the table
// was a real improvement over the hand-written mirror it replaced — but removed the
// mirror itself: the engine now hands each grant its EFFECTIVE PERMISSION SET in
// /v1/auth/whoami and the console does set membership, so there is no client-side rule
// left to generate. The inventory below is still the source of truth the console's
// permission STRINGS are checked against (scripts/check-console-perms.mjs).
//
// Usage:
//
//	go run ./tools/permsdump            # the JSON inventory, to stdout
//	go run ./tools/permsdump -o <file>  # write to a file instead
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// Role is a built-in role, in privilege order. The inventory reports a grant
// decision per role because a divergence is not "the permission is missing" but
// "the console opens it to a LOWER role than the engine does".
var roles = []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner}

// Route is one mounted module route and the permission it requires.
type Route struct {
	Method  string `json:"method"`
	Pattern string `json:"pattern"`
	Perm    string `json:"permission"`
}

// ModuleInfo is one module's declaration surface.
type ModuleInfo struct {
	Namespace   string   `json:"namespace"`
	Permissions []string `json:"permissions"`
	Routes      []Route  `json:"routes"`
}

// Declaration is one permission: where it comes from and what each role gets.
type Declaration struct {
	// Forms are the declaration forms carrying this permission, sorted:
	// "core", "privileged", "module:<ns>", "route:<ns>".
	Forms []string `json:"forms"`
	// Grants is RoleGrants(role, perm) for every built-in role — the ENGINE's
	// answer, executed, not modeled.
	Grants map[string]bool `json:"grants"`
}

// Inventory is the emitted document.
type Inventory struct {
	// Schema guards against a consumer silently reading a differently-shaped file.
	Schema string   `json:"schema"`
	Roles  []string `json:"roles"`
	// Declared maps permission -> declaration. Sorted on marshal (Go maps encode
	// with sorted string keys), so the file is byte-stable across runs.
	Declared map[string]*Declaration `json:"declared"`
	Modules  []ModuleInfo            `json:"modules"`
}

// recordingRegistrar captures what APIRoutes mounts instead of serving it. It is
// the only honest way to read the route requirement: the permission is an argument
// to Handle, so nothing short of running APIRoutes observes conditional mounts.
type recordingRegistrar struct{ out *[]Route }

func (r recordingRegistrar) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
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
func (r recordingRegistrar) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, _ api.ModuleHandler) {
	*r.out = append(*r.out, Route{Method: method, Pattern: pattern, Perm: string(perm)})
}

func main() {
	out := flag.String("o", "", "write to this file instead of stdout")
	flag.Parse()

	b, err := json.MarshalIndent(build(), "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "permsdump: %v\n", err)
		os.Exit(1)
	}
	buf := append(b, '\n')
	if *out == "" {
		os.Stdout.Write(buf)
		return
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "permsdump: %v\n", err)
		os.Exit(1)
	}
}

// build assembles the inventory from the live engine.
func build() *Inventory {
	inv := &Inventory{
		Schema:   "olivares.permissions.inventory/1",
		Roles:    roles,
		Declared: map[string]*Declaration{},
	}

	add := func(perm, form string) {
		d, ok := inv.Declared[perm]
		if !ok {
			d = &Declaration{Grants: map[string]bool{}}
			for _, r := range roles {
				// The engine decides. This is RoleGrants itself, not a model of it.
				d.Grants[r] = auth.RoleGrants(r, auth.Permission(perm))
			}
			inv.Declared[perm] = d
		}
		for _, f := range d.Forms {
			if f == form {
				return
			}
		}
		d.Forms = append(d.Forms, form)
	}

	// Form 1 — the explicit per-role CORE sets. Union over every role, because a
	// permission only owner holds (resource:admin) is declared just as much as one
	// viewer holds. This is where the concatenated permissions surface: they never
	// existed as literals, so they can only be read out of the built sets.
	for _, r := range roles {
		for _, p := range auth.PermissionsForRole(r) {
			add(string(p), "core")
		}
	}

	// Form 2 — the privileged reads, gated above the read tier and deliberately
	// absent from the per-role core sets above.
	for _, p := range auth.PrivilegedReadPerms() {
		add(string(p), "privileged")
	}

	// The system permission: declared, and granted to no tenant role by design
	// (only the superadmin flag holds it). Without this line the inventory would
	// call the console's system:* checks undeclared.
	add(string(auth.PermSystemAdmin), "core")

	// Forms 3 and 4 — what each module DECLARES and what its routes REQUIRE.
	for _, m := range allModules() {
		ns := m.APINamespace()
		info := ModuleInfo{Namespace: ns, Permissions: []string{}, Routes: []Route{}}
		for _, p := range m.Permissions() {
			info.Permissions = append(info.Permissions, string(p))
			add(string(p), "module:"+ns)
		}
		m.APIRoutes(recordingRegistrar{out: &info.Routes})
		for _, rt := range info.Routes {
			add(rt.Perm, "route:"+ns)
		}
		sort.Strings(info.Permissions)
		sort.Slice(info.Routes, func(i, j int) bool {
			a, b := info.Routes[i], info.Routes[j]
			if a.Pattern != b.Pattern {
				return a.Pattern < b.Pattern
			}
			return a.Method < b.Method
		})
		inv.Modules = append(inv.Modules, info)
	}
	sort.Slice(inv.Modules, func(i, j int) bool { return inv.Modules[i].Namespace < inv.Modules[j].Namespace })

	for _, d := range inv.Declared {
		sort.Strings(d.Forms)
	}
	return inv
}
