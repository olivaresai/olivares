// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// systemScope implements store.SystemScope: the privileged, cross-tenant
// operations (provisioning, deletion, global verification). It is only reachable
// from Store.System, which the engine alone holds.
type systemScope struct {
	s        *sqlStore
	tx       *sql.Tx
	poisoned error
}

// poison records a lifecycle-operation refusal on the transaction envelope.
// A System callback is ordinary Go code and can accidentally discard the
// returned error; without this sticky bit SQLite would still allow the callback
// to return nil and commit whatever statements preceded that refusal. Keep the
// first error so errors.Is retains the concrete deny-closed reason.
func (sys *systemScope) poison(err error) {
	if err != nil && sys.poisoned == nil {
		sys.poisoned = err
	}
}

// auditLogFor builds the audit writer for one tenant chain with the SAME
// configuration a tenant scope gets (scope.go). System-path appends
// (provisioning, cross-tenant events) are evidence like any other: they must be
// spool-accounted and budget-guarded, or the incremental counter drifts from
// the boot recompute and the system chain becomes a budget bypass.
func (sys *systemScope) auditLogFor(tenant model.TenantID) *auditLog {
	return &auditLog{
		tx: sys.tx, tenant: tenant, dia: sys.s.dia, clock: sys.s.clock,
		signEvent:     sys.s.signEvent,
		spoolMaxBytes: sys.s.spoolMaxBytes, spoolOnFull: sys.s.spoolOnFull,
		blindMeta: sys.s.blindMeta,
	}
}

// bindFor binds a specific tenant for the current statement on Postgres (so an
// RLS-checked write/read targets that tenant); on SQLite the cleared pin already
// permits the privileged path, so it is a no-op.
func (sys *systemScope) bindFor(ctx context.Context, tenant model.TenantID) error {
	// SQLite also needs the ordinary tenant pin for its tripwire triggers.
	// bindDirectoryTenant additionally clears the one-transaction writer
	// presentation there, so a generation can never survive a rebind.
	return bindDirectoryTenant(ctx, sys.tx, sys.s.dia, tenant)
}

// CreateOrg provisions a new tenant: it allocates the tenant id (which is also
// the org id), inserts the org row, and seeds the org's audit chain.
func (sys *systemScope) CreateOrg(
	ctx context.Context,
	org model.Org,
) (_ model.Org, retErr error) {
	defer func() { sys.poison(retErr) }()
	// HA write-gate: provisioning a tenant is a write, so a standby must not
	// do it. EnsureSystemTenant is deliberately NOT gated (it is the idempotent
	// bootstrap a node runs as it is promoted, before leadership is advertised).
	if !sys.s.elector.active() {
		return model.Org{}, store.ErrNotLeader
	}
	tenant := model.NewTenantID()
	now := sys.s.clock.Now()
	org.ID = model.ID(tenant)
	org.TenantID = tenant
	org.CreatedAt = now
	org.UpdatedAt = now
	org.Version = 1
	org.DeletedAt = nil

	writer, err := acquireDirectoryWriter(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return model.Org{}, err
	}
	if err := sys.bindFor(ctx, tenant); err != nil {
		return model.Org{}, err
	}
	// Seed the order-5 authorization fact before the higher-order organization
	// source fact. Any later failure rolls both rows and the audit append back.
	if err := insertDirectoryEpochRow(ctx, sys.tx, sys.s.dia, tenant); err != nil {
		return model.Org{}, err
	}
	// Seed the order-25 authorization generation in the same transaction as the
	// tenant, directory epoch, default workspace and audit-chain head. A partial
	// tenant can therefore never escape CreateOrg.
	if err := insertAuthorizationEpochRow(ctx, sys.tx, sys.s.dia, tenant); err != nil {
		return model.Org{}, err
	}
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return model.Org{}, err
	}
	if err := sys.insertOrgRow(ctx, org); err != nil {
		return model.Org{}, err
	}
	// Seed the tenant's default workspace (FASE X /) in the same transaction,
	// so a freshly-provisioned tenant always has the workspace an unset
	// WorkspaceID resolves to. It is unaudited: the org.create event below already
	// records provisioning, and seeding the default workspace is part of it (so a
	// new tenant's audit chain still starts at seq 1 with org.create, unchanged).
	if err := sys.insertDefaultWorkspaceRow(ctx, tenant); err != nil {
		return model.Org{}, err
	}

	a := sys.auditLogFor(tenant)
	draft := model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: "org.create", TargetKind: orgDescriptor.Kind, TargetID: org.ID,
	}
	event, err := a.Append(ctx, draft)
	if err != nil {
		return model.Org{}, err
	}
	if err := requireSystemAuditEvent(ctx, sys.tx, sys.s.dia, event, tenant, draft); err != nil {
		return model.Org{}, err
	}
	if err := finishDirectoryWriter(ctx, sys.tx, sys.s.dia); err != nil {
		return model.Org{}, err
	}
	return org, nil
}

