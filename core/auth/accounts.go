// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Account/credential management errors.
var (
	// ErrSetupComplete means a superadmin already exists, so first-boot setup is
	// closed.
	ErrSetupComplete = errors.New("auth: setup already complete")

	// ErrCoordinationUnavailable means a decision that must be serialized across
	// the cluster could not take its lock, so it refused rather than race. It is
	// RETRYABLE — nothing was written — and it is deliberately distinct from the
	// domain refusals next to it: "someone already did it" and "I could not tell
	// whether anyone did" are different facts, and collapsing them would tell an
	// operator the work is done when nothing was written.
	ErrCoordinationUnavailable = errors.New("auth: coordination unavailable")
	// ErrWeakPassword means a password is shorter than the minimum length.
	ErrWeakPassword = errors.New("auth: password too short")
	// ErrInvalidRole means a role name is not a built-in role.
	ErrInvalidRole = errors.New("auth: invalid role")
	// ErrInvalidToken means a token specification is internally inconsistent
	// (e.g. superadmin with a bound tenant, or a bound token with no tenant/role).
	ErrInvalidToken = errors.New("auth: invalid token specification")
	// ErrRoleCeiling means the actor tried to grant or mint a role above its own
	// in the target tenant (a vertical privilege-escalation guard). The API maps
	// it to 403.
	ErrRoleCeiling = errors.New("auth: cannot grant a role above your own")
	// ErrWorkspaceConfined guards the confinement at the authority-CHANGING writer
	// paths (token issuance, membership grants) that the Scoped forbid cannot reach —
	// those are tenant-level actions with no resolvable target workspace. A workspace-confined
	// principal may not mint a tenant-wide credential nor author a membership outside its own
	// workspace (either would lift its own confinement). The API maps it to 403.
	ErrWorkspaceConfined = errors.New("auth: a workspace-confined principal cannot perform this tenant-wide action")
)

// checkRoleCeiling enforces that a non-superadmin actor may not grant/mint a role
// that outranks its own role in tenant. A superadmin (the system role) is exempt.
func checkRoleCeiling(actor Principal, tenant model.TenantID, role string) error {
	if actor.Superadmin {
		return nil
	}
	actorRole, ok := actor.RoleIn(tenant)
	if !ok || RoleRank(role) > RoleRank(actorRole) {
		return ErrRoleCeiling
	}
	return nil
}

// MinPasswordLen is the minimum accepted password length.
const MinPasswordLen = 8

// HasAnyUser reports whether any user exists. It is the first-boot setup gate,
// re-checked transactionally per request rather than cached in a flag.
func (a *Authenticator) HasAnyUser(ctx context.Context) (bool, error) {
	var exists bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, err := as.Users().List(ctx, model.Query{Limit: 1})
		exists = len(us) > 0
		return err
	})
	return exists, err
}

// NewUser is the input to CreateUser.
type NewUser struct {
	Email       string
	DisplayName string
	Password    string
	Superadmin  bool
}

// bootstrapLockKey serializes first-boot bootstrap across every process on the
// same database. It is a single global key with no tenant in it on purpose: the
// decision it protects is "does ANY user exist", which is global by definition —
// a per-tenant key would let two tenants bootstrap two "first" superadmins.
const bootstrapLockKey = "auth.bootstrap"

// bootstrapRendezvous is a TEST SEAM, nil in production and called on the way in
// to the bootstrap transaction, BEFORE the lock is taken.
//
// It exists because the defect this file guards against is an INTERLEAVING, and a
// test that merely starts goroutines together does not produce one: measured on a
// live PostgreSQL, six concurrent bootstraps released by a channel close ran
// sequentially — five of them read the winner's committed row and correctly
// declined, so the suite passed WITH THE LOCK REMOVED. The same six, made to hold
// at a barrier after their read, produced SIX superadmins. A race that only
// reproduces when the scheduler cooperates is a race the suite cannot police.
//
// The seam is placed BEFORE the lock on purpose: a rendezvous placed after it
// would deadlock the very serialization it is meant to observe, and a test that
// hangs when the code is CORRECT is worse than no test.
var bootstrapRendezvous func()

