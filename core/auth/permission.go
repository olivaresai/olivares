// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "strings"

// Permission is a "<resource>:<verb>" string for a core resource (e.g.
// "agent:read") or a "<namespace>:<resource>:<verb>" string for a module
// resource (e.g. "rrw:access_path:read"). The verb is always the last segment
// and is one of read, write or admin. Strings (not an enum) keep the set open so
// a module can declare its own permissions without an engine release.
type Permission string

// The permission verbs, in increasing privilege.
const (
	// VerbRead is read/list access.
	VerbRead = "read"
	// VerbWrite is create/update/delete access.
	VerbWrite = "write"
	// VerbAdmin is administrative access (manage accounts, settings, lifecycle).
	VerbAdmin = "admin"
)

// The built-in roles, in increasing privilege. They are code-defined for the
// base; custom per-tenant roles are a documented later extension.
const (
	// RoleViewer can read.
	RoleViewer = "viewer"
	// RoleEditor can read and write operational entities.
	RoleEditor = "editor"
	// RoleAdmin can additionally manage tenant IAM and settings.
	RoleAdmin = "admin"
	// RoleOwner has every tenant permission.
	RoleOwner = "owner"
)

// IsRole reports whether r is a known built-in role.
func IsRole(r string) bool {
	switch r {
	case RoleViewer, RoleEditor, RoleAdmin, RoleOwner:
		return true
	default:
		return false
	}
}

// RoleRank is the privilege ordering of the built-in roles
// (viewer < editor < admin < owner); an unknown role ranks 0. It is the basis of
// the role ceiling: a principal may never grant or mint a role above its own.
func RoleRank(r string) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	default:
		return 0
	}
}

// Verb returns the permission's trailing verb segment.
func (p Permission) Verb() string {
	s := string(p)
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// Resource returns the permission's resource segment: the segment just before the
// trailing verb ("agent:read" -> "agent", "rrw:access_path:read" -> "access_path").
// It is the resource KIND the ABAC/PDP evaluators key on (populated into
// Request.Resource on the request path, IDN-09). It is "" for a verb-less string.
func (p Permission) Resource() string {
	s := string(p)
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return ""
	}
	prefix := s[:i]
	if j := strings.LastIndexByte(prefix, ':'); j >= 0 {
		return prefix[j+1:]
	}
	return prefix
}

// IsModule reports whether the permission is namespaced (module-declared):
// "<namespace>:<resource>:<verb>" has two colons, a core permission has one.
func (p Permission) IsModule() bool {
	return strings.Count(string(p), ":") >= 2
}

// PermSystemAdmin is the system-level permission: provisioning tenants,
// cross-tenant operations, global verification. Only a superadmin holds it.
const PermSystemAdmin Permission = "system:admin"

// PermIngestWrite authorizes pushing observations into the estate over the
// distributed collector→core ingest plane (CB-1 option C). It is a privileged,
// infrastructure-level write — a collector injects estate-wide facts — so it is
// granted at ADMIN tier and up, never to an ordinary editor, on top of the
// mandatory collector mTLS client certificate (docs/SECURITY-HARDENING.md, §3). A leaked editor
// token therefore cannot forge ingestion.
const PermIngestWrite Permission = "ingest:write"

// PermAuthzRead gates the authorization-query surface: the AuthZEN PDP
// evaluation + batch endpoints and the reverse/enumeration search endpoints
// (subject/resource/action — "who can access R" / "what can X access"). It is a
// PRIVILEGED read: probing the authorization decision for arbitrary subjects
// reconstructs the access matrix, recon-relevant exactly like the access graph
// (docs/SECURITY-HARDENING.md), so it is granted from editor up and NEVER to the lowest viewer
// role (privilegedReadPerms), on top of the bearer auth a PEP/reviewer presents.
const PermAuthzRead Permission = "authz:read"

// PermAuthzAdmin gates the sealed access-review EXPORT: a bulk
// reconstruction of who-can-access-a-resource, sealed into the audit ledger with a
// content digest. It is an admin-tier privileged action (granted to admin/owner),
// and the HTTP route additionally requires an AAL3 step-up.
const PermAuthzAdmin Permission = "authz:admin"