// insertDefaultWorkspaceRow inserts a tenant's default workspace row (the
// reserved "default" slug) under the current tenant bind. It mirrors
// insertOrgRow: a direct, fully-stamped insert with no separate audit event
// (provisioning is recorded once, by the org.create / boot path). The caller
// binds the tenant first.
func (sys *systemScope) insertDefaultWorkspaceRow(ctx context.Context, tenant model.TenantID) error {
	now := sys.s.clock.Now()
	ws := model.Workspace{
		BaseFields: model.BaseFields{
			ID: model.NewID(), TenantID: tenant, CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		Name: "Default", Slug: model.DefaultWorkspaceSlug, Status: model.StatusActive,
	}
	rec, err := workspaceCodec.Encode(ws)
	if err != nil {
		return err
	}
	baseToRecord(rec, ws.BaseFields, false)
	cols := workspaceDescriptor.AllColumns()
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = rec[c]
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		directoryWriterRelation(sys.s.dia, workspaceDescriptor.Table),
		strings.Join(cols, ", "), placeholders(len(cols)))
	result, err := sys.tx.ExecContext(ctx, sys.s.dia.Rebind(q), args...)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("insert default workspace affected %d rows, want exactly one", rows)
	}
	initializerScope := &tenantScope{s: sys.s, tx: sys.tx, tenant: tenant}
	if err := initializerScope.initializeWorkspace(ctx, ws); err != nil {
		// Sticky poison is load-bearing here: ensureDefaultWorkspace treats an
		// insert conflict as idempotent success, and a System caller can discard
		// EnsureDefaultWorkspaces' error. Neither may commit a workspace whose
		// mandatory module bootstrap failed after the row insert.
		sys.poison(err)
		return fmt.Errorf("initialize default workspace %s: %w", ws.ID, err)
	}
	return nil
}

// EnsureDefaultWorkspaces back-fills the default workspace for every existing
// tenant that lacks one (FASE X /) — the path for tenants provisioned
// before. It is idempotent (a tenant that already has its default is
// skipped) and safe on every boot. It iterates ListOrgs, so on a multi-tenant
// Postgres deployment it covers the tenants the configured pool can see (full
// coverage needs the BYPASSRLS admin pool, like every cross-tenant System read);
// SQLite and the per-tenant CreateOrg seed are unaffected. The reserved system
// tenant holds no business workspaces and is skipped.
func (sys *systemScope) EnsureDefaultWorkspaces(ctx context.Context) error {
	// ListOrgsVisible, not ListOrgs, and this is the named exception rather than a
	// convenience. boot.go RETURNS this error, so inheriting the fail-closed read
	// would stop every Postgres deployment without --admin-dsn from booting at all —
	// trading a back-fill that self-heals for an estate that will not start. The
	// back-fill is idempotent and re-runs every boot: a tenant this pool cannot see
	// keeps its default from CreateOrg, or gets it on the pass after an admin pool
	// is provisioned. Missing one costs a later pass; refusing to boot costs the
	// service. Nothing here certifies coverage, so nothing here may claim it.
	orgs, _, err := sys.ListOrgsVisible(ctx)
	if err != nil {
		return err
	}
	for _, org := range orgs {
		if org.TenantID.IsZero() || org.TenantID.IsSystem() {
			continue
		}
		if err := sys.ensureDefaultWorkspace(ctx, org.TenantID); err != nil {
			return fmt.Errorf("ensure default workspace for tenant %s: %w", org.TenantID, err)
		}
	}
	return nil
}

// ensureDefaultWorkspace inserts one tenant's default workspace if it is absent.
// It binds the tenant, probes for an existing default by slug, and inserts only
// when missing. A concurrent boot that inserts between the probe and the insert
// trips the (tenant_id, slug) unique index, which is ErrConflict — success for an
// idempotent ensure, not an error.
func (sys *systemScope) ensureDefaultWorkspace(ctx context.Context, tenant model.TenantID) error {
	if err := sys.bindFor(ctx, tenant); err != nil {
		return err
	}
	exists, err := sys.hasDefaultWorkspace(ctx, tenant)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := sys.insertDefaultWorkspaceRow(ctx, tenant); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	return nil
}

// hasDefaultWorkspace reports whether tenant already has its default workspace.
// The caller binds the tenant first.
func (sys *systemScope) hasDefaultWorkspace(ctx context.Context, tenant model.TenantID) (bool, error) {
	q := sys.s.dia.Rebind(fmt.Sprintf(
		"SELECT 1 FROM %s WHERE tenant_id = ? AND slug = ? LIMIT 1",
		directoryWriterRelation(sys.s.dia, workspaceDescriptor.Table),
	))
	var one int
	err := sys.tx.QueryRowContext(ctx, q, tenant.String(), model.DefaultWorkspaceSlug).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// insertOrgRow inserts a fully-stamped org row. Callers set the base fields and
// bind the tenant first.
func (sys *systemScope) insertOrgRow(ctx context.Context, org model.Org) error {
	rec, err := orgCodec.Encode(org)
	if err != nil {
		return err
	}
	baseToRecord(rec, org.BaseFields, false)
	cols := orgDescriptor.AllColumns()
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = rec[c]
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		directoryWriterRelation(sys.s.dia, orgDescriptor.Table),
		strings.Join(cols, ", "), placeholders(len(cols)))
	result, err := sys.tx.ExecContext(ctx, sys.s.dia.Rebind(q), args...)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("insert organization affected %d rows, want exactly one", rows)
	}
	return nil
}

