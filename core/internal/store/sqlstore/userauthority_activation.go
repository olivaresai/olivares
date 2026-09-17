// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"math"
	"reflect"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// OpenDirectoryWriterMaintenance returns only durable transition testimony.
// The prepared pools remain private, all common Open checks run, and every
// handle closes before return. The one deferred gate is legacy H coverage.
func OpenDirectoryWriterMaintenance(ctx context.Context, cfg store.Config, register func(store.ExtensionRegistry) error, expectedGeneration int64) (before, after store.DirectoryStatus, changed bool, retErr error) {
	_, err := openPrepared(ctx, cfg, register, prepareThroughReadiness, func(s *sqlStore) error {
		var err error
		before, after, changed, err = ActivateDirectoryWriter(ctx, s, cfg, expectedGeneration)
		return err
	},
		// No admission and no custody measurement: directory maintenance is an
		// independent command with no boot above it, so it takes its own ordinary root
		// lease and is subject to the same restore gate as any other caller. It does not
		// gain access to another operation's pending target by being a maintenance path,
		// and it never loads a signing key, so it has nothing to observe.
		publicationInputs{})
	return before, after, changed, err
}

var directoryActivationBeforeCASTestHook func(context.Context, *sql.Tx) error

func runDirectoryActivation(ctx context.Context, s *sqlStore, authority directoryActivationAuthority, expectedGeneration int64, mutate bool) (directoryActivationAttempt, error) {
	var out directoryActivationAttempt
	err := withMigrationLock(ctx, authority.ownerDB, s.dia, func(mdb dialect.Execer) error {
		tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(s.dia))
		if err != nil {
			return err
		}
		defer tx.Rollback()
		state, err := acquireDirectoryWriter(ctx, tx, s.dia)
		if err != nil {
			return err
		}
		out.state, out.prestate = state, state
		presentation, err := captureDirectoryActivationPresentation(ctx, tx, s.dia)
		if err != nil {
			return err
		}
		if err := lockDirectoryActivationSources(ctx, tx, s.dia); err != nil {
			return err
		}
		witnesses, err := openDirectoryActivationWitnesses(ctx, tx, s, authority)
		if err != nil {
			return err
		}
		defer witnesses.close()
		if err := verifyDirectoryActivationDatabaseIdentity(ctx, tx, witnesses); err != nil {
			return err
		}
		if err := verifyCoreDirectoryRelationsExact(ctx, tx, s.dia, coreDescriptors()); err != nil {
			return err
		}
		if err := verifyUserAuthorityRelation(ctx, tx, s.dia); err != nil {
			return err
		}
		if err := verifyDirectoryWriterGuardsExact(ctx, tx, witnesses.appAuthority(tx), s.dia, authority.hardened, authority.roles); err != nil {
			return err
		}
		if s.engine == store.EnginePostgres {
			hardened, err := resolveGuardMetadataPosture(ctx, tx, s.dia, authority.roles)
			if err != nil {
				return err
			}
			if hardened != authority.hardened {
				return directoryUnavailable("activation role posture changed", nil)
			}
			if err := verifyPostgresUserAuthorityLock(ctx, tx, authority.roles); err != nil {
				return err
			}
			present, err := verifyPostgresDirectoryInventory(ctx, witnesses.appAuthority(tx), authority.roles)
			if err != nil {
				return err
			}
			if authority.adminRole == "" && !present {
				return directoryUnavailable("activation has no complete inventory authority", nil)
			}
			if authority.adminRole != "" {
				if err := verifyPostgresDirectoryActivationAdminReadOnly(ctx, tx, authority.adminRole); err != nil {
					return err
				}
			}
		}
		queryer := directoryTenantEnumerator(tx)
		closedRoutine := s.engine == store.EnginePostgres && witnesses.admin == nil
		inventoryAuthority := "sqlite"
		if s.engine == store.EnginePostgres {
			inventoryAuthority = "admin_dsn"
			if closedRoutine {
				queryer = witnesses.appAuthority(tx)
				inventoryAuthority = "closed_routine"
			} else {
				queryer = witnesses.admin
			}
		}
		inventory, err := readDirectoryInventory(ctx, queryer, s.dia, closedRoutine, false)
		if err != nil {
			return err
		}
		if err := bindDirectoryTenant(ctx, tx, s.dia, model.SystemTenantID); err != nil {
			return err
		}
		users, err := readUserAuthorityCoverage(ctx, tx, s.dia)
		if err != nil {
			return err
		}
		out.inventory, out.beforeInventory = inventory, inventory
		out.users, out.beforeUsers = users, users
		out.before = directoryActivationCoverageStatus(s, state, inventory, users, inventoryAuthority)
		out.after = out.before
		target := directoryWriterControlState{Mode: directoryWriterEnforced, ExpectedGeneration: expectedGeneration + 1, CoverageProtocol: coverageProtocolTarget}
		if state.CoverageProtocol == coverageProtocolTarget {
			if state != target {
				return fmt.Errorf("%w: target generation does not match the exact retry", store.ErrConflict)
			}
			if len(users.Missing) != 0 {
				return directoryUnavailable("target retry has incomplete H coverage", nil)
			}
			if err := lockAndVerifyActivationEpochs(ctx, tx, s.dia, inventory); err != nil {
				return err
			}
			return restoreDirectoryActivationPresentation(ctx, tx, s.dia, presentation)
		}
		if state.CoverageProtocol != coverageProtocolLegacy || state.ExpectedGeneration != expectedGeneration || (state.Mode != directoryWriterStaged && state.Mode != directoryWriterEnforced) {
			return fmt.Errorf("%w: activation predecessor is not exact", store.ErrConflict)
		}
		if !mutate {
			if err := lockAndVerifyActivationEpochs(ctx, tx, s.dia, inventory); err != nil {
				return err
			}
			return restoreDirectoryActivationPresentation(ctx, tx, s.dia, presentation)
		}
		if err := backfillUserAuthorityCoverage(ctx, tx, s.dia, state, users); err != nil {
			return err
		}
		if err := lockAndVerifyActivationEpochs(ctx, tx, s.dia, inventory); err != nil {
			return err
		}
		postInventory := inventory
		postInventory.Epochs = maps.Clone(inventory.Epochs)
		for _, tenant := range inventory.BusinessTenants {
			if inventory.Epochs[tenant] == math.MaxInt64 {
				return directoryUnavailable("activation directory epoch exhausted", nil)
			}
			if err := bindDirectoryTenant(ctx, tx, s.dia, tenant); err != nil {
				return err
			}
			if err := armLegacyDirectoryActivation(ctx, tx, s.dia, state); err != nil {
				return err
			}
			if err := bumpDirectoryEpochExact(ctx, tx, s.dia, tenant); err != nil {
				return err
			}
			postInventory.Epochs[tenant]++
		}
		// The independently pinned snapshot cannot see this transaction's G
		// changes. Source table locks hold its estate stable; every new G is
		// therefore verified separately on the owner transaction below.
		stable, err := readDirectoryInventory(ctx, queryer, s.dia, closedRoutine, false)
		if err != nil {
			return err
		}
		wantStable := inventory
		if s.engine == store.EngineSQLite {
			wantStable = postInventory
		}
		if !reflect.DeepEqual(stable, wantStable) {
			return directoryUnavailable("activation directory inventory changed", nil)
		}
		if err := lockAndVerifyActivationEpochs(ctx, tx, s.dia, postInventory); err != nil {
			return err
		}
		if err := bindDirectoryTenant(ctx, tx, s.dia, model.SystemTenantID); err != nil {
			return err
		}
		postUsers, err := readUserAuthorityCoverage(ctx, tx, s.dia)
		if err != nil {
			return err
		}
		if len(postUsers.Missing) != 0 || !reflect.DeepEqual(postUsers.Users, users.Users) {
			return directoryUnavailable("activation H coverage incomplete or User inventory changed", nil)
		}
		for id, row := range users.Rows {
			if postUsers.Rows[id] != row {
				return directoryUnavailable("activation changed a retained H", nil)
			}
		}
		if len(postUsers.Rows) != len(users.Rows)+len(users.Missing) {
			return directoryUnavailable("activation H cardinality changed", nil)
		}
		for _, id := range users.Missing {
			if postUsers.Rows[id].Version != 1 {
				return directoryUnavailable("activation H backfill did not create version one", nil)
			}
		}
		if directoryActivationBeforeCASTestHook != nil {
			if err := directoryActivationBeforeCASTestHook(ctx, tx); err != nil {
				return err
			}
		}
		query := s.dia.Rebind("UPDATE " + directoryWriterRelation(s.dia, dialect.DirectoryWriterControlTable) + " SET mode=?,expected_generation=expected_generation+1,coverage_protocol=? WHERE control_key=? AND mode=? AND expected_generation=? AND coverage_protocol=? AND expected_generation<?")
		result, err := tx.ExecContext(ctx, query, string(directoryWriterEnforced), coverageProtocolTarget, directoryWriterLockKey, string(state.Mode), expectedGeneration, coverageProtocolLegacy, int64(math.MaxInt64))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: directory activation CAS did not affect one row: %v", store.ErrConflict, err)
		}
		// No source, H or G DML follows the exact cutover CAS.
		after, err := readDirectoryWriterControlState(ctx, tx, s.dia)
		if err != nil {
			return err
		}
		if after != target {
			return directoryUnavailable("activation target readback differs", nil)
		}
		if err := restoreDirectoryActivationPresentation(ctx, tx, s.dia, presentation); err != nil {
			return err
		}
		out.state, out.inventory, out.users, out.changed = after, postInventory, postUsers, true
		out.after = directoryActivationCoverageStatus(s, after, postInventory, postUsers, inventoryAuthority)
		if directoryActivationCommitTestHook != nil {
			if err := directoryActivationCommitTestHook(tx); err != nil {
				return &directoryActivationCommitError{cause: err}
			}
			return nil
		}
		if err := tx.Commit(); err != nil {
			return &directoryActivationCommitError{cause: err}
		}
		return nil
	})
	return out, err
}

