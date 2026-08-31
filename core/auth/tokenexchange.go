// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Token exchange (RFC 8693) with resource indicators (RFC 8707).
//
// This is the first-party delegation primitive: it takes a subject token and
// mints a SHORT-LIVED, DOWN-SCOPED, AUDIENCE-BOUND child token for an agent
// acting on behalf of a principal (or another agent). It stays inside the
// opaque-token doctrine (docs/SECURITY-HARDENING.md): the issued credential is an ordinary
// opaque olvk_ token, NOT a JWT — the delegation/audience/scope semantics live in
// server-side columns (model.APIToken), and a resource server reads them by
// introspection, never by decoding the token. JWT is confined to the SSO
// assertion-validation seam, never to first-party authority.
//
// Down-scoping is OUR invariant, not an RFC mandate: the child's authority is
// CLAMPED so it can never exceed the subject's. Because the clamp is expressed as
// a lower built-in role, the existing authorizer enforces it with no new
// machinery — a read-only delegated token is simply a viewer-role token. Audience
// binding (RFC 8707) is what defeats the confused-deputy attack: a token minted
// for one resource is rejected at another (the resource server checks
// Principal.HasAudience). Every exchange is recorded on the audit ledger.

// RFC 8693 / RFC 8707 protocol constants.
const (
	// GrantTypeTokenExchange is the RFC 8693 grant_type.
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	// TokenTypeAccessToken is the only token type this AS accepts as a subject/
	// actor type and the only type it issues (first-party tokens are opaque
	// access tokens).
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
)

// DefaultExchangeTTL is how long a freshly exchanged delegated token is valid by
// default. It is deliberately short: a delegated token is for a bounded task, not
// a standing credential, and is additionally clamped to never outlive its subject.
const DefaultExchangeTTL = 15 * time.Minute

// Token-exchange errors. The API maps these to RFC 8693/6749 OAuth error codes
// (invalid_request, invalid_target); ErrRoleCeiling maps to 403.
var (
	// ErrInvalidExchange is a malformed or self-inconsistent exchange request.
	ErrInvalidExchange = errors.New("auth: invalid token-exchange request")
	// ErrInvalidSubjectToken means the subject (or actor) token is missing,
	// unknown, revoked, expired, or of an unsupported type.
	ErrInvalidSubjectToken = errors.New("auth: invalid subject token")
	// ErrInvalidTarget means a requested resource/audience is malformed or not
	// permitted (maps to the RFC 8693 invalid_target error).
	ErrInvalidTarget = errors.New("auth: invalid or unauthorized target")
	// ErrAgentBlocked is returned when a token exchange names an agent that is
	// blocked, orphaned, or whose sponsor does not match the subject.
	ErrAgentBlocked = errors.New("auth: agent identity blocked or invalid for delegation")
)

// AgentLifecycleChecker validates an agent identity's lifecycle status for
// token exchange. The governance module satisfies this interface. When nil,
// agent-OBO is unavailable (requested_actor is rejected).
type AgentLifecycleChecker interface {
	// CheckAgentForExchange validates that the named agent:
	// (1) exists with kind=agent, (2) is not blocked/orphaned,
	// (3) has sponsorRef matching the given sponsor external_id.
	// Returns nil if the agent is valid for delegation.
	CheckAgentForExchange(ctx context.Context, tenant model.TenantID, agentRef, sponsorRef string) error
}

// SetAgentLifecycleChecker wires the governance module's agent validation
// into the token exchange. Nil disables agent-OBO.
func (a *Authenticator) SetAgentLifecycleChecker(c AgentLifecycleChecker) {
	a.agentChecker = c
}

