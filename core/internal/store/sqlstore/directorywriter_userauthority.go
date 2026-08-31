// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// authorityGuardedRepo serializes only authority-bearing Create/Update paths.
// Reads and authority-reducing Delete stay ordinary repository operations. The
// validation callback runs from tracker.prepare after the global directory lock
// has been acquired, closing stale pre-read races in credential issuers.
type authorityGuardedRepo[T any] struct {
	inner          store.Repository[T]
	tracker        *directoryWriteTracker
	validateCreate func(context.Context, T) error
	validateUpdate func(context.Context, T) error
}

func newAuthorityGuardedRepo[T any](
	inner store.Repository[T],
	tracker *directoryWriteTracker,
	validateCreate func(context.Context, T) error,
	validateUpdate func(context.Context, T) error,
) store.Repository[T] {
	return &authorityGuardedRepo[T]{
		inner: inner, tracker: tracker,
		validateCreate: validateCreate, validateUpdate: validateUpdate,
	}
}

func (r *authorityGuardedRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	return r.inner.Get(ctx, id)
}

func (r *authorityGuardedRepo[T]) List(
	ctx context.Context,
	query model.Query,
) ([]T, model.Page, error) {
	return r.inner.List(ctx, query)
}

func (r *authorityGuardedRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	locker, ok := r.inner.(store.RowLocker[T])
	if !ok {
		var zero T
		return zero, errors.New("authority repository does not implement row locking")
	}
	return locker.Lock(ctx, id)
}

func (r *authorityGuardedRepo[T]) Create(
	ctx context.Context,
	in T,
) (_ T, retErr error) {
	if r.tracker == nil {
		return r.inner.Create(ctx, in)
	}
	defer func() { r.tracker.poison(retErr) }()
	if err := r.tracker.prepare(ctx, func() ([]model.TenantID, error) {
		return nil, r.validateCreate(ctx, in)
	}); err != nil {
		var zero T
		return zero, err
	}
	return r.inner.Create(ctx, in)
}

func (r *authorityGuardedRepo[T]) Update(
	ctx context.Context,
	in T,
) (_ T, retErr error) {
	if r.tracker == nil {
		return r.inner.Update(ctx, in)
	}
	defer func() { r.tracker.poison(retErr) }()
	if err := r.tracker.prepare(ctx, func() ([]model.TenantID, error) {
		return nil, r.validateUpdate(ctx, in)
	}); err != nil {
		var zero T
		return zero, err
	}
	return r.inner.Update(ctx, in)
}

func (r *authorityGuardedRepo[T]) Delete(ctx context.Context, id model.ID) error {
	return r.inner.Delete(ctx, id)
}

func authSessionAuthorityRepo(
	ts *tenantScope,
	inner store.Repository[model.AuthSession],
) store.Repository[model.AuthSession] {
	validate := func(ctx context.Context, session model.AuthSession) error {
		return validateAuthorityUsers(ctx, ts, session.UserID)
	}
	return newDirectoryTrackedRepo(
		inner,
		ts.directoryWriter,
		directoryTenantResolver[model.AuthSession]{
			create: func(ctx context.Context, session model.AuthSession) ([]model.TenantID, error) {
				return nil, validate(ctx, session)
			},
			update: func(ctx context.Context, session model.AuthSession) ([]model.TenantID, error) {
				old, err := inner.Get(ctx, session.ID)
				if err != nil {
					return nil, err
				}
				if err := validate(ctx, old); err != nil {
					return nil, err
				}
				if err := validate(ctx, session); err != nil {
					return nil, err
				}
				if authSessionRenewalOnly(old, session) {
					return nil, nil
				}
				return authSessionDirectoryTenants(ctx, ts, old.UserID, session.UserID)
			},
			delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
				old, err := inner.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				if err := validateAuthSessionDeleteShape(old); err != nil {
					return nil, err
				}
				return authSessionDirectoryTenants(ctx, ts, old.UserID)
			},
		},
	)
}