// EnsureSystemTenant idempotently provisions the reserved system tenant's own
// org row (id == SystemTenantID) and seeds its audit-chain genesis, so the chain
// that holds auth and cross-tenant events is well-formed from sequence 1. It is a
// no-op returning the existing row on subsequent boots.
func (sys *systemScope) EnsureSystemTenant(ctx context.Context) (model.Org, error) {
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return model.Org{}, err
	}
	if org, err := sys.GetOrg(ctx, model.SystemTenantID); err == nil {
		return org, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Org{}, err
	}

	now := sys.s.clock.Now()
	org := model.Org{
		BaseFields: model.BaseFields{
			ID: model.ID(model.SystemTenantID), TenantID: model.SystemTenantID,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		Name: "System", Slug: "system", Status: model.StatusActive,
	}
	if err := sys.insertOrgRow(ctx, org); err != nil {
		return model.Org{}, err
	}
	a := sys.auditLogFor(model.SystemTenantID)
	if _, err := a.Append(ctx, model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: "system.genesis", TargetKind: orgDescriptor.Kind, TargetID: org.ID,
	}); err != nil {
		return model.Org{}, err
	}
	return org, nil
}

// SetOrgRegion sets (or clears, with region == "") a tenant's data-residency pin
// (orgs.data_region —). It reads the org under the tenant bind, then UPDATEs
// the pin, updated_at and version with an optimistic-concurrency guard on the
// version it read: a concurrent SetOrgSettings on the same row (which also writes
// data_region back through the codec) therefore fails its own version CAS instead
// of silently reverting the pin. The change is recorded to the tenant's audit chain.
func (sys *systemScope) SetOrgRegion(ctx context.Context, tenant model.TenantID, region string) (model.Org, error) {
	// HA write-gate: re-pinning a tenant is a write, so a standby must not do
	// it — same gate as CreateOrg/DropTenant; inert on a single-node store.
	if !sys.s.elector.active() {
		return model.Org{}, store.ErrNotLeader
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return model.Org{}, fmt.Errorf("%w: cannot pin reserved tenant", store.ErrInvalidDescriptor)
	}
	// GetOrg binds the tenant for the transaction and reads the current row.
	org, err := sys.GetOrg(ctx, tenant)
	if err != nil {
		return model.Org{}, err
	}
	prevVersion := org.Version
	org.DataRegion = region
	org.UpdatedAt = sys.s.clock.Now()
	org.Version = prevVersion + 1
	q := sys.s.dia.Rebind(fmt.Sprintf(
		"UPDATE %s SET data_region = ?, updated_at = ?, version = ? WHERE id = ? AND tenant_id = ? AND version = ?",
		directoryWriterRelation(sys.s.dia, orgDescriptor.Table)))
	res, err := sys.tx.ExecContext(ctx, q, region, org.UpdatedAt.String(), org.Version, tenant.String(), tenant.String(), prevVersion)
	if err != nil {
		return model.Org{}, mapWriteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return model.Org{}, err
	}
	if n == 0 {
		return model.Org{}, store.ErrConflict // the org changed under us; caller retries
	}
	a := sys.auditLogFor(tenant)
	if _, err := a.Append(ctx, model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: "org.set_region", TargetKind: orgDescriptor.Kind, TargetID: org.ID,
	}); err != nil {
		return model.Org{}, err
	}
	return org, nil
}

// SetOrgStatus withdraws (StatusSuspended) or restores (StatusActive) a tenant's
// service by writing orgs.status — and nothing else. It mirrors SetOrgRegion
// exactly: the row is read under the tenant bind, then UPDATEd with an
// optimistic-concurrency guard on the version it read, so a concurrent
// SetOrgSettings (which writes status back through the codec) fails its own
// version CAS instead of silently reviving a suspended tenant. The change is
// recorded to the tenant's audit chain under distinct actions, so "when did we
// stop serving them" and "when did we resume" are both answerable from evidence.
//
// It accepts only active/suspended: the other LifecycleStatus values are not
// service states for an org, and silently storing one would leave a tenant in a
// state the guard has no rule for.
func (sys *systemScope) SetOrgStatus(
	ctx context.Context,
	tenant model.TenantID,
	status model.LifecycleStatus,
) (_ model.Org, retErr error) {
	defer func() { sys.poison(retErr) }()
	// HA write-gate: withdrawing service is a write, so a standby must not
	// do it — same gate as CreateOrg/SetOrgRegion/DropTenant.
	if !sys.s.elector.active() {
		return model.Org{}, store.ErrNotLeader
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return model.Org{}, fmt.Errorf("%w: cannot suspend reserved tenant", store.ErrInvalidDescriptor)
	}
	if status != model.StatusActive && status != model.StatusSuspended {
		return model.Org{}, fmt.Errorf("%w: org status must be %q or %q", store.ErrInvalidDescriptor,
			model.StatusActive, model.StatusSuspended)
	}
	// Serialize before discovering the old status. Otherwise an auth/directory
	// writer could commit between this read and the epoch bump and leave the
	// status change outside the snapshot fence.
	writer, err := acquireDirectoryWriter(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return model.Org{}, err
	}
	// GetOrg binds the tenant for the transaction and reads the current row.
	org, err := sys.GetOrg(ctx, tenant)
	if err != nil {
		return model.Org{}, err
	}
	// Idempotent, in the same sense as the superadmin account lifecycle in
	// core/auth: a re-assertion still lands on the ledger, but it changes NO state.
	// Bumping the version on a no-op would invalidate any concurrent holder of the
	// current version — so a control plane replaying a webhook could make an
	// unrelated update fail its CAS — and would leave two different answers to
	// "when did we stop serving them".
	if org.Status != status {
		if err := bumpDirectoryEpochExact(ctx, sys.tx, sys.s.dia, tenant); err != nil {
			return model.Org{}, err
		}
		if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
			return model.Org{}, err
		}
		prevVersion := org.Version
		org.Status = status
		org.UpdatedAt = sys.s.clock.Now()
		org.Version = prevVersion + 1
		q := sys.s.dia.Rebind(fmt.Sprintf(
			"UPDATE %s SET status = ?, updated_at = ?, version = ? WHERE id = ? AND tenant_id = ? AND version = ?",
			directoryWriterRelation(sys.s.dia, orgDescriptor.Table)))
		res, err := sys.tx.ExecContext(ctx, q, string(status), org.UpdatedAt.String(), org.Version, tenant.String(), tenant.String(), prevVersion)
		if err != nil {
			return model.Org{}, mapWriteErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return model.Org{}, err
		}
		if n == 0 {
			return model.Org{}, store.ErrConflict // the org changed under us; caller retries
		}
		if err := finishDirectoryWriter(ctx, sys.tx, sys.s.dia); err != nil {
			return model.Org{}, err
		}
	}
	action := "org.restore_service"
	if status == model.StatusSuspended {
		action = "org.suspend_service"
	}
	a := sys.auditLogFor(tenant)
	if _, err := a.Append(ctx, model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: action, TargetKind: orgDescriptor.Kind, TargetID: org.ID,
	}); err != nil {
		return model.Org{}, err
	}
	return org, nil
}

