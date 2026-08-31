// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// superadminQuery lists rows whose bool is_superadmin column is true. It is built
// inline (not via byEq, which binds a string) because the column is KindBool — a
// string "true" would not match (cf. superadmin.go's lockout query).
func superadminQuery() model.Query {
	return model.Query{Filters: []model.Filter{{Column: "is_superadmin", Op: model.OpEq, Value: true}}}
}

// building the authorization principal for an ARBITRARY subject (not the
// request caller), so a reverse query / the AuthZEN PDP can ask "what can THIS
// subject access" honestly.
//
// The honesty hinge: these reuse the EXACT path authentication uses — loadGrants
// (the user's real roles AND the S256 gated, nested directory-group closure) and
// newPrincipal — so the principal handed to Authorize is identical to the one the
// subject's own live session/token would carry. There is no second, looser notion
// of "this user's roles" that could drift from what a real login computes. A
// decision made about a simulated subject is therefore exactly the decision the
// enforced path would make for that subject.
//
// What is NOT trusted: nothing about the subject comes from the caller's input
// except its identity (the id/email to look up). Roles, group membership and the
// superadmin flag are read from the store, so a PEP can never widen a decision by
// asserting attributes about the subject it asks about.

// PrincipalForUser builds the authorization principal for the stored user named by
// ref (its id or, as an AuthZEN-interop convenience, its email). It loads the user's
// real roles and gated nested-group memberships exactly as a login does, so the
// returned principal authorizes identically to that user's live session.
//
// assurance is the authenticator-assurance level to evaluate the user AT (clamped to
// [AAL1, AAL3]); pass AAL3 to see the user's MAXIMUM standing entitlement (what they
// reach after a step-up — the safe direction for an access review, which must not
// under-report), or AAL1 to see access without a step-up. A user is, by the model,
// always at least AAL1.
//
// found is false (with a nil error) when no such ACTIVE user exists — an absent,
// disabled or SSO-suspended account can authorize nothing, so the honest answer for
// it is "no access", never a fabricated principal.
func (a *Authenticator) PrincipalForUser(ctx context.Context, ref string, assurance int) (p Principal, found bool, err error) {
	verr := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, ok, e := lookupUser(ctx, as, ref)
		if e != nil || !ok {
			return e
		}
		if u.Status != model.StatusActive {
			return nil // inactive ⇒ found stays false: it authorizes nothing
		}
		grants, groups, confined, e := loadGrants(ctx, as, u.ID)
		if e != nil {
			return e
		}
		// CredID is the user id (not a live session id): a simulated principal stands
		// for the user's STANDING entitlement, not one credential. No authored grant
		// keys on a specific credential id (targets Role/User/Group), so this is
		// decision-irrelevant; it gives the Cedar principal a stable, meaningful UID.
		p = newPrincipal(KindUser, u.ID, u.ID, u.IsSuperadmin, u.DisplayName, grants, groups).withConfinements(confined)
		p.AAL = clampUserAAL(assurance)
		found = true
		return nil
	})
	if verr != nil {
		return Principal{}, false, verr
	}
	return p, found, nil
}

// PrincipalForToken builds the authorization principal for the stored API token with
// the given id, exactly as token authentication would (a single bound grant, or the
// system role for a superadmin token; never any directory groups; AAL stays 0 — a
// token carries no human assurance, an invariant a simulation must not break).
//
// found is false (nil error) for an absent, revoked, expired or MISCONFIGURED token
// (a bound token with no tenant/role authenticates to nothing) — each can authorize
// nothing, so the honest answer is "no access".
func (a *Authenticator) PrincipalForToken(ctx context.Context, tokenID model.ID) (p Principal, found bool, err error) {
	verr := a.st.AuthView(ctx, func(as store.AuthScope) error {
		t, e := as.Tokens().Get(ctx, tokenID)
		if e != nil {
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			return e
		}
		pr, ok := a.principalFromToken(t)
		if !ok {
			return nil
		}
		p, found = pr, true
		return nil
	})
	if verr != nil {
		return Principal{}, false, verr
	}
	return p, found, nil
}

