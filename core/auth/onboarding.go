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
// from the console — create (or reuse) the account and grant its membership in
// ONE transaction. It is the tenant-scoped counterpart to the superadmin-only
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
	// InviteToken is the single-use token to deliver to the invitee (invite mode,
	// new account only). Shown ONCE — never stored in cleartext, never logged.
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
		existing, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			// The person already has an account: just grant the membership. We never
			// reset another account's password or re-invite an existing user from here.
			res.User = existing[0]
			res.Created = false
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
	if len(password) < MinPasswordLen {
		return "", model.AuthSession{}, ErrWeakPassword
	}
	_, selector, secret, ok := ParseToken(token)
	if !ok {
		return "", model.AuthSession{}, ErrInviteInvalid
	}
	var (
		user model.User
		sess model.AuthSession
		tok  string
	)
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		invites, _, err := as.Invites().List(ctx, byEq("selector", selector, 1))
		if err != nil {
			return err
		}
		if len(invites) == 0 {
			return ErrInviteInvalid
		}
		inv := invites[0]
		now := a.clock.Now()
		if inv.AcceptedAt != nil || inv.ExpiresAt.Before(now) || !SecretMatches(secret, inv.SecretHash) {
			return ErrInviteInvalid
		}
		// The token is proven valid — only NOW pay for the argon2 hash, so an
		// unauthenticated caller spraying bogus tokens cannot force expensive
		// hashing (the cheap, high-entropy token check gates it).
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		// Resolve the invited account by the invite's email (the account was created
		// at invite time). Defensive: a missing account voids the invite.
		users, _, err := as.Users().List(ctx, byEq("email", normalizeEmail(inv.Email), 1))
		if err != nil {
			return err
		}
		if len(users) == 0 {
			return ErrInviteInvalid
		}
		u := users[0]
		u.PasswordHash = hash
		u.Status = model.StatusActive
		if u, err = as.Users().Update(ctx, u); err != nil {
			return err
		}
		inv.AcceptedAt = &now
		if _, err := as.Invites().Update(ctx, inv); err != nil {
			return err
		}
		// The activation is attributed to the account itself (the invitee redeeming
		// their own invite), never to an email or the issuing admin.
		if _, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:" + u.ID.String(), ActorKind: model.ActorUser,
			Action: "user.invite.accept", TargetKind: "core.user_invite", TargetID: inv.ID,
		}); err != nil {
			return err
		}
		user = u
		// Mint the session in the same transaction so activation and credential
		// commit atomically. A password-set login is AAL1 with amr ["pwd"].
		t, s, err := a.mintSessionTx(ctx, as, user, ip, "user.invite.accept", []string{"pwd"})
		if err != nil {
			return err
		}
		tok, sess = t, s
		return nil
	})
	if err != nil {
		return "", model.AuthSession{}, err
	}
	return tok, sess, nil
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
			if inv.AcceptedAt == nil && !inv.ExpiresAt.Before(now) {
				out = append(out, inv)
			}
		}
		return nil
	})
	return out, err
}

// RevokeInvite deletes a pending invitation. It is bound to tenant so a caller
// can only revoke its own tenant's invites (a cross-tenant id reads as not-found).
func (a *Authenticator) RevokeInvite(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
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
