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

// SCIM provisioning (RFC 7643/7644) — the engine-side store operations a SCIM
// service-provider handler drives. A SCIM connection is bound to ONE tenant (its
// admin token's tenant); it manages global Users AS MEMBERS of that tenant. So a
// SCIM "user" is the (global User, this-tenant membership) pairing:
//   - joiner: find-or-create the global User by userName(email) and grant it a
//     membership in the bound tenant (least privilege: viewer by default; an
//     operator elevates roles out of band or via the SCIM group→role mapping,
//     scim_groups.go).
//   - mover: update directory attributes; active=false offboards.
//   - leaver: remove the membership in the bound tenant and revoke the user's
//     tenant-bound tokens; if the user is left in NO tenant, deactivate the
//     account and revoke all its sessions (the "departed employee" path).
// A SCIM token for tenant T can never see or mutate a user that is not a member
// of T (the cross-tenant isolation a security team checks).

// SCIMDefaultRole is the role a SCIM-provisioned user receives in the bound
// tenant. Least privilege: a freshly provisioned identity reads, nothing more,
// until an operator elevates it.
const SCIMDefaultRole = RoleViewer

// ErrInvalidScimUser means a SCIM user resource is missing its required userName.
// The handler maps it to SCIM 400 invalidValue.
var ErrInvalidScimUser = errors.New("auth: invalid SCIM user (userName required)")

// SCIMUserInput is the directory attribute set a SCIM create/replace carries.
type SCIMUserInput struct {
	// UserName is the SCIM userName, mapped to the user's email (unique, the IdP's
	// match key). Normalized on write.
	UserName string
	// ExternalID is the IdP's stable id (SCIM externalId); optional.
	ExternalID string
	// DisplayName is a human label.
	DisplayName string
	// Active is the SCIM administrative status; false offboards.
	Active bool
	// The SCIM enterprise User extension attributes (RFC 7643 §4.3) the provider
	// stores write-through. Optional; empty leaves/clears the stored value.
	EmployeeNumber string
	Department     string
	Manager        string
	// Agent extension (draft-abbey-scim-agent-extension-00 — defensive/
	// opt-in, never mandatory). Populated when the IdP sends the extension;
	// empty when absent (the user provisions normally). Carried here for future
	// consumers; not wired to enforcement.
	AgentKind       string
	AgentSponsorRef string
	AgentDelegation string
}

// SCIMProvisionUser find-or-creates a global user by userName(email), sets its
// directory attributes, and ensures it has a membership in tenant. It returns the
// stored user and whether it was newly created (so the handler can answer 201 vs
// 200). A userName already taken by a DIFFERENT account surfaces as ErrConflict
// (the handler maps it to SCIM 409 uniqueness).
func (a *Authenticator) SCIMProvisionUser(ctx context.Context, actor Principal, tenant model.TenantID, in SCIMUserInput) (model.User, bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return model.User{}, false, ErrInvalidToken
	}
	email := normalizeEmail(in.UserName)
	if email == "" {
		return model.User{}, false, ErrInvalidScimUser
	}
	var out model.User
	var created bool
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		existing, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		var u model.User
		if len(existing) > 0 {
			u = existing[0]
			applyDirectoryAttrs(&u, in)
			if u, err = as.Users().Update(ctx, u); err != nil {
				return err
			}
		} else {
			u = model.User{Email: email, PasswordHash: ""} // SSO/SCIM-provisioned: no local password
			applyDirectoryAttrs(&u, in)
			u, err = as.Users().Create(ctx, u)
			if err != nil {
				return err
			}
			created = true
			if err := auditAct(ctx, as, actor, "scim.user.create", "core.user", u.ID); err != nil {
				return err
			}
		}
		// Ensure the membership in the bound tenant (idempotent).
		if _, ok, err := membershipOf(ctx, as, u.ID, tenant); err != nil {
			return err
		} else if !ok {
			if _, err := as.Memberships().Create(ctx, model.Membership{
				UserID: u.ID, TargetTenantID: tenant, Role: SCIMDefaultRole,
			}); err != nil {
				return err
			}
			if err := auditAct(ctx, as, actor, "scim.user.join", "core.membership", u.ID); err != nil {
				return err
			}
		}
		out = u
		return nil
	})
	return out, created, err
}

