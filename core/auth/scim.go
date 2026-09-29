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
//   - joiner: create the global User for userName(email) and grant it a
//     membership in the bound tenant (least privilege: viewer by default; an
//     operator elevates roles out of band or via the SCIM group→role mapping,
//     scim_groups.go). A create never writes an account that already exists: one
//     that is already a member of the bound tenant comes back as it stands, one
//     that is not is a uniqueness conflict.
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

// SCIMUserNameKey is the stored address a SCIM userName names. A caller that
// looks for an existing resource before provisioning must read the address the
// way the write reads it, or the two disagree about which account a request
// names and the check passes over the very account the write then finds.
// SCIMProvisionUser applies exactly this function to its input.
func SCIMUserNameKey(userName string) string { return normalizeEmail(userName) }

// SCIMProvisionUser is the joiner path: it creates a global user for
// userName(email), sets its directory attributes, and grants it a membership in
// tenant. It returns the stored user and whether THIS call created it: the route
// answers 201 with a Location for an account it made, and 200 for one it found.
//
// A create never replaces an existing account's directory attributes. An address
// already held by a member of the bound tenant is idempotent only if an explicitly
// supplied externalId matches the stored one exactly. An omitted externalId
// preserves the optional legacy path; create never adopts or clears that field.
// The account comes back as it stands, and PUT/PATCH is how its attributes move.
// An address held by an account that is NOT a member of the bound tenant is
// ErrConflict (the handler maps it to SCIM 409 uniqueness): the address is taken,
// and this connection has no authority over the account holding it.
//
// The two answers are deliberately the same one. A create that succeeded — or
// that failed differently — for an address held elsewhere in the deployment would
// tell one tenant's connection which addresses exist in all the others, and would
// let it write that account's directory attributes and its lifecycle status,
// which is a disable in every tenant the account belongs to. Joining an existing
// account to a tenant requires consent. The existing exception is readmission by
// the account's own custodian after retirement completes; it has the same
// external identity check before the membership or retirement record can change.
func (a *Authenticator) SCIMProvisionUser(ctx context.Context, actor Principal, tenant model.TenantID, in SCIMUserInput) (model.User, bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return model.User{}, false, ErrInvalidToken
	}
	email := SCIMUserNameKey(in.UserName)
	if email == "" {
		return model.User{}, false, ErrInvalidScimUser
	}
	var out model.User
	var created bool
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := refuseForeignDomain(ctx, as, tenant, email, ""); err != nil {
			return err
		}
		existing, _, err := as.Users().List(ctx, byEq("email", email, 1))
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			// The address is taken. Whether this connection may have the resource
			// is decided by the membership boundary every other SCIM verb stands
			// behind (SCIMGetMember). The one account a connection may bring back
			// is one its own tenant created and removed: it is re-admitted once
			// that removal completed. Any other account is not written.
			if err := prepareUserAuthorityWrite(ctx, as, existing[0].ID); err != nil {
				return err
			}
			u, err := as.Users().Get(ctx, existing[0].ID)
			if err != nil {
				return err
			}
			// The locked account, not the email alone, must match an explicit
			// directory identity. Check before custodian readmission can write.
			// Use the same conflict as a foreign account without disclosing it.
			if in.ExternalID != "" && in.ExternalID != u.ExternalID {
				return store.ErrConflict
			}
			member, custodian, err := joinOrConsent(ctx, as, u, tenant)
			switch {
			case errors.Is(err, ErrConsentRequired):
				return store.ErrConflict
			case err != nil:
				return err
			case custodian:
				if _, err := readmitCustodian(ctx, as, actor, u, tenant, SCIMDefaultRole, ""); err != nil {
					return err
				}
			case !member:
				return store.ErrConflict
			}
			out = u
			return nil
		}
		// SSO/SCIM-provisioned: no local password. This tenant created the
		// account, so its credentials are the tenant's.
		u := model.User{Email: email, PasswordHash: "", CredentialCustody: model.CustodyTenant, CustodyTenantID: tenant}
		applyDirectoryAttrs(&u, in)
		if u, err = as.Users().Create(ctx, u); err != nil {
			return err
		}
		created = true
		if err := auditAct(ctx, as, actor, "scim.user.create", "core.user", u.ID); err != nil {
			return err
		}
		// The account is new, so it holds no membership yet: grant the one this
		// connection provisions it into.
		if _, err := as.Memberships().Create(ctx, model.Membership{
			UserID: u.ID, TargetTenantID: tenant, Role: SCIMDefaultRole,
		}); err != nil {
			return err
		}
		if err := auditAct(ctx, as, actor, "scim.user.join", "core.membership", u.ID); err != nil {
			return err
		}
		out = u
		return nil
	})
	return out, created, err
}