// ExchangeRequest is a parsed, validated RFC 8693 token-exchange request.
type ExchangeRequest struct {
	// SubjectToken is the opaque token being down-scoped (REQUIRED).
	SubjectToken string
	// SubjectTokenType MUST be TokenTypeAccessToken for first-party tokens.
	SubjectTokenType string
	// ActorToken, when present, makes this a DELEGATION (act-on-behalf-of); absent
	// is impersonation. RFC 8693 §2.1.
	ActorToken string
	// ActorTokenType is REQUIRED iff ActorToken is present, and MUST be empty
	// otherwise.
	ActorTokenType string
	// Resources are RFC 8707 resource indicators: absolute URIs (no fragment).
	Resources []string
	// Audiences are RFC 8693 logical audience names.
	Audiences []string
	// Scope is the requested authority as verb tiers (read/write/admin); the
	// granted scope is the intersection with the subject's authority.
	Scope []string
	// RequestedTokenType is OPTIONAL and never required; only access tokens issue.
	RequestedTokenType string
	// Name is an optional label for the minted token.
	Name string
	// RequestedActorRef names the agent identity (by external_id) that should
	// act under the exchanged authority (agent-OBO). When present, the
	// exchange validates the agent exists with kind=agent, is not blocked/
	// orphaned, and the subject IS the agent's human sponsor. The minted token
	// carries the agent reference.
	RequestedActorRef string
}

// ExchangeResult is the RFC 8693 §2.2.1 successful response plus the stored
// record (without the secret).
type ExchangeResult struct {
	// AccessToken is the opaque olvk_ token string (shown once).
	AccessToken string
	// IssuedTokenType is always TokenTypeAccessToken.
	IssuedTokenType string
	// TokenType is "Bearer".
	TokenType string
	// ExpiresIn is the child token lifetime in seconds.
	ExpiresIn int
	// Scope is the granted scope (verb tiers). It is narrower than or equal to the
	// subject's authority.
	Scope []string
	// Narrowed reports whether the granted authority is strictly below the
	// subject's (a real down-scope occurred).
	Narrowed bool
	// Stored is the persisted token record (no secret).
	Stored model.APIToken
}

