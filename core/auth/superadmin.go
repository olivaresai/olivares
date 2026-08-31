// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Superadmin lifecycle: enable/disable the INTERNAL superadmin account(s).
// A superadmin is a GLOBAL, system-path principal with no business-tenant
// membership (model.User.IsSuperadmin), so the tenant-scoped SCIM activate/
// deactivate (SCIMSetMemberActive) cannot reach it — this is its only lifecycle
// surface. Some regulations/security policies require that a standing built-in
// administrator be disable-able; this provides that, NON-DESTRUCTIVELY: the
// account is flipped to StatusInactive and its live credentials are cut, never
// deleted, and re-enabling restores it. A disabled superadmin drops out of the
// ACTIVE population the console reports (seatcap.go, ActiveUserCount) — a usage
// figure only: since B10 no seat limit is enforced in any tier.

// ErrNotSuperadmin means the lifecycle target is not a superadmin account. This
// surface governs only superadmin (global, system-path) principals; a business-
// tenant member is enabled/disabled through the SCIM / onboarding paths, which
// preserve its membership. Mapped to 409.
var ErrNotSuperadmin = errors.New("auth: target is not a superadmin account")

// ErrLastSuperadmin is the deny-closed guard against TOTAL lockout: disabling the
// last ACTIVE superadmin would leave no principal able to reach the System path,
// and re-enabling is itself a superadmin action — an irreversible-by-normal-means
// state. At least one OTHER active superadmin must exist (provision a second one
// first) before this one can be disabled. Mapped to 409.
var ErrLastSuperadmin = errors.New("auth: cannot disable the last active superadmin")

// SetSuperadminActive enables or disables an INTERNAL superadmin account, recording
// the acting principal. It is the global-principal counterpart to the tenant-scoped
// SCIMSetMemberActive.
//
// Disabling (active=false) flips the account to StatusInactive — which blocks
// password login (Login) and existing SESSIONS (authSession re-checks status) — AND
// revokes every credential the account holds: all sessions and all of its API
// tokens (cascading delegated children). The token revocation is LOAD-BEARING, not
// belt-and-braces: authToken does NOT re-check user status, so a superadmin's
// UNBOUND system token would otherwise keep full access after a mere status flip.
// revokeUserAccess is tenant-scoped (it filters tokens by bound tenant) and cannot
// cut an unbound superadmin token; revokeAllUserCredentials does.
//
// Enabling (active=true) restores StatusActive only. Credentials revoked at disable
// time are NOT revived (revocation is one-way); the superadmin authenticates afresh.
// This mirrors the SCIM re-activate semantics.
//
// It is deny-closed against total lockout (ErrLastSuperadmin) and idempotent: a
// no-op call still records the (re)assertion on the ledger but changes no state. The
// lockout guard and the flip run in ONE write transaction so they commit atomically;
// under Postgres a perfectly concurrent pair at the boundary shares the same bounded
// RW-tx window the federation cap accepts (federation_config.go), and even that edge
// is recoverable —
// `olivares superadmin enable` revives an account offline.
func (a *Authenticator) SetSuperadminActive(ctx context.Context, actor Principal, id model.ID, active bool) (model.User, error) {
	var out model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		if !u.IsSuperadmin {
			return ErrNotSuperadmin
		}
		want, action := model.StatusActive, "user.superadmin.enable"
		if !active {
			want, action = model.StatusInactive, "user.superadmin.disable"
		}
		// Deny-closed lockout guard: never let a disable leave ZERO active
		// superadmins. Only relevant when this account is currently active and is
		// about to be deactivated.
		if !active && u.Status == model.StatusActive {
			other, err := hasOtherActiveSuperadmin(ctx, as, u.ID)
			if err != nil {
				return err
			}
			if !other {
				return ErrLastSuperadmin
			}
		}
		if u.Status != want {
			u.Status = want
			if u, err = as.Users().Update(ctx, u); err != nil {
				return err
			}
		}
		out = u
		if err := auditAct(ctx, as, actor, action, "core.user", id); err != nil {
			return err
		}
		// Cut live credentials on disable (idempotent — safe to re-run on a no-op
		// re-disable). Unbound superadmin tokens MUST be revoked explicitly because
		// authToken does not check user status.
		if !active {
			return revokeAllUserCredentials(ctx, as, actor, id)
		}
		return nil
	})
	if err != nil {
		return model.User{}, err
	}
	return out, nil
}

// ListSuperadmins returns every superadmin account (active and inactive) for the
// operator lifecycle surfaces (the console superadmin list, the `olivares
// superadmin status` CLI). It is read-only and pages to completion.
func (a *Authenticator) ListSuperadmins(ctx context.Context) ([]model.User, error) {
	var out []model.User
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		us, err := drainList(ctx, as.Users().List, model.Query{
			Filters: []model.Filter{{Column: "is_superadmin", Op: model.OpEq, Value: true}},
		})
		out = us
		return err
	})
	return out, err
}

// hasOtherActiveSuperadmin reports whether at least one ACTIVE superadmin OTHER
// than self exists. It filters on the status + is_superadmin columns and reads at
// most two rows: the guard only needs to know whether a SECOND active superadmin
// exists besides self. With >=2 active superadmins any two returned include one
// that is not self; with exactly one (self), only self is returned. self is
// guaranteed to be in the filtered set (the caller checked it is an active
// superadmin in this same transaction before the flip).
func hasOtherActiveSuperadmin(ctx context.Context, as store.AuthScope, self model.ID) (bool, error) {
	admins, _, err := as.Users().List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: "status", Op: model.OpEq, Value: string(model.StatusActive)},
			{Column: "is_superadmin", Op: model.OpEq, Value: true},
		},
		Limit: 2,
	})
	if err != nil {
		return false, err
	}
	for _, u := range admins {
		if u.ID != self {
			return true, nil
		}
	}
	return false, nil
}

// revokeAllUserCredentials revokes EVERY credential a user holds: all of its API
// tokens (regardless of binding — a superadmin's system tokens are UNBOUND, so the
// tenant-scoped revokeUserAccess cannot reach them), cascading each token's
// delegated children, plus all of its sessions. It is the full credential cut used
// when DISABLING a global principal. It pages to completion (no silent truncation)
// and is idempotent (already-revoked rows are skipped).
func revokeAllUserCredentials(ctx context.Context, as store.AuthScope, actor Principal, id model.ID) error {
	toks, err := drainList(ctx, as.Tokens().List, byEq("user_id", id.String(), 0))
	if err != nil {
		return err
	}
	for _, t := range toks {
		if !t.Revoked {
			if err := revokeTokenTree(ctx, as, actor, t.ID); err != nil {
				return err
			}
		}
	}
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", id.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if !s.Revoked {
			s.Revoked = true
			if _, err := as.Sessions().Update(ctx, s); err != nil {
				return err
			}
		}
	}
	return nil
}
