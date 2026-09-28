// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Console onboarding (FASE X): bring a NON-federated person into a tenant
// from the console — create the account and grant its membership in ONE
// transaction. An address that already has an account never joins it: a member
// takes the unchanged re-grant path, an account this tenant created and later
// removed is re-admitted once its removal completed, and any other account
// answers ErrConsentRequired with nothing written. It is the tenant-scoped counterpart to the superadmin-only
// CreateUser: the actor needs membership:write in the target tenant (RBAC, gated
// at the handler) and the granted role passes the actor's role ceiling, and a new
// account can NEVER be made a superadmin here. Two delivery modes:
//
//   - password: the admin sets the initial password; the account is active at once.
//   - invite:   a single-use token is minted; the invitee sets their own password
//     at AcceptInvite. The membership is granted up front so access is live the
//     moment the account is activated.
//
// SSO-only remains the federated path (FindOrProvisionByEmail) and is untouched.

// inviteTTL bounds how long an onboarding invitation may sit unaccepted.
const inviteTTL = 7 * 24 * time.Hour

// ErrInviteInvalid means an onboarding invite token is unknown, expired or
// already used. It is deliberately coarse (one error for all three) so the accept
// endpoint is never an oracle for which tokens exist. Mapped to 400.
var ErrInviteInvalid = errors.New("auth: invite is invalid, expired or already used")

// OnboardInput is the input to OnboardMember.
type OnboardInput struct {
	// Email is the invitee's login identifier.
	Email string
	// DisplayName is a human label for a newly created account.
	DisplayName string
	// Role is the built-in role to grant in the tenant (ceiling-checked).
	Role string
	// Password sets the initial password (password mode). Empty selects invite mode.
	Password string
	// Invite selects invite mode (mint a single-use token) instead of a password.
	Invite bool
}

// OnboardResult is the outcome of OnboardMember.
type OnboardResult struct {
	// User is the created or reused account.
	User model.User
	// Membership is the granted (or updated) membership.
	Membership model.Membership
	// Created reports whether a NEW account was created (false = the email already
	// had an account and only the membership was granted/updated).
	Created bool
	// InviteToken is the single-use token for the invitation mailer (invite mode,
	// new account only). It goes only to the invitee's address, never into a
	// response, a log or the audit.
	InviteToken string
	// InviteID is the stored invite's id (for revocation/listing).
	InviteID model.ID
	// ExpiresAt is when the invite expires (invite mode only).
	ExpiresAt *model.Timestamp
}

// OnboardMember creates-or-reuses an account and grants its tenant membership in
// one transaction. A new account is NEVER a superadmin. The role is validated and
// ceiling-checked against the actor before any write.
func (a *Authenticator) OnboardMember(ctx context.Context, actor Principal, tenant model.TenantID, in OnboardInput) (OnboardResult, error) {
	if !IsRole(in.Role) {
		return OnboardResult{}, ErrInvalidRole
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return OnboardResult{}, ErrInvalidToken
	}
	if err := checkRoleCeiling(actor, tenant, in.Role); err != nil {
		return OnboardResult{}, err
	}
	// onboarding grants a TENANT-WIDE membership (grantMembershipTx with a zero
	// workspace, below), so a workspace-confined actor may not onboard — it has no tenant-level
	// authority and cannot mint members outside its fence (mirrors the GrantMembership guard;
	// the HTTP PEP already forbids the collection write, this is the service-layer backstop).
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return OnboardResult{}, ErrWorkspaceConfined
	}
	email := normalizeEmail(in.Email)
	if email == "" {
		return OnboardResult{}, ErrInvalidToken
	}
	if !in.Invite { // password mode validates the password up front
		if len(in.Password) < MinPasswordLen {
			return OnboardResult{}, ErrWeakPassword
		}
	}
	var (
		passwordHash string
		res          OnboardResult
		inviteCred   Credential
	)
	if !in.Invite {
		h, err := HashPassword(in.Password)
		if err != nil {
			return OnboardResult{}, err
		}
		passwordHash = h
	} else {
		c, err := NewCredential(PrefixInvite)
		if err != nil {
			return OnboardResult{}, err
		}
		inviteCred = c
	}

	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// A foreign-claimed domain is refused before the address is looked up, so
		// the answer reveals configuration, never an account.
		if err := refuseForeignDomain(ctx, as, tenant, email, ""); err != nil {
			return err
		}
		existing, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			// The person already has an account. This tenant never joins it, never
			// resets its password and never re-invites it.
			if err := prepareUserAuthorityWrite(ctx, as, existing[0].ID); err != nil {
				return err
			}
			u, err := as.Users().Get(ctx, existing[0].ID)
			if err != nil {
				return err
			}
			member, custodian, err := joinOrConsent(ctx, as, u, tenant)
			if err != nil {
				return err
			}
			res.User = u
			res.Created = false
			if custodian {
				m, err := readmitCustodian(ctx, as, actor, u, tenant, in.Role, "")
				res.Membership = m
				return err
			}
			if !member {
				return ErrConsentRequired
			}
		} else {
			// The retained seat seam over a NEW account (reusing an existing one,
			// the branch above, never went through it). Since B10 it is an
			// unconditional no-op: onboarding is never refused for seat reasons.
			if err := a.enforceSeatCapTx(ctx, as); err != nil {
				return err
			}
			u, err := as.Users().Create(ctx, model.User{
				Email: email, DisplayName: in.DisplayName, Status: model.StatusActive,
				PasswordHash: passwordHash, IsSuperadmin: false, // deny-closed: never superadmin
				// This tenant created the account: its credentials are the tenant's.
				CredentialCustody: model.CustodyTenant, CustodyTenantID: tenant,
			})
			if err != nil {
				return err
			}
			if err := auditAct(ctx, as, actor, "user.create", "core.user", u.ID); err != nil {
				return err
			}
			res.User = u
			res.Created = true
			if in.Invite {
				exp := model.NewTimestamp(a.clock.Now().Time().Add(inviteTTL))
				inv, err := as.Invites().Create(ctx, model.UserInvite{
					Email: email, TargetTenantID: tenant, Role: in.Role,
					Selector: inviteCred.Selector, SecretHash: inviteCred.SecretHash,
					ExpiresAt: exp, CreatedBy: actor.Actor(),
				})
				if err != nil {
					return err
				}
				if err := auditAct(ctx, as, actor, "user.invite", "core.user_invite", inv.ID); err != nil {
					return err
				}
				res.InviteID = inv.ID
				res.InviteToken = inviteCred.Token
				res.ExpiresAt = &exp
			}
		}
		// Onboarding grants a tenant-wide membership (no workspace confinement).
		m, err := grantMembershipTx(ctx, as, actor, res.User.ID, tenant, in.Role, model.ID(""))
		if err != nil {
			return err
		}
		res.Membership = m
		return nil
	})
	if err != nil {
		return OnboardResult{}, err
	}
	return res, nil
}