// ExchangeToken performs an RFC 8693 token exchange. caller is the authenticated
// API client invoking the endpoint (used only as the audit actor); authority to
// exchange comes from possession of the subject token, which can only ever yield
// a LESSER token. It mints a down-scoped, audience-bound opaque child token in
// one transaction with its audit event.
func (a *Authenticator) ExchangeToken(ctx context.Context, caller Principal, req ExchangeRequest) (ExchangeResult, error) {
	if caller.IsPurposeRestricted() {
		return ExchangeResult{}, fmt.Errorf("%w: purpose-restricted callers cannot exchange credentials", ErrInvalidExchange)
	}
	// 1. Validate the grant shape (RFC 8693 §2.1).
	if req.SubjectToken == "" || req.SubjectTokenType == "" {
		return ExchangeResult{}, fmt.Errorf("%w: subject_token and subject_token_type are required", ErrInvalidExchange)
	}
	if req.SubjectTokenType != TokenTypeAccessToken {
		return ExchangeResult{}, fmt.Errorf("%w: unsupported subject_token_type %q (only %s)", ErrInvalidSubjectToken, req.SubjectTokenType, TokenTypeAccessToken)
	}
	// actor_token and actor_token_type must be supplied together (or both absent).
	if (req.ActorToken == "") != (req.ActorTokenType == "") {
		return ExchangeResult{}, fmt.Errorf("%w: actor_token and actor_token_type must be supplied together", ErrInvalidExchange)
	}
	if req.ActorToken != "" && req.ActorTokenType != TokenTypeAccessToken {
		return ExchangeResult{}, fmt.Errorf("%w: unsupported actor_token_type %q (only %s)", ErrInvalidExchange, req.ActorTokenType, TokenTypeAccessToken)
	}
	if req.RequestedTokenType != "" && req.RequestedTokenType != TokenTypeAccessToken {
		return ExchangeResult{}, fmt.Errorf("%w: only %s can be issued", ErrInvalidExchange, TokenTypeAccessToken)
	}

	// 2. Validate targets (RFC 8707): each resource an absolute URI with no
	// fragment; each audience non-empty; both honored against an optional
	// AS-side allowlist.
	audience, err := a.validateTargets(req.Resources, req.Audiences)
	if err != nil {
		return ExchangeResult{}, err
	}

	// 3. Resolve the subject and its single tenant + role.
	subject, err := a.Authenticate(ctx, req.SubjectToken)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("%w: %v", ErrInvalidSubjectToken, err)
	}
	if subject.Superadmin {
		return ExchangeResult{}, fmt.Errorf("%w: cannot down-scope a system (superadmin) token; mint a tenant-bound token first", ErrInvalidExchange)
	}
	if subject.IsPurposeRestricted() {
		return ExchangeResult{}, fmt.Errorf("%w: purpose-restricted credentials cannot be exchanged", ErrInvalidExchange)
	}
	tenants := subject.Tenants()
	if len(tenants) != 1 {
		return ExchangeResult{}, fmt.Errorf("%w: subject must resolve to exactly one tenant (got %d)", ErrInvalidExchange, len(tenants))
	}
	tenant := tenants[0]
	subjectRole, _ := subject.RoleIn(tenant)

	// a token exchange must not launder a workspace-confined principal into an
	// unconfined child token (a bound token is tenant-wide and carries no confinement). The
	// child acts for the SUBJECT (impersonation) or under the ACTOR (delegation), so if the
	// relevant principal is confined in the tenant, refuse — the same deny-closed posture as
	// IssueToken (defense-in-depth: a confined principal cannot mint the parent token either).
	if _, confined := subject.ConfinedWorkspaceIn(tenant); confined {
		return ExchangeResult{}, ErrWorkspaceConfined
	}

	// 4. Delegation vs impersonation.
	ownerUser := subject.UserID
	var actAs model.ID
	if req.ActorToken != "" {
		actor, aerr := a.Authenticate(ctx, req.ActorToken)
		if aerr != nil {
			return ExchangeResult{}, fmt.Errorf("%w: actor: %v", ErrInvalidSubjectToken, aerr)
		}
		if actor.IsPurposeRestricted() {
			return ExchangeResult{}, fmt.Errorf("%w: purpose-restricted credentials cannot be exchange actors", ErrInvalidExchange)
		}
		// The actor may only act for a subject at or below its own role ceiling
		// (a viewer cannot mint a delegated token acting for an admin).
		if err := checkRoleCeiling(actor, tenant, subjectRole); err != nil {
			return ExchangeResult{}, err // ErrRoleCeiling -> 403
		}
		if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
			return ExchangeResult{}, ErrWorkspaceConfined // a confined actor cannot delegate a tenant-wide token
		}
		ownerUser = actor.UserID
		actAs = subject.UserID
	}

	// 4b. Agent-OBO: if requested_actor names an agent, validate its
	// lifecycle and bind the minted token to it.
	var agentRef string
	if req.RequestedActorRef != "" {
		if a.agentChecker == nil {
			return ExchangeResult{}, fmt.Errorf("%w: agent-OBO unavailable (no lifecycle checker configured)", ErrInvalidExchange)
		}
		// The subject of the exchange must be the agent's human sponsor.
		// We resolve the subject's external_id from the user record — this is
		// the SCIM externalId set by the IdP and stored as the convergence anchor
		// in the NHI lifecycle row's sponsor_ref column.
		sponsorRef, serr := a.resolveUserExternalID(ctx, subject.UserID, tenant)
		if serr != nil {
			return ExchangeResult{}, fmt.Errorf("%w: cannot resolve sponsor identity: %v", ErrAgentBlocked, serr)
		}
		if err := a.agentChecker.CheckAgentForExchange(ctx, tenant, req.RequestedActorRef, sponsorRef); err != nil {
			return ExchangeResult{}, fmt.Errorf("%w: %v", ErrAgentBlocked, err)
		}
		agentRef = req.RequestedActorRef
	}

	// 5. Down-scope: clamp the child role to min(requested tier, subject tier).
	grantedTier, narrowed := downscopeTier(subjectRole, req.Scope)
	childRole := roleForTier(grantedTier)
	grantedScope := scopeForTier(grantedTier)

	// 6. Mint inside one transaction with the audit event, clamping the expiry to
	// the subject's own expiry so a child never outlives its subject.
	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return ExchangeResult{}, err
	}
	now := a.clock.Now()
	childExp := now.Time().Add(a.exchangeTTL())
	name := req.Name
	if name == "" {
		name = "exchanged"
	}
	// ParentTokenID is specifically API-token ancestry. A KindUser CredID names
	// an auth session, not a token; storing it here breaks both the token FK/guard
	// and revocation traversal. Session subjects are still expiry-clamped below.
	var parentTokenID model.ID
	if subject.Kind == KindToken {
		parentTokenID = subject.CredID
	}

	var stored model.APIToken
	mutErr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		subExp, e := credentialExpiry(ctx, as, subject)
		if e != nil {
			return e
		}
		if subExp != nil && subExp.Time().Before(childExp) {
			childExp = subExp.Time()
		}
		ce := model.NewTimestamp(childExp)
		t, e := as.Tokens().Create(ctx, model.APIToken{
			Name: name, UserID: ownerUser, Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: tenant, Role: childRole, IsSuperadmin: false, ExpiresAt: &ce,
			Audience: audience, ActAsUserID: actAs, ParentTokenID: parentTokenID,
			Scope: strings.Join(grantedScope, " "), AgentRef: agentRef,
		})
		if e != nil {
			return e
		}
		stored = t
		auditMeta := map[string]any{
			"subject":   subject.Actor(),
			"act_as":    actAs.String(),
			"audience":  audience,
			"scope":     strings.Join(grantedScope, " "),
			"role":      childRole,
			"delegated": req.ActorToken != "",
		}
		// Only include agent_ref in audit meta if it is non-empty
		if agentRef != "" {
			auditMeta["agent_ref"] = agentRef
		}
		_, e = as.Audit().Append(ctx, model.AuditDraft{
			Actor: caller.Actor(), ActorKind: caller.ActorKind(),
			Action: "token.exchange", TargetKind: "core.api_token", TargetID: t.ID,
			Meta: auditMeta,
		})
		return e
	})
	if mutErr != nil {
		return ExchangeResult{}, mutErr
	}

	return ExchangeResult{
		AccessToken:     cred.Token,
		IssuedTokenType: TokenTypeAccessToken,
		TokenType:       "Bearer",
		ExpiresIn:       int(time.Until(childExp).Seconds()),
		Scope:           grantedScope,
		Narrowed:        narrowed,
		Stored:          stored,
	}, nil
}