// lockBootstrapTransaction takes the cluster-wide bootstrap lock on the
// transaction that is about to run count==0 + insert.
//
// WHY IT IS NOT ENOUGH TO BE ATOMIC: the check and the insert already share one
// transaction, and on SQLite the single writer serializes them. On PostgreSQL
// they do not serialize — two concurrent setup calls can both read zero users
// under READ COMMITTED and both proceed; only UNIQUE(email) stops them, and only
// when the two racing operators typed the SAME email. Two DIFFERENT emails race
// to two superadmins, which is precisely the "first user" invariant this path
// exists to hold.
//
// It FAILS CLOSED when the scope cannot lock, because store.TransactionLocker
// says a correctness-sensitive caller must: a store decorator that dropped the
// capability would otherwise silently restore the race it is meant to remove,
// and a bootstrap that races is worse than a bootstrap that refuses — the
// operator can retry a refusal, but cannot un-create a second superadmin.
func lockAuthTransaction(ctx context.Context, as store.AuthScope, key string) error {
	locker, ok := as.(store.TransactionLocker)
	if !ok {
		return fmt.Errorf("%w: %s cannot serialize: store scope provides no transaction lock", ErrCoordinationUnavailable, key)
	}
	if err := locker.LockTransaction(ctx, key); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrCoordinationUnavailable, key, err)
	}
	return nil
}

func lockBootstrapTransaction(ctx context.Context, as store.AuthScope) error {
	return lockAuthTransaction(ctx, as, bootstrapLockKey)
}

// BootstrapSuperadmin creates the first superadmin during first-boot setup,
// WITHOUT a tenant membership. It is the credential-only bootstrap used where the
// caller provisions no tenant of its own (the demo estate seeds its own org, the
// unit tests drive the authenticator directly). The first-boot API path uses
// BootstrapSuperadminOwning: a superadmin with no membership has no tenant to act
// in, which is exactly the unusable estate documented there.
func (a *Authenticator) BootstrapSuperadmin(ctx context.Context, email, password string) (model.User, error) {
	u, _, err := a.BootstrapSuperadminOwning(ctx, email, password, "")
	return u, err
}

// BootstrapSuperadminOwning creates the first superadmin during first-boot setup
// and, when tenant is non-zero, grants it the OWNER role in that tenant. It is
// atomic: it refuses (ErrSetupComplete) if any user already exists, and the
// check+insert+grant run in ONE transaction, so the account and its first
// membership commit together or not at all. On SQLite (the single-node default)
// the single writer serializes concurrent setup attempts; the UNIQUE(email) index
// is the backstop. (A Postgres advisory lock around bootstrap is a documented
// hardening follow-up.)
//
// The membership is what makes a fresh install usable: every tenant-scoped route
// resolves its tenant from the caller's grants or an explicit header, so a
// superadmin with zero grants has no tenant to select and the console can only
// ever be told "tenant required". The CALLER creates the tenant first and passes
// its id here (the auth partition holds no cross-tenant foreign key, so this
// method does not — and cannot — verify the org row exists).
func (a *Authenticator) BootstrapSuperadminOwning(ctx context.Context, email, password string, tenant model.TenantID) (model.User, model.Membership, error) {
	if len(password) < MinPasswordLen {
		return model.User{}, model.Membership{}, ErrWeakPassword
	}
	if !tenant.IsZero() && tenant.IsSystem() {
		// The reserved system tenant is the auth partition itself, never a business
		// tenant a human is granted a role in.
		return model.User{}, model.Membership{}, fmt.Errorf("%w: invalid tenant", ErrInvalidToken)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return model.User{}, model.Membership{}, err
	}
	var (
		outUser model.User
		outMem  model.Membership
	)
	mutErr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if bootstrapRendezvous != nil {
			bootstrapRendezvous()
		}
		if err := lockBootstrapTransaction(ctx, as); err != nil {
			return err
		}
		existing, _, err := as.Users().List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return ErrSetupComplete
		}
		u, err := as.Users().Create(ctx, model.User{
			Email: normalizeEmail(email), DisplayName: "Administrator",
			Status: model.StatusActive, PasswordHash: hash, IsSuperadmin: true,
		})
		if err != nil {
			return err
		}
		outUser = u
		if _, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "user.bootstrap", TargetKind: "core.user", TargetID: u.ID,
		}); err != nil {
			return err
		}
		if tenant.IsZero() {
			return nil
		}
		// A direct Create, not grantMembershipTx: there is provably no membership to
		// upsert (no user existed a statement ago) and no acting principal to check a
		// role ceiling against — provisioning is attributed to the system, exactly
		// like the user.bootstrap event above.
		m, err := as.Memberships().Create(ctx, model.Membership{
			UserID: u.ID, TargetTenantID: tenant, Role: RoleOwner,
		})
		if err != nil {
			return err
		}
		outMem = m
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "membership.bootstrap", TargetKind: "core.membership", TargetID: m.ID,
		})
		return err
	})
	if mutErr != nil {
		return model.User{}, model.Membership{}, mutErr
	}
	return outUser, outMem, nil
}

