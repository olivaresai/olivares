// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Module is implemented by a /modules module that exposes HTTP routes. The
// engine mounts the routes under /v1/m/<namespace>/ already wrapped with
// authentication, tenant resolution and authorization, so a module route never
// sees an unauthenticated or unscoped request and cannot escape its tenant. This
// is the route half of the contract that unblocks the module sessions (B/C/D).
type Module interface {
	// APINamespace is the module's namespace; for a module that owns store entities it
	// MUST equal the module's store namespace, and it must never collide with a
	// reserved API segment. A route-only console module (one that owns no entities and
	// reuses another module's tables) may use a hyphenated REST namespace the web
	// already calls (e.g. "claude-policy"). Routes mount under /v1/m/<namespace>/.
	APINamespace() string
	// APIRoutes mounts the module's routes on reg.
	APIRoutes(reg RouteRegistrar)
	// Permissions declares the permissions the module's routes require, so roles
	// can grant them. They must be namespaced ("<namespace>:<resource>:<verb>").
	Permissions() []auth.Permission
}

// RouteRegistrar mounts a module's routes. Each route is mounted at
// /v1/m/<namespace>/<pattern> by construction — a module cannot mount outside its
// own subtree — and is wrapped so the handler runs only after tenant resolution
// and an authorization check for perm.
type RouteRegistrar interface {
	// Handle mounts handler for method and pattern, requiring perm before it runs.
	// The authorization is COLLECTION-level: no entity, no workspace, so a
	// scope-tree grant cannot match. Use HandleEntity for a route that acts on one
	// stored row.
	Handle(method, pattern string, perm auth.Permission, handler ModuleHandler)
	// HandleEntity mounts an ENTITY route: before the handler runs, the engine reads
	// the targeted row and authorizes against its STORED lineage, so a
	// workspace-scoped grant can reach it and a workspace-confined principal is held
	// to it. See EntityRef for what the module declares and why it is a declaration
	// rather than an inference.
	HandleEntity(method, pattern string, perm auth.Permission, ref EntityRef, handler ModuleHandler)
}

// EntityRef is a module's DECLARATION of how one route's entity maps onto the
// scope tree. Nothing here is inferred: a column called "workspace_ref" means a
// cost-attribution dimension in one module and the declaring principal's workspace in
// another, so deriving lineage from a column NAME would build an authorization axis out
// of whatever a schema author happened to call a field. The module says it explicitly or
// the route stays collection-level.
type EntityRef struct {
	// Kind is the store entity kind the route acts on ("<ns>.<entity>").
	Kind model.Kind
	// IDParam is the URL path parameter carrying the entity id (e.g. "id"). Exactly
	// one of IDParam and BodyIDField must be set.
	IDParam string
	// BodyIDField is the top-level JSON string field carrying the entity id for a
	// collection-shaped command whose canonical route cannot put the target in the
	// path. The engine uses it only to locate the STORED row before authorization;
	// it restores the request body byte-for-byte for the module's own strict decoder.
	// Exactly one of IDParam and BodyIDField must be set.
	BodyIDField string
	// WorkspaceColumn is the column holding the RESOLVED workspace model.ID for this
	// entity. It must be the workspace the row BELONGS to — not the workspace of
	// whoever created it, and not a billing dimension. Blank means the entity carries
	// no lineage: the route is then authorized exactly as Handle would, which is the
	// safe direction (no tree, so no tree grant matches).
	WorkspaceColumn string
	// ResourceKind overrides the auth resource kind for the request. Blank uses the
	// permission's resource segment, which is what almost every route wants.
	ResourceKind string
	// ConcealDeniedAsNotFound is an opt-in for exact point reads whose existence is
	// itself protected information. After authentication and tenant resolution, an
	// absent row or a denied authorization decision is answered as the same 404.
	// Failures to obtain the stored row needed for that decision remain 503: an
	// unavailable authority fact must never be disguised as clean absence.
	//
	// The option requires Kind so the engine can establish whether the row exists.
	// It is deliberately per-route; ordinary entity routes retain their 403 denial.
	ConcealDeniedAsNotFound bool
}

// ModuleHandler is a module route handler. It receives the authorized principal,
// the single resolved tenant, and a least-privilege data handle.
type ModuleHandler func(w http.ResponseWriter, r *http.Request, mc ModuleContext)

// ModuleContext is what a module route handler is given.
type ModuleContext struct {
	// Principal is the authenticated, authorized caller.
	Principal auth.Principal
	// Tenant is the single canonical resolved tenant for the request.
	Tenant model.TenantID
	// RecordingSession is the exact active session ID returned by the recording
	// gate for this request. It is set only for a recorded module route and lets a
	// handler atomically bind governed state to the same session Gate reserved,
	// without re-resolving by credential across a concurrency window.
	RecordingSession model.ID
	// Resource is the exact resource against which the route wrapper authorized
	// this request. Collection routes carry the permission-derived kind with no ID;
	// entity routes additionally carry the target ID and its STORED workspace.
	Resource auth.ResourceAttrs
	// Data is PINNED to Tenant: a route handler can only ever touch the single
	// tenant the request was authorized for — it cannot pass another tenant id.
	Data ScopedData
}

// ScopedData is the tenant-PINNED data handle a module ROUTE handler receives. It
// exposes View/Mutate with NO tenant parameter — every operation runs against the
// request's single authorized tenant. (Event-driven modules use the
// tenant-parameterized ModuleData via DataConsumer instead.)
type ScopedData interface {
	// View runs fn read-only against the pinned tenant.
	View(ctx context.Context, fn func(store.Scope) error) error
	// Mutate runs fn read-write against the pinned tenant.
	Mutate(ctx context.Context, fn func(store.Scope) error) error
	// Export runs fn over the pinned tenant's exportable data (its ledger and its
	// module records). It is the portability door: unlike View it survives a
	// withdrawal of service, and it is recorded on the tenant's chain when used
	// during one. A module route that is NOT giving the customer their own data
	// back must not call it.
	Export(ctx context.Context, fn func(store.ExportScope) error) error
}

