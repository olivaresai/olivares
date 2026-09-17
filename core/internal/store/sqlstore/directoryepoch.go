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
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// directoryWriterLockKey is private engine ABI. No repository, module or
// caller can select another key and accidentally leave its directory mutation
// outside the one global order.
const directoryWriterLockKey = dialect.DirectoryWriterControlKey

type directoryEpochBootResult struct {
	coverageComplete           bool
	control                    directoryWriterControlState
	userAuthorityComplete      bool
	inventory                  BusinessDirectoryInventory
	inventoryAuthority         string
	inventoryUnavailableReason string
}

type directoryReconcileOptions struct {
	roles       guardRoles
	maintenance bool
}

// directoryEpochBeforeInsertTestHook is nil outside this package's tests. It
// exposes the transaction boundary after earlier tenant inserts have staged
// and before a selected later insert, so rollback-all is deterministic rather
// than inferred from a schema error that the v7 verifier would reject first.
var directoryEpochBeforeInsertTestHook func(model.TenantID) error

// tenantDropAfterAuthorizationFactTestHook is nil outside deterministic lock-
// order tests. It pauses DropTenant only after a named fact delete has acquired
// its production locks; it is never an alternate production control path.
var tenantDropAfterAuthorizationFactTestHook func(model.Kind) error