// AcceptInvite redeems a single-use onboarding token: it sets the account's
// password, activates it, marks the invite used, and mints a fresh session — all
// atomically. An unknown/expired/used token is ErrInviteInvalid (coarse, no
// oracle). It throttles nothing here because the token itself is the high-entropy
// gate; a brute force would have to guess a 256-bit secret.
func (a *Authenticator) AcceptInvite(ctx context.Context, token, password, ip string) (string, model.AuthSession, error) {
	attempt, err := a.beginLogin(ctx, ip)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	if len(password) < MinPasswordLen {
		return "", model.AuthSession{}, ErrWeakPassword
	}
	_, selector, secret, ok := ParseToken(token)
	if !ok {
		return "", model.AuthSession{}, ErrInviteInvalid
	}
	var user model.User
	var invite model.UserInvite
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		invite, user, err = a.pendingInvite(ctx, as, selector, secret)
		return err
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	// The invitation proves identity before password-policy evaluation. Hashing is
	// outside the write transaction, like password Login, and invalid tokens never
	// reach it. Both decisive records are revalidated before their first mutation.
	hash, err := HashPassword(password)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	return a.mintSession(ctx, attempt, user, user.CustodyScope(), "user.invite.accept", passwordLogin, func(as store.AuthScope) error {
		inv, u, err := a.pendingInvite(ctx, as, selector, secret)
		if err != nil {
			return err
		}
		if inv.ID != invite.ID || inv.Version != invite.Version || u.ID != user.ID || u.Version != user.Version {
			return ErrInviteInvalid
		}
		if err := prepareUserAuthorityWrite(ctx, as, u.ID); err != nil {
			return err
		}
		if err := inviteStillOwnsAccount(ctx, as, inv, u); err != nil {
			return err
		}
		u.PasswordHash = hash
		if _, err := as.Users().Update(ctx, u); err != nil {
			return err
		}
		now := a.clock.Now()
		inv.AcceptedAt = &now
		if _, err := as.Invites().Update(ctx, inv); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:" + u.ID.String(), ActorKind: model.ActorUser,
			Action: "user.invite.accept", TargetKind: "core.user_invite", TargetID: inv.ID,
		})
		return err
	})
}

// inviteStillOwnsAccount refuses an invitation whose account is no longer
// exactly what the invitation created: active, never a superadmin, with no
// password, no sign-in subject and no session yet, and a member of the
// invitation's tenant only. Redeeming it never writes the account's status.
func inviteStillOwnsAccount(ctx context.Context, as store.AuthScope, inv model.UserInvite, u model.User) error {
	if u.Status != model.StatusActive || u.IsSuperadmin || u.PasswordHash != "" || u.SsoSubject != "" {
		return ErrInviteInvalid
	}
	ms, err := drainList(ctx, as.Memberships().List, byEq("user_id", u.ID.String(), 0))
	if err != nil {
		return err
	}
	if len(ms) != 1 || ms[0].TargetTenantID != inv.TargetTenantID {
		return ErrInviteInvalid
	}
	sessions, _, err := as.Sessions().List(ctx, byEq("user_id", u.ID.String(), 1))
	if err != nil {
		return err
	}
	if len(sessions) != 0 {
		return ErrInviteInvalid
	}
	return nil
}

