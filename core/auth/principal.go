// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"sort"

	"github.com/olivaresai/olivares/core/model"
)

// PrincipalKind classifies an authenticated identity.
type PrincipalKind string

// The principal kinds.
const (
	// KindUser is a human operator authenticated by a session.
	KindUser PrincipalKind = "user"
	// KindToken is a programmatic caller authenticated by an API token.
	KindToken PrincipalKind = "token"
)

// PrincipalRef is the opaque, version-bound reference to the exact durable
// credential from which an authenticated request principal was built. Its
// fields deliberately remain private: callers may retain and compare a ref,
// but cannot manufacture, rebind, or serialize one from request data.
//
// A zero PrincipalRef is invalid. Ref validates the full shape again before it
// ever returns one, so a malformed in-memory Principal cannot turn into a
// reusable credential handle.
type PrincipalRef struct {
	kind         PrincipalKind
	credentialID model.ID
	version      int64
}

// Principal is the authenticated identity of a request. It carries everything
// authorization needs WITHOUT another store read: the tenant→role grants, the
// tenant→group memberships, the superadmin flag, and the audit-actor string. A
// session principal carries the user's full membership set (roles AND directory
// groups); a token principal carries only its single bound grant (least
// privilege — a token is never the user's whole membership set, so it carries no
// group memberships either).
type Principal struct {
	// Kind is whether this is a user (session) or a token.
	Kind PrincipalKind
	// UserID is the acting user (for a token: its owner; may be zero for a
	// standalone system token).
	UserID model.ID
	// CredID is the credential's id: the session id (KindUser) or token id
	// (KindToken). It is the revocation handle and part of the audit actor.
	CredID model.ID
	// Superadmin grants the system role: cross-tenant operations and provisioning.
	Superadmin bool
	// DisplayName is a non-sensitive label for UI/logs.
	DisplayName string
	// AAL is the EFFECTIVE authenticator assurance level of the credential
	// (NIST SP 800-63B-4 vocabulary, target standard — no conformance
	// claim): 1 = single factor, 3 = a phishing-resistant hardware ceremony
	// (WebAuthn UV / PIV) this engine verified and whose step-up freshness
	// window is still open. It is 0 for a non-session principal (an API token
	// carries NO human assurance) — consumers treat anything below their floor
	// as not elevated, so the zero value fails closed.
	AAL int
	// AMR lists the authentication methods used on the session ("pwd", "sso",
	// "webauthn", "piv"); empty for token principals and legacy sessions.
	AMR []string

	// AgentIdentity is the authenticated agent/NHI external_id, populated when
	// the credential identifies an agent (e.g. a token whose subject maps to an
	// agent row). When non-empty, the source-scope resolver uses it as the
	// effective actor reference instead of a caller-declared agent_ref — closing
	// the confused-deputy path. Empty for a human session or a
	// token with no agent binding.
	AgentIdentity string

	// SessionIdentity is the authenticated canonical session SID carried by
	// a server-issued session credential. It is deliberately separate from
	// CredID (the API-token revocation handle) and from AgentIdentity (the wider
	// NHI identity): neither one proves which sibling session is calling.
	SessionIdentity string
	// SessionWorkspaceID is the exact core authz workspace bound into a
	// server-issued communication-session credential. It is deliberately not a
	// sessions.workspace filesystem-root id. The principal is also confined to
	// this value so module/store reads cannot widen it to the tenant.
	SessionWorkspaceID model.ID
	// SessionRunRef is the exact supervised runtime generation bound into a
	// server-issued runtime credential. Work-session parses it from its legacy
	// private Name shape; communication-session reads its dedicated server-authored
	// column. It is never accepted from request input. Sessions uses it to reject a
	// holder_run_ref for a different run even when that run belongs to the same
	// SID or agent.
	SessionRunRef string
	// SessionFence is the exact admission-Claim generation that authorized a
	// server-issued runtime credential. A resumed/taken-over SID has a
	// higher fence, so a bearer from the dead generation cannot revive merely
	// because the canonical SID is live again.
	SessionFence int64

	// grants maps each tenant the principal may act in to its role there. For a
	// token principal it holds at most one entry (the bound tenant).
	grants map[model.TenantID]string

	// groups maps each tenant to the directory groups the principal is a GATED
	// member of there: the group ids (model.UserGroup.ID as strings), including
	// every ancestor reached through group nesting (S256). It is the subject-side
	// of the authorization graph — buildPrincipalEntity turns each id into a Cedar
	// `Group::"<id>"` principal parent so a scoped grant whose subject is the group
	// (or any group it is nested under) matches every member. A group appears here
	// ONLY where the user holds a direct membership in the group's tenant — the
	// SAME deny-closed gate loadGrants applies to a group's MappedRole, so an IdP
	// roster push never admits a non-member. Empty for a token principal (least
	// privilege) and for a synthetic ScopedPrincipal.
	groups map[model.TenantID][]string

	// audiences is the set of resource indicators / audiences a delegated
	// (token-exchanged) credential is bound to (RFC 8707/8693). It is empty for a
	// session and for an ordinary (non-exchanged) token. A resource server checks
	// HasAudience before honoring the token, which defeats the confused-deputy
	// attack. ActAs is the principal a delegated token acts on behalf of.
	audiences []string
	actAs     model.ID

	// confined maps each tenant where the principal's membership is CONFINED to a
	// workspace (Membership.WorkspaceID) to that workspace id. A confined
	// principal holds its tenant role only WITHIN that workspace: the engine forbids
	// any action targeting a different workspace, overriding the tenant-wide RBAC grant.
	// A workspace-scoped SESSION membership populates it through loadGrants. The
	// private communication-session parser also populates it from the token's
	// server-authored WorkspaceID. A tenant-wide membership, an ordinary token
	// and a synthetic principal carry none. Empty is the historical, unconfined
	// behavior — so this is back-compat.
	confined map[model.TenantID]model.ID

	// restricted is a hard, server-authored permission ceiling for a
	// purpose-specific bearer. Unlike a role or a scoped grant, it can only
	// narrow: Authorizer refuses every permission absent from this set before a
	// positive scoped grant is considered. Ordinary principals leave it nil.
	restricted map[model.TenantID]map[Permission]struct{}

	// localVia / localSubject / localMeta identify a LOCAL OPERATOR: somebody
	// acting through a locally authenticated privileged path (the CLI, boot
	// seeding, a host SIGHUP) rather than through an account. They are unexported
	// and set only by NewLocalOperator (localoperator.go), so a caller cannot
	// assert an attribution by writing a struct literal — which is exactly how
	// five distinct authorities came to share one anonymous audit subject.
	localVia     string
	localSubject string
	localMeta    map[string]any
	// localSystem marks a local path NO human triggered (boot seeding, a host
	// SIGHUP). It changes ActorKind to system, so an auditor filtering for human
	// privileged activity does not find an event nobody performed.
	localSystem bool

	// credentialRef is set only by Authenticate after it has resolved the exact
	// durable session/token row. Synthetic, lookup, PEP, local and delegation
	// principals therefore have no reusable credential reference.
	credentialRef PrincipalRef
	// evidence carries private, tenant-bound provenance installed only by
	// ResolvePrincipalScope. AuthorizeEvidence consumes it to attest a candidate
	// positive core decision, but product routes and readiness remain OFF until
	// the later K3 composition cut.
	evidence principalEvidenceProvenance
}