// reconcileDirectoryEpochs is the authoritative, per-boot forward path for
// databases whose already-tracked core v2 predates the v7 directory relation.
// It deliberately does not live in v7: reconcileColumns must first have made
// every current core table available.
//
// The write transaction always belongs to the application pool. PostgreSQL's
// cross-tenant enumeration comes from one independently attested BYPASSRLS
// admin transaction. That repeatable-read/read-only snapshot is kept until the
// inventory has been consumed and validated, then committed before the
// application transaction. With no admin pool PostgreSQL enumerates through the
// attested closed inventory routine on the application transaction, under the
// same global writer lock; an absent routine yields an explicitly incomplete
// witness (nothing written) only for a staged legacy ordinary boot, and is a
// refusal for enforced or maintenance. SQLite enumerates on the application
// transaction, which also avoids asking its single-connection pool for a nested
// connection.
func reconcileDirectoryEpochs(
	ctx context.Context,
	db, adminDB *sql.DB,
	dia dialect.Dialect,
	adminRoleForBoot guardRoleFact,
	options ...directoryReconcileOptions,
) (directoryEpochBootResult, error) {
	var out directoryEpochBootResult
	var opts directoryReconcileOptions
	if len(options) != 0 {
		opts = options[0]
	}
	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return out, fmt.Errorf("sqlstore: directory epoch reconcile begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	state, err := acquireDirectoryWriter(ctx, tx, dia)
	if err != nil {
		return out, fmt.Errorf("sqlstore: directory epoch reconcile: %w", err)
	}
	out.control = state

	inventory := directoryReconcileInventory{queryer: tx}
	closedRoutine := dia.Name() == store.EnginePostgres && adminDB == db
	out.inventoryAuthority = "sqlite"
	if dia.Name() == store.EnginePostgres {
		if err := verifyPostgresUserAuthorityLock(ctx, tx, opts.roles); err != nil {
			return out, err
		}
		present, err := verifyPostgresDirectoryInventory(ctx, tx, opts.roles)
		if err != nil {
			return out, err
		}
		if closedRoutine {
			if !present {
				if state.Mode != directoryWriterStaged || state.CoverageProtocol != coverageProtocolLegacy || opts.maintenance {
					return out, directoryUnavailable("closed directory inventory routine is required", nil)
				}
				out.inventoryAuthority = ""
				out.inventoryUnavailableReason = "closed_routine_missing"
				return out, nil
			}
			out.inventoryAuthority = "closed_routine"
		} else {
			out.inventoryAuthority = "admin_dsn"
			inventory, err = openDirectoryReconcileInventory(ctx, tx, adminDB, dia, adminRoleForBoot)
			if err != nil {
				return out, err
			}
		}
	}
	defer inventory.close()
	allowPartial := state.Mode == directoryWriterStaged && state.CoverageProtocol == coverageProtocolLegacy && !opts.maintenance
	observed, err := readDirectoryInventory(ctx, inventory.queryer, dia, closedRoutine, allowPartial)
	if err != nil {
		return out, fmt.Errorf("enumerate directory tenants: %w", err)
	}
	out.inventory = observed
	if observed.System.ID == "" {
		if !allowPartial || len(observed.BusinessTenants) != 0 || len(observed.Epochs) != 0 {
			return out, directoryUnavailable("nonempty directory inventory lacks SYSTEM witness", nil)
		}
		out.inventoryUnavailableReason = "system_bootstrap_pending"
		return out, nil
	}
	for _, tenant := range observed.BusinessTenants {
		if err := bindDirectoryTenant(ctx, tx, dia, tenant); err != nil {
			return out, fmt.Errorf("bind directory tenant %s: %w", tenant, err)
		}
		epoch, found, err := readDirectoryEpochRow(ctx, tx, dia, tenant)
		if err != nil {
			return out, fmt.Errorf("read directory epoch for tenant %s: %w", tenant, err)
		}
		if found {
			if epoch.Version != observed.Epochs[tenant] {
				return out, directoryUnavailable("directory inventory version changed under writer lock", nil)
			}
			continue
		}
		if !allowPartial {
			return out, directoryUnavailable("enforced or maintenance directory epoch is absent", nil)
		}
		if err := insertDirectoryEpochRow(ctx, tx, dia, tenant); err != nil {
			return out, fmt.Errorf("backfill directory epoch for tenant %s: %w", tenant, err)
		}
		out.inventory.Epochs[tenant] = 1
	}
	if err := out.inventory.requireComplete(); err != nil {
		return out, err
	}
	if err := bindDirectoryTenant(ctx, tx, dia, model.SystemTenantID); err != nil {
		return out, err
	}
	coverage, err := readUserAuthorityCoverage(ctx, tx, dia)
	if err != nil {
		return out, err
	}
	out.userAuthorityComplete = len(coverage.Missing) == 0
	if !out.userAuthorityComplete && state.Mode == directoryWriterEnforced && (!opts.maintenance || state.CoverageProtocol != coverageProtocolLegacy) {
		return out, directoryUnavailable("enforced User authority coverage is incomplete", nil)
	}

	if err := restoreSystemDirectoryBaseline(ctx, tx, dia); err != nil {
		return out, fmt.Errorf("restore system baseline after directory backfill: %w", err)
	}
	if err := inventory.commit(); err != nil {
		return out, fmt.Errorf("sqlstore: directory epoch reconcile inventory: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("sqlstore: directory epoch reconcile commit: %w", err)
	}
	out.coverageComplete = true
	return out, nil
}

// directoryTenantEnumerator is the narrow query surface shared by *sql.Tx and
// *sql.DB. It keeps the Postgres pool split visible at the call site above.
type directoryTenantEnumerator interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// directoryReconcileInventory is deliberately narrower than *sql.Tx. The two
// reconcilers may enumerate through it, keep its snapshot alive and commit that
// snapshot, but cannot use it for any data mutation.
type directoryReconcileInventory struct {
	queryer directoryTenantEnumerator
	adminTx *sql.Tx
}

// openDirectoryReconcileInventory pins all PostgreSQL facts consumed by an
// epoch backfill to one AdminDSN transaction. It is called only after appTx has
// acquired the global writer lock, so its repeatable-read snapshot cannot miss
// a cooperating CreateOrg/DropTenant commit. The role posture, database
// identity challenge and inventory query all use this exact transaction.
func openDirectoryReconcileInventory(
	ctx context.Context,
	appTx *sql.Tx,
	adminDB *sql.DB,
	dia dialect.Dialect,
	adminRoleForBoot guardRoleFact,
) (directoryReconcileInventory, error) {
	out := directoryReconcileInventory{queryer: appTx}
	if dia.Name() != store.EnginePostgres {
		return out, nil
	}

	adminTx, err := adminDB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return directoryReconcileInventory{}, fmt.Errorf(
			"%w: begin pinned AdminDSN epoch inventory snapshot: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	out.adminTx = adminTx
	posture, err := dia.ConnRolePosture(ctx, adminTx)
	if err != nil {
		out.close()
		return directoryReconcileInventory{}, fmt.Errorf(
			"%w: attest live AdminDSN epoch inventory posture: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	if err := requirePinnedDirectoryAdminPosture(posture, adminRoleForBoot); err != nil {
		out.close()
		return directoryReconcileInventory{}, err
	}
	if err := verifyDirectoryActivationDatabaseIdentity(
		ctx, appTx, directoryActivationWitnesses{admin: adminTx},
	); err != nil {
		out.close()
		return directoryReconcileInventory{}, fmt.Errorf(
			"%w: pinned AdminDSN epoch inventory identity: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	out.queryer = adminTx
	return out, nil
}

func (i *directoryReconcileInventory) close() {
	if i.adminTx != nil {
		_ = i.adminTx.Rollback()
		i.adminTx = nil
	}
}

// commit closes the authoritative inventory snapshot before the application
// transaction is allowed to commit. Any snapshot-commit failure therefore
// leaves the staged epoch writes to the caller's deferred rollback.
func (i *directoryReconcileInventory) commit() error {
	if i.adminTx == nil {
		return nil
	}
	err := i.adminTx.Commit()
	i.adminTx = nil
	if err != nil {
		return fmt.Errorf(
			"%w: commit pinned AdminDSN epoch inventory snapshot: %v",
			store.ErrEnumerationNotAuthoritative, err,
		)
	}
	return nil
}

func enumerateDirectoryTenants(
	ctx context.Context,
	q directoryTenantEnumerator,
	dia dialect.Dialect,
) ([]model.TenantID, error) {
	query := "SELECT id, tenant_id FROM " + directoryWriterRelation(dia, orgDescriptor.Table) +
		" ORDER BY tenant_id, id"
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[model.TenantID]struct{})
	var tenants []model.TenantID
	for rows.Next() {
		var rawID, rawTenant string
		if err := rows.Scan(&rawID, &rawTenant); err != nil {
			return nil, err
		}
		tenant := model.TenantID(rawTenant)
		if tenant.IsSystem() {
			if rawID != tenant.String() {
				return nil, fmt.Errorf("system organization id %q does not equal tenant id", rawID)
			}
			continue
		}
		epoch := model.DirectoryEpoch{BaseFields: model.BaseFields{
			ID: model.ID(rawID), TenantID: tenant, Version: 1,
		}}
		if err := epoch.Validate(); err != nil {
			return nil, fmt.Errorf("organization %q is not a canonical directory tenant: %w", rawID, err)
		}
		if _, duplicate := seen[tenant]; duplicate {
			return nil, fmt.Errorf("directory tenant %s appears more than once", tenant)
		}
		seen[tenant] = struct{}{}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].String() < tenants[j].String() })
	return tenants, nil
}

func acquireDirectoryWriter(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (directoryWriterControlState, error) {
	switch dia.Name() {
	case store.EnginePostgres:
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			directoryWriterLockKey,
		); err != nil {
			return directoryWriterControlState{}, fmt.Errorf("take directory writer lock: %w", err)
		}
	case store.EngineSQLite:
		if err := reserveSQLiteDirectoryWriter(ctx, tx, dia); err != nil {
			return directoryWriterControlState{}, err
		}
	default:
		return directoryWriterControlState{}, fmt.Errorf("unsupported engine %q", dia.Name())
	}
	state, err := readDirectoryWriterControlState(ctx, tx, dia)
	if err != nil {
		return directoryWriterControlState{}, err
	}
	if err := verifyDirectoryWriterPresentationBaseline(ctx, tx, dia); err != nil {
		return directoryWriterControlState{}, err
	}
	return state, nil
}

// reserveSQLiteDirectoryWriter performs the first statement of any privileged
// SQLite transaction before it establishes a WAL read snapshot. A transaction
// that SELECTs first cannot upgrade after a concurrent writer commits and gets
// SQLITE_BUSY_SNAPSHOT even with busy_timeout; the qualified zero-row DELETE
// reserves the sole writer without changing or healing the marker. Its later
// baseline read still detects any pre-existing marker contamination.
func reserveSQLiteDirectoryWriter(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) error {
	if dia.Name() != store.EngineSQLite {
		return nil
	}
	// #nosec G202 -- the relation comes from dialect.DirectoryWriterMarkerTable and is quoted; the WHERE is the fixed `0` and there are NO runtime values at all
	query := "DELETE FROM " +
		directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable) + " WHERE 0"
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("take SQLite directory writer reservation: %w", err)
	}
	return nil
}

// readLegacyDirectoryWriterControlState reads the frozen three-column predecessor
// from the application transaction. It intentionally does not call the full
// per-boot verifier: that verifier creates a PostgreSQL shape probe and belongs
// to the owner/migration authority, while split-owner app has SELECT only.
func readLegacyDirectoryWriterControlState(
	ctx context.Context,
	q directoryTenantEnumerator,
	dia dialect.Dialect,
) (directoryWriterControlState, error) {
	var out directoryWriterControlState
	query := "SELECT control_key, mode, expected_generation FROM " +
		directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	if dia.Name() == store.EngineSQLite {
		query = "SELECT control_key, mode, expected_generation, " +
			"typeof(control_key), typeof(mode), typeof(expected_generation) FROM " +
			directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return out, fmt.Errorf("read directory writer control: %w", err)
	}
	defer rows.Close()

	var count int
	var key, mode string
	var generation int64
	var keyType, modeType, generationType string
	for rows.Next() {
		count++
		if count > 1 {
			return out, fmt.Errorf("%w: singleton contains more than one row",
				errDirectoryWriterControlInvalid)
		}
		if dia.Name() == store.EngineSQLite {
			err = rows.Scan(
				&key, &mode, &generation, &keyType, &modeType, &generationType,
			)
		} else {
			err = rows.Scan(&key, &mode, &generation)
		}
		if err != nil {
			return out, fmt.Errorf("read directory writer control: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("read directory writer control: %w", err)
	}
	if count != 1 || key != directoryWriterLockKey || generation <= 0 ||
		(mode != string(directoryWriterStaged) && mode != string(directoryWriterEnforced)) {
		return out, fmt.Errorf(
			"%w: singleton is count=%d key=%q mode=%q expected_generation=%d",
			errDirectoryWriterControlInvalid, count, key, mode, generation,
		)
	}
	if dia.Name() == store.EngineSQLite &&
		(keyType != "text" || modeType != "text" || generationType != "integer") {
		return out, fmt.Errorf(
			"%w: SQLite singleton storage classes are %q/%q/%q, want text/text/integer",
			errDirectoryWriterControlInvalid, keyType, modeType, generationType,
		)
	}
	return directoryWriterControlState{
		Mode: directoryWriterMode(mode), ExpectedGeneration: generation,
	}, nil
}

func bindDirectoryTenant(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) error {
	if dia.Name() == store.EngineSQLite {
		if _, err := tx.ExecContext(ctx,
			// #nosec G202 -- the table name is dialect.DirectoryWriterMarkerTable, a compile-time constant, run through quoteIdent; nothing here comes from a caller
			"DELETE FROM "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable),
		); err != nil {
			return fmt.Errorf("clear directory writer marker before tenant bind: %w", err)
		}
		if err := clearDirectoryTenant(ctx, tx, dia); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			// #nosec G202 -- the relation is dialect.ScopeTenantTable (compile-time constant) via quoteIdent; the only value, tenant, travels as a ? placeholder below
			"INSERT INTO "+directoryWriterRelation(dia, dialect.ScopeTenantTable)+
				"(tenant_id) VALUES (?)",
			tenant.String(),
		); err != nil {
			return fmt.Errorf("bind qualified SQLite tenant: %w", err)
		}
		return nil
	}
	return dia.BindTenant(ctx, tx, tenant)
}

func clearDirectoryTenant(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	if dia.Name() == store.EngineSQLite {
		if _, err := tx.ExecContext(ctx,
			// #nosec G202 -- same closed relation as the INSERT above: dialect.ScopeTenantTable through quoteIdent, no value interpolated at all
			"DELETE FROM "+directoryWriterRelation(dia, dialect.ScopeTenantTable),
		); err != nil {
			return fmt.Errorf("clear qualified SQLite tenant: %w", err)
		}
		return nil
	}
	return dia.ClearTenant(ctx, tx)
}

func armDirectoryWriter(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, state directoryWriterControlState) error {
	return armDirectoryWriterCoverage(ctx, tx, dia, state.ExpectedGeneration, false)
}

// The legacy presentation is private to the maintenance cutover. A normal writer
// always presents the compiled target, including against an enforced predecessor.
func armLegacyDirectoryActivation(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, state directoryWriterControlState) error {
	if state.CoverageProtocol != coverageProtocolLegacy || state.ExpectedGeneration < 1 {
		return directoryUnavailable("activation legacy prestate is not exact", nil)
	}
	return armDirectoryWriterCoverage(ctx, tx, dia, state.ExpectedGeneration, true)
}

func armDirectoryWriterCoverage(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, generation int64, legacy bool) error {
	protocol := coverageProtocolTarget
	if legacy {
		protocol = coverageProtocolLegacy
	}
	switch dia.Name() {
	case store.EnginePostgres:
		for _, setting := range [][2]string{{dialect.DirectoryWriterGenerationGUC, fmt.Sprint(generation)}, {directoryCoverageProtocolGUC, protocol}} {
			var got string
			if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.set_config($1, $2, true)", setting[0], setting[1]).Scan(&got); err != nil {
				return err
			}
			if got != setting[1] {
				return directoryUnavailable("writer presentation differs from compiled protocol", nil)
			}
		}
		return nil
	case store.EngineSQLite:
		// #nosec G202 -- SQLite branch: the only interpolated text is directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable), a dialect constant; the DELETE carries no values.
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable)); err != nil {
			return err
		}
		// #nosec G202 -- same dialect-constant relation; the control key, generation and coverage protocol travel as the three bound ? arguments.
		_, err := tx.ExecContext(ctx, "INSERT INTO "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable)+"(control_key,generation,coverage_protocol) VALUES (?,?,?)", directoryWriterLockKey, generation, protocol)
		return err
	default:
		return fmt.Errorf("arm directory writer: unsupported engine %q", dia.Name())
	}
}