// TenantHasMembership reports whether ANY membership targets tenant. It is the
// ownership probe first-boot setup consults before rolling back a tenant it
// provisioned: a tenant that already has a member belongs to a superadmin that
// won the bootstrap race, and must never be dropped by the loser's compensation.
func (a *Authenticator) TenantHasMembership(ctx context.Context, tenant model.TenantID) (bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return false, nil
	}
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		ms, _, err := as.Memberships().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}},
			Limit:   1,
		})
		found = len(ms) > 0
		return err
	})
	return found, err
}

// CreateUser creates an operator account, recording the acting principal.
func (a *Authenticator) CreateUser(ctx context.Context, actor Principal, in NewUser) (model.User, error) {
	if in.Password != "" && len(in.Password) < MinPasswordLen {
		return model.User{}, ErrWeakPassword
	}
	var hash string
	if in.Password != "" {
		h, err := HashPassword(in.Password)
		if err != nil {
			return model.User{}, err
		}
		hash = h
	}
	var out model.User
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// The retained seat seam. Since B10 it is an unconditional no-op (accounts
		// are unlimited in every self-hosted tier); the call stays so the "no seat
		// figure gates account creation" invariant lives in ONE place (seatcap.go).
		if err := a.enforceSeatCapTx(ctx, as); err != nil {
			return err
		}
		u, err := as.Users().Create(ctx, model.User{
			Email: normalizeEmail(in.Email), DisplayName: in.DisplayName,
			Status: model.StatusActive, PasswordHash: hash, IsSuperadmin: in.Superadmin,
		})
		if err != nil {
			return err
		}
		out = u
		return auditAct(ctx, as, actor, "user.create", "core.user", u.ID)
	})
	return out, err
}

// SetPassword sets a user's password (admin reset or self-service), re-hashing
// with the current parameters.
func (a *Authenticator) SetPassword(ctx context.Context, actor Principal, userID model.ID, password string) error {
	if len(password) < MinPasswordLen {
		return ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, userID)
		if err != nil {
			return err
		}
		u.PasswordHash = hash
		if _, err := as.Users().Update(ctx, u); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "user.set_password", "core.user", userID)
	})
}

// GrantMembership grants (or updates) a user's role in a tenant, OPTIONALLY confining the
// membership to one workspace (a non-zero workspaceID scopes the grant to that
// workspace; zero is the historical tenant-wide membership). The workspace id belongs to
// the GRANTED tenant's space; the CALLER (the API handler) validates it exists there before
// calling — the auth store holds no cross-tenant foreign key.
func (a *Authenticator) GrantMembership(ctx context.Context, actor Principal, userID model.ID, tenant model.TenantID, role string, workspaceID model.ID) (model.Membership, error) {
	if !IsRole(role) {
		return model.Membership{}, ErrInvalidRole
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return model.Membership{}, fmt.Errorf("%w: invalid tenant", ErrInvalidToken)
	}
	if err := checkRoleCeiling(actor, tenant, role); err != nil {
		return model.Membership{}, err
	}
	// a workspace-confined actor may author memberships ONLY within its OWN workspace.
	// A tenant-wide (zero) or foreign-workspace grant would lift a confinement — its own (if it
	// re-grants itself tenant-wide) or another member's — and reach outside the fence. This is
	// the authority-changing writer the Scoped forbid cannot gate (membership:write is a
	// tenant-level action with no resolvable target workspace), so it is enforced here.
	if confinedWS, confined := actor.ConfinedWorkspaceIn(tenant); confined && workspaceID != confinedWS {
		return model.Membership{}, ErrWorkspaceConfined
	}
	var out model.Membership
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		m, e := grantMembershipTx(ctx, as, actor, userID, tenant, role, workspaceID)
		out = m
		return e
	})
	return out, err
}