// validateTargets validates RFC 8707 resources (absolute URIs, no fragment) and
// RFC 8693 audiences (non-empty), then enforces the optional AS-side allowlist
// (empty allowlist = accept any well-formed target). The validated set is joined
// for storage on the token's Audience column.
func (a *Authenticator) validateTargets(resources, audiences []string) (string, error) {
	parts := make([]string, 0, len(resources)+len(audiences))
	for _, r := range resources {
		u, err := url.Parse(strings.TrimSpace(r))
		if err != nil || !u.IsAbs() || u.Fragment != "" {
			return "", fmt.Errorf("%w: resource %q must be an absolute URI with no fragment", ErrInvalidTarget, r)
		}
		parts = append(parts, strings.TrimSpace(r))
	}
	for _, aud := range audiences {
		if strings.TrimSpace(aud) == "" {
			return "", fmt.Errorf("%w: audience must be non-empty", ErrInvalidTarget)
		}
		parts = append(parts, strings.TrimSpace(aud))
	}
	if len(a.allowedAudiences) > 0 {
		for _, p := range parts {
			if !a.allowedAudiences[p] {
				return "", fmt.Errorf("%w: %q is not an allowed target", ErrInvalidTarget, p)
			}
		}
	}
	return strings.Join(parts, "\n"), nil
}

// exchangeTTL is the configured (or default) delegated-token lifetime.
func (a *Authenticator) exchangeTTL() time.Duration {
	if a.exchangeTTLDur > 0 {
		return a.exchangeTTLDur
	}
	return DefaultExchangeTTL
}