func finishDirectoryWriter(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	if dia.Name() == store.EnginePostgres {
		var protocol string
		if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.set_config($1, '', true)", directoryCoverageProtocolGUC).Scan(&protocol); err != nil {
			return err
		}
		if protocol != "" {
			return directoryUnavailable("clear writer coverage protocol", nil)
		}
	}
	switch dia.Name() {
	case store.EnginePostgres:
		var got string
		if err := tx.QueryRowContext(ctx,
			"SELECT pg_catalog.set_config($1, '', true)",
			dialect.DirectoryWriterGenerationGUC,
		).Scan(&got); err != nil {
			return fmt.Errorf("clear PostgreSQL directory writer generation: %w", err)
		}
		if got != "" {
			return fmt.Errorf("clear PostgreSQL directory writer generation returned %q", got)
		}
	case store.EngineSQLite:
		if _, err := tx.ExecContext(ctx,
			// #nosec G202 -- dialect.DirectoryWriterMarkerTable through quoteIdent, in the SQLite branch where the schema prefix is the literal main.
			"DELETE FROM "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable),
		); err != nil {
			return fmt.Errorf("clear SQLite directory writer marker: %w", err)
		}
	default:
		return fmt.Errorf("finish directory writer: unsupported engine %q", dia.Name())
	}
	return verifyDirectoryWriterPresentationBaseline(ctx, tx, dia)
}

