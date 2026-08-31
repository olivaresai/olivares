// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SSO login completion: turn a validated FederatedIdentity into a first-party
// opaque session. The identity has already been verified by the Federation
// provider (its signature/nonce/conditions checked behind the seam); this code
// only maps it to a local user and mints the session — JWT never becomes a
// first-party credential (docs/SECURITY-HARDENING.md).

// WithGroupMapper sets the reserved enterprise login-time group-mapping capability
// (U2) and returns the Authenticator for chaining. Like WithLoginPolicy /
// WithSeatPolicy it is called once at boot, before serving. nil (the open build)
// means asserted IdP groups are extracted but never mapped to grants.
func (a *Authenticator) WithGroupMapper(m GroupMapper) *Authenticator {
	a.groupMapper = m
	return a
}

// GroupMappingAvailable reports whether THIS build can turn an IdP-asserted group
// into a grant at login (a GroupMapper is wired). The API surfaces it as the
// groups_mapped_by signal so the console never implies a capability the binary
// lacks — symmetric with the login-policy enforced_by signal.
func (a *Authenticator) GroupMappingAvailable() bool { return a.groupMapper != nil }

// CompleteSSO finds or JIT-provisions the local user for a validated federated
// identity and mints a session for it. It is the callback-leg completion the API
// calls after Federation.ValidateAssertion. A disabled local account is refused
// (ErrUnauthenticated) rather than silently re-activated.
//
// tenant is the login's resolved scope — the SELECTED IdP's TargetTenantID (U5), the
// multi-IdP tenant that actually authenticated, or GlobalFederationScope/"" for the
// deployment-wide/global login. It scopes login-time directory-group reconciliation (U2).
// scimAuthoritative is the SELECTED IdP's D4 flag, passed in (not re-derived by a scope
// lookup that, with several configs per scope, could read a sibling's flag): SCIM
// authority makes SSO never JIT-create and never reconcile groups at login. Both are inert
// for a global/"" tenant and in the open build (no GroupMapper), so the open path is
// byte-identical to before.
func (a *Authenticator) CompleteSSO(ctx context.Context, id FederatedIdentity, ip string, tenant model.TenantID, scimAuthoritative bool) (string, model.AuthSession, error) {
	// the network allow-list applies to EVERY login, so an SSO completion from
	// a peer outside the configured CIDRs is refused BEFORE the local user is found or
	// JIT-provisioned. require-SSO is NOT consulted here: this IS the SSO path it exists
	// to permit. A nil login policy (the open build) is a no-op — byte-identical to today.
	if err := a.enforceNetwork(ctx, ip); err != nil {
		a.auditLoginBlocked(ctx, "anonymous", ip, "network_not_allowed")
		return "", model.AuthSession{}, err
	}
	// D4 — a SCIM-authoritative IdP makes SCIM the sole authority: SSO never
	// JIT-creates and never reconciles groups at login. The flag is the RESOLVED IdP's,
	// so it is the policy of the config that actually authenticated the user.
	authoritative := scimAuthoritative
	user, err := a.findOrProvision(ctx, id, !authoritative)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	if user.Status != model.StatusActive {
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	// A superadmin — the cross-tenant/system root — must authenticate FIRST-PARTY
	// (password + a phishing-resistant step-up), NEVER via an unattended IdP email
	// assertion. Correlation is by verified email, and a superadmin is always a
	// local, password-provisioned account (external_id ""), so an SSO completion that
	// merely matched its email would bypass the password and mint a system-root
	// session. Refuse it — a defense against a (possibly second, differently-trusted
	// per-tenant) IdP asserting the superadmin's email. SSO-provisioned users are
	// never superadmin (findOrProvision creates IsSuperadmin=false), so this never
	// blocks a legitimate SSO login.
	if user.IsSuperadmin {
		a.auditLoginBlocked(ctx, "user:"+user.ID.String(), ip, "sso_into_superadmin_refused")
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	// U3 — bind the issuer-qualified subject onto an email-matched account that
	// has none, NOW that it has passed every eligibility guard (active, non-superadmin).
	// Deferring the stamp to here — rather than to findOrProvision, which runs before
	// the guards — is the fix for the "a refused login must leave no trace" invariant:
	// a disabled or superadmin account, or any assertion refused above, never persists
	// an attacker-influenced binding or audit row. Best-effort and never-overwrite; a
	// no-op for a JIT-created account (bound at create) or when no issuer was surfaced.
	a.bindSubjectIfUnset(ctx, user.ID, id.QualifiedSubject())
	// U2 — reconcile the user's directory-group membership from the groups the
	// IdP asserted, so login-driven membership lights up the MappedRole elevation and
	// group-subject (S256) grants that already exist. A no-op in the open build (no
	// GroupMapper), for a "" tenant, when SCIM is authoritative, or with no asserted
	// groups. Best-effort: a reconciliation error never fails an otherwise-valid login
	// (the user simply carries the memberships they already had).
	if a.groupMapper != nil && !authoritative && tenant != "" && tenant != GlobalFederationScope && len(id.Groups) > 0 {
		a.reconcileAssertedGroups(ctx, user.ID, tenant, id.Groups)
	}
	// A federated login is AAL1 here regardless of how the IdP authenticated:
	// this engine verified only the assertion, not the authenticator, and the
	// assurance claim is never inflated beyond what was verified first-party
	// (SP 800-63-4 defines no acr/amr conveyance mapping to trust instead).
	return a.mintSession(ctx, user, ip, "sso.login", []string{"sso"})
}

// FindOrProvisionByEmail returns the local user for a federated identity, JIT-
// provisioning an SSO-only account (no local password) on first login. The name is
// historical: correlation is now issuer-qualified-subject first with email as the
// fallback (see findOrProvision U3), not email-only. It is the backward-
// compatible entry (JIT always allowed); CompleteSSO uses the internal findOrProvision
// so a SCIM-authoritative scope can refuse JIT (D4). The
// provisioning actor is the system (an unattended SSO login is never attributed
// to the raw federated email — docs/SECURITY-HARDENING.md). A newly provisioned user has NO tenant
// membership; tenant access is granted separately (SCIM/admin), so a first SSO
// login authenticates the person without granting estate access by default.
func (a *Authenticator) FindOrProvisionByEmail(ctx context.Context, id FederatedIdentity) (model.User, error) {
	return a.findOrProvision(ctx, id, true)
}

// findOrProvision correlates a federated identity to a local user and, when allowJIT
// is true, JIT-creates an SSO-only account (no local password) on first login. When
// allowJIT is false (a SCIM-authoritative scope D4), an identity with no
// existing local user is refused (ErrUnauthenticated) rather than silently created.
//
// Correlation order (U3):
//  1. the issuer-qualified SSO subject (sso_subject = "<issuer>\x1f<subject>"), when
//     the provider surfaced a VERIFIED issuer. This is rename-resilient (an email
//     change never forks the account) and cross-IdP-safe: a bare subject value can
//     never select an account provisioned by a DIFFERENT issuer — the collision the
//     unqualified external_id column could not prevent (§D5).
//  2. the verified, globally-unique EMAIL — the fallback for accounts that predate
//     their subject binding (local/SCIM-provisioned users, or any account created
//     before U3). This is a PURE READ: it never mutates the matched account. The
//     subject binding is bootstrapped onto an email-matched account by CompleteSSO
//     AFTER its eligibility guards (bindSubjectIfUnset), so a REFUSED login (disabled
//     or superadmin account) never persists an attacker-influenced binding here.
//
// A JIT-created account is written WITH its subject binding: it is brand-new, active
// and non-superadmin, so it is eligible by construction — the hazard is only in
// stamping a PRE-EXISTING account, which is why only that stamp is deferred past the
// guards. external_id is deliberately NOT a correlation key here: it is SCIM's
// unqualified externalId (RFC 7643), shared-namespace with the raw subject. It is
// still written on JIT create so SCIM/CAEP can correlate THEIR OWN PATCH/DELETE by it.
func (a *Authenticator) findOrProvision(ctx context.Context, id FederatedIdentity, allowJIT bool) (model.User, error) {
	email := normalizeEmail(id.Email)
	if email == "" {
		return model.User{}, ErrUnauthenticated
	}
	qualified := id.QualifiedSubject()
	var out model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// 1. Issuer-qualified subject: the safe, rename-resilient key.
		if qualified != "" {
			bySub, _, err := as.Users().List(ctx, byEq("sso_subject", qualified, 1))
			if err != nil {
				return err
			}
			if len(bySub) > 0 {
				out = bySub[0]
				return nil
			}
		}
		// 2. Email fallback — a PURE READ. The subject-binding bootstrap for an
		// email-matched account is done by CompleteSSO after its eligibility guards
		// (bindSubjectIfUnset), never here, so a refused login leaves no trace.
		byMail, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		if len(byMail) > 0 {
			out = byMail[0]
			return nil
		}
		// D4 — a SCIM-authoritative scope never provisions from a login.
		if !allowJIT {
			return ErrUnauthenticated
		}
		name := id.DisplayName
		if name == "" {
			name = email
		}
		u, err := as.Users().Create(ctx, model.User{
			Email: email, DisplayName: name, Status: model.StatusActive,
			PasswordHash: "",         // SSO-only: no local password
			ExternalID:   id.Subject, // SCIM/CAEP correlate their own ops by this
			SsoSubject:   qualified,  // "" when no issuer ⇒ NULL; email stays the key
		})
		if err != nil {
			return err
		}
		out = u
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "sso.user.provision", TargetKind: "core.user", TargetID: u.ID,
		})
		return err
	})
	return out, err
}