// Ref returns the opaque, exact credential reference for an authenticated
// session or API-token principal. It returns false for every synthetic or
// otherwise unbound principal, including local, lookup, PEP and delegation
// identities.
func (p Principal) Ref() (PrincipalRef, bool) {
	ref := p.credentialRef
	if p.Kind != KindUser && p.Kind != KindToken ||
		ref.kind != p.Kind || ref.credentialID != p.CredID || !validPrincipalRef(ref) {
		return PrincipalRef{}, false
	}
	return ref, true
}

// HasAudience reports whether this principal's credential is bound to target. A
// token with NO audience binding (a session, or an ordinary token) is unbound and
// HasAudience always reports false: an unbound credential is never accepted by a
// resource server that requires audience binding. A resource server that does not
// require binding simply does not call this.
func (p Principal) HasAudience(target string) bool {
	for _, a := range p.audiences {
		if a == target {
			return true
		}
	}
	return false
}

// Audiences returns the resource indicators / audiences this credential is bound
// to (empty when unbound).
func (p Principal) Audiences() []string {
	out := make([]string, len(p.audiences))
	copy(out, p.audiences)
	return out
}

// ActAs returns the principal this (delegated) credential acts on behalf of, and
// whether it is a delegation at all.
func (p Principal) ActAs() (model.ID, bool) { return p.actAs, !p.actAs.IsZero() }