func authTokenAuthorityRepo(
	ts *tenantScope,
	inner store.Repository[model.APIToken],
) store.Repository[model.APIToken] {
	validate := func(ctx context.Context, token model.APIToken) error {
		return validateAPITokenAuthorityChain(ctx, ts, inner, token)
	}
	return newDirectoryTrackedRepo(
		inner,
		ts.directoryWriter,
		directoryTenantResolver[model.APIToken]{
			create: func(ctx context.Context, token model.APIToken) ([]model.TenantID, error) {
				return nil, validate(ctx, token)
			},
			update: func(ctx context.Context, token model.APIToken) ([]model.TenantID, error) {
				old, err := inner.Get(ctx, token.ID)
				if err != nil {
					return nil, err
				}
				if err := validate(ctx, old); err != nil {
					return nil, err
				}
				if err := validate(ctx, token); err != nil {
					return nil, err
				}
				if authTokenRenewalOnly(old, token) {
					return nil, nil
				}
				return authTokenDirectoryTenants(old, token), nil
			},
			delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
				old, err := inner.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				if err := validateAPITokenDeleteShape(old); err != nil {
					return nil, err
				}
				return authTokenDirectoryTenants(old), nil
			},
		},
	)
}

// authSessionDirectoryTenants returns every tenant whose effective principal
// can depend on one of the supplied users. The caller holds the global
// directory-writer lock, so the membership/group union cannot race the write
// whose credential fence is being prepared.
func authSessionDirectoryTenants(
	ctx context.Context,
	ts *tenantScope,
	userIDs ...model.ID,
) ([]model.TenantID, error) {
	seen := make(map[model.ID]struct{}, len(userIDs))
	canonical := make([]model.ID, 0, len(userIDs))
	for _, id := range userIDs {
		if id.IsZero() {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		canonical = append(canonical, id)
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].String() < canonical[j].String()
	})

	var tenants []model.TenantID
	for _, id := range canonical {
		resolved, err := authUserDirectoryTenants(ctx, ts, id)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, resolved...)
	}
	return tenants, nil
}

// authTokenDirectoryTenants returns the old/new concrete tenant bindings. A
// valid global superadmin token is unbound and deliberately contributes none:
// principal evidence for global authority stays unavailable until a future
// design can fence the complete tenant inventory atomically.
func authTokenDirectoryTenants(tokens ...model.APIToken) []model.TenantID {
	tenants := make([]model.TenantID, 0, len(tokens))
	for _, token := range tokens {
		if !token.BoundTenantID.IsZero() {
			tenants = append(tenants, token.BoundTenantID)
		}
	}
	return tenants
}

// Delete must remain a cleanup path after authority has already disappeared.
// It therefore validates only the durable row identity needed to discover and
// fence affected tenants; unlike Create/Update it never requires the User or a
// token parent to remain live.
func validateAuthSessionDeleteShape(session model.AuthSession) error {
	if err := validateCredentialDeleteBase("auth session", session.BaseFields); err != nil {
		return err
	}
	if session.UserID.IsZero() {
		return directoryUnavailable("auth session delete User is absent", nil)
	}
	if err := validateCreateID(session.UserID); err != nil {
		return directoryUnavailable("auth session delete User is not canonical", err)
	}
	return nil
}

func validateAPITokenDeleteShape(token model.APIToken) error {
	if err := validateCredentialDeleteBase("API token", token.BaseFields); err != nil {
		return err
	}
	for _, ref := range []struct {
		name string
		id   model.ID
	}{
		{name: "User", id: token.UserID},
		{name: "act-as User", id: token.ActAsUserID},
		{name: "parent token", id: token.ParentTokenID},
	} {
		name, id := ref.name, ref.id
		if id.IsZero() {
			continue
		}
		if err := validateCreateID(id); err != nil {
			return directoryUnavailable("API token delete "+name+" is not canonical", err)
		}
	}
	return nil
}