// coreResources are the engine resources gated by a permission, granted by the
// generic read/write/admin tiers below. The ledger is gated as "audit"; tenant
// IAM as user/membership/token; tenant settings as "tenant". The access graph is
// deliberately NOT in this list: viewing it is a privileged read gated above the
// viewer tier (see privilegedReadPerms / RoleGrants), not an ordinary entity
// read, because the R/RW map is the most sensitive asset (docs/SECURITY-HARDENING.md, §8).
var coreReadWriteResources = []string{
	"agent", "session", "provider", "model", "mcp_server", "skill", "tool",
	"resource", "identity", "policy", "cost", "eval", "finding", "health",
	"deployment",
}

// privilegedReadPerms are the read permissions gated ABOVE the generic read
// tier: no ROLE below editor confers them — on top of any per-read self-audit.
// Listed explicitly (not by a naming convention) so the privileged set is auditable
// and a module cannot accidentally widen it; without this gate a module-namespaced
// read would fall through the module verb tier to viewers (roleGrantsVerb →
// viewer=read).
//
// the exact scope of that guarantee, stated precisely because the code used to
// claim more than it enforces. This gate binds the ROLE TIER: it is why holding
// `viewer` never carries one of these. It is NOT an absolute ceiling on the principal.
// A tenant admin — who does hold them — may confer one on a viewer-tier user through an
// explicit, audited scoped grant clamped by canDelegate, and since made module
// permissions grantable that now includes the module-namespaced entries here. That is
// the intended meaning of delegable RBAC: an authority may hand out a subset of what it
// holds. What must never happen is a role acquiring one implicitly, and that is what
// this list prevents.
//
// The access-graph trio: viewing the agent→resource R/RW map (and its
// permitted-vs-observed drift) is a recon-relevant action on the product's most
// sensitive asset (docs/SECURITY-HARDENING.md, §8). The core "accessgraph:read" (legacy handler
// view) and the access-map module's "accessmap:graph:read"/"accessmap:drift:read"
// (module III, the canonical graph) are the same product capability.
//
// "security:observed:read" gates receiving guardrail.observed events
// through the eventing platform: the payload is a redacted excerpt of OBSERVED
// AGENT TEXT (sdk/event.ObservedText) — already producer-redacted and bounded,
// but still the most content-like fact on the bus, so it never flows to a
// viewer-tier consumer. No HTTP route uses it today; the eventing module's
// per-event RBAC filter is its consumer.
// accessGraphReconPerms are the tenant-wide access-MATRIX reads a workspace-confined
// principal must be DENIED (F2): the access graph, its permitted-vs-observed drift, the
// attack-path views and the who-can-access reverse query all reconstruct the FULL
// cross-workspace matrix and cannot be row-filtered to one workspace (AccessEdge has no
// workspace_id; origins may be identities/sessions). A confined operator has no scoped view of
// them, so the scoped-authz engine forbids these perms when the principal is confined — which
// covers every route that uses them (the core /v1/access-edges, the access-map module's
// /graph|/neighbors|/drift|/attack-paths, and the authz reverse query) in one place.
var accessGraphReconPerms = map[Permission]struct{}{
	"accessgraph:read":     {},
	"accessmap:graph:read": {},
	"accessmap:drift:read": {},
	"authz:read":           {},
}

// IsAccessGraphReconPerm reports whether perm is a tenant-wide access-matrix recon read a
// workspace-confined principal must not perform (F2).
func IsAccessGraphReconPerm(perm Permission) bool {
	_, ok := accessGraphReconPerms[perm]
	return ok
}

var privilegedReadPerms = map[Permission]struct{}{
	"accessgraph:read":       {},
	"accessmap:graph:read":   {},
	"accessmap:drift:read":   {},
	"security:observed:read": {},
	// The authorization-query surface: querying the PDP about arbitrary
	// subjects / enumerating who-can-access-what reconstructs the access matrix, the
	// same recon concern as the access graph above — editor and up, never viewer.
	"authz:read": {},
	// The identity/federation POSTURE surface: the WIF object graph, the SSO connection
	// state, the External Keys (CMEK) inventory and workspace residency. It is NEGATIVE
	// security posture — which workspaces have NO customer-managed key, i.e. where data
	// is provider-encrypted — and a map of where the weak link is is the most useful
	// thing an attacker can read. Same recon concern as the access graph and the
	// authorization-query surface above: editor and up, never the lowest viewer role.
	//
	// Deliberately NOT governance:identity:read, which gates the NHI ROSTER and is
	// viewer-tier by design (modules/governance/roster_identity_test.go:284 asserts a
	// viewer can read it). Promoting that one would have broken a documented consumer to
	// protect a surface it does not cover.
	"governance:idposture:read": {},
	// The Claude Code per-developer ROI drill-down exposes the developer email + their
	// productivity. Deny-closed for the lowest viewer role (the team/org adoption
	// aggregates ride the ordinary viewer-read tier); editor and above by default, and an
	// org tightens it via custom roles.: per-team default, per-developer opt-in.
	"adoption:developer:read": {},
}