// GetOrg returns one tenant's org row.
func (sys *systemScope) GetOrg(ctx context.Context, tenant model.TenantID) (model.Org, error) {
	if err := sys.bindFor(ctx, tenant); err != nil {
		return model.Org{}, err
	}
	cols := orgDescriptor.AllColumns()
	q := fmt.Sprintf("SELECT %s FROM %s WHERE id = ? AND tenant_id = ?",
		strings.Join(cols, ", "), directoryWriterRelation(sys.s.dia, orgDescriptor.Table))
	st, err := newScanState(orgDescriptor, cols)
	if err != nil {
		return model.Org{}, err
	}
	row := sys.tx.QueryRowContext(ctx, sys.s.dia.Rebind(q), tenant.String(), tenant.String())
	if err := row.Scan(st.dests...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Org{}, store.ErrNotFound
		}
		return model.Org{}, err
	}
	return decodeOrg(st.record())
}

// ListOrgs returns every tenant's org row, or store.ErrEnumerationNotAuthoritative
// when this store cannot make that claim. It is the fail-closed door: a caller that
// certifies coverage over "all tenants" gets an error rather than a short list it
// would report as complete.
//
// The rows the pool CAN see are still returned alongside the error, so a caller that
// deliberately catches the sentinel keeps them without a second query. That is not
// an invitation: the supported way to tolerate a partial list is ListOrgsVisible,
// which names the tolerance.
func (sys *systemScope) ListOrgs(ctx context.Context) ([]model.Org, error) {
	orgs, authoritative, err := sys.ListOrgsVisible(ctx)
	if err != nil {
		return nil, err
	}
	if !authoritative {
		return orgs, fmt.Errorf("%w: engine %q holds no BYPASSRLS admin pool, so this System read is RLS-limited to the cleared tenant GUC and returned %d row(s) that CANNOT be read as the whole estate; provision a NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql) and pass --admin-dsn",
			store.ErrEnumerationNotAuthoritative, sys.s.engine, len(orgs))
	}
	return orgs, nil
}