func validateCredentialDeleteBase(name string, base model.BaseFields) error {
	if err := validateCreateID(base.ID); err != nil {
		return directoryUnavailable(name+" delete id is not canonical", err)
	}
	if base.TenantID != model.SystemTenantID {
		return directoryUnavailable(name+" delete crossed the system partition", nil)
	}
	if base.Version < 1 {
		return directoryUnavailable(name+" delete version is invalid", nil)
	}
	return nil
}

// authSessionRenewalOnly recognizes the one update that cannot invalidate a
// previously resolved principal: the same exact session with a non-decreasing
// expiry. Any identity, bearer, revocation, assurance or metadata change takes
// the conservative fenced path.
func authSessionRenewalOnly(old, next model.AuthSession) bool {
	return authorityBaseUnchanged(old.BaseFields, next.BaseFields) &&
		old.UserID == next.UserID &&
		old.Selector == next.Selector &&
		bytes.Equal(old.SecretHash, next.SecretHash) &&
		!old.ExpiresAt.IsZero() && !next.ExpiresAt.IsZero() &&
		!next.ExpiresAt.Before(old.ExpiresAt) &&
		old.Revoked == next.Revoked &&
		old.CreatedIP == next.CreatedIP &&
		old.AAL == next.AAL &&
		slices.Equal(old.AMR, next.AMR) &&
		authorityOptionalTimestampEqual(old.AALExpiresAt, next.AALExpiresAt)
}

// authTokenRenewalOnly is intentionally positive and exhaustive: only expiry
// extension/removal and last-used bookkeeping may differ. Every authority or
// runtime-binding field must remain byte-for-byte identical.
func authTokenRenewalOnly(old, next model.APIToken) bool {
	return authorityBaseUnchanged(old.BaseFields, next.BaseFields) &&
		old.Name == next.Name &&
		old.UserID == next.UserID &&
		old.Selector == next.Selector &&
		bytes.Equal(old.SecretHash, next.SecretHash) &&
		old.BoundTenantID == next.BoundTenantID &&
		old.Role == next.Role &&
		old.IsSuperadmin == next.IsSuperadmin &&
		authorityExpiryNotNarrower(old.ExpiresAt, next.ExpiresAt) &&
		old.Revoked == next.Revoked &&
		old.Purpose == next.Purpose &&
		old.Audience == next.Audience &&
		old.ActAsUserID == next.ActAsUserID &&
		old.ParentTokenID == next.ParentTokenID &&
		old.Scope == next.Scope &&
		old.AgentRef == next.AgentRef &&
		old.SessionRef == next.SessionRef &&
		old.WorkspaceID == next.WorkspaceID &&
		old.SessionRunRef == next.SessionRunRef &&
		old.SessionFence == next.SessionFence
}

func authorityBaseUnchanged(old, next model.BaseFields) bool {
	return old.ID == next.ID &&
		old.TenantID == next.TenantID &&
		authorityTimestampEqual(old.CreatedAt, next.CreatedAt) &&
		authorityTimestampEqual(old.UpdatedAt, next.UpdatedAt) &&
		old.Version == next.Version &&
		authorityOptionalTimestampEqual(old.DeletedAt, next.DeletedAt)
}

func authorityExpiryNotNarrower(old, next *model.Timestamp) bool {
	if old == nil {
		return next == nil
	}
	if old.IsZero() {
		return false
	}
	if next == nil {
		return true
	}
	return !next.IsZero() && !next.Before(*old)
}

func authorityOptionalTimestampEqual(left, right *model.Timestamp) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return authorityTimestampEqual(*left, *right)
}

func authorityTimestampEqual(left, right model.Timestamp) bool {
	return left.Time().Equal(right.Time())
}