// restoreSystemDirectoryBaseline owns the final presentation of a privileged
// transaction: it persists the reserved SYSTEM pin and removes any generation
// proof left by its last tenant-local operation. System and the boot reconciler
// call this at their transaction boundary rather than relying on each callback
// or loop iteration to remember which tenant it bound last.
func restoreSystemDirectoryBaseline(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) error {
	if err := bindDirectoryTenant(ctx, tx, dia, model.SystemTenantID); err != nil {
		return err
	}
	return finishDirectoryWriter(ctx, tx, dia)
}

// restoreDirectorySystemBaseline publishes the normal SQLite presentation
// after privileged module migrations have completed and before the application
// backfill transaction starts. PostgreSQL pins are transaction-local, so there
// is no durable baseline to publish there.
func restoreDirectorySystemBaseline(
	ctx context.Context,
	db dialect.Execer,
	dia dialect.Dialect,
) error {
	if dia.Name() != store.EngineSQLite {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin directory SYSTEM baseline restore: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := restoreSystemDirectoryBaseline(ctx, tx, dia); err != nil {
		return fmt.Errorf("restore directory SYSTEM baseline: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit directory SYSTEM baseline: %w", err)
	}
	return nil
}

func verifyDirectoryWriterPresentationBaseline(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) error {
	if dia.Name() == store.EnginePostgres {
		var protocol string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(pg_catalog.current_setting($1, true), '')", directoryCoverageProtocolGUC).Scan(&protocol); err != nil {
			return err
		}
		if protocol != "" {
			return directoryUnavailable("writer coverage protocol baseline is not empty", nil)
		}
	}
	switch dia.Name() {
	case store.EnginePostgres:
		var generation string
		if err := tx.QueryRowContext(ctx,
			"SELECT COALESCE(pg_catalog.current_setting($1, true), '')",
			dialect.DirectoryWriterGenerationGUC,
		).Scan(&generation); err != nil {
			return fmt.Errorf("read PostgreSQL directory writer generation baseline: %w", err)
		}
		if generation != "" {
			return fmt.Errorf(
				"%w: PostgreSQL directory writer generation baseline is %q, want empty",
				errDirectoryWriterControlInvalid, generation,
			)
		}
	case store.EngineSQLite:
		var count int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable),
		).Scan(&count); err != nil {
			return fmt.Errorf("read SQLite directory writer marker baseline: %w", err)
		}
		if count != 0 {
			return fmt.Errorf("%w: SQLite marker baseline contains %d row(s), want empty",
				errDirectoryWriterControlInvalid, count)
		}
	default:
		return fmt.Errorf("verify directory writer baseline: unsupported engine %q", dia.Name())
	}
	return nil
}

