// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// A workspace's place in the organization tree (core v25 parent_id and path) is
// lineage: the resolver walks it to nest a department under its parent. Core v8
// installed the workspace guard over slug alone, so the guard has two editions:
//
//   - edition 1, installed with core v8, projects slug;
//   - edition 2, installed by version 2 of the lineage guard tracker, projects slug,
//     parent_id and path.
//
// The tracker names the edition a database is at. Version 2 runs after core v25
// has added the columns, so a guard never names a column the relation lacks.
const (
	lineageGuardsTracker             = "schema_migrations_lineage_guards"
	lineageWorkspaceTreeGuardVersion = 2
	lineageWorkspaceTreeGuardName    = "workspace_tree_guards"
	lineageWorkspaceRelation         = "workspaces"
)

// lineageRelationsEdition1 names the frozen core v8 catalog.
func lineageRelationsEdition1() []lineageRelation {
	return lineageRelations
}

// lineageRelationsEdition2 adds the workspace tree position without changing
// the catalog consumed by historical migration constructors and generators.
func lineageRelationsEdition2() []lineageRelation {
	return []lineageRelation{
		{"core.session", "sessions", []string{"workspace_id", "agent_id", "model_id", "deleted_at"}},
		{"core.agent", "agents", []string{"workspace_id", "deleted_at"}},
		{"core.resource", "resources", []string{"workspace_id", "path", "kind", "sensitivity"}},
		{"core.workspace", "workspaces", []string{"slug", "parent_id", "path"}},
		{"core.agent_group", "agent_groups", []string{"workspace_id", "slug"}},
		{"core.agent_group_member", "agent_group_members", []string{"agent_id", "group_id"}},
	}
}

func lineageWorkspaceTreeGuardObjects(dia dialect.Dialect) []lineageSQLObject {
	return lineageGuardObjectsFor(dia, lineageRelationsEdition2())
}