// ListOrgsVisible returns the org rows this pool can see and whether that set is
// AUTHORITATIVE — the named exception documented on store.SystemScope, for callers
// whose work is legitimately best-effort per tenant.
//
// On a successful call, authoritative is false in exactly one configuration:
// Postgres with no dedicated BYPASSRLS admin pool. There the System transaction
// runs on the application pool,
// which is FORCE-RLS-scoped to a tenant GUC the System path has cleared, so the
// query matches nothing — fail-closed-empty, never a cross-tenant leak, but also
// never evidence that the estate is empty. On SQLite there are no roles and a single
// connection, so the System transaction IS the whole estate. A Postgres admin
// read re-attests the exact boot-pinned NOSUPERUSER BYPASSRLS identity and safe
// trigger posture inside one repeatable-read/read-only transaction, then queries
// the inventory through that same transaction. The Open-time fact alone is not
// sufficient evidence for a later pooled connection.
func (sys *systemScope) ListOrgsVisible(ctx context.Context) ([]model.Org, bool, error) {
	adminPool := sys.s.adminDB != nil && sys.s.adminDB != sys.s.db
	if !adminPool {
		orgs, err := sys.listOrgsVisibleRows(ctx, sys.tx)
		// Postgres is the only engine with roles and RLS. Without the distinct
		// admin pool its cleared app transaction is deliberately non-authoritative.
		return orgs, sys.s.engine != store.EnginePostgres, err
	}

	adminTx, err := sys.s.adminDB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, false, fmt.Errorf(
			"%w: begin pinned AdminDSN inventory snapshot: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	defer adminTx.Rollback() //nolint:errcheck // committed on success; failure is returned below

	posture, err := sys.s.dia.ConnRolePosture(ctx, adminTx)
	if err != nil {
		return nil, false, fmt.Errorf(
			"%w: attest live AdminDSN inventory posture: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	if err := requirePinnedDirectoryAdminPosture(posture, sys.s.directoryAdminRole); err != nil {
		return nil, false, err
	}
	// The exact live role is necessary but not sufficient: a same-role DSN can
	// still address another database, or a database with the same name on another
	// cluster. Challenge the application-side System transaction and this exact
	// read-only admin snapshot before consuming any inventory rows. Advisory locks
	// do not grant row-write authority, so the admin ceremony remains read-only.
	if err := verifyDirectoryActivationDatabaseIdentity(
		ctx, sys.tx, directoryActivationWitnesses{admin: adminTx},
	); err != nil {
		return nil, false, fmt.Errorf(
			"%w: pinned AdminDSN inventory identity: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	orgs, err := sys.listOrgsVisibleRows(ctx, adminTx)
	if err != nil {
		return nil, false, fmt.Errorf(
			"%w: query pinned AdminDSN inventory snapshot: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	if err := adminTx.Commit(); err != nil {
		return nil, false, fmt.Errorf(
			"%w: commit pinned AdminDSN inventory snapshot: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	return orgs, true, nil
}

func requirePinnedDirectoryAdminPosture(
	posture dialect.RolePosture,
	boot guardRoleFact,
) error {
	if !boot.Known || strings.TrimSpace(boot.Role) == "" || posture.Role != boot.Role ||
		!posture.BypassRLS || posture.Superuser ||
		posture.ReplicationRole == "" || posture.TriggersDisabled() {
		return fmt.Errorf(
			"%w: live AdminDSN must be exact boot-pinned NOSUPERUSER BYPASSRLS with triggers active: role=%q want=%q known=%t superuser=%t bypassrls=%t replication_role=%q",
			store.ErrEnumerationNotAuthoritative,
			posture.Role, boot.Role, boot.Known, posture.Superuser, posture.BypassRLS,
			posture.ReplicationRole,
		)
	}
	return nil
}

func (sys *systemScope) listOrgsVisibleRows(
	ctx context.Context,
	queryer directoryTenantEnumerator,
) ([]model.Org, error) {
	cols := orgDescriptor.AllColumns()
	q := sys.s.dia.Rebind(fmt.Sprintf("SELECT %s FROM %s ORDER BY id ASC",
		strings.Join(cols, ", "), directoryWriterRelation(sys.s.dia, orgDescriptor.Table)))
	rows, err := queryer.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Org
	for rows.Next() {
		st, err := newScanState(orgDescriptor, cols)
		if err != nil {
			return nil, err
		}
		if err := rows.Scan(st.dests...); err != nil {
			return nil, err
		}
		org, err := decodeOrg(st.record())
		if err != nil {
			return nil, err
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// DropTenant deletes every ordinary entity row owned by tenant, recording the
// deletion to the system audit chain first. Append-only evidence and descriptors
// whose lifecycle declares RetainOnTenantDrop survive; purging them is the
// separate retention path.
func (sys *systemScope) DropTenant(ctx context.Context, tenant model.TenantID) (retErr error) {
	defer func() { sys.poison(retErr) }()
	// HA write-gate: deleting a tenant is a write; a standby must not do it.
	if !sys.s.elector.active() {
		return store.ErrNotLeader
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return fmt.Errorf("%w: cannot drop reserved tenant", store.ErrInvalidDescriptor)
	}
	if err := (model.DirectoryEpoch{BaseFields: model.BaseFields{
		ID: model.ID(tenant), TenantID: tenant, Version: 1,
	}}).Validate(); err != nil {
		return fmt.Errorf("%w: invalid tenant: %v", store.ErrInvalidDescriptor, err)
	}
	writer, err := acquireDirectoryWriter(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return err
	}
	// A missing epoch is unknown directory evidence, not proof that the tenant
	// is already gone. Refuse before appending tenant.drop or deleting anything.
	if err := sys.bindFor(ctx, tenant); err != nil {
		return err
	}
	if _, found, err := readDirectoryEpochRow(ctx, sys.tx, sys.s.dia, tenant); err != nil {
		return directoryEpochUnavailable(err)
	} else if !found {
		return fmt.Errorf("%w: tenant %s has no directory epoch",
			store.ErrDirectoryUnavailable, tenant)
	}
	// Directory epoch has global lock order 5. Retire it before any source
	// DELETE can acquire the Identity=10 or Agent=20 locks; rollback restores it
	// if audit, a higher-order delete, credential cleanup or commit later fails.
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return err
	}
	if err := deleteDirectoryEpochExact(ctx, sys.tx, sys.s.dia, tenant); err != nil {
		return err
	}
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return err
	}
	sysAudit := sys.auditLogFor(model.SystemTenantID)
	draft := model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: "tenant.drop", TargetKind: orgDescriptor.Kind, TargetID: model.ID(tenant),
	}
	event, err := sysAudit.Append(ctx, draft)
	if err != nil {
		return err
	}
	if err := requireSystemAuditEvent(
		ctx, sys.tx, sys.s.dia, event, model.SystemTenantID, draft,
	); err != nil {
		return err
	}

	if err := sys.bindFor(ctx, tenant); err != nil {
		return err
	}
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return err
	}
	for _, d := range tenantDropDescriptors(sys.s.reg) {
		if d.Kind == model.AuthorizationEpochKind {
			// Unlike Identity/Agent facts, exactly one epoch must exist. Validate
			// its complete shape at order 25 and delete the exact observed version;
			// absence, corruption or a concurrent bump refuses the whole drop.
			epoch, found, err := readAuthorizationEpochRow(ctx, sys.tx, sys.s.dia, tenant)
			if err != nil {
				return authorizationEpochUnavailable("validate before tenant drop", err)
			}
			if !found {
				return authorizationEpochUnavailable("row is absent before tenant drop", nil)
			}
			q := sys.s.dia.Rebind(fmt.Sprintf(
				"DELETE FROM %s WHERE id = ? AND tenant_id = ? AND version = ?",
				directoryWriterRelation(sys.s.dia, d.Table),
			))
			result, err := sys.tx.ExecContext(
				ctx, q, epoch.ID.String(), tenant.String(), epoch.Version,
			)
			if err != nil {
				return authorizationEpochUnavailable("delete during tenant drop", err)
			}
			rows, err := result.RowsAffected()
			if err != nil {
				return authorizationEpochUnavailable("count delete during tenant drop", err)
			}
			if rows != 1 {
				return authorizationEpochUnavailable(
					fmt.Sprintf("tenant drop CAS affected %d rows", rows), nil,
				)
			}
		} else {
			q := sys.s.dia.Rebind(fmt.Sprintf("DELETE FROM %s WHERE tenant_id = ?",
				directoryWriterRelation(sys.s.dia, d.Table)))
			if _, err := sys.tx.ExecContext(ctx, q, tenant.String()); err != nil {
				return fmt.Errorf("drop tenant %s: %w", d.Table, err)
			}
		}
		if err := sys.requireNoRows(ctx, d.Table, "tenant_id = ?", tenant.String()); err != nil {
			return err
		}
		if d.AuthorizationFact && tenantDropAfterAuthorizationFactTestHook != nil {
			if err := tenantDropAfterAuthorizationFactTestHook(d.Kind); err != nil {
				return err
			}
		}
	}
	if err := sys.deleteOrgExact(ctx, tenant); err != nil {
		return err
	}

	// The auth partition lives in the system tenant, so the per-tenant deletes
	// above never reach the credential rows that REFERENCE the dropped tenant.
	// Purge them explicitly under a system-tenant binding, or dropping a tenant
	// would leave dangling memberships and still-valid tokens bound to it.
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return err
	}
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return err
	}
	if err := sys.deleteTenantAuthEstate(ctx, tenant); err != nil {
		return err
	}
	return finishDirectoryWriter(ctx, sys.tx, sys.s.dia)
}

// deleteTenantAuthEstate removes the closed set of ordinary system-partition
// rows that grant, configure or carry credentials for tenant. Rows with only an
// indirect parent reference are deleted before that parent, and every relation
// is pinned to the engine schema so a SQLite TEMP table cannot retain a live
// credential while making the lifecycle operation appear successful.
//
// Deliberately retained auth evidence is not listed here: users, auth_sessions,
// webauthn_credentials and set_seen_jtis outlive a business tenant. Scope and
// source tenant matches are exact canonical UUID strings; no slug or alias is
// inferred, and global SYSTEM-scoped rows are never selected.
func (sys *systemScope) deleteTenantAuthEstate(
	ctx context.Context,
	tenant model.TenantID,
) error {
	systemID := model.SystemTenantID.String()
	tenantID := tenant.String()
	relation := func(table string) string {
		return directoryWriterRelation(sys.s.dia, table)
	}
	exec := func(table, query string, args ...any) error {
		if _, err := sys.tx.ExecContext(ctx, sys.s.dia.Rebind(query), args...); err != nil {
			return fmt.Errorf("drop tenant %s cleanup: %w", table, err)
		}
		return nil
	}
	requireEmpty := func(table, where string, args ...any) error {
		return sys.requireNoRows(ctx, table, "tenant_id = ? AND ("+where+")",
			append([]any{systemID}, args...)...)
	}

	groups := relation(userGroupDescriptor.Table)
	configs := relation(federationConfigDescriptor.Table)
	services := relation(pepServiceDescriptor.Table)
	tokens := relation(apiTokenDescriptor.Table)
	groupsForTenant := "SELECT id FROM " + groups +
		" WHERE tenant_id = ? AND target_tenant_id = ?"
	configsForTenant := "SELECT id FROM " + configs +
		" WHERE tenant_id = ? AND target_tenant_id = ?"
	servicesForTenant := "SELECT id FROM " + services +
		" WHERE tenant_id = ? AND target_tenant_id = ?"
	tokensForTenant := "SELECT id FROM " + tokens +
		" WHERE tenant_id = ? AND bound_tenant_id = ?"

	// Group-member rows carry no target of their own; attribution comes from the
	// group. Delete them before the target groups.
	if err := exec(userGroupMemberDescriptor.Table, fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = ? AND group_id IN (%s)",
		relation(userGroupMemberDescriptor.Table), groupsForTenant,
	), systemID, systemID, tenantID); err != nil {
		return err
	}
	if err := requireEmpty(userGroupMemberDescriptor.Table,
		"group_id IN ("+groupsForTenant+")",
		systemID, tenantID); err != nil {
		return err
	}

	// Federation claims are a projection of configs. Match both the explicit
	// target and the parent config so a divergent projection cannot dangle.
	federationClaimWhere := "target_tenant_id = ? OR config_id IN (" +
		configsForTenant + ")"
	if err := exec(federationDomainClaimDescriptor.Table, fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = ? AND (%s)",
		relation(federationDomainClaimDescriptor.Table), federationClaimWhere,
	), systemID, tenantID, systemID, tenantID); err != nil {
		return err
	}
	if err := requireEmpty(federationDomainClaimDescriptor.Table,
		federationClaimWhere,
		tenantID, systemID, tenantID); err != nil {
		return err
	}

	// Claims and handles depend on PEP services. Their explicit target remains
	// authoritative, while the service selector defensively removes a divergent
	// child before its soon-to-be-deleted parent.
	for _, child := range []struct {
		table      string
		serviceCol string
	}{
		{pdpDecisionClaimDescriptor.Table, "pep_service_id"},
		{delegationHandleDescriptor.Table, "pep_service_id"},
	} {
		childWhere := "target_tenant_id = ? OR " + child.serviceCol +
			" IN (" + servicesForTenant + ")"
		if err := exec(child.table, fmt.Sprintf(
			"DELETE FROM %s WHERE tenant_id = ? AND (%s)",
			relation(child.table), childWhere,
		), systemID, tenantID, systemID, tenantID); err != nil {
			return err
		}
		if err := requireEmpty(child.table,
			childWhere,
			tenantID, systemID, tenantID); err != nil {
			return err
		}
	}

	// Credential bindings have no target axis. A row belongs to this estate when
	// either its PEP service or its underlying API token belongs to tenant.
	credentialWhere := "service_id IN (" + servicesForTenant + ") OR " +
		"token_id IN (" + tokensForTenant + ")"
	if err := exec(pepServiceCredentialDescriptor.Table, fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = ? AND (%s)",
		relation(pepServiceCredentialDescriptor.Table), credentialWhere,
	), systemID, systemID, tenantID, systemID, tenantID); err != nil {
		return err
	}
	if err := requireEmpty(pepServiceCredentialDescriptor.Table,
		credentialWhere,
		systemID, tenantID, systemID, tenantID); err != nil {
		return err
	}

	for _, direct := range []struct {
		table string
		where string
		args  []any
	}{
		{membershipDescriptor.Table, "target_tenant_id = ?", []any{tenantID}},
		{userInviteDescriptor.Table, "target_tenant_id = ?", []any{tenantID}},
		{secretEntryDescriptor.Table, "scope = ?", []any{tenantID}},
		{sourceDefDescriptor.Table, "(scope = ? OR tenant = ?)", []any{tenantID, tenantID}},
		{federationConfigDescriptor.Table, "target_tenant_id = ?", []any{tenantID}},
		{pepServiceDescriptor.Table, "target_tenant_id = ?", []any{tenantID}},
		{userGroupDescriptor.Table, "target_tenant_id = ?", []any{tenantID}},
		{apiTokenDescriptor.Table, "bound_tenant_id = ?", []any{tenantID}},
	} {
		args := append([]any{systemID}, direct.args...)
		if err := exec(direct.table, fmt.Sprintf(
			"DELETE FROM %s WHERE tenant_id = ? AND %s",
			relation(direct.table), direct.where,
		), args...); err != nil {
			return err
		}
		if err := requireEmpty(direct.table, direct.where, direct.args...); err != nil {
			return err
		}
	}
	return nil
}

// requireNoRows is the lifecycle postcondition for variable-cardinality
// deletes. RowsAffected cannot distinguish a complete delete from SQLite's
// BEFORE DELETE RAISE(IGNORE), so DropTenant re-reads each closed selector while
// its parents still exist and refuses the transaction unless it is empty.
func (sys *systemScope) requireNoRows(
	ctx context.Context,
	table string,
	where string,
	args ...any,
) error {
	query := sys.s.dia.Rebind(fmt.Sprintf(
		"SELECT COUNT(*) FROM %s WHERE %s",
		directoryWriterRelation(sys.s.dia, table), where,
	))
	var count int64
	if err := sys.tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return fmt.Errorf("verify drop tenant %s cleanup: %w", table, err)
	}
	if count != 0 {
		return fmt.Errorf("%w: drop tenant left %d row(s) in %s",
			store.ErrDirectoryUnavailable, count, table)
	}
	return nil
}