// SCIMUpdateUser applies a directory attribute set to a tenant member
// (PUT/PATCH). A tenant never writes an account's global status: active=false
// is a scoped offboard — the resource answers once as active:false and is then
// gone from this tenant — and active=true writes nothing. Attributes are the
// account's global record, so a tenant writes them only for an account it
// alone governs (tenantGoverned) and that the deployment has not suspended; a
// rename also passes the foreign-domain check. Any other change answers
// ErrNotTenantGoverned and nothing is written.
func (a *Authenticator) SCIMUpdateUser(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID, in SCIMUserInput) (model.User, error) {
	if _, err := a.SCIMGetMember(ctx, tenant, id); err != nil {
		return model.User{}, err
	}
	var out model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, id); err != nil {
			return err
		}
		// The preflight may have raced a departure. Preparation holds the
		// directory writer lock, so this membership remains valid until commit.
		if _, ok, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if !ok {
			return store.ErrNotFound
		}
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		if !in.Active {
			if _, err := a.scopedOffboard(ctx, as, actor, id, tenant, "scim.deactivate"); err != nil {
				return err
			}
			out = u
			out.Status = model.StatusInactive // this tenant's view of the resource
			return nil
		}
		next := u
		if in.UserName != "" {
			next.Email = normalizeEmail(in.UserName)
		}
		applyDirectoryAttrs(&next, in)
		next.Status = u.Status
		if next == u {
			out = u
			return nil
		}
		// A globally suspended account's attributes belong to the deployment that
		// suspended it: no tenant writes them, whatever its custody.
		if u.Status != model.StatusActive {
			return ErrNotTenantGoverned
		}
		governed, err := tenantGoverned(ctx, as, u, tenant)
		if err != nil {
			return err
		}
		if !governed {
			return ErrNotTenantGoverned
		}
		if next.Email != u.Email {
			if err := refuseForeignDomain(ctx, as, tenant, next.Email, ""); err != nil {
				return err
			}
		}
		if out, err = as.Users().Update(ctx, next); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "scim.user.update", "core.user", id)
	})
	return out, err
}

// SCIMDeprovisionUser is the leaver path: a scoped offboard of the account
// from tenant. The account's global status, its other tenants, its
// account-scope sessions and its authenticators are never touched, however many
// memberships it has left. An account that is not a member of tenant, read
// inside the transaction, is a no-op. It is idempotent.
func (a *Authenticator) SCIMDeprovisionUser(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// Serialize the membership read before claiming any User authority.
		// An absent resource (including a departed or unknown User) is a no-op:
		// nothing is written, not even a retirement record.
		if err := prepareUserAuthorityWrite(ctx, as); err != nil {
			return err
		}
		if _, ok, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if !ok {
			return nil
		}
		// A member leaves by the scoped offboard, under the same lock: it declares
		// the account's H before its first write.
		_, err := a.scopedOffboard(ctx, as, actor, id, tenant, "scim.deprovision")
		return err
	})
}

// Compound callbacks declare their complete H set before their first G write
// or audit. Repeating that set in a shared helper cannot acquire a later H.
func prepareUserAuthorityWrite(ctx context.Context, as store.AuthScope, ids ...model.ID) error {
	writer, ok := as.(store.AuthUserAuthorityWriter)
	if !ok {
		return store.ErrDirectoryUnavailable
	}
	return writer.PrepareUserAuthorityWrite(ctx, ids)
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
// email/userName: the match key is owned by the provision (create by email) and
// update (conditional rename) paths, which differ on create vs replace. PATCH
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