// Actor returns the audit-actor string for this principal: "user:<id>",
// "token:<id>", or "local:<path>:<subject>" for a locally authenticated
// privileged operation — never a secret, never an email (docs/SECURITY-HARDENING.md,
// model.AuditEvent).
//
// It is total because many callers cannot handle an error here. The path that
// must NOT be total is the one that writes evidence: use AttributableActor there,
// which refuses a principal that cannot name anybody (localoperator.go).
func (p Principal) Actor() string {
	if p.IsLocalOperator() {
		return p.localActorString()
	}
	if p.Kind == KindToken {
		return "token:" + p.CredID.String()
	}
	return "user:" + p.UserID.String()
}

// ActorKind returns the audit actor-kind for this principal.
func (p Principal) ActorKind() string {
	if p.localSystem {
		return model.ActorSystem
	}
	if p.Kind == KindToken {
		return "token"
	}
	return model.ActorUser
}

// WithAgentIdentity returns a copy of the principal carrying the given
// authenticated agent identity. The resolver uses it to override a
// caller-declared agent_ref (confused-deputy hardening).
func (p Principal) WithAgentIdentity(agentID string) Principal {
	p.AgentIdentity = agentID
	return p
}

// WithActAs returns a copy of the principal carrying the user this delegated
// credential acts on behalf of.
func (p Principal) WithActAs(userID model.ID) Principal {
	p.actAs = userID
	return p
}

// RoleIn returns the principal's role in tenant, and whether it has any.
func (p Principal) RoleIn(tenant model.TenantID) (string, bool) {
	r, ok := p.grants[tenant]
	return r, ok
}

// ConfinedWorkspaceIn reports the workspace the principal's membership in tenant is
// confined to (Membership.WorkspaceID), and whether it is confined at all. A
// confined principal may act ONLY within that workspace — the scoped engine forbids
// any action targeting another workspace, overriding its tenant-wide RBAC role. A
// tenant-wide membership (WorkspaceID zero), an ordinary token, and a synthetic
// principal are never confined (ok=false). A private communication-session token
// is the exception: its durable server-authored workspace is a hard confinement.
func (p Principal) ConfinedWorkspaceIn(tenant model.TenantID) (model.ID, bool) {
	ws, ok := p.confined[tenant]
	return ws, ok && !ws.IsZero()
}

// withConfinements returns a copy of the principal carrying the per-tenant workspace
// confinements resolved from server-authoritative facts (a defensive copy;
// empty leaves the principal unconfined). It is unexported: loadGrants supplies
// membership confinements and the private communication-session parser supplies
// its durable token binding; request input can supply neither.
func (p Principal) withConfinements(confined map[model.TenantID]model.ID) Principal {
	if len(confined) == 0 {
		return p
	}
	cp := make(map[model.TenantID]model.ID, len(confined))
	for k, v := range confined {
		cp[k] = v
	}
	p.confined = cp
	return p
}

// withRestrictedPermissions returns a purpose-specific principal whose
// authority is capped to the exact permissions supplied for tenant. It is kept
// private so request input can never manufacture this ceiling or its grants.
func (p Principal) withRestrictedPermissions(tenant model.TenantID, permissions ...Permission) Principal {
	set := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		if permission != "" {
			set[permission] = struct{}{}
		}
	}
	p.restricted = map[model.TenantID]map[Permission]struct{}{tenant: set}
	return p
}

// restrictedPermission reports whether p is purpose-restricted and, when it
// is, whether the exact tenant+permission pair is inside its hard ceiling.
func (p Principal) restrictedPermission(tenant model.TenantID, permission Permission) (bool, bool) {
	if p.restricted == nil {
		return false, false
	}
	set, ok := p.restricted[tenant]
	if !ok {
		return true, false
	}
	_, allowed := set[permission]
	return true, allowed
}

// IsWorkSessionCredential reports whether this principal was authenticated
// from the server-issued, exact-session work credential. It does not grant
// authority; Authorizer enforces the private hard ceiling above. The sessions
// handler uses this marker only to narrow its shared work:write route to fenced
// execution commands.
func (p Principal) IsWorkSessionCredential() bool {
	restricted, lease := p.restrictedPermission(singleRestrictedTenant(p), WorkSessionLeaseWrite)
	_, work := p.restrictedPermission(singleRestrictedTenant(p), WorkSessionWorkWrite)
	return restricted && lease && work && p.SessionIdentity != "" && p.SessionRunRef != "" &&
		p.SessionFence > 0
}