// grantMembershipTx upserts a (user, tenant) membership at role and audits it,
// INSIDE the caller's auth transaction. It is the shared core of GrantMembership
// and the onboarding path (OnboardMember), so a user created and granted access in
// one transaction shares its atomicity. The caller is responsible for the role
// validity / ceiling checks (GrantMembership does them up front; OnboardMember
// does them before opening its transaction).
func grantMembershipTx(ctx context.Context, as store.AuthScope, actor Principal, userID model.ID, tenant model.TenantID, role string, workspaceID model.ID) (model.Membership, error) {
	existing, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
		{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
	}, Limit: 1})
	if err != nil {
		return model.Membership{}, err
	}
	if len(existing) > 0 {
		m := existing[0]
		// Guard the TARGET's current role too: a non-superadmin actor may not modify
		// (e.g. demote) a member who already outranks the actor — checking only the
		// NEW role would let an admin overwrite an owner's membership.
		if err := checkRoleCeiling(actor, tenant, m.Role); err != nil {
			return model.Membership{}, err
		}
		// A re-grant fully specifies the membership: it sets both the role AND the workspace
		// confinement, so a re-grant can narrow a tenant-wide member to a workspace or
		// widen a confined one back (both within the granter's role ceiling, checked above).
		m.Role = role
		m.WorkspaceID = workspaceID
		updated, err := as.Memberships().Update(ctx, m)
		if err != nil {
			return model.Membership{}, err
		}
		return updated, auditAct(ctx, as, actor, "membership.update", "core.membership", updated.ID)
	}
	created, err := as.Memberships().Create(ctx, model.Membership{UserID: userID, TargetTenantID: tenant, Role: role, WorkspaceID: workspaceID})
	if err != nil {
		return model.Membership{}, err
	}
	return created, auditAct(ctx, as, actor, "membership.grant", "core.membership", created.ID)
}

// TokenSpec is the input to IssueToken.
type TokenSpec struct {
	Name        string
	BoundTenant model.TenantID
	Role        string
	Superadmin  bool
	ExpiresAt   *model.Timestamp
}

// IssueToken mints an API token and returns the secret token string (shown once)
// plus the stored record (without the secret). A superadmin token must be
// unbound; a bound token must name a tenant and a valid role.
func (a *Authenticator) IssueToken(ctx context.Context, actor Principal, spec TokenSpec) (string, model.APIToken, error) {
	if spec.Superadmin {
		if !spec.BoundTenant.IsZero() {
			return "", model.APIToken{}, fmt.Errorf("%w: superadmin token must be unbound", ErrInvalidToken)
		}
		// Only a superadmin may mint a superadmin (system-role) token.
		if !actor.Superadmin {
			return "", model.APIToken{}, ErrRoleCeiling
		}
	} else {
		if spec.BoundTenant.IsZero() || spec.BoundTenant.IsSystem() || !IsRole(spec.Role) {
			return "", model.APIToken{}, fmt.Errorf("%w: bound token needs a non-system tenant and a role", ErrInvalidToken)
		}
		// A token may not carry a role above the actor's own role in that tenant.
		if err := checkRoleCeiling(actor, spec.BoundTenant, spec.Role); err != nil {
			return "", model.APIToken{}, err
		}
		// a workspace-confined principal cannot mint a token — a bound token is
		// tenant-wide in its bound tenant (tokens carry no workspace confinement), so issuing
		// one would launder the confined principal into unconfined tenant-wide authority
		// (deny-closed; the Scoped forbid cannot see this tenant-level issuance path).
		if _, confined := actor.ConfinedWorkspaceIn(spec.BoundTenant); confined {
			return "", model.APIToken{}, ErrWorkspaceConfined
		}
	}
	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return "", model.APIToken{}, err
	}
	var stored model.APIToken
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		t, err := as.Tokens().Create(ctx, model.APIToken{
			Name: spec.Name, UserID: actor.UserID, Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: spec.BoundTenant, Role: spec.Role, IsSuperadmin: spec.Superadmin,
			ExpiresAt: spec.ExpiresAt,
		})
		if err != nil {
			return err
		}
		stored = t
		return auditAct(ctx, as, actor, "token.issue", "core.api_token", t.ID)
	})
	if err != nil {
		return "", model.APIToken{}, err
	}
	return cred.Token, stored, nil
}