// SetExchangePolicy configures the token-exchange TTL and the optional allowed-
// target allowlist (nil/empty = accept any well-formed target). It is wired from
// the composition root; the zero value keeps the safe defaults.
func (a *Authenticator) SetExchangePolicy(ttl time.Duration, allowedTargets []string) {
	a.exchangeTTLDur = ttl
	if len(allowedTargets) == 0 {
		a.allowedAudiences = nil
		return
	}
	set := make(map[string]bool, len(allowedTargets))
	for _, t := range allowedTargets {
		set[strings.TrimSpace(t)] = true
	}
	a.allowedAudiences = set
}

// credentialExpiry returns the subject credential's expiry (a session always has
// one; a token may not), so the exchange can clamp the child to it.
func credentialExpiry(ctx context.Context, as store.AuthScope, p Principal) (*model.Timestamp, error) {
	switch p.Kind {
	case KindUser:
		s, err := as.Sessions().Get(ctx, p.CredID)
		if err != nil {
			return nil, err
		}
		exp := s.ExpiresAt
		return &exp, nil
	case KindToken:
		t, err := as.Tokens().Get(ctx, p.CredID)
		if err != nil {
			return nil, err
		}
		return t.ExpiresAt, nil
	default:
		return nil, nil
	}
}

// --- verb-tier <-> role down-scoping -----------------------------------------

// verbTierForRole maps a built-in role to its maximum verb tier
// (viewer->1 read, editor->2 write, admin/owner->3 admin).
func verbTierForRole(role string) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin, RoleOwner:
		return 3
	default:
		return 0
	}
}

// verbRank maps a verb to its tier (read<write<admin); 0 for anything else.
func verbRank(verb string) int {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case VerbRead:
		return 1
	case VerbWrite:
		return 2
	case VerbAdmin:
		return 3
	default:
		return 0
	}
}

// roleForTier maps a granted verb tier back to the minimal built-in role that
// holds it. A delegated token is capped at admin (never owner — owner is a
// tenant-ownership role, not a delegation target).
func roleForTier(tier int) string {
	switch tier {
	case 1:
		return RoleViewer
	case 2:
		return RoleEditor
	default:
		return RoleAdmin
	}
}

// scopeForTier returns the cumulative verb scope for a tier.
func scopeForTier(tier int) []string {
	switch {
	case tier >= 3:
		return []string{VerbRead, VerbWrite, VerbAdmin}
	case tier == 2:
		return []string{VerbRead, VerbWrite}
	default:
		return []string{VerbRead}
	}
}

// resolveUserExternalID looks up the SCIM externalId (ExternalID field) for a
// user. This is the convergence anchor used to match a sponsor against an
// agent's lifecycle row (the agent is registered with sponsor_ref == the
// sponsor's externalId). Returns empty string if the user has no externalId
// (locally-created, never SCIM-provisioned) — which is valid and will cause
// the sponsor-match check to fail cleanly.
func (a *Authenticator) resolveUserExternalID(ctx context.Context, userID model.ID, _ model.TenantID) (string, error) {
	if userID.IsZero() {
		return "", nil
	}
	var ref string
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // no user row: no externalId
			}
			return err
		}
		ref = u.ExternalID
		return nil
	})
	return ref, err
}

// downscopeTier computes the granted verb tier as min(requested, subject), and
// whether that is strictly below the subject's own tier (a real narrowing). An
// empty request inherits the subject tier (no narrowing); a non-empty request
// with no recognized verb is treated as read (least privilege).
func downscopeTier(subjectRole string, requestedScope []string) (tier int, narrowed bool) {
	subjectTier := verbTierForRole(subjectRole)
	if len(requestedScope) == 0 {
		return subjectTier, false
	}
	requestedTier := 0
	for _, s := range requestedScope {
		if r := verbRank(s); r > requestedTier {
			requestedTier = r
		}
	}
	if requestedTier == 0 {
		requestedTier = 1 // fail-safe to read when nothing is recognized
	}
	granted := requestedTier
	if subjectTier < granted {
		granted = subjectTier
	}
	return granted, granted < subjectTier
}