// requireSystemAuditEvent turns audit degrade's explicit zero,nil result into a
// refusal for lifecycle mutations that cannot exist without their evidence.
// It also verifies the minimum immutable projection before the surrounding
// transaction is allowed to commit.
func requireSystemAuditEvent(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	event model.AuditEvent,
	tenant model.TenantID,
	draft model.AuditDraft,
) error {
	if event.Seq <= 0 {
		return fmt.Errorf("%w: required %s evidence was not persisted",
			store.ErrAuditSpoolFull, draft.Action)
	}
	parsedID, err := model.ParseID(event.ID.String())
	if err != nil || parsedID != event.ID || event.ID.IsZero() {
		return fmt.Errorf("required %s audit event has non-canonical id %q",
			draft.Action, event.ID)
	}
	if len(event.Hash) != 32 || event.TenantID != tenant ||
		event.Action != draft.Action || event.TargetKind != draft.TargetKind ||
		event.TargetID != draft.TargetID {
		return fmt.Errorf("required %s audit event has invalid projection", draft.Action)
	}
	query := dia.Rebind("SELECT tenant_id, seq, action, target_kind, target_id, hash FROM " +
		directoryWriterRelation(dia, auditTable) + " WHERE id = ? LIMIT 2")
	rows, err := tx.QueryContext(ctx, query, event.ID.String())
	if err != nil {
		return fmt.Errorf("revalidate required %s audit event: %w", draft.Action, err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var count int
	var storedTenant, action, targetKind, targetID string
	var seq int64
	var hash []byte
	for rows.Next() {
		count++
		if err := rows.Scan(
			&storedTenant, &seq, &action, &targetKind, &targetID, &hash,
		); err != nil {
			return fmt.Errorf("revalidate required %s audit event: %w", draft.Action, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("revalidate required %s audit event: %w", draft.Action, err)
	}
	if count != 1 || storedTenant != tenant.String() || seq != event.Seq ||
		action != draft.Action || targetKind != string(draft.TargetKind) ||
		targetID != draft.TargetID.String() || !bytes.Equal(hash, event.Hash) {
		return fmt.Errorf("%w: required %s audit event is absent or divergent in %s",
			store.ErrDirectoryUnavailable, draft.Action,
			directoryWriterRelation(dia, auditTable))
	}
	headQuery := dia.Rebind("SELECT seq, hash FROM " +
		directoryWriterRelation(dia, auditHeadsTable) + " WHERE tenant_id = ? LIMIT 2")
	headRows, err := tx.QueryContext(ctx, headQuery, tenant.String())
	if err != nil {
		return fmt.Errorf("revalidate required %s audit head: %w", draft.Action, err)
	}
	defer headRows.Close() //nolint:errcheck // rows.Err below is authoritative
	var headCount int
	var headSeq int64
	var headHash []byte
	for headRows.Next() {
		headCount++
		if err := headRows.Scan(&headSeq, &headHash); err != nil {
			return fmt.Errorf("revalidate required %s audit head: %w", draft.Action, err)
		}
	}
	if err := headRows.Err(); err != nil {
		return fmt.Errorf("revalidate required %s audit head: %w", draft.Action, err)
	}
	if headCount != 1 || headSeq != event.Seq || !bytes.Equal(headHash, event.Hash) {
		return fmt.Errorf("%w: required %s audit head is absent or divergent in %s",
			store.ErrDirectoryUnavailable, draft.Action,
			directoryWriterRelation(dia, auditHeadsTable))
	}
	return nil
}

// tenantDropDescriptors puts every remaining authorization fact before all
// ordinary tenant data and orders those facts by the same global lock order as
// LockAuthoritySnapshot. Registry order is catalog order (Agent precedes
// Identity today) and therefore must never be used as a concurrency protocol.
func tenantDropDescriptors(reg *registry) []model.EntityDescriptor {
	var facts, ordinary []model.EntityDescriptor
	for _, d := range reg.descriptors() {
		// Append-only tables and explicitly retained mutable no-delete tables are
		// durable evidence. Org and epoch have exact, separately ordered deletes.
		if d.AppendOnly || d.RetainOnTenantDrop ||
			d.Kind == orgDescriptor.Kind || d.Kind == model.DirectoryEpochKind {
			continue
		}
		if d.AuthorizationFact {
			facts = append(facts, d)
		} else {
			ordinary = append(ordinary, d)
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].AuthorizationLockOrder != facts[j].AuthorizationLockOrder {
			return facts[i].AuthorizationLockOrder < facts[j].AuthorizationLockOrder
		}
		return facts[i].Kind < facts[j].Kind
	})
	return append(facts, ordinary...)
}

func (sys *systemScope) deleteOrgExact(ctx context.Context, tenant model.TenantID) error {
	query := sys.s.dia.Rebind("DELETE FROM " +
		directoryWriterRelation(sys.s.dia, orgDescriptor.Table) +
		" WHERE id = ? AND tenant_id = ?")
	result, err := sys.tx.ExecContext(ctx, query, tenant.String(), tenant.String())
	if err != nil {
		return fmt.Errorf("drop tenant %s: %w", orgDescriptor.Table, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("drop tenant %s affected %d rows, want exactly one",
			orgDescriptor.Table, rows)
	}
	return nil
}

// Verify checks a tenant's audit chain from fromSeq.
func (sys *systemScope) Verify(ctx context.Context, tenant model.TenantID, fromSeq int64) (store.VerifyReport, error) {
	if err := sys.bindFor(ctx, tenant); err != nil {
		return store.VerifyReport{}, err
	}
	a := sys.auditLogFor(tenant)
	return a.Verify(ctx, fromSeq)
}

// decodeOrg reconstructs an Org from a row record.
func decodeOrg(rec model.Record) (model.Org, error) {
	base, err := baseFromRecord(rec)
	if err != nil {
		return model.Org{}, err
	}
	return orgCodec.Decode(base, rec)
}
