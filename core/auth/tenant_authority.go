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

// A tenant's authority over accounts ends at its own boundary. A tenant never
// joins an existing account on its own, never writes an account's global status,
// never cuts the account anywhere but in itself, and never creates or renames an
// address into a domain another organization's identity provider claims. This
// file holds those guards and the scoped offboard every tenant disable uses.

// Containment errors. The API maps each to its answer.
var (
	// ErrConsentRequired means the account exists and is not a member of the
	// tenant: joining it needs its holder's consent. Answered 202 consent_required,
	// the same answer whatever the account's state.
	ErrConsentRequired = errors.New("auth: the account exists; joining it requires its holder's consent")
	// ErrForeignDomain means the address's domain is claimed by another
	// organization's identity provider. Answered 409 domain_claimed_elsewhere
	// (403 on SCIM).
	ErrForeignDomain = errors.New("auth: the address's domain is claimed by another organization's identity provider")
	// ErrNotTenantGoverned means the account is not governed by this tenant alone,
	// so the tenant may not write its global attributes. Answered 403.
	ErrNotTenantGoverned = errors.New("auth: the account is not governed by this organization alone")
	// ErrRecoveryRequired means the account predates custody records and needs the
	// deployment's recovery before it can join a further organization.
	ErrRecoveryRequired = errors.New("auth: the account needs the deployment's recovery before it can join")
	// ErrRetirementPending means the account's removal from this tenant has not
	// completed, so it cannot be re-admitted yet. Answered 409 retirement_pending.
	ErrRetirementPending = errors.New("auth: the account's removal from this organization has not completed")
	// ErrInviteDeliveryUnavailable means no invitation mailer is configured, so an
	// invitation cannot reach its invitee. Answered 409 invite_delivery_unavailable.
	ErrInviteDeliveryUnavailable = errors.New("auth: no invitation mailer is configured")
)

// refuseForeignDomain refuses email when its domain is claimed by the identity
// provider of a business tenant other than t (or than trusted, when t
// explicitly trusts that tenant). Claims of the deployment's own provider do
// not count. It runs before any lookup, so it reveals configuration, never an
// account.
func refuseForeignDomain(ctx context.Context, as store.AuthScope, t model.TenantID, email string, trusted model.TenantID) error {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return nil
	}
	domain := model.NormalizeFederationDomain(email[at+1:])
	if domain == "" {
		return nil
	}
	claims, err := drainList(ctx, as.FederationDomainClaims().List, byEq("domain", domain, 0))
	if err != nil {
		return err
	}
	for _, c := range claims {
		owner := c.TargetTenantID
		if owner.IsZero() || owner.IsSystem() || owner == t || (!trusted.IsZero() && owner == trusted) {
			continue
		}
		return ErrForeignDomain
	}
	return nil
}

// tenantGoverned reports whether t alone governs the account: it is not a
// superadmin, every membership it holds is in t, and its holder has not taken
// custody of it. Only then may t write the account's global attributes.
func tenantGoverned(ctx context.Context, as store.AuthScope, u model.User, t model.TenantID) (bool, error) {
	if u.IsSuperadmin || u.CredentialCustody == model.CustodyHolder {
		return false, nil
	}
	ms, err := drainList(ctx, as.Memberships().List, byEq("user_id", u.ID.String(), 0))
	if err != nil {
		return false, err
	}
	for _, m := range ms {
		if m.TargetTenantID != t {
			return false, nil
		}
	}
	return true, nil
}