// coreRolePerms is the explicit permission set granted by each built-in role for
// CORE permissions. Module permissions are granted by verb tier (see RoleGrants).
var coreRolePerms = buildCoreRolePerms()

func buildCoreRolePerms() map[string]map[Permission]struct{} {
	viewer := map[Permission]struct{}{}
	for _, r := range coreReadWriteResources {
		viewer[Permission(r+":"+VerbRead)] = struct{}{}
	}
	// Viewers may read the evidence ledger but not enumerate IAM accounts.
	viewer["audit:read"] = struct{}{}
	viewer["tenant:read"] = struct{}{}

	editor := cloneSet(viewer)
	for _, r := range coreReadWriteResources {
		editor[Permission(r+":"+VerbWrite)] = struct{}{}
	}

	admin := cloneSet(editor)
	for _, r := range []string{"user", "membership", "token", "tenant"} {
		admin[Permission(r+":"+VerbRead)] = struct{}{}
		admin[Permission(r+":"+VerbWrite)] = struct{}{}
	}
	admin["tenant:admin"] = struct{}{}
	// Pushing observations from a collector is an admin-tier infrastructure write
	// (CB-1 option C), gated additionally by the collector's mTLS client cert.
	admin[PermIngestWrite] = struct{}{}
	// The sealed access-review export: an admin-tier privileged action; the
	// route also requires an AAL3 step-up. (authz:read — the query surface — is a
	// privileged read granted to editor and up via privilegedReadPerms.)
	admin[PermAuthzAdmin] = struct{}{}

	owner := cloneSet(admin)
	// Owner additionally holds admin verb on every read/write resource.
	for _, r := range coreReadWriteResources {
		owner[Permission(r+":"+VerbAdmin)] = struct{}{}
	}

	return map[string]map[Permission]struct{}{
		RoleViewer: viewer, RoleEditor: editor, RoleAdmin: admin, RoleOwner: owner,
	}
}

func cloneSet(in map[Permission]struct{}) map[Permission]struct{} {
	out := make(map[Permission]struct{}, len(in)+8)
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}

// roleVerbTier reports the highest verb a role grants for MODULE permissions:
// viewer→read, editor→write (implies read), admin/owner→admin (implies all).
func roleGrantsVerb(role, verb string) bool {
	switch role {
	case RoleViewer:
		return verb == VerbRead
	case RoleEditor:
		return verb == VerbRead || verb == VerbWrite
	case RoleAdmin, RoleOwner:
		return verb == VerbRead || verb == VerbWrite || verb == VerbAdmin
	default:
		return false
	}
}

// RoleGrants reports whether a built-in role grants a permission. Core
// permissions consult the explicit per-role set (precise); module permissions
// are granted by verb tier so a module author who declares "ns:thing:read" gets
// it granted to viewers without an engine change. The system permission is never
// granted by any tenant role (only the superadmin flag holds it).
func RoleGrants(role string, perm Permission) bool {
	if perm == PermSystemAdmin {
		return false
	}
	// Privileged reads (docs/SECURITY-HARDENING.md): granted from editor up and never to the
	// lowest viewer role, regardless of the generic read/verb tier. Checked before
	// the module/core split so the module-namespaced entries do not fall through
	// roleGrantsVerb's viewer=read default.
	if _, ok := privilegedReadPerms[perm]; ok {
		return RoleRank(role) >= RoleRank(RoleEditor)
	}
	if perm.IsModule() {
		return roleGrantsVerb(role, perm.Verb())
	}
	set, ok := coreRolePerms[role]
	if !ok {
		return false
	}
	_, ok = set[perm]
	return ok
}

// PrivilegedReadPerms returns the sorted privileged reads: the permissions
// RoleGrants gates ABOVE the generic read tier (editor and up, never viewer),
// checked before the core/module split. They are deliberately absent from
// PermissionsForRole, which enumerates only the per-role CORE set — so without
// this accessor the privileged set is invisible to any out-of-package consumer
// and an inventory built from PermissionsForRole alone would report them as
// undeclared. The returned slice is a copy; the set itself is immutable.
//
// It exists for the console↔engine sync guard: the console mirrors these
// decisions client-side, and an inventory that cannot SEE a declaration form
// reports the permissions using it as invented. A guard with false positives
// gets switched off.
func PrivilegedReadPerms() []Permission {
	out := make([]Permission, 0, len(privilegedReadPerms))
	for p := range privilegedReadPerms {
		out = append(out, p)
	}
	sortPerms(out)
	return out
}

