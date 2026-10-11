// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package permcensus records mounted module permissions and the engine's role
// grants. Both standalone and native inventories share this recorder; the
// composition root supplies the modules for its compiled edition.
package permcensus

import (
	"sort"
	"strings"

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
	Surface string `json:"surface"`
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
type recordingRegistrar struct {
	out       *[]Route
	namespace string
}

func (r recordingRegistrar) Handle(method, pattern string, perm auth.Permission, h api.ModuleHandler) {
	*r.out = append(*r.out, Route{Method: strings.ToUpper(method), Pattern: pattern, Perm: string(perm), Surface: routeSurface(r.namespace, method, pattern, h)})
}

// Optional registration doors must be visible to this recorder too. Their
// assurance, scope and response headers do not change the required permission.
func (r recordingRegistrar) HandlePolicy(method, pattern string, perm auth.Permission, _ api.RouteMetadata, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}
func (r recordingRegistrar) HandleNoStore(method, pattern string, perm auth.Permission, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}
func (r recordingRegistrar) HandleEntityNoStore(method, pattern string, perm auth.Permission, ref api.EntityRef, h api.ModuleHandler) {
	r.HandleEntity(method, pattern, perm, ref, h)
}
func (r recordingRegistrar) WithCollectionScope(_ api.CollectionScopeRef) api.RouteRegistrar {
	return r
}

var _ api.NoStoreRouteRegistrar = recordingRegistrar{}
var _ api.CollectionScopeRouteRegistrar = recordingRegistrar{}

// HandleSystem records the engine's system door without assigning tenant grants.
func (r recordingRegistrar) HandleSystem(method, pattern string, h api.ModuleHandler) {
	r.Handle(method, pattern, auth.PermSystemAdmin, h)
}

var _ api.SystemRouteRegistrar = recordingRegistrar{}

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
func (r recordingRegistrar) HandleSealed(method, pattern string, perm auth.Permission, sealed api.SealedRoute, h api.ModuleHandler) {
	r.HandlePolicy(method, pattern, perm, sealed.Metadata(), h)
}

// Build inventories the modules supplied by the engine composition root.
func Build(modules []api.Module) *Inventory {
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
	for _, m := range modules {
		ns := m.APINamespace()
		info := ModuleInfo{Namespace: ns, Permissions: []string{}, Routes: []Route{}}
		for _, p := range m.Permissions() {
			info.Permissions = append(info.Permissions, string(p))
			add(string(p), "module:"+ns)
		}
		m.APIRoutes(recordingRegistrar{out: &info.Routes, namespace: ns})
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