// scopedOffboard removes the account from one tenant: its membership, its rows in
// the tenant's groups, the tokens bound to the tenant (by owner or by act-as, with
// their exchanged children), the sessions scoped to it, and it writes or keeps the
// exclusion that is also the retirement record. It is the only thing a tenant's
// disable, deprovision or compromise report does: the account's global status,
// its other tenants, its account-scope sessions and its authenticators are never
// touched.
//
// It runs in the caller's auth transaction, under the account's authority lock.
// An account that is no longer a member and whose record is retiring, blocked or
// retired is in the same retirement: nothing is written, the authority version
// does not move, and the pump is woken for the existing record. Otherwise the
// record starts a new retirement generation, due at once by the authenticator's
// clock, and the account's authority version moves exactly once for the record
// (session revocations move it too).
//
// The wake is only a signal to look for due records, and it is sent before the
// caller commits: a pump that looks too early finds nothing yet, and the record's
// schedule brings it back.
func (a *Authenticator) scopedOffboard(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID, cause string) (model.TenantExclusion, error) {
	if t.IsZero() || t.IsSystem() {
		return model.TenantExclusion{}, ErrInvalidToken
	}
	if err := prepareUserAuthorityWrite(ctx, as, user); err != nil {
		return model.TenantExclusion{}, err
	}
	membership, member, err := membershipOf(ctx, as, user, t)
	if err != nil {
		return model.TenantExclusion{}, err
	}
	rec, found, err := retirementRecord(ctx, as, user, t)
	if err != nil {
		return model.TenantExclusion{}, err
	}
	if !member && found && rec.RetirementState != model.RetirementLifted {
		a.wakeRetirement()
		return rec, nil
	}
	if member {
		if err := as.Memberships().Delete(ctx, membership.ID); err != nil {
			return model.TenantExclusion{}, err
		}
	}
	if err := removeTenantGroupRows(ctx, as, user, t); err != nil {
		return model.TenantExclusion{}, err
	}
	if err := revokeTenantTokens(ctx, as, actor, user, t); err != nil {
		return model.TenantExclusion{}, err
	}
	if err := revokeScopedSessions(ctx, as, user, t); err != nil {
		return model.TenantExclusion{}, err
	}
	u, err := as.Users().Get(ctx, user)
	if err != nil {
		return model.TenantExclusion{}, err
	}
	if _, err := as.Users().Update(ctx, u); err != nil {
		return model.TenantExclusion{}, err
	}
	now := a.clock.Now()
	if found {
		rec.RetirementGeneration++
		rec.RetirementState = model.RetirementRetiring
		rec.CreatedBy = actor.Actor()
		rec.BlockingRefs, rec.ModuleResults = "", ""
		rec.Attempts = 0
		rec.NextAttemptAt = &now
		if rec, err = as.TenantExclusions().Update(ctx, rec); err != nil {
			return model.TenantExclusion{}, err
		}
	} else {
		if rec, err = as.TenantExclusions().Create(ctx, model.TenantExclusion{
			UserID: user, TargetTenantID: t, Kind: model.ExclusionOffboard, CreatedBy: actor.Actor(),
			RetirementGeneration: 1, RetirementState: model.RetirementRetiring, NextAttemptAt: &now,
		}); err != nil {
			return model.TenantExclusion{}, err
		}
	}
	if err := metaAudit(ctx, as, actor, "membership.offboard", "core.user", user, map[string]any{
		"tenant": t.String(), "cause": cause, "generation": rec.RetirementGeneration,
	}); err != nil {
		return model.TenantExclusion{}, err
	}
	a.wakeRetirement()
	return rec, nil
}

// OffboardFromTenant is the scoped offboard for a caller that already runs in an
// auth transaction on the tenant's behalf, such as a tenant's erasure request:
// it removes the account from t exactly as a tenant's disable does, and never
// touches the global account.
func (a *Authenticator) OffboardFromTenant(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID, cause string) (model.TenantExclusion, error) {
	return a.scopedOffboard(ctx, as, actor, user, t, cause)
}