// pepServiceCredentialAuthorityRepo closes the direct AuthScope seam as well
// as the higher-level BindPEPCredential path. A caller may have read a token
// before waiting for a concurrent User retirement; the token and every User in
// its ancestry are therefore re-read only after the global directory lock.
func pepServiceCredentialAuthorityRepo(
	ts *tenantScope,
	inner store.Repository[model.PEPServiceCredential],
) store.Repository[model.PEPServiceCredential] {
	tokens := newTypedRepo(ts.repo(apiTokenDescriptor), apiTokenCodec)
	validate := func(ctx context.Context, credential model.PEPServiceCredential) error {
		if credential.TokenID.IsZero() {
			return directoryUnavailable("PEP credential token is absent", nil)
		}
		token, err := tokens.Get(ctx, credential.TokenID)
		if err != nil {
			return unavailableAuthoritySource("PEP credential token", err)
		}
		return validateAPITokenAuthorityChain(ctx, ts, tokens, token)
	}
	return newAuthorityGuardedRepo(
		inner,
		ts.directoryWriter,
		validate,
		func(ctx context.Context, credential model.PEPServiceCredential) error {
			old, err := inner.Get(ctx, credential.ID)
			if err != nil {
				return err
			}
			if err := validate(ctx, old); err != nil {
				return err
			}
			return validate(ctx, credential)
		},
	)
}

const maxAuthorityTokenAncestry = 64

// validateAPITokenAuthorityChain validates the candidate token plus its whole
// ancestry. Checking only the immediate parent lets a zero-User intermediate
// hide a retired User at the root and turn a descendant into live authority.
// Canonical IDs, system ownership, cycles and an intentionally closed depth are
// all deny-closed under the same global lock.
func validateAPITokenAuthorityChain(
	ctx context.Context,
	ts *tenantScope,
	tokens store.Repository[model.APIToken],
	token model.APIToken,
) error {
	seen := make(map[model.ID]struct{}, maxAuthorityTokenAncestry)
	if !token.ID.IsZero() {
		if err := validateCreateID(token.ID); err != nil {
			return directoryUnavailable("API token id is not canonical", err)
		}
		seen[token.ID] = struct{}{}
	}
	current := token
	for depth := 0; ; depth++ {
		if current.TenantID != "" && current.TenantID != model.SystemTenantID {
			return directoryUnavailable("API token ancestry crossed the system partition", nil)
		}
		if current.Version < 0 {
			return directoryUnavailable("API token ancestry has an invalid version", nil)
		}
		if err := validateAuthorityUsers(
			ctx, ts, current.UserID, current.ActAsUserID,
		); err != nil {
			return err
		}
		parentID := current.ParentTokenID
		if parentID.IsZero() {
			return nil
		}
		if depth >= maxAuthorityTokenAncestry-1 {
			return directoryUnavailable("API token ancestry exceeds the closed depth", nil)
		}
		if err := validateCreateID(parentID); err != nil {
			return directoryUnavailable("API token parent id is not canonical", err)
		}
		if _, duplicate := seen[parentID]; duplicate {
			return directoryUnavailable("API token ancestry contains a cycle", nil)
		}
		seen[parentID] = struct{}{}
		parent, err := tokens.Get(ctx, parentID)
		if err != nil {
			return unavailableAuthoritySource("API token parent", err)
		}
		if parent.ID != parentID || parent.TenantID != model.SystemTenantID || parent.Version < 1 {
			return directoryUnavailable("API token parent is not canonical", nil)
		}
		current = parent
	}
}

func webAuthnAuthorityRepo(
	ts *tenantScope,
	inner store.Repository[model.WebAuthnCredential],
) store.Repository[model.WebAuthnCredential] {
	validate := func(ctx context.Context, credential model.WebAuthnCredential) error {
		return validateAuthorityUsers(ctx, ts, credential.UserID)
	}
	return newAuthorityGuardedRepo(
		inner,
		ts.directoryWriter,
		validate,
		func(ctx context.Context, credential model.WebAuthnCredential) error {
			old, err := inner.Get(ctx, credential.ID)
			if err != nil {
				return err
			}
			if err := validate(ctx, old); err != nil {
				return err
			}
			return validate(ctx, credential)
		},
	)
}