// IsCommunicationSessionCredential reports whether this principal was built
// from the dedicated communication runtime credential with its exact immutable
// four-permission ceiling and durable session binding. It is a marker only;
// Authorizer and the sessions service still enforce permission, grant, resource,
// causal-recipient and live-Claim guards independently.
func (p Principal) IsCommunicationSessionCredential() bool {
	tenant := singleRestrictedTenant(p)
	if tenant.IsZero() || p.SessionIdentity == "" || p.SessionWorkspaceID.IsZero() ||
		p.SessionRunRef == "" || p.SessionFence < 1 {
		return false
	}
	set := p.restricted[tenant]
	if len(set) != 4 {
		return false
	}
	for _, permission := range []Permission{
		CommunicationSessionDeliveryRead,
		CommunicationSessionDeliveryWrite,
		CommunicationSessionMessageSendWrite,
		CommunicationSessionHandoffResponseWrite,
	} {
		if _, ok := set[permission]; !ok {
			return false
		}
	}
	workspace, confined := p.ConfinedWorkspaceIn(tenant)
	return confined && workspace == p.SessionWorkspaceID
}

// IsPurposeRestricted reports whether this authenticated principal carries a
// server-authored hard permission ceiling. Non-Authorizer protocol edges that
// historically treat tenant membership alone as authority must reject it before
// consulting IsMember/Tenants; otherwise they would bypass that ceiling.
func (p Principal) IsPurposeRestricted() bool { return p.restricted != nil }

// PurposePermissionsIn returns a defensive, sorted copy of the hard permission
// ceiling for tenant and whether this is a purpose-restricted principal. It is
// used by introspection surfaces so they report the same ceiling Authorizer
// enforces; callers cannot use the returned slice to widen the private set.
func (p Principal) PurposePermissionsIn(tenant model.TenantID) ([]Permission, bool) {
	if p.restricted == nil {
		return nil, false
	}
	set := p.restricted[tenant]
	out := make([]Permission, 0, len(set))
	for permission := range set {
		out = append(out, permission)
	}
	sortPerms(out)
	return out, true
}

func singleRestrictedTenant(p Principal) model.TenantID {
	if len(p.restricted) != 1 {
		return ""
	}
	for tenant := range p.restricted {
		return tenant
	}
	return ""
}

// IsMember reports whether the principal has any grant in tenant.
func (p Principal) IsMember(tenant model.TenantID) bool {
	_, ok := p.grants[tenant]
	return ok
}

// GroupsIn returns the directory group ids the principal is a gated member of in
// tenant — the user's direct groups plus every group they are nested under
// (S256) — as a defensive copy (nil when none). Each id materializes a Cedar
// `Group::"<id>"` principal parent, so a scoped grant whose subject is a group
// matches every member. A token/synthetic principal carries none.
func (p Principal) GroupsIn(tenant model.TenantID) []string {
	src := p.groups[tenant]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Tenants returns the tenants the principal may act in, sorted.
func (p Principal) Tenants() []model.TenantID {
	out := make([]model.TenantID, 0, len(p.grants))
	for t := range p.grants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// newPrincipal builds a principal with a defensive copy of grants and group
// memberships (groups may be nil — a token/synthetic principal carries none).
func newPrincipal(kind PrincipalKind, userID, credID model.ID, superadmin bool, name string, grants map[model.TenantID]string, groups map[model.TenantID][]string) Principal {
	g := make(map[model.TenantID]string, len(grants))
	for k, v := range grants {
		g[k] = v
	}
	var grp map[model.TenantID][]string
	if len(groups) > 0 {
		grp = make(map[model.TenantID][]string, len(groups))
		for k, v := range groups {
			cp := make([]string, len(v))
			copy(cp, v)
			grp[k] = cp
		}
	}
	return Principal{Kind: kind, UserID: userID, CredID: credID, Superadmin: superadmin, DisplayName: name, grants: g, groups: grp}
}

// withCredentialRef binds an already-authenticated principal to the exact
// durable credential version just resolved by Authenticate. The caller keeps
// malformed rows from reaching this helper; Ref independently validates the
// result before exposing it.
func (p Principal) withCredentialRef(version int64) Principal {
	p.credentialRef = PrincipalRef{
		kind:         p.Kind,
		credentialID: p.CredID,
		version:      version,
	}
	return p
}

// sortPerms sorts a permission slice in place (stable display order).
func sortPerms(p []Permission) {
	sort.Slice(p, func(i, j int) bool { return p[i] < p[j] })
}