// scopedData pins a store to one tenant (the request's resolved tenant).
type scopedData struct {
	st     store.Store
	tenant model.TenantID
}

func (d scopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.View(ctx, d.tenant, confineIfMarked(ctx, d.tenant, fn))
}

func (d scopedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

func (d scopedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	// Mutate is confined exactly like View, and for the same reason: after the
	// PDP has authorized a route on ONE target, the handler can still touch other
	// rows of the tenant. It is also the path several governed READS take, since
	// they self-audit in a committed transaction.
	return d.st.Mutate(ctx, d.tenant, confineIfMarked(ctx, d.tenant, fn))
}

// confineIfMarked wraps a unit-of-work callback so it receives a
// workspace-confined Scope whenever the request context carries a confinement
// for this tenant (B-03). Without the mark it returns fn unchanged, so
// every engine path — the four pumps, boot, the schedulers — keeps the raw
// tenant-pinned Scope it has always had.
//
// A confinement that cannot be RESOLVED (a membership pointing at a deleted
// workspace, a tenant whose default workspace was never seeded) fails the unit
// of work rather than running it unconfined.
func confineIfMarked(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) func(store.Scope) error {
	ws, confined := moduleBoundaryFrom(ctx, tenant)
	if !confined {
		return fn
	}
	return func(sc store.Scope) error {
		confinedScope, err := store.ConfineWorkspace(ctx, sc, ws)
		if err != nil {
			return err
		}
		return fn(confinedScope)
	}
}

// NewScopedData pins st to tenant. It is exported so the composition root can
// construct a ModuleContext for non-HTTP paths (e.g. the MCP retrieval upstream).
//
// It carries ENGINE authority: the returned handle confines only when the
// context it is used with was marked by an authorized HTTP request. The four
// in-process pumps call it with an unmarked context and are unaffected.
func NewScopedData(st store.Store, tenant model.TenantID) ScopedData {
	return scopedData{st: st, tenant: tenant}
}

// ModuleData is the least-privilege data seam a module receives. It exposes only
// tenant-scoped View/Mutate — never the cross-tenant System path, never the auth
// partition. A module can read/write within a tenant (as the engine does for
// ingest) but cannot provision, list across tenants, or reach credentials.
type ModuleData interface {
	// View runs fn in a read-only transaction pinned to tenant.
	View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error
	// Mutate runs fn in a read-write transaction pinned to tenant.
	Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error
}

// DataConsumer is the engine-side seam by which an in-process module receives its
// data handle at boot (before Start), analogous to SchemaProvider for schema. An
// out-of-process module cannot receive it (no store over the wire) and is a bus
// consumer only.
type DataConsumer interface {
	// UseData hands the module its tenant-scoped data accessor.
	UseData(ModuleData)
}

// moduleData adapts a store.Store to ModuleData, withholding System by construction.
type moduleData struct{ st store.Store }

// NewModuleData returns a least-privilege ModuleData over st (used by the engine
// boot to satisfy a module's DataConsumer seam).
func NewModuleData(st store.Store) ModuleData { return moduleData{st: st} }

// View/Mutate honor the same request-scoped confinement as ScopedData. A module
// holds this handle from boot, so it has no principal of its own; the authority
// that applies is the one carried by the context it is called with. Called from
// a background pump (an unmarked context) it is engine-authoritative as before;
// called from inside an authorized request by a handler that reached for the
// boot handle instead of mc.Data, it is confined.
func (m moduleData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return m.st.View(ctx, tenant, confineIfMarked(ctx, tenant, fn))
}

func (m moduleData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return m.st.Mutate(ctx, tenant, confineIfMarked(ctx, tenant, fn))
}

// reservedAPISegments are top-level path/namespace segments a module may not use.
var reservedAPISegments = map[string]bool{
	"v1": true, "m": true, "auth": true, "system": true, "setup": true,
	"audit": true, "agents": true, "access-edges": true, "users": true,
	"tokens": true, "memberships": true, "orgs": true, "server-info": true,
	"healthz": true, "openapi.json": true, "core": true, "scim": true,
}

// validateNamespace checks a module API namespace: a lowercase identifier, not a
// reserved segment, bounded in length (mirrors the store's module-namespace rule).
func validateNamespace(ns string) error {
	if !isModuleNamespace(ns) {
		return fmt.Errorf("api: invalid module namespace %q", ns)
	}
	if reservedAPISegments[ns] {
		return fmt.Errorf("api: module namespace %q is reserved", ns)
	}
	return nil
}

// isModuleNamespace reports whether s is a valid module API route namespace: it must
// start with a lowercase letter, contain only lowercase letters, digits, '_' or '-',
// be at most 32 chars, and not end in a separator. A '-' is permitted as an INTERNAL
// separator so a route-only console module can mount the hyphenated REST namespaces the
// web already calls (e.g. /v1/m/claude-policy, /v1/m/claude-agents —). A module
// that ALSO registers store entities keys them by a dotted Kind ("<ns>.<entity>") whose
// namespace must be a SQL-safe identifier, so such modules keep a hyphen-free namespace
// by convention (the store registry enforces its own rule); the route-only consoles own
// no entities and reuse the governance module's tables.
func isModuleNamespace(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	last := s[len(s)-1]
	if last == '-' || last == '_' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
