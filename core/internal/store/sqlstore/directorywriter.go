// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// directoryWriterAfterLockTestHook and directoryWriterBeforeSourceTestHook are
// nil outside this package's tests. They expose the two ordering boundaries M117
// must distinguish without weakening or replacing a production control path.
var (
	directoryWriterAfterLockTestHook    func()
	directoryWriterBeforeSourceTestHook func(context.Context, *directoryWriteTracker) error
	errDirectoryWriterAuditFirst        = errors.New("tenant audit append preceded directory writer lock")
)

// directoryWriterTxOptions prevents a PostgreSQL deployment's session default
// from silently changing the writer protocol. At REPEATABLE READ the advisory
// lock statement fixes a snapshot before it waits, so tenant discovery after
// the wait could miss the writer that just committed. READ COMMITTED gives each
// post-lock discovery statement a fresh snapshot. SQLite keeps its native
// transaction options because its writer reservation provides the equivalent
// serialization boundary and the driver need not accept PostgreSQL isolation
// names.
func directoryWriterTxOptions(dia dialect.Dialect) *sql.TxOptions {
	if dia.Name() == store.EnginePostgres {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return nil
}

// directoryWriteTracker owns the directory-writer protocol for one Mutate
// transaction. Every protected repository in the scope shares this instance,
// so one tenant is fenced at most once even when a callback changes several
// directory facts.
type directoryWriteTracker struct {
	tx                   *sql.Tx
	dia                  dialect.Dialect
	presentationTenant   model.TenantID
	locked               bool
	control              directoryWriterControlState
	bumped               map[model.TenantID]struct{}
	poisoned             error
	auditBeforeDirectory bool
}

func newDirectoryWriteTracker(
	tx *sql.Tx,
	dia dialect.Dialect,
	presentationTenant model.TenantID,
) *directoryWriteTracker {
	return &directoryWriteTracker{
		tx: tx, dia: dia, presentationTenant: presentationTenant,
		bumped: make(map[model.TenantID]struct{}),
	}
}

// poison makes it impossible for a callback to discard a protected writer's
// error and commit only its earlier epoch bumps. The transaction envelope
// checks the first error after the callback returns.
func (t *directoryWriteTracker) poison(err error) {
	if err != nil && t.poisoned == nil {
		t.poisoned = err
	}
}

// noteAudit runs immediately before a tenant audit Append can acquire its
// per-tenant chain lock. Retirement orders global-directory before tenant-audit;
// remembering the inverse order lets prepare reject before attempting the
// global lock instead of forming a deadlock cycle.
func (t *directoryWriteTracker) noteAudit() {
	if !t.locked {
		t.auditBeforeDirectory = true
	}
}

// prepare takes the global writer lock before calling discover. That order is
// intentional: old/new membership and group reads are directory facts too, so
// discovering before the lock would permit a torn tenant set. It then bumps
// each newly affected tenant in canonical order and arms the exact generation
// only after restoring the transaction's normal tenant presentation.
func (t *directoryWriteTracker) prepare(
	ctx context.Context,
	discover func() ([]model.TenantID, error),
) error {
	if t.poisoned != nil {
		return fmt.Errorf("directory writer transaction is poisoned: %w", t.poisoned)
	}
	if !t.locked && t.auditBeforeDirectory {
		return fmt.Errorf(
			"%w: directory writes must precede tenant audit appends",
			errDirectoryWriterAuditFirst,
		)
	}
	if !t.locked {
		control, err := acquireDirectoryWriter(ctx, t.tx, t.dia)
		if err != nil {
			return err
		}
		t.control = control
		t.locked = true
		if directoryWriterAfterLockTestHook != nil {
			directoryWriterAfterLockTestHook()
		}
	}

	tenants, err := discover()
	if err != nil {
		return err
	}
	tenants, err = canonicalDirectoryTenants(tenants)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if _, already := t.bumped[tenant]; already {
			continue
		}
		if err := bindDirectoryTenant(ctx, t.tx, t.dia, tenant); err != nil {
			return fmt.Errorf("bind directory writer tenant %s: %w", tenant, err)
		}
		if err := bumpDirectoryEpochExact(ctx, t.tx, t.dia, tenant); err != nil {
			return err
		}
		t.bumped[tenant] = struct{}{}
	}

	if err := bindDirectoryTenant(
		ctx, t.tx, t.dia, t.presentationTenant,
	); err != nil {
		return fmt.Errorf("restore directory writer tenant presentation: %w", err)
	}
	if err := armDirectoryWriter(ctx, t.tx, t.dia, t.control); err != nil {
		return err
	}
	if directoryWriterBeforeSourceTestHook != nil {
		if err := directoryWriterBeforeSourceTestHook(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// finish removes the generation proof before commit and verifies the empty
// marker/GUC baseline. Rebinding first also proves an auth writer did not leave
// the transaction presented as one of the business tenants it fenced.
func (t *directoryWriteTracker) finish(ctx context.Context) error {
	if !t.locked {
		return nil
	}
	if err := bindDirectoryTenant(
		ctx, t.tx, t.dia, t.presentationTenant,
	); err != nil {
		return fmt.Errorf("restore final directory writer tenant presentation: %w", err)
	}
	return finishDirectoryWriter(ctx, t.tx, t.dia)
}

func canonicalDirectoryTenants(
	in []model.TenantID,
) ([]model.TenantID, error) {
	seen := make(map[model.TenantID]struct{}, len(in))
	out := make([]model.TenantID, 0, len(in))
	for _, tenant := range in {
		if tenant.IsSystem() {
			return nil, directoryUnavailable(
				"directory writer resolved the reserved SYSTEM tenant", nil,
			)
		}
		if tenant.IsZero() {
			return nil, directoryUnavailable("directory writer resolved a zero tenant", nil)
		}
		raw := tenant.String()
		parsed, err := uuid.Parse(raw)
		if err != nil || parsed.String() != raw ||
			parsed.Version() != uuid.Version(7) || parsed.Variant() != uuid.RFC4122 {
			return nil, directoryUnavailable(
				fmt.Sprintf("directory writer resolved non-canonical tenant %q", raw), nil,
			)
		}
		if _, duplicate := seen[tenant]; duplicate {
			continue
		}
		seen[tenant] = struct{}{}
		out = append(out, tenant)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// bumpDirectoryEpochExact advances one already-existing canonical epoch using
// the database clock observed by this transaction. There is no upsert or
// missing-as-zero path: absence, malformed state and a failed exact CAS all
// deny closed as unavailable directory evidence.
func bumpDirectoryEpochExact(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) error {
	epoch, found, err := readDirectoryEpochRow(ctx, tx, dia, tenant)
	if err != nil {
		return directoryUnavailable("read epoch before directory write", err)
	}
	if !found {
		return directoryUnavailable(
			fmt.Sprintf("tenant %s has no epoch for directory write", tenant), nil,
		)
	}
	now, err := directoryTransactionNow(ctx, tx, dia)
	if err != nil {
		return directoryUnavailable("read database clock before epoch bump", err)
	}
	query := dia.Rebind("UPDATE " +
		directoryWriterRelation(dia, directoryEpochDescriptor.Table) +
		" SET updated_at = ?, version = ?" +
		" WHERE id = ? AND tenant_id = ? AND version = ?")
	result, err := tx.ExecContext(
		ctx, query, now.String(), epoch.Version+1, tenant.String(),
		tenant.String(), epoch.Version,
	)
	if err != nil {
		return directoryUnavailable("bump directory epoch", mapWriteErr(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return directoryUnavailable("count bumped directory epochs", err)
	}
	if rows != 1 {
		return directoryUnavailable(
			fmt.Sprintf("epoch bump for tenant %s affected %d rows, want exactly one", tenant, rows),
			nil,
		)
	}
	return nil
}

type directoryTenantResolver[T any] struct {
	create func(context.Context, T) ([]model.TenantID, error)
	update func(context.Context, T) ([]model.TenantID, error)
	delete func(context.Context, model.ID) ([]model.TenantID, error)
}

// directoryTrackedRepo decorates only the eight closed-inventory directory
// repositories. Reads delegate unchanged; every Create/Update/Delete performs
// the shared pre-bump protocol before reaching the source table.
type directoryTrackedRepo[T any] struct {
	inner   store.Repository[T]
	tracker *directoryWriteTracker
	resolve directoryTenantResolver[T]
}

func newDirectoryTrackedRepo[T any](
	inner store.Repository[T],
	tracker *directoryWriteTracker,
	resolve directoryTenantResolver[T],
) store.Repository[T] {
	return &directoryTrackedRepo[T]{inner: inner, tracker: tracker, resolve: resolve}
}

// mutableDirectoryRepo is intentionally not a store.Repository: its concrete
// method set omits Delete as well as its public interface. User and Identity
// hard deletion therefore cannot be recovered through a type assertion around
// the directory writer decorator.
type mutableDirectoryRepo[T any] struct {
	inner store.Repository[T]
}

func newMutableDirectoryRepo[T any](inner store.Repository[T]) store.MutableRepository[T] {
	return &mutableDirectoryRepo[T]{inner: inner}
}

func (r *mutableDirectoryRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	return r.inner.Get(ctx, id)
}

func (r *mutableDirectoryRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	locker, ok := r.inner.(store.RowLocker[T])
	if !ok {
		var zero T
		return zero, fmt.Errorf("directory repository does not implement row locking")
	}
	return locker.Lock(ctx, id)
}

func (r *mutableDirectoryRepo[T]) List(
	ctx context.Context,
	query model.Query,
) ([]T, model.Page, error) {
	return r.inner.List(ctx, query)
}

func (r *mutableDirectoryRepo[T]) Create(ctx context.Context, in T) (T, error) {
	return r.inner.Create(ctx, in)
}

func (r *mutableDirectoryRepo[T]) Update(ctx context.Context, in T) (T, error) {
	return r.inner.Update(ctx, in)
}

func (r *directoryTrackedRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	return r.inner.Get(ctx, id)
}

func (r *directoryTrackedRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	locker, ok := r.inner.(store.RowLocker[T])
	if !ok {
		var zero T
		return zero, fmt.Errorf("directory repository does not implement row locking")
	}
	return locker.Lock(ctx, id)
}

func (r *directoryTrackedRepo[T]) List(
	ctx context.Context,
	query model.Query,
) ([]T, model.Page, error) {
	return r.inner.List(ctx, query)
}

func (r *directoryTrackedRepo[T]) Create(
	ctx context.Context,
	in T,
) (_ T, retErr error) {
	if r.tracker == nil {
		return r.inner.Create(ctx, in)
	}
	defer func() { r.tracker.poison(retErr) }()
	if err := r.tracker.prepare(ctx, func() ([]model.TenantID, error) {
		return r.resolve.create(ctx, in)
	}); err != nil {
		var zero T
		return zero, err
	}
	return r.inner.Create(ctx, in)
}

func (r *directoryTrackedRepo[T]) Update(
	ctx context.Context,
	in T,
) (_ T, retErr error) {
	if r.tracker == nil {
		return r.inner.Update(ctx, in)
	}
	defer func() { r.tracker.poison(retErr) }()
	if err := r.tracker.prepare(ctx, func() ([]model.TenantID, error) {
		return r.resolve.update(ctx, in)
	}); err != nil {
		var zero T
		return zero, err
	}
	return r.inner.Update(ctx, in)
}

func (r *directoryTrackedRepo[T]) Delete(
	ctx context.Context,
	id model.ID,
) (retErr error) {
	if r.tracker == nil {
		return r.inner.Delete(ctx, id)
	}
	defer func() { r.tracker.poison(retErr) }()
	if err := r.tracker.prepare(ctx, func() ([]model.TenantID, error) {
		return r.resolve.delete(ctx, id)
	}); err != nil {
		return err
	}
	return r.inner.Delete(ctx, id)
}

func tenantLocalDirectoryResolver[T any](
	tenant model.TenantID,
) directoryTenantResolver[T] {
	resolve := func(context.Context, T) ([]model.TenantID, error) {
		return []model.TenantID{tenant}, nil
	}
	return directoryTenantResolver[T]{
		create: resolve,
		update: resolve,
		delete: func(context.Context, model.ID) ([]model.TenantID, error) {
			return []model.TenantID{tenant}, nil
		},
	}
}

func agentDirectoryResolver(
	sc *tenantScope,
	inner store.Repository[model.Agent],
) directoryTenantResolver[model.Agent] {
	identities := newTypedRepo(sc.repo(identityDescriptor), identityCodec)
	validateBinding := func(ctx context.Context, agent model.Agent) error {
		// An unbound Agent is not yet a stable communication principal. Other
		// validation layers may permit it while onboarding; there is no tombstone
		// key to consult until IdentityID is assigned.
		if agent.IdentityID.IsZero() {
			return nil
		}
		identityRef := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  agent.IdentityID,
		}
		if err := identityRef.Validate(); err != nil {
			return err
		}
		_, identityRetired, err := sc.ReadDirectoryTombstone(ctx, identityRef)
		if err != nil {
			return err
		}
		identity, err := identities.Get(ctx, agent.IdentityID)
		identityFound := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if identityFound && (identity.ID != agent.IdentityID || identity.TenantID != sc.tenant ||
			identity.Version < 1) {
			return directoryUnavailable("Agent stable Identity is not canonical", nil)
		}
		switch {
		case identityFound && identityRetired:
			return directoryUnavailable("Agent stable Identity coexists with retirement evidence", nil)
		case !identityFound && identityRetired:
			return fmt.Errorf("%w: identity %s",
				store.ErrDirectoryPrincipalRetired, agent.IdentityID)
		case !identityFound:
			return directoryUnavailable(
				"Agent stable Identity is absent without tombstone", nil,
			)
		}
		workspace := agent.WorkspaceID
		if workspace.IsZero() {
			def, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return directoryUnavailable("resolve Agent effective workspace", err)
			}
			workspace = def.ID
		}
		ref := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  agent.IdentityID,
			WorkspaceRef:  workspace,
		}
		if err := ref.Validate(); err != nil {
			return err
		}
		_, retired, err := sc.ReadDirectoryTombstone(ctx, ref)
		if err != nil {
			return err
		}
		if retired {
			bindings, err := countPhysicalAgentPrincipalBindings(
				ctx, sc.tx, sc.s.dia, sc.tenant, agent.IdentityID, workspace, "",
			)
			if err != nil {
				return err
			}
			if bindings != 0 {
				return directoryUnavailable(
					"Agent principal coexists with a physical binding", nil,
				)
			}
			return fmt.Errorf("%w: agent identity %s in workspace %s",
				store.ErrDirectoryPrincipalRetired, agent.IdentityID, workspace)
		}
		return nil
	}
	return directoryTenantResolver[model.Agent]{
		create: func(ctx context.Context, agent model.Agent) ([]model.TenantID, error) {
			if err := validateBinding(ctx, agent); err != nil {
				return nil, err
			}
			return []model.TenantID{sc.tenant}, nil
		},
		update: func(ctx context.Context, agent model.Agent) ([]model.TenantID, error) {
			// Establish that the exact source is still live before validating the
			// requested target binding; Update's OCC check remains authoritative.
			old, err := inner.Get(ctx, agent.ID)
			if err != nil {
				return nil, err
			}
			if err := validateBinding(ctx, old); err != nil {
				return nil, err
			}
			if err := validateBinding(ctx, agent); err != nil {
				return nil, err
			}
			return []model.TenantID{sc.tenant}, nil
		},
		delete: func(context.Context, model.ID) ([]model.TenantID, error) {
			// Public Agent Delete remains the existing reversible soft-delete.
			// It does not create retirement evidence and remains usable to clean up
			// an anomalous binding without claiming the principal was retired.
			return []model.TenantID{sc.tenant}, nil
		},
	}
}

func authUserDirectoryResolver(
	ts *tenantScope,
) directoryTenantResolver[model.User] {
	return directoryTenantResolver[model.User]{
		create: func(context.Context, model.User) ([]model.TenantID, error) {
			return nil, nil
		},
		update: func(ctx context.Context, user model.User) ([]model.TenantID, error) {
			return authUserDirectoryTenants(ctx, ts, user.ID)
		},
		delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
			return authUserDirectoryTenants(ctx, ts, id)
		},
	}
}

func authMembershipDirectoryResolver(
	ts *tenantScope,
	inner store.Repository[model.Membership],
) directoryTenantResolver[model.Membership] {
	return directoryTenantResolver[model.Membership]{
		create: func(ctx context.Context, membership model.Membership) ([]model.TenantID, error) {
			if err := validateAuthorityUsers(ctx, ts, membership.UserID); err != nil {
				return nil, err
			}
			return []model.TenantID{membership.TargetTenantID}, nil
		},
		update: func(ctx context.Context, membership model.Membership) ([]model.TenantID, error) {
			old, err := inner.Get(ctx, membership.ID)
			if err != nil {
				return nil, err
			}
			if err := validateAuthorityUsers(
				ctx, ts, old.UserID, membership.UserID,
			); err != nil {
				return nil, err
			}
			return []model.TenantID{old.TargetTenantID, membership.TargetTenantID}, nil
		},
		delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
			old, err := inner.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			return []model.TenantID{old.TargetTenantID}, nil
		},
	}
}

func authGroupDirectoryResolver(
	inner store.Repository[model.UserGroup],
) directoryTenantResolver[model.UserGroup] {
	return directoryTenantResolver[model.UserGroup]{
		create: func(_ context.Context, group model.UserGroup) ([]model.TenantID, error) {
			return []model.TenantID{group.TargetTenantID}, nil
		},
		update: func(ctx context.Context, group model.UserGroup) ([]model.TenantID, error) {
			old, err := inner.Get(ctx, group.ID)
			if err != nil {
				return nil, err
			}
			return []model.TenantID{old.TargetTenantID, group.TargetTenantID}, nil
		},
		delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
			old, err := inner.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			return []model.TenantID{old.TargetTenantID}, nil
		},
	}
}

func authGroupMemberDirectoryResolver(
	ts *tenantScope,
	memberRepo store.Repository[model.UserGroupMember],
	groupRepo store.Repository[model.UserGroup],
) directoryTenantResolver[model.UserGroupMember] {
	groupTenant := func(ctx context.Context, id model.ID) (model.TenantID, error) {
		group, err := groupRepo.Get(ctx, id)
		if err != nil {
			return "", err
		}
		return group.TargetTenantID, nil
	}
	return directoryTenantResolver[model.UserGroupMember]{
		create: func(ctx context.Context, member model.UserGroupMember) ([]model.TenantID, error) {
			if err := validateAuthorityUsers(ctx, ts, member.UserID); err != nil {
				return nil, err
			}
			tenant, err := groupTenant(ctx, member.GroupID)
			return []model.TenantID{tenant}, err
		},
		update: func(ctx context.Context, member model.UserGroupMember) ([]model.TenantID, error) {
			old, err := memberRepo.Get(ctx, member.ID)
			if err != nil {
				return nil, err
			}
			if err := validateAuthorityUsers(
				ctx, ts, old.UserID, member.UserID,
			); err != nil {
				return nil, err
			}
			oldTenant, err := groupTenant(ctx, old.GroupID)
			if err != nil {
				return nil, err
			}
			newTenant, err := groupTenant(ctx, member.GroupID)
			if err != nil {
				return nil, err
			}
			return []model.TenantID{oldTenant, newTenant}, nil
		},
		delete: func(ctx context.Context, id model.ID) ([]model.TenantID, error) {
			old, err := memberRepo.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			tenant, err := groupTenant(ctx, old.GroupID)
			return []model.TenantID{tenant}, err
		},
	}
}

// authUserDirectoryTenants returns the union of a user's direct membership
// tenants and the tenants reached through its group memberships. The caller
// already holds the global directory-writer lock.
func authUserDirectoryTenants(
	ctx context.Context,
	ts *tenantScope,
	userID model.ID,
) ([]model.TenantID, error) {
	memberships := directoryWriterRelation(ts.s.dia, membershipDescriptor.Table)
	groupMembers := directoryWriterRelation(ts.s.dia, userGroupMemberDescriptor.Table)
	groups := directoryWriterRelation(ts.s.dia, userGroupDescriptor.Table)
	query := ts.s.dia.Rebind(fmt.Sprintf(`
SELECT m.target_tenant_id
FROM %s AS m
WHERE m.tenant_id = ? AND m.user_id = ?
UNION
SELECT g.target_tenant_id
FROM %s AS gm
JOIN %s AS g
  ON g.id = gm.group_id AND g.tenant_id = gm.tenant_id
WHERE gm.tenant_id = ? AND gm.user_id = ?
ORDER BY 1`, memberships, groupMembers, groups))
	rows, err := ts.tx.QueryContext(
		ctx, query,
		model.SystemTenantID.String(), userID.String(),
		model.SystemTenantID.String(), userID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("discover user directory tenants: %w", err)
	}
	defer rows.Close()

	var tenants []model.TenantID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("discover user directory tenants: %w", err)
		}
		tenants = append(tenants, model.TenantID(strings.TrimSpace(raw)))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("discover user directory tenants: %w", err)
	}
	return tenants, nil
}