// removeTenantGroupRows deletes the account's rows in t's groups. A stale row
// grants nothing without a membership, but it would re-elevate the account the
// moment a membership reappeared. Groups of other tenants are untouched.
func removeTenantGroupRows(ctx context.Context, as store.AuthScope, user model.ID, t model.TenantID) error {
	rows, err := drainList(ctx, as.GroupMembers().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, r := range rows {
		grp, err := as.Groups().Get(ctx, r.GroupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		if grp.TargetTenantID != t {
			continue
		}
		if err := as.GroupMembers().Delete(ctx, r.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// revokeTenantTokens revokes, drained, every token bound to t whose owner or
// act-as subject is the account, with its exchanged children.
func revokeTenantTokens(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID) error {
	for _, col := range []string{"user_id", "act_as_user_id"} {
		toks, err := drainList(ctx, as.Tokens().List, byEq(col, user.String(), 0))
		if err != nil {
			return err
		}
		for _, tok := range toks {
			if tok.BoundTenantID == t && !tok.Revoked {
				if err := revokeTokenTree(ctx, as, actor, tok.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// revokeScopedSessions revokes the account's live sessions scoped to t. An
// account-scope session is never revoked by a tenant.
func revokeScopedSessions(ctx context.Context, as store.AuthScope, user model.ID, t model.TenantID) error {
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || s.TenantScope != t {
			continue
		}
		s.Revoked = true
		if _, err := as.Sessions().Update(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// excludeAccountSessions writes a session exclusion from t for every live
// account-scope session of the account, and revokes its sessions scoped to t:
// the tenant cuts every session that carries it, and no other tenant's.
func excludeAccountSessions(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID) error {
	if err := revokeScopedSessions(ctx, as, user, t); err != nil {
		return err
	}
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || !s.TenantScope.IsZero() {
			continue
		}
		if err := excludeSession(ctx, as, actor, user, s.ID, t); err != nil {
			return err
		}
	}
	return nil
}

// excludeSession excludes one account-scope session from t, once.
func excludeSession(ctx context.Context, as store.AuthScope, actor Principal, user, session model.ID, t model.TenantID) error {
	rows, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, x := range rows {
		if x.Kind == model.ExclusionSession && x.SessionID == session && x.TargetTenantID == t {
			return nil
		}
	}
	_, err = as.TenantExclusions().Create(ctx, model.TenantExclusion{
		UserID: user, TargetTenantID: t, SessionID: session, Kind: model.ExclusionSession, CreatedBy: actor.Actor(),
	})
	return err
}

// readmitCustodian re-admits into t an account t itself created (custody
// tenant:t) and later removed. The earlier retirement must have completed: a
// record that is retiring or blocked refuses with ErrRetirementPending and
// nothing is written. A retired record is lifted, keeping its epoch as the
// account's floor in t, so a grant snapshot from before the retirement never
// authorizes the new incarnation. The caller holds the account's authority lock.
func readmitCustodian(ctx context.Context, as store.AuthScope, actor Principal, u model.User, t model.TenantID, role string, workspaceID model.ID) (model.Membership, error) {
	rec, found, err := retirementRecord(ctx, as, u.ID, t)
	if err != nil {
		return model.Membership{}, err
	}
	if found {
		switch rec.RetirementState {
		case model.RetirementLifted:
		case model.RetirementRetired:
			rec.RetirementState = model.RetirementLifted
			rec.NextAttemptAt = nil
			if _, err := as.TenantExclusions().Update(ctx, rec); err != nil {
				return model.Membership{}, err
			}
		default:
			return model.Membership{}, ErrRetirementPending
		}
	}
	if _, err := as.Users().Update(ctx, u); err != nil {
		return model.Membership{}, err
	}
	m, err := as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: t, Role: role, WorkspaceID: workspaceID})
	if err != nil {
		return model.Membership{}, err
	}
	return m, metaAudit(ctx, as, actor, "membership.readmit", "core.membership", m.ID, map[string]any{
		"tenant": t.String(), "generation": rec.RetirementGeneration,
	})
}

// joinOrConsent decides what a tenant write that names an existing account may
// do in t: a member takes the unchanged re-grant path (member=true); an account
// t created is re-admitted by readmitCustodian; any other account, whatever its
// state, answers ErrConsentRequired and nothing is written.
func joinOrConsent(ctx context.Context, as store.AuthScope, u model.User, t model.TenantID) (member bool, custodian bool, err error) {
	if _, ok, err := membershipOf(ctx, as, u.ID, t); err != nil {
		return false, false, err
	} else if ok {
		return true, false, nil
	}
	if !u.IsSuperadmin && u.CredentialCustody == model.CustodyTenant && u.CustodyTenantID == t {
		return false, true, nil
	}
	return false, false, ErrConsentRequired
}