// bindSubjectIfUnset stamps the issuer-qualified subject onto an account that has
// none, so a later login correlates by subject (rename-resilient U3). It runs
// ONLY from CompleteSSO, AFTER the eligibility guards (active + non-superadmin), so a
// refused login never persists a binding or an audit row. It re-reads the account in
// its own transaction and NEVER overwrites an existing binding — a second issuer that
// merely matched the account's email cannot seize its identity. Best-effort: any error
// (including a lost race on the unique index) is swallowed, the login still succeeds,
// and the binding is set on a subsequent login. A no-op when qualified is "" (no issuer
// surfaced) or the account is already bound.
func (a *Authenticator) bindSubjectIfUnset(ctx context.Context, userID model.ID, qualified string) {
	if qualified == "" {
		return
	}
	_ = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, userID)
		if err != nil {
			return err
		}
		if u.SsoSubject != "" {
			return nil // already bound — never overwrite
		}
		u.SsoSubject = qualified
		if _, err := as.Users().Update(ctx, u); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "sso.user.subject_bound", TargetKind: "core.user", TargetID: userID,
		})
		return err
	})
}

// reconcileAssertedGroups ADDS the user to the tenant's directory groups the IdP
// asserted at login (U2), via the reserved enterprise GroupMapper (which owns
// the asserted→group MATCHING the open build lacks). It is ADD-ONLY: it never
// removes a membership, so it can never fight SCIM or an operator over the roster
// (SCIM/operator own removal); the SCIM-authoritative toggle (D4) is the escape
// hatch for deployments that want SCIM to be the only writer. Best-effort — the
// caller ignores the error (login still succeeds with the memberships already held).
//
// No per-add role-ceiling is applied here (unlike the SCIM member-add path,
// scim_groups.go: "a role grant by the ACTOR … NOT by the IdP"). Login membership
// IS the IdP directly asserting who is in a group — the case that comment excludes.
// The escalation ceiling for this path is enforced ELSEWHERE and earlier: the
// group→role MAPPING is set only via ConfigureGroupRole, which is ceiling-checked
// (an operator can map a group only to a role at or below their own). So a login can
// grant at most the role an authorized operator already bound to a group the IdP
// vouches the user into — the trust split is: operator owns the mapping, IdP owns
// membership. The per-tenant gate (authenticator.go loadGrants) still requires a
// direct membership before any group confers anything, so a bare group row never
// admits on its own.
func (a *Authenticator) reconcileAssertedGroups(ctx context.Context, userID model.ID, tenant model.TenantID, asserted []string) {
	_ = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// The tenant's directory groups (auth rows share the system tenant; the
		// target_tenant_id column isolates one tenant's groups from another's).
		groups, err := drainList(ctx, as.Groups().List, byEq("target_tenant_id", tenant.String(), 0))
		if err != nil || len(groups) == 0 {
			return err
		}
		want := a.groupMapper.MapAssertedGroups(asserted, groups)
		if len(want) == 0 {
			return nil
		}
		// Add only the memberships the user does not already hold (idempotent, and it
		// keeps the unique (group,user) index from rejecting a duplicate).
		existing, err := drainList(ctx, as.GroupMembers().List, byEq("user_id", userID.String(), 0))
		if err != nil {
			return err
		}
		have := make(map[model.ID]bool, len(existing))
		for _, m := range existing {
			have[m.GroupID] = true
		}
		for _, gid := range want {
			if have[gid] {
				continue
			}
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: gid, UserID: userID}); err != nil {
				return err
			}
			have[gid] = true
			if _, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "sso.group.reconcile", TargetKind: "core.user_group", TargetID: gid,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