// lineageWorkspaceTreeGuardsRecorded reports whether the tracker records version 2.
// A missing tracker (a database that never reached guard installation) is edition 1;
// a version 2 row that is not the canonical record is refused.
func lineageWorkspaceTreeGuardsRecorded(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (bool, error) {
	columns, err := dia.TableColumns(ctx, tx, lineageGuardsTracker)
	if err != nil || len(columns) == 0 {
		return false, err
	}
	var name, phase string
	var reverted sql.NullString
	err = tx.QueryRowContext(ctx, dia.Rebind("SELECT name, phase, reverted_at FROM "+directoryWriterRelation(dia, lineageGuardsTracker)+" WHERE version = ?"),
		lineageWorkspaceTreeGuardVersion).Scan(&name, &phase, &reverted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	case name != lineageWorkspaceTreeGuardName || phase != migrate.Expand.String() || reverted.Valid:
		return false, lineageUnavailable("noncanonical lineage guard tracking record", nil)
	}
	return true, nil
}

// lineageGuardObjectsRecorded is the guard edition the tracker names: what a boot
// must find installed.
func lineageGuardObjectsRecorded(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) ([]lineageSQLObject, error) {
	recorded, err := lineageWorkspaceTreeGuardsRecorded(ctx, tx, dia)
	if err != nil {
		return nil, err
	}
	if recorded {
		return lineageWorkspaceTreeGuardObjects(dia), nil
	}
	return lineageGuardObjects(dia), nil
}

// reconcileLineageWorkspaceTreeGuards moves the workspace guard from edition 1 to
// edition 2 once. It refuses a database whose edition 1 guard is absent or altered,
// as the first installation does, and verifies the whole inventory afterwards.
func reconcileLineageWorkspaceTreeGuards(ctx context.Context, db dialect.Execer, dia dialect.Dialect, hardened bool, roles guardRoles) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	recorded, err := lineageWorkspaceTreeGuardsRecorded(ctx, tx, dia)
	if err != nil {
		return err
	}
	if recorded {
		return nil // reconcileLineageGuardsForWorkspaceTree verified this edition earlier in the same boot
	}
	if err := migrate.ApplyTx(ctx, tx, dia, lineageGuardsTracker, []migrate.Migration{{
		Version: lineageWorkspaceTreeGuardVersion, Name: lineageWorkspaceTreeGuardName,
		Exec: func(ctx context.Context, tx *sql.Tx) error { return replaceLineageWorkspaceGuards(ctx, tx, dia) },
	}}); err != nil {
		return err
	}
	for _, object := range lineageWorkspaceTreeGuardObjects(dia) {
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil || !present {
			return lineageUnavailable("missing or altered guard "+object.name, err)
		}
	}
	if err := verifyLineageSourcesFor(ctx, tx, dia, lineageWorkspaceTreeGuardObjects(dia)); err != nil {
		return err
	}
	if dia.Name() == store.EnginePostgres {
		if err := reconcileLineageACL(ctx, tx, dia, hardened, roles); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// replaceLineageWorkspaceGuards swaps the workspace guard objects. SQLite triggers
// are dropped and recreated; the PostgreSQL trigger is unchanged and its function is
// replaced in place, which keeps the trigger enabled ALWAYS and the function's owner.
func replaceLineageWorkspaceGuards(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	workspaceObjects := func(objects []lineageSQLObject) (out []lineageSQLObject) {
		for _, object := range objects {
			if object.table == lineageWorkspaceRelation {
				out = append(out, object)
			}
		}
		return out
	}
	for _, object := range workspaceObjects(lineageGuardObjects(dia)) {
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil || !present {
			return lineageUnavailable("missing or altered edition 1 guard "+object.name, err)
		}
		if dia.Name() == store.EngineSQLite {
			if _, err := tx.ExecContext(ctx, "DROP TRIGGER main."+object.name); err != nil {
				return lineageUnavailable("drop "+object.name, err)
			}
		}
	}
	for _, object := range workspaceObjects(lineageWorkspaceTreeGuardObjects(dia)) {
		statement := object.statement
		if dia.Name() == store.EnginePostgres {
			if object.body == "" {
				continue // the trigger itself is unchanged
			}
			statement = strings.Replace(statement, "CREATE FUNCTION ", "CREATE OR REPLACE FUNCTION ", 1)
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return lineageUnavailable("install "+object.name, err)
		}
		if present, err := verifyLineageGuard(ctx, tx, dia, object); err != nil || !present {
			return lineageUnavailable("new guard did not verify "+object.name, err)
		}
	}
	return nil
}

// reconcileLineageGuardsForWorkspaceTree keeps the v1 installation and its
// migration bindings intact. Once v2 is recorded, only its inventory is admitted.
func reconcileLineageGuardsForWorkspaceTree(ctx context.Context, db dialect.Execer, dia dialect.Dialect, hardened bool, roles guardRoles) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	recorded, err := lineageWorkspaceTreeGuardsRecorded(ctx, tx, dia)
	if err != nil {
		return err
	}
	if !recorded {
		if err := tx.Rollback(); err != nil {
			return err
		}
		return reconcileLineageGuards(ctx, db, dia, hardened, roles)
	}
	var installed int
	// #nosec G202 -- directoryWriterRelation quotes the compiled tracker name; version 1 is the historical installation version.
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+directoryWriterRelation(dia, lineageGuardsTracker)+" WHERE version = 1").Scan(&installed); err != nil {
		return err
	}
	if installed != 1 {
		return lineageUnavailable("edition 1 installation record is missing", nil)
	}
	if err := verifyLineageRelations(ctx, tx, dia); err != nil {
		return err
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, "SELECT guards_ready FROM "+directoryWriterRelation(dia, lineageControlTable)+" WHERE singleton = 1").Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return lineageUnavailable("edition 2 guards are not ready", nil)
	}
	objects := lineageWorkspaceTreeGuardObjects(dia)
	for _, object := range objects {
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil || !present {
			return lineageUnavailable("missing or altered guard "+object.name, err)
		}
	}
	if err := verifyLineageSourcesFor(ctx, tx, dia, objects); err != nil {
		return err
	}
	if dia.Name() == store.EnginePostgres {
		if err := reconcileLineageACL(ctx, tx, dia, hardened, roles); err != nil {
			return err
		}
	}
	return tx.Commit()
}