// GetToken returns a stored API token by id (so a caller can authorize against
// its bound tenant before revoking). The secret hash is present in the returned
// value but the API never exposes it.
func (a *Authenticator) GetToken(ctx context.Context, id model.ID) (model.APIToken, error) {
	var t model.APIToken
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		got, e := as.Tokens().Get(ctx, id)
		t = got
		return e
	})
	return t, err
}

// RevokeToken marks an API token revoked, cascading to any tokens that were
// exchanged FROM it (RFC 8693 delegation chain): revoking a parent invalidates
// the whole down-scoped subtree, so offboarding a credential closes every
// delegated token it spawned. The cascade is idempotent.
func (a *Authenticator) RevokeToken(ctx context.Context, actor Principal, tokenID model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return revokeTokenTree(ctx, as, actor, tokenID)
	})
}

// revokeTokenTree revokes a token and, recursively, every token that names it as
// ParentTokenID. It is safe to call on an already-revoked token (it still walks
// the subtree, so a partially-revoked chain is fully closed).
func revokeTokenTree(ctx context.Context, as store.AuthScope, actor Principal, tokenID model.ID) error {
	t, err := as.Tokens().Get(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if !t.Revoked {
		t.Revoked = true
		if _, err := as.Tokens().Update(ctx, t); err != nil {
			return err
		}
		if err := auditAct(ctx, as, actor, "token.revoke", "core.api_token", tokenID); err != nil {
			return err
		}
	}
	children, _, err := as.Tokens().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "parent_token_id", Op: model.OpEq, Value: tokenID.String()}},
		Limit:   1000,
	})
	if err != nil {
		return err
	}
	for _, c := range children {
		if c.ID == tokenID {
			continue // defensive: never recurse into self
		}
		if err := revokeTokenTree(ctx, as, actor, c.ID); err != nil {
			return err
		}
	}
	return nil
}

// auditAct appends an audit event attributed to the acting principal.
func auditAct(ctx context.Context, as store.AuthScope, actor Principal, action string, targetKind model.Kind, target model.ID) error {
	return metaAudit(ctx, as, actor, action, targetKind, target, nil)
}

// metaAudit is auditAct plus structured Meta (counts, role names — never an
// email, never a secret; docs/SECURITY-HARDENING.md).
func metaAudit(ctx context.Context, as store.AuthScope, actor Principal, action string, targetKind model.Kind, target model.ID, meta map[string]any) error {
	// FAIL CLOSED on an unattributable principal (B-12). Appending an event
	// whose actor is the bare string "user:" is worse than refusing the write: it
	// produces evidence that looks complete, reads as a real subject in a SIEM,
	// and cannot be told apart from the four other authorities that produced the
	// identical string. A privileged write we cannot attribute is one we do not do.
	subject, err := actor.AttributableActor()
	if err != nil {
		return err
	}
	// The principal's own provenance (who/how/why/host/uid/pid for a local
	// operator) rides EVERY event it causes, so a privileged command added later
	// inherits the attribution instead of having to remember it. Action meta is
	// merged on top; the provenance keys are namespaced actor_* so neither can
	// silently overwrite the other.
	if prov := actor.AuditMeta(); len(prov) > 0 {
		merged := prov
		for k, v := range meta {
			merged[k] = v
		}
		meta = merged
	}
	_, err = as.Audit().Append(ctx, model.AuditDraft{
		Actor: subject, ActorKind: actor.ActorKind(),
		Action: action, TargetKind: targetKind, TargetID: target, Meta: meta,
	})
	return err
}