func insertDirectoryEpochRow(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) error {
	if directoryEpochBeforeInsertTestHook != nil {
		if err := directoryEpochBeforeInsertTestHook(tenant); err != nil {
			return err
		}
	}
	now, err := directoryTransactionNow(ctx, tx, dia)
	if err != nil {
		return err
	}
	epoch := model.DirectoryEpoch{BaseFields: model.BaseFields{
		ID: model.ID(tenant), TenantID: tenant,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}}
	rec, err := directoryEpochCodec.Encode(epoch)
	if err != nil {
		return err
	}
	baseToRecord(rec, epoch.BaseFields, false)
	cols := directoryEpochDescriptor.AllColumns()
	args := make([]any, len(cols))
	for i, col := range cols {
		args[i] = rec[col]
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		directoryWriterRelation(dia, directoryEpochDescriptor.Table),
		strings.Join(cols, ", "), placeholders(len(cols)))
	result, err := tx.ExecContext(ctx, dia.Rebind(query), args...)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("inserted %d directory epoch rows, want exactly one", rows)
	}
	return nil
}

// directoryTransactionNow observes the database clock through the exact
// transaction that persists the epoch. An injected application clock may be
// deliberately skewed and cannot authorize a K3 directory fact.
func directoryTransactionNow(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (model.Timestamp, error) {
	switch dia.Name() {
	case store.EnginePostgres:
		var now time.Time
		if err := tx.QueryRowContext(ctx,
			"SELECT pg_catalog.clock_timestamp()",
		).Scan(&now); err != nil {
			return model.Timestamp{}, fmt.Errorf("read PostgreSQL directory clock: %w", err)
		}
		return model.NewTimestamp(now), nil
	case store.EngineSQLite:
		var canonical string
		if err := tx.QueryRowContext(ctx,
			`SELECT strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')`,
		).Scan(&canonical); err != nil {
			return model.Timestamp{}, fmt.Errorf("read SQLite directory clock: %w", err)
		}
		now, err := model.ParseTimestamp(canonical)
		if err != nil {
			return model.Timestamp{}, fmt.Errorf(
				"parse SQLite directory clock %q: %w", canonical, err,
			)
		}
		return now, nil
	default:
		return model.Timestamp{}, fmt.Errorf("directory clock: unsupported engine %q", dia.Name())
	}
}

func readDirectoryEpochRow(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) (model.DirectoryEpoch, bool, error) {
	cols := directoryEpochDescriptor.AllColumns()
	query := dia.Rebind("SELECT " + strings.Join(cols, ", ") + " FROM " +
		directoryWriterRelation(dia, directoryEpochDescriptor.Table) +
		" WHERE tenant_id = ? LIMIT 2")
	rows, err := tx.QueryContext(ctx, query, tenant.String())
	if err != nil {
		return model.DirectoryEpoch{}, false, err
	}
	defer rows.Close()

	var records []model.Record
	for rows.Next() {
		state, err := newScanState(directoryEpochDescriptor, cols)
		if err != nil {
			return model.DirectoryEpoch{}, false, err
		}
		if err := rows.Scan(state.dests...); err != nil {
			return model.DirectoryEpoch{}, false, err
		}
		records = append(records, state.record())
	}
	if err := rows.Err(); err != nil {
		return model.DirectoryEpoch{}, false, err
	}
	switch len(records) {
	case 0:
		return model.DirectoryEpoch{}, false, nil
	case 1:
		base, err := baseFromRecord(records[0])
		if err != nil {
			return model.DirectoryEpoch{}, false, err
		}
		out, err := directoryEpochCodec.Decode(base, records[0])
		if err != nil {
			return model.DirectoryEpoch{}, false, err
		}
		if out.TenantID != tenant {
			return model.DirectoryEpoch{}, false,
				fmt.Errorf("directory epoch tenant %s does not match scope %s", out.TenantID, tenant)
		}
		return out, true, nil
	default:
		return model.DirectoryEpoch{}, false,
			fmt.Errorf("more than one directory epoch row for tenant %s", tenant)
	}
}

func deleteDirectoryEpochExact(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) error {
	query := dia.Rebind("DELETE FROM " +
		directoryWriterRelation(dia, directoryEpochDescriptor.Table) +
		" WHERE id = ? AND tenant_id = ?")
	result, err := tx.ExecContext(ctx, query, tenant.String(), tenant.String())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("delete directory epoch for tenant %s affected %d rows, want exactly one",
			tenant, rows)
	}
	return nil
}