// SCIMUpdateUser applies a full directory attribute set to a tenant member
// (PUT/PATCH). When active flips to false it DISABLES the account — the SCIM
// resource remains retrievable as active:false, but the user's access is cut:
// its tenant-bound tokens (and their exchanged children) and ALL its sessions are
// revoked. The membership stays (the record is preserved until a DELETE
// offboards it). It returns the updated user.
func (a *Authenticator) SCIMUpdateUser(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID, in SCIMUserInput) (model.User, error) {
	if _, err := a.SCIMGetMember(ctx, tenant, id); err != nil {
		return model.User{}, err
	}
	var out model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		if in.UserName != "" {
			u.Email = normalizeEmail(in.UserName)
		}
		applyDirectoryAttrs(&u, in)
		if out, err = as.Users().Update(ctx, u); err != nil {
			return err
		}
		if err := auditAct(ctx, as, actor, "scim.user.update", "core.user", id); err != nil {
			return err
		}
		if !in.Active {
			// Disabling cuts access: revoke tenant-bound tokens (cascade) + all sessions.
			if err := revokeUserAccess(ctx, as, actor, id, tenant, true); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// revokeUserAccess revokes the user's tokens bound to tenant (cascading to their
// exchanged children) and, when allSessions is true, every session the user
// holds. It is the shared credential-cut used by both disable and deprovision.
func revokeUserAccess(ctx context.Context, as store.AuthScope, actor Principal, id model.ID, tenant model.TenantID, allSessions bool) error {
	toks, _, err := as.Tokens().List(ctx, byEq("user_id", id.String(), 1000))
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.BoundTenantID == tenant && !t.Revoked {
			if err := revokeTokenTree(ctx, as, actor, t.ID); err != nil {
				return err
			}
		}
	}
	if !allSessions {
		return nil
	}
	sessions, _, err := as.Sessions().List(ctx, byEq("user_id", id.String(), 1000))
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

// SCIMDeprovisionUser is the leaver path. It removes the user's membership in
// tenant — and its rows in this tenant's groups — and revokes the user's tokens
// BOUND to that tenant (cascading to their exchanged children). If the user is
// left with no memberships at all, the account is deactivated and ALL its
// sessions revoked — the departed-employee guarantee (IDN-04). It is
// idempotent.
func (a *Authenticator) SCIMDeprovisionUser(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// 1. Remove the membership in this tenant.
		if m, ok, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if ok {
			if err := as.Memberships().Delete(ctx, m.ID); err != nil {
				return err
			}
			if err := auditAct(ctx, as, actor, "scim.user.leave", "core.membership", id); err != nil {
				return err
			}
		}
		// 2. Remove the user's rows in THIS tenant's groups. A stale member row
		// grants nothing today (loadGrants requires a direct membership in the
		// group's tenant), but it would silently RE-ELEVATE the user the moment a
		// membership reappeared — the leaver must leave the rosters too. Rows
		// whose group is gone are left alone (no tenant attribution to act on);
		// groups of OTHER tenants are untouched (a tenant's SCIM connection never
		// edits another tenant's rosters).
		rows, err := drainList(ctx, as.GroupMembers().List, byEq("user_id", id.String(), 0))
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
			if grp.TargetTenantID != tenant {
				continue
			}
			if err := as.GroupMembers().Delete(ctx, r.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		// 3. If the user has remaining memberships elsewhere, only its tenant-bound
		// credentials are cut. If it is left in NO tenant, deactivate the account
		// and revoke every session — the departed-employee guarantee.
		remaining, _, err := as.Memberships().List(ctx, byEq("user_id", id.String(), 1))
		if err != nil {
			return err
		}
		orphaned := len(remaining) == 0
		if err := revokeUserAccess(ctx, as, actor, id, tenant, orphaned); err != nil {
			return err
		}
		if orphaned {
			u, err := as.Users().Get(ctx, id)
			if err != nil {
				return err
			}
			if u.Status != model.StatusInactive {
				u.Status = model.StatusInactive
				if _, err := as.Users().Update(ctx, u); err != nil {
					return err
				}
			}
			// a departed employee's registered authenticators go with the
			// account — a later re-activation never inherits the leaver's
			// hardware bindings.
			creds, _, err := as.WebAuthnCredentials().List(ctx, byEq("user_id", id.String(), 1000))
			if err != nil {
				return err
			}
			for _, c := range creds {
				if err := as.WebAuthnCredentials().Delete(ctx, c.ID); err != nil {
					return err
				}
			}
			if err := auditAct(ctx, as, actor, "scim.user.deprovision", "core.user", id); err != nil {
				return err
			}
		}
		return nil
	})
}

// SCIMListMembers returns the users that are members of tenant (the SCIM resource
// set for that connection).
func (a *Authenticator) SCIMListMembers(ctx context.Context, tenant model.TenantID) ([]model.User, error) {
	var users []model.User
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		// Drained, not single-page: the SCIM resource set must be complete (the
		// IdP reconciles against it), and the store clamps any Limit to 1000.
		ms, err := drainList(ctx, as.Memberships().List, byEq("target_tenant_id", tenant.String(), 0))
		if err != nil {
			return err
		}
		for _, m := range ms {
			u, err := as.Users().Get(ctx, m.UserID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return err
			}
			users = append(users, u)
		}
		return nil
	})
	return users, err
}