// TenantPrincipals lists the candidate principal POPULATION for a "who can access R"
// review in tenant: every user with a direct membership there, every superadmin (the
// system role reaches every tenant) and every active API token bound to the tenant
// (plus active unbound superadmin tokens). Each is built with PrincipalForUser-grade
// fidelity (real roles + nested groups), users at the given assurance.
//
// HONEST LIMIT — the population is the set of principals with a tenant relationship.
// It is a superset of everyone the RBAC, role- and group-scoped grant paths can
// authorize. It does NOT include a user a free-form Cedar grant names DIRECTLY by
// `User::"<id>"` while that user holds no membership in the tenant (such a user
// authenticates with an empty membership set yet still matches the grant). Callers
// surface the enumerated population so completeness is never implicitly overclaimed;
// closing that edge would require parsing the tenant's authored grant policy for
// direct subject references (a documented follow-up).
func (a *Authenticator) TenantPrincipals(ctx context.Context, tenant model.TenantID, assurance int) ([]Principal, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return nil, nil
	}
	var out []Principal
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		seenUser := map[model.ID]bool{}
		addUser := func(id model.ID) error {
			if id.IsZero() || seenUser[id] {
				return nil
			}
			seenUser[id] = true
			u, e := as.Users().Get(ctx, id)
			if e != nil {
				if errors.Is(e, store.ErrNotFound) {
					return nil
				}
				return e
			}
			if u.Status != model.StatusActive {
				return nil
			}
			grants, groups, confined, e := loadGrants(ctx, as, u.ID)
			if e != nil {
				return e
			}
			pr := newPrincipal(KindUser, u.ID, u.ID, u.IsSuperadmin, u.DisplayName, grants, groups).withConfinements(confined)
			pr.AAL = clampUserAAL(assurance)
			out = append(out, pr)
			return nil
		}

		// Users with a direct membership in the tenant.
		ms, e := drainList(ctx, as.Memberships().List, byEq("target_tenant_id", tenant.String(), 0))
		if e != nil {
			return e
		}
		for _, m := range ms {
			if e := addUser(m.UserID); e != nil {
				return e
			}
		}
		// Superadmins — the system role reaches every tenant, so they are candidates
		// for any tenant's review regardless of a membership row.
		sas, e := drainList(ctx, as.Users().List, superadminQuery())
		if e != nil {
			return e
		}
		for _, u := range sas {
			if e := addUser(u.ID); e != nil {
				return e
			}
		}
		// Active API tokens bound to the tenant, plus active unbound superadmin tokens
		// (which reach every tenant). principalFromToken drops revoked/expired/
		// misconfigured tokens — they authorize nothing.
		bound, e := drainList(ctx, as.Tokens().List, byEq("bound_tenant_id", tenant.String(), 0))
		if e != nil {
			return e
		}
		saTokens, e := drainList(ctx, as.Tokens().List, superadminQuery())
		if e != nil {
			return e
		}
		seenTok := map[model.ID]bool{}
		for _, t := range append(bound, saTokens...) {
			if seenTok[t.ID] {
				continue
			}
			seenTok[t.ID] = true
			if pr, ok := a.principalFromToken(t); ok {
				out = append(out, pr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// principalFromToken builds the principal for a stored token exactly as authToken
// does (minus the secret check — the caller is enumerating, not authenticating) and
// reports whether the token can authorize anything at all: a revoked, expired or
// misconfigured (bound-but-no-tenant/role) token reports false. AAL stays 0.
func (a *Authenticator) principalFromToken(t model.APIToken) (Principal, bool) {
	if t.Revoked {
		return Principal{}, false
	}
	if t.ExpiresAt != nil {
		now := a.clock.Now()
		switch t.Purpose {
		case WorkSessionCredentialPurpose:
			if workSessionCredentialExpired(*t.ExpiresAt, now) {
				return Principal{}, false
			}
		case CommunicationSessionCredentialPurpose:
			if communicationSessionCredentialExpired(*t.ExpiresAt, now) {
				return Principal{}, false
			}
		default:
			if t.ExpiresAt.Before(now) {
				return Principal{}, false
			}
		}
	}
	// Runtime session credentials authenticate on the ordinary HTTP edge but
	// carry separate exact private ceilings. Reconstruct the same principal here;
	// every other purpose remains confined to its dedicated protocol path.
	switch t.Purpose {
	case WorkSessionCredentialPurpose:
		return workSessionPrincipal(t)
	case CommunicationSessionCredentialPurpose:
		return communicationSessionPrincipal(t)
	case "":
		// Continue through ordinary token validation below.
	default:
		return Principal{}, false
	}
	if t.SessionRef != "" || !t.WorkspaceID.IsZero() || t.SessionRunRef != "" ||
		t.SessionFence != 0 {
		return Principal{}, false
	}
	grants := map[model.TenantID]string{}
	if !t.IsSuperadmin {
		if t.BoundTenantID.IsZero() || !IsRole(t.Role) {
			return Principal{}, false
		}
		grants[t.BoundTenantID] = t.Role
	}
	p := newPrincipal(KindToken, t.UserID, t.ID, t.IsSuperadmin, t.Name, grants, nil)
	// Carry the delegation binding exactly as authToken does, so a simulated
	// (token-exchanged) principal is byte-identical to the authenticated one. Authorize
	// does not read these, but matching them removes any doubt about divergence.
	if t.Audience != "" {
		p.audiences = strings.Split(t.Audience, "\n")
	}
	p.actAs = t.ActAsUserID
	// Agent-OBO parity: the live path binds the token to its agent identity, and
	// agent-scoped policy only matches through Principal.AgentIdentity — a simulated
	// principal without it answers reverse queries differently than enforcement acts.
	if t.AgentRef != "" {
		p = p.WithAgentIdentity(t.AgentRef)
	}
	return p, true
}

// lookupUser resolves a user by id first, then (AuthZEN-interop convenience) by
// normalized email. It returns ok=false (nil error) when neither matches.
func lookupUser(ctx context.Context, as store.AuthScope, ref string) (model.User, bool, error) {
	if id, err := model.ParseID(ref); err == nil && !id.IsZero() {
		u, e := as.Users().Get(ctx, id)
		switch {
		case e == nil:
			return u, true, nil
		case !errors.Is(e, store.ErrNotFound):
			return model.User{}, false, e
		}
	}
	us, _, e := as.Users().List(ctx, byEq("email", normalizeEmail(ref), 1))
	if e != nil {
		return model.User{}, false, e
	}
	if len(us) == 0 {
		return model.User{}, false, nil
	}
	return us[0], true, nil
}

// clampUserAAL bounds a requested assurance to the user range [AAL1, AAL3]: a user
// session is never below AAL1, and AAL3 is the ceiling the engine recognizes.
func clampUserAAL(assurance int) int {
	if assurance < AAL1 {
		return AAL1
	}
	if assurance > AAL3 {
		return AAL3
	}
	return assurance
}
