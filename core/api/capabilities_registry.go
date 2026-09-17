// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// routeDescriptor is the ENGINE'S OWN record of one mounted route, filed by
// chiRegistrar.handle at registration time.
//
// ⛔ IT IS FILED FROM THE REGISTRATION AND NEVER FROM THE PUBLISHED DOCUMENT. The
// OpenAPI document is a rendering of these routes; deriving the descriptors back
// out of it would make the projection describe a SECOND table that drifts, and the
// two facts a projection cannot get wrong — which door the route was mounted
// through, and how its denial is written — do not appear in the document at all.
//
// ⛔ AND THE MODE TRAVELS AS THE REGISTRAR PASSED IT, not as something inferred from
// the metadata. A governed route whose metadata went empty would otherwise describe
// itself as ungoverned, which is exactly the silent downgrade routeGovernance exists
// as a named parameter to prevent (middleware.go).
type routeDescriptor struct {
	method   string
	pattern  string
	perm     auth.Permission
	governed routeGovernance
	meta     auth.RouteMetadata
	// entity is the route's declared EntityRef, retained WHOLE: locator, kind,
	// workspace column, resource kind and the concealment choice. Nil is a
	// collection route.
	entity *EntityRef
	// collectionScope is the workspace selector a COLLECTION route declared. Nil for
	// every route that declared none, which is every route by default.
	collectionScope *CollectionScopeRef
	// projector is the OWNING module's own capability adapter, captured at mount from
	// the very module whose APIRoutes registered this route. It is carried here rather
	// than looked up by namespace string so a projection can never be answered by
	// another module's adapter through a prefix that happens to match.
	projector ModuleCapabilityProjector
}

// routeDescriptorKey is the public identity of an operation: "METHOD <spec path>".
// It is the same string the DTO accepts, and it is built from the mount layout
// rather than typed twice.
func routeDescriptorKey(method, pattern string) string {
	return strings.ToUpper(method) + " " + pattern
}

// declareRouteDescriptor files one mounted route under its FULL spec path.
//
// The namespace prefix is composed exactly as declareRouteResponseHeaders composes
// it, and for the same reason: mountModules mounts every module at /v1/m/<ns> and
// nowhere else, so a key built any other way would silently match nothing.
func (cr chiRegistrar) declareRouteDescriptor(
	method, pattern string,
	perm auth.Permission,
	ref *EntityRef,
	meta auth.RouteMetadata,
	governed routeGovernance,
) {
	if cr.s == nil {
		return
	}
	full := canonicalRoutePattern("/v1/m/" + cr.ns + pattern)
	descriptor := routeDescriptor{
		method: strings.ToUpper(method), pattern: full, perm: perm,
		governed: governed, meta: meta, projector: cr.projector,
	}
	if ref != nil {
		copied := *ref
		descriptor.entity = &copied
	} else if cr.collectionScope != nil {
		// ⛔ ONLY A COLLECTION CARRIES IT. An entity route resolves its workspace from
		// the STORED row (entityResource), so attaching a request-named selector to it
		// would offer a second, caller-controlled answer to a question the store has
		// already answered — the one substitution EntityRef.WorkspaceColumn exists to
		// refuse.
		copied := *cr.collectionScope
		descriptor.collectionScope = &copied
	}
	if cr.s.routeDescriptors == nil {
		cr.s.routeDescriptors = map[string]routeDescriptor{}
	}
	cr.s.routeDescriptors[routeDescriptorKey(descriptor.method, full)] = descriptor
}

// routeDescriptorFor returns the descriptor for a public operation identity.
func (s *Server) routeDescriptorFor(operation string) (routeDescriptor, bool) {
	if s == nil || s.routeDescriptors == nil {
		return routeDescriptor{}, false
	}
	descriptor, ok := s.routeDescriptors[operation]
	return descriptor, ok
}

// ---------------------------------------------------------------------------
// Corroborated collection workspace
// ---------------------------------------------------------------------------

// workspaceAdmission is the classified result of corroborating a requested
// collection workspace. It is deliberately NOT an http status and NOT an error the
// caller can write straight to the wire: the resolver keeps its reasons internal and
// the caller decides one public shape for the whole non-admissible family.
type workspaceAdmission int