// SCIMFindMember returns a tenant member matched by an indexed attribute (column
// "email" or "external_id") equal to value — the fast path for the IdP's
// pre-create existence check (userName eq / externalId eq). found is false when
// no member matches.
func (a *Authenticator) SCIMFindMember(ctx context.Context, tenant model.TenantID, column, value string) (model.User, bool, error) {
	var out model.User
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, byEq(column, value, 10))
		if err != nil {
			return err
		}
		for _, u := range us {
			if _, ok, err := membershipOf(ctx, as, u.ID, tenant); err != nil {
				return err
			} else if ok {
				out, found = u, true
				return nil
			}
		}
		return nil
	})
	return out, found, err
}

// SCIMGetMember returns a tenant member by id, or ErrNotFound when the user does
// not exist OR is not a member of tenant (so a SCIM token cannot probe the global
// user table — the not-found==not-a-member rule mirrors the cross-tenant oracle
// guard).
func (a *Authenticator) SCIMGetMember(ctx context.Context, tenant model.TenantID, id model.ID) (model.User, error) {
	var out model.User
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		if _, ok, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if !ok {
			return store.ErrNotFound
		}
		out = u
		return nil
	})
	return out, err
}

// membershipOf returns the user's membership in tenant and whether one exists.
func membershipOf(ctx context.Context, as store.AuthScope, userID model.ID, tenant model.TenantID) (model.Membership, bool, error) {
	ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
		{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
	}, Limit: 1})
	if err != nil {
		return model.Membership{}, false, err
	}
	if len(ms) == 0 {
		return model.Membership{}, false, nil
	}
	return ms[0], true, nil
}

// applyDirectoryAttrs copies the SCIM mover attributes a create/replace carries —
// display name, externalId, the active→status mapping and the enterprise-extension
// fields (employeeNumber, department, manager) — from in onto u. It does NOT touch
// email/userName: the match key is owned by the provision (find-or-create by email)
// and update (conditional rename) paths, which differ on create vs replace. PATCH
// reaches here too, having pre-merged the current state into in (handlers_scim.go),
// so an attribute the PATCH did not mention keeps its current value rather than
// being cleared.
func applyDirectoryAttrs(u *model.User, in SCIMUserInput) {
	u.DisplayName = in.DisplayName
	u.ExternalID = in.ExternalID
	u.Status = scimStatus(in.Active)
	u.EmployeeNumber = in.EmployeeNumber
	u.Department = in.Department
	u.Manager = in.Manager
}

// scimStatus maps SCIM active to the lifecycle status.
func scimStatus(active bool) model.LifecycleStatus {
	if active {
		return model.StatusActive
	}
	return model.StatusInactive
}