func directoryStatusFromBoot(
	engine store.Engine,
	hardened bool,
	result directoryEpochBootResult,
) store.DirectoryStatus {
	posture := store.DirectoryWriterSingleRoleCapability
	switch {
	case engine == store.EngineSQLite:
		posture = store.DirectoryWriterSQLiteCapability
	case hardened:
		posture = store.DirectoryWriterSplitOwner
	}
	return store.DirectoryStatus{
		Enabled:                       false,
		EpochCoverageComplete:         result.coverageComplete,
		ControlMode:                   store.DirectoryControlMode(result.control.Mode),
		WriterPosture:                 posture,
		ExpectedGeneration:            result.control.ExpectedGeneration,
		CoverageProtocol:              result.control.CoverageProtocol,
		UserAuthorityCoverageComplete: result.userAuthorityComplete,
		InventoryAuthority:            result.inventoryAuthority,
		InventoryOrgCount:             len(result.inventory.BusinessTenants) + boolCount(result.inventory.System.ID != ""),
		InventoryBusinessOrgCount:     len(result.inventory.BusinessTenants),
		InventoryEpochCount:           len(result.inventory.Epochs),
		InventoryUnavailableReason:    result.inventoryUnavailableReason,
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DirectoryStatus implements store.DirectoryStatuser. The witness is immutable
// for this process. Maintenance returns separate durable evidence; every serving
// process must reopen to recompute coverage after activation.
func (s *sqlStore) DirectoryStatus(context.Context) (store.DirectoryStatus, bool, error) {
	return s.directoryStatus, true, nil
}

var _ store.DirectoryStatuser = (*sqlStore)(nil)

// Keep the sentinel relationship explicit for callers that classify malformed
// evidence without depending on this package's private errors.
func directoryEpochUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrDirectoryUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %v", store.ErrDirectoryUnavailable, err)
}
