// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Keep the historical admission used by core v9 immutable. New identities get
// their own pre-DDL check, and join the current inventory for ownership checks.
func versionedBootObjects(dia dialect.Dialect, reg *registry) (*managedObjectSet, error) {
	set := newManagedObjectSet(dia.Name())
	if err := set.addStatements("OS account reservations", osAccountReservationStmts()); err != nil {
		return nil, err
	}
	for _, name := range []string{
		"schema_migrations_rollout", "schema_migrations_rollout_guards",
		"schema_migrations_directory_guards", "schema_migrations_lineage_guards",
		"schema_migrations_appendonly_scope", "schema_migrations_leader",
		"schema_migrations_os_account_guards",
		"schema_migrations_audit_blind_guards",
	} {
		set.add(managedObject{class: managedClassRelation, name: name, origin: "versioned boot schema"})
	}
	for _, desc := range reg.moduleDescriptors() {
		set.add(managedObject{class: managedClassRelation, name: "schema_migrations_tbl_" + desc.Table, origin: "module descriptor migration tracker"})
	}
	return set, nil
}

func buildCurrentManagedObjectSet(dia dialect.Dialect, descs []model.EntityDescriptor, reg *registry, plans []moduleFileMigrationPlan) (*managedObjectSet, error) {
	set, err := buildManagedObjectSet(dia, descs, reg, plans)
	if err != nil {
		return nil, err
	}
	extra, err := versionedBootObjects(dia, reg)
	if err != nil {
		return nil, err
	}
	for _, bucket := range extra.byClass {
		for _, object := range bucket {
			set.add(object)
		}
	}
	return set, nil
}

func preflightVersionedBootSchema(ctx context.Context, db dialect.Execer, dia dialect.Dialect, reg *registry) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read-only admission
	tracked, err := coreTrackingRelationExists(ctx, tx, dia)
	if err != nil {
		return err
	}
	if tracked {
		versions, err := readCanonicalCoreTrackingVersions(ctx, tx, dia)
		if err != nil {
			return err
		}
		if len(versions) != 0 {
			return nil
		}
	}
	set, err := versionedBootObjects(dia, reg)
	if err != nil {
		return err
	}
	present, err := censusManagedNamespace(ctx, tx, dia, set)
	if err != nil {
		return err
	}
	for _, found := range present {
		if found.object.name != "schema_migrations_rollout" {
			return fmt.Errorf("%w: fresh bootstrap collides with %s", ErrGuardManifestNoEdge, found.object)
		}
	}
	return verifyVersionedRolloutCheckpoint(ctx, tx, dia, reg.rolloutControls())
}

// Classification and its tracker commit together before core v1. Accept that
// resumable checkpoint only when both the schema and the initial rows are exact.
func verifyVersionedRolloutCheckpoint(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, controls []store.RolloutControl) error {
	const table = "schema_migrations_rollout"
	set := newManagedObjectSet(dia.Name())
	set.add(managedObject{class: managedClassRelation, name: table})
	present, err := censusManagedNamespace(ctx, tx, dia, set)
	if err != nil || len(present) == 0 {
		return err
	}
	ddl := migrate.TrackingTableDDL(dia, table)
	if dia.Name() == store.EnginePostgres {
		if err := verifyPostgresRolloutShape(ctx, tx, table, ddl); err != nil {
			return err
		}
	} else {
		var stored string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM main.sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&stored); err != nil {
			return err
		}
		if normalizeSQLiteTableDDL(stored, table) != freshTrackerContractT5 {
			return fmt.Errorf("%w: invalid rollout tracker shape", ErrGuardManifestNoEdge)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, name, applied_at, phase, reverted_at FROM "+table)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var version int
		var name, applied, phase string
		var reverted sql.NullString
		if err := rows.Scan(&version, &name, &applied, &phase, &reverted); err != nil {
			rows.Close()
			return err
		}
		if version != 1 || name != "rollout_tables" || applied == "" || phase != "expand" || reverted.Valid {
			rows.Close()
			return fmt.Errorf("%w: invalid rollout migration checkpoint", ErrGuardManifestNoEdge)
		}
		count++
	}
	if err := closeRows(rows, "rollout migration checkpoint"); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: invalid rollout migration checkpoint row count", ErrGuardManifestNoEdge)
	}
	presentRollout, err := verifyFreshRolloutPreState(ctx, tx, dia, controls)
	if err != nil {
		return err
	}
	if !presentRollout {
		return fmt.Errorf("%w: rollout migration tracker without the complete rollout schema", ErrGuardManifestNoEdge)
	}
	return nil
}