const (
	// workspaceAdmissionInvalid is a malformed or non-canonical selector. It is
	// decided from the request alone, with NO row read.
	workspaceAdmissionInvalid workspaceAdmission = iota
	// workspaceAdmissionNotAdmissible is a scope this caller may not be admitted to:
	// a known absence, a known non-active status, or a confinement the selector
	// crosses. It is NOT unknown, and the root amendment requires it to share the
	// collection's own generic refusal.
	workspaceAdmissionNotAdmissible
	// workspaceAdmissionUnknown is authority that could not be established: a real
	// store failure or an id/tenant integrity mismatch. It is never recoded as a
	// known denial or as absence.
	workspaceAdmissionUnknown
	// workspaceAdmissionOK is a corroborated, active, in-tenant workspace.
	workspaceAdmissionOK
)

// corroborateActiveWorkspace establishes that a REQUESTED workspace selector names a
// current, active workspace of this tenant that the caller's confinement permits,
// using the engine's own store authority — before any authorization decision is made
// for a collection route.
//
// ⛔ IT IS NOT communicationSelectedScope MOVED. That helper needs an api.ModuleContext
// and mc.Data.View, both of which are installed AFTER authorization (server.go handle),
// so it cannot run before the PEP; and fabricating a privileged ModuleContext to reach
// it would hand a pre-authorization path the module's confined data handle. This runs on
// the authenticated request context with the engine Store, takes the tenant the shared
// resolver already resolved, and returns a classification rather than a response.
//
// ⛔ AND IT NEVER WRITES HTTP. The public shape of the non-admissible family is decided
// by ONE caller (handleScopedCollection) so that a known absence, a known non-active
// workspace and a known outer denial cannot drift into three distinguishable answers —
// which is what the ratified amendment forbids and what a resolver that wrote its own
// 503 and 403 produced.
func (s *Server) corroborateActiveWorkspace(
	ctx context.Context,
	p auth.Principal,
	tenant model.TenantID,
	raw string,
) (model.ID, workspaceAdmission) {
	workspace, err := model.ParseID(raw)
	if err != nil || workspace.IsZero() || workspace.String() != raw {
		return "", workspaceAdmissionInvalid
	}
	if confined, ok := p.ConfinedWorkspaceIn(tenant); ok && !p.Superadmin && confined != workspace {
		return "", workspaceAdmissionNotAdmissible
	}
	admission := workspaceAdmissionUnknown
	viewErr := s.st.View(ctx, tenant, func(sc store.Scope) error {
		current, err := sc.Workspaces().Get(ctx, workspace)
		if errors.Is(err, store.ErrNotFound) {
			// A KNOWN absence is a scope nobody can be admitted to. It is not
			// "I could not look", and the amendment says so in as many words.
			admission = workspaceAdmissionNotAdmissible
			return nil
		}
		if err != nil {
			admission = workspaceAdmissionUnknown
			return nil
		}
		if current.ID != workspace || current.TenantID != tenant {
			// An integrity mismatch between what was asked for and what came back is
			// authority we could not establish. Recoding it as absence would invent a
			// fact; recoding it as a denial would invent a decision.
			admission = workspaceAdmissionUnknown
			return nil
		}
		if current.Status != model.StatusActive {
			admission = workspaceAdmissionNotAdmissible
			return nil
		}
		admission = workspaceAdmissionOK
		return nil
	})
	if viewErr != nil {
		return "", workspaceAdmissionUnknown
	}
	if admission != workspaceAdmissionOK {
		return "", admission
	}
	return workspace, workspaceAdmissionOK
}

// collectionScopeSelector reads the ONE canonical occurrence of a declared workspace
// selector from a request's query.
//
// Cardinality is part of the FORM, so it is judged here — before any lookup — and a
// repeated parameter is invalid rather than silently first-wins. Absence is invalid
// too: the three pilot collections require the selector, and treating an absent one as
// "the default workspace" would authorize a scope the caller never named.
func collectionScopeSelector(r *http.Request, scope CollectionScopeRef) (string, bool) {
	if scope.WorkspaceQueryParam == "" {
		return "", false
	}
	values, ok := r.URL.Query()[scope.WorkspaceQueryParam]
	if !ok || len(values) != 1 {
		return "", false
	}
	return values[0], true
}