func lockAndVerifyActivationEpochs(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, inventory BusinessDirectoryInventory) error {
	if err := inventory.requireComplete(); err != nil {
		return err
	}
	for _, tenant := range inventory.BusinessTenants {
		if err := bindDirectoryTenant(ctx, tx, dia, tenant); err != nil {
			return err
		}
		if dia.Name() == store.EnginePostgres {
			var version int64
			if err := tx.QueryRowContext(ctx, "SELECT version FROM public.core_directory_epoch WHERE id=$1 AND tenant_id=$1 FOR UPDATE", tenant.String()).Scan(&version); err != nil {
				return directoryUnavailable("lock activation G", err)
			}
			if version != inventory.Epochs[tenant] {
				return directoryUnavailable("activation G differs from inventory", nil)
			}
		}
		epoch, found, err := readDirectoryEpochRow(ctx, tx, dia, tenant)
		if err != nil || !found || epoch.Version != inventory.Epochs[tenant] {
			return directoryUnavailable("activation G shape/version unavailable", err)
		}
	}
	return nil
}

func directoryActivationCoverageStatus(s *sqlStore, state directoryWriterControlState, inventory BusinessDirectoryInventory, users userAuthorityCoverage, authority string) store.DirectoryStatus {
	return store.DirectoryStatus{Enabled: false, EpochCoverageComplete: true, ControlMode: store.DirectoryControlMode(state.Mode), WriterPosture: s.directoryStatus.WriterPosture,
		ExpectedGeneration: state.ExpectedGeneration, CoverageProtocol: state.CoverageProtocol, UserAuthorityCoverageComplete: len(users.Missing) == 0, InventoryAuthority: authority,
		InventoryOrgCount: len(inventory.BusinessTenants) + 1, InventoryBusinessOrgCount: len(inventory.BusinessTenants), InventoryEpochCount: len(inventory.Epochs)}
}

func directoryActivationTargetMatches(attempt, observed directoryActivationAttempt) bool {
	return observed.state.Mode == directoryWriterEnforced && observed.state.CoverageProtocol == coverageProtocolTarget &&
		observed.state.ExpectedGeneration == attempt.prestate.ExpectedGeneration+1 && len(observed.users.Missing) == 0 &&
		observed.inventory.requireComplete() == nil && reflect.DeepEqual(observed.inventory, attempt.inventory)
}

func directoryActivationPrestateMatches(attempt, observed directoryActivationAttempt) bool {
	return observed.state == attempt.prestate && reflect.DeepEqual(observed.inventory, attempt.beforeInventory) && reflect.DeepEqual(observed.users, attempt.beforeUsers)
}