func delegationHandleAuthorityRepo(
	ts *tenantScope,
	inner store.Repository[model.DelegationHandle],
) store.Repository[model.DelegationHandle] {
	validate := func(ctx context.Context, handle model.DelegationHandle) error {
		if err := validateAuthorityUsers(
			ctx, ts, handle.SubjectUserID, handle.ActAsUserID,
		); err != nil {
			return err
		}
		if handle.SourceCredID.IsZero() {
			return directoryUnavailable("delegation source credential is absent", nil)
		}
		switch handle.SourceCredKind {
		case "user":
			sessions := newTypedRepo(ts.repo(authSessionDescriptor), authSessionCodec)
			session, err := sessions.Get(ctx, handle.SourceCredID)
			if err != nil {
				return unavailableAuthoritySource("delegation session", err)
			}
			return validateAuthorityUsers(ctx, ts, session.UserID)
		case "token":
			tokens := newTypedRepo(ts.repo(apiTokenDescriptor), apiTokenCodec)
			token, err := tokens.Get(ctx, handle.SourceCredID)
			if err != nil {
				return unavailableAuthoritySource("delegation token", err)
			}
			return validateAPITokenAuthorityChain(ctx, ts, tokens, token)
		default:
			return directoryUnavailable("delegation source credential kind is not closed", nil)
		}
	}
	return newAuthorityGuardedRepo(
		inner,
		ts.directoryWriter,
		validate,
		func(ctx context.Context, handle model.DelegationHandle) error {
			old, err := inner.Get(ctx, handle.ID)
			if err != nil {
				return err
			}
			if err := validate(ctx, old); err != nil {
				return err
			}
			return validate(ctx, handle)
		},
	)
}

func unavailableAuthoritySource(name string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return directoryUnavailable(name+" is absent after authority lock", nil)
	}
	return err
}

// validateAuthorityUsers is called only from tracker.prepare's discover
// callback, after the global writer lock. A missing source without a tombstone
// is corruption/unavailability; a matching immutable tombstone is the explicit
// one-way retirement refusal.
func validateAuthorityUsers(
	ctx context.Context,
	ts *tenantScope,
	ids ...model.ID,
) error {
	seen := make(map[model.ID]struct{}, len(ids))
	canonical := make([]model.ID, 0, len(ids))
	for _, id := range ids {
		if id.IsZero() {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		canonical = append(canonical, id)
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].String() < canonical[j].String()
	})
	users := newTypedRepo(ts.repo(userDescriptor), userCodec)
	for _, id := range canonical {
		rec, tombstoneFound, err := ts.readOneDirectoryRecord(
			ctx,
			userTombstoneDescriptor,
			"tenant_id = ? AND principal_kind = ? AND principal_ref = ?",
			model.SystemTenantID.String(), string(model.DirectoryPrincipalUser), id.String(),
		)
		if err != nil {
			return directoryUnavailable("read User retirement guard", err)
		}
		var tombstone model.UserTombstone
		if tombstoneFound {
			base, err := baseFromRecord(rec)
			if err != nil {
				return directoryUnavailable("decode User retirement guard", err)
			}
			tombstone, err = userTombstoneCodec.Decode(base, rec)
			if err != nil || tombstone.PrincipalRef != id {
				return directoryUnavailable("validate User retirement guard", err)
			}
			if err := ts.validateDirectoryAuditAnchor(
				ctx, model.SystemTenantID, tombstone.AuditAnchor,
			); err != nil {
				return err
			}
		}
		user, err := users.Get(ctx, id)
		userFound := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if userFound && (user.ID != id || user.TenantID != model.SystemTenantID || user.Version < 1) {
			return directoryUnavailable("authority-bearing User is not canonical", nil)
		}
		switch {
		case userFound && tombstoneFound:
			return directoryUnavailable("authority-bearing User coexists with retirement evidence", nil)
		case !userFound && tombstoneFound:
			return fmt.Errorf("%w: user %s", store.ErrDirectoryPrincipalRetired, id)
		case !userFound:
			return directoryUnavailable("authority-bearing User is absent without tombstone", nil)
		}
	}
	return nil
}