// PermissionsForRole returns the sorted core permissions a role grants (for
// display / docs). It does not enumerate module permissions (open set) nor the
// gated privileged reads (privilegedReadPerms) — those are granted by
// RoleGrants outside the per-role core set.
func PermissionsForRole(role string) []Permission {
	set := coreRolePerms[role]
	out := make([]Permission, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sortPerms(out)
	return out
}

// scopeableKindSet is the membership index of the scope-grantable resource kinds
// (coreReadWriteResources), built once.
var scopeableKindSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(coreReadWriteResources))
	for _, k := range coreReadWriteResources {
		m[k] = struct{}{}
	}
	return m
}()

// TreeScopeableKinds returns the resource kinds that live in the SCOPE TREE
// (workspace → agent-group → resource/folder): the engine resources whose stored row the
// scope resolver can walk, so a `resource in Workspace::…` condition can actually match
// them. IAM kinds (user/membership/token) and the system permission are deliberately not
// here — their resources are not in the tree.
//
// It is the catalog a grant's resource-CLASS filter is validated against, and it is
// NARROWER than ScopeableKinds: a module permission is grantable (a custom role may
// confer it) without being a tree node. Conflating the two is what made the whole module
// surface undelegable — the old single catalog answered "may a grant target this kind?"
// with the answer to "does this kind live in the tree?". The returned slice is a copy.
func TreeScopeableKinds() []string {
	out := make([]string, len(coreReadWriteResources))
	copy(out, coreReadWriteResources)
	return out
}

// IsTreeScopeableKind reports whether kind is an scope-tree resource kind.
func IsTreeScopeableKind(kind string) bool {
	_, ok := scopeableKindSet[kind]
	return ok
}

// ScopeableKinds returns every kind a custom role or scoped grant may confer permissions
// on: the tree kinds (TreeScopeableKinds) plus the module permission kinds a mounted
// module registered (module_catalog.go). Sorted, tree kinds first in their canonical
// order so an existing console rendering is unchanged at the head of the list.
func ScopeableKinds() []string {
	mod := ModuleScopeableKinds()
	out := make([]string, 0, len(coreReadWriteResources)+len(mod))
	out = append(out, coreReadWriteResources...)
	return append(out, mod...)
}

// IsScopeableKind reports whether kind may appear in a granted permission: an tree
// kind, or a module kind ("<ns>:<res>") a mounted module registered. It is deny-closed on
// an empty registry — an engine that registers no module keeps exactly the tree catalog.
func IsScopeableKind(kind string) bool {
	if _, ok := scopeableKindSet[kind]; ok {
		return true
	}
	return isRegisteredModuleKind(kind)
}

// RoleResourcePerms returns a built-in role's permissions RESTRICTED to the
// scopeable catalog: the precise set projects when a scope grant confers a
// built-in role within a workspace/agent-group/resource-class, and the set the
// delegation ceiling treats as a tenant admin/owner's grantable domain. It is
// coreRolePerms[role] filtered to the tree kinds (so the IAM and tenant/audit
// permissions are dropped) PLUS every registered module permission the role grants.
//
// The module half is what makes a module surface delegable at all. Without it
// the ceiling's permSubset test could never be satisfied for a module permission — an
// admin's domain would not contain it — so widening the catalog alone would have
// produced an authoring surface whose every write is rejected by canDelegate. It uses
// RoleGrants, the SAME predicate the live RBAC layer applies, so a projected grant of
// "editor" confers exactly what the editor role confers: privileged reads
// (privilegedReadPerms) stay editor-and-up, and the verb tier decides the rest. No
// over-grant, no under-grant.
//
// An unknown role yields an empty slice (deny-closed: RoleGrants is false for it, and
// coreRolePerms has no entry). Sorted.
func RoleResourcePerms(role string) []Permission {
	set := coreRolePerms[role]
	out := make([]Permission, 0, len(set))
	for p := range set {
		if IsTreeScopeableKind(p.Resource()) {
			out = append(out, p)
		}
	}
	for _, p := range ModulePermissions() {
		if RoleGrants(role, p) {
			out = append(out, p)
		}
	}
	sortPerms(out)
	return out
}