// pendingInvite reads both authority records without writing. The caller must
// revalidate them in its mutation after work performed outside the store scope.
func (a *Authenticator) pendingInvite(ctx context.Context, as store.AuthScope, selector, secret string) (model.UserInvite, model.User, error) {
	invites, _, err := as.Invites().List(ctx, byEq("selector", selector, 1))
	if err != nil {
		return model.UserInvite{}, model.User{}, err
	}
	if len(invites) == 0 {
		return model.UserInvite{}, model.User{}, ErrInviteInvalid
	}
	inv := invites[0]
	if inv.AcceptedAt != nil || !a.clock.Now().Before(inv.ExpiresAt) || !SecretMatches(secret, inv.SecretHash) {
		return model.UserInvite{}, model.User{}, ErrInviteInvalid
	}
	users, _, err := as.Users().List(ctx, byEq("email", normalizeEmail(inv.Email), 1))
	if err != nil {
		return model.UserInvite{}, model.User{}, err
	}
	if len(users) == 0 {
		return model.UserInvite{}, model.User{}, ErrInviteInvalid
	}
	return inv, users[0], nil
}

// ListPendingInvites returns a tenant's unaccepted, unexpired invitations (no
// token material). It is the console's "pending invites" list.
func (a *Authenticator) ListPendingInvites(ctx context.Context, tenant model.TenantID) ([]model.UserInvite, error) {
	var out []model.UserInvite
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		invites, _, err := as.Invites().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}},
			Limit:   1000,
		})
		if err != nil {
			return err
		}
		now := a.clock.Now()
		for _, inv := range invites {
			// Same half-open window as AcceptInvite: pending means strictly before
			// ExpiresAt, so an invite at its exact expiration instant is already gone
			// from the list and the console never offers a token the accept leg refuses.
			if inv.AcceptedAt == nil && now.Before(inv.ExpiresAt) {
				out = append(out, inv)
			}
		}
		return nil
	})
	return out, err
}

// ResendInvite rotates a pending invitation's secret and restarts its expiry,
// so only the newly mailed token redeems it. It returns the invitation and the
// new token for the invitation mailer; the token never reaches a response.
func (a *Authenticator) ResendInvite(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) (model.UserInvite, string, error) {
	cred, err := NewCredential(PrefixInvite)
	if err != nil {
		return model.UserInvite{}, "", err
	}
	var out model.UserInvite
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// The same lock order as RevokeInvite: global admission before the row.
		if err := prepareUserAuthorityWrite(ctx, as); err != nil {
			return err
		}
		inv, err := as.Invites().Get(ctx, id)
		if err != nil {
			return err
		}
		if inv.TargetTenantID != tenant {
			return store.ErrNotFound
		}
		if inv.AcceptedAt != nil {
			return ErrInviteInvalid
		}
		inv.Selector, inv.SecretHash = cred.Selector, cred.SecretHash
		inv.ExpiresAt = model.NewTimestamp(a.clock.Now().Time().Add(inviteTTL))
		if out, err = as.Invites().Update(ctx, inv); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "user.invite.resend", "core.user_invite", id)
	})
	if err != nil {
		return model.UserInvite{}, "", err
	}
	return out, cred.Token, nil
}

// RevokeInvite deletes a pending invitation. It is bound to tenant so a caller
// can only revoke its own tenant's invites (a cross-tenant id reads as not-found).
func (a *Authenticator) RevokeInvite(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// LOCK ORDER, not authorization. This callback deletes an invitation row and
		// then appends an audit event, and the audit log the auth scope hands out
		// takes the GLOBAL directory lock BEFORE any audit lock. Reaching the
		// invitation row first therefore ran the pair in the INVERSE order to
		// AcceptInvite, which already reaches global through Users().Update and only
		// then locks that same invitation row. On PostgreSQL the two form a real
		// cycle — Accept holds global and waits for the row, Revoke holds the row and
		// waits for global — and one of them dies with a deadlock.
		//
		// The admission door is the compound-callback helper with an EMPTY User set:
		// revocation changes no User, so it declares none. PrepareUserAuthorityWrite
		// still runs the directory prepare for a zero-length set, which is precisely
		// what is wanted here — it takes global before the first source write and
		// restores the transaction's tenant presentation without bumping H or any
		// tenant epoch. Nothing below is relaxed: the tenant check, the coarse
		// not-found and the delete/audit atomicity are unchanged.
		if err := prepareUserAuthorityWrite(ctx, as); err != nil {
			return err
		}
		inv, err := as.Invites().Get(ctx, id)
		if err != nil {
			return err
		}
		if inv.TargetTenantID != tenant {
			return store.ErrNotFound // never a cross-tenant existence oracle
		}
		if err := as.Invites().Delete(ctx, id); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "user.invite.revoke", "core.user_invite", id)
	})
}
