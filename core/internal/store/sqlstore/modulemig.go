// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// moduleFileMigrationPlan is one module's fully loaded and transition-hooked
// migration plan. Plans are assembled before schema work begins, so a declared
// transition that names no exact migration version refuses boot before any
// migration can write.
type moduleFileMigrationPlan struct {
	namespace     string
	trackingTable string
	migrations    []migrate.Migration
}

// prepareModuleFileMigrations loads and binds any migration filesystems a module
// registered (for secondary indexes, backfills and unregistered helper tables).
// Each module's filesystem is expected to hold a per-engine subdirectory
// ("sqlite/" or "postgres/") of "NNNN_name.sql" files; each file is one
// statement and one migration, applied in filename order under a per-module
// tracking table. Registered entity tables are NOT created here — the engine
// generates them from descriptors — so this path is additive only.
func prepareModuleFileMigrations(
	dia dialect.Dialect,
	engine store.Engine,
	reg *registry,
) ([]moduleFileMigrationPlan, error) {
	subdir := string(engine)
	plans := make([]moduleFileMigrationPlan, 0, len(reg.modMig))
	seenNamespaces := make(map[string]bool, len(reg.modMig))
	for _, mm := range reg.modMig {
		seenNamespaces[mm.namespace] = true
		migs, err := loadFileMigrations(mm.fsys, subdir)
		if err != nil {
			return nil, fmt.Errorf("module %q migrations: %w", mm.namespace, err)
		}
		table := "schema_migrations_mod_" + mm.namespace
		if err := attachSchemaTransitionHooks(dia, engine, mm.namespace, table, migs, reg); err != nil {
			return nil, fmt.Errorf("module %q migrations: %w", mm.namespace, err)
		}
		if len(migs) != 0 {
			plans = append(plans, moduleFileMigrationPlan{
				namespace: mm.namespace, trackingTable: table, migrations: migs,
			})
		}
	}
	for namespace, invariant := range reg.invariants {
		if seenNamespaces[namespace] || !hasSchemaTransitions(invariant.byEngine[engine]) {
			continue
		}
		return nil, fmt.Errorf(
			"module %q declares schema-trigger transitions for engine %q but registered no migration filesystem",
			namespace, engine,
		)
	}
	return plans, nil
}

// applyModuleFileMigrations applies a plan that was fully prepared before the
// migration phase. No filesystem read or transition attachment happens here.
func applyModuleFileMigrations(
	ctx context.Context,
	db dialect.Execer,
	dia dialect.Dialect,
	plans []moduleFileMigrationPlan,
) error {
	for _, plan := range plans {
		if err := migrate.Apply(ctx, db, dia, plan.trackingTable, plan.migrations); err != nil {
			return fmt.Errorf("module %q migrations: %w", plan.namespace, err)
		}
	}
	return nil
}

// loadFileMigrations reads "NNNN_name.sql" files from a subdirectory of fsys and
// turns each into a single-statement migration.
func loadFileMigrations(fsys fs.FS, subdir string) ([]migrate.Migration, error) {
	entries, err := fs.ReadDir(fsys, subdir)
	if err != nil {
		// A module may not ship migrations for this engine; that is not an error.
		return nil, nil //nolint:nilerr // absent per-engine dir means "nothing to apply"
	}
	var migs []migrate.Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := versionFromName(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(fsys, path.Join(subdir, e.Name()))
		if err != nil {
			return nil, err
		}
		stmt := strings.TrimSpace(string(body))
		if stmt == "" {
			continue
		}
		migs = append(migs, migrate.Migration{Version: version, Name: e.Name(), Stmts: []string{stmt}})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// versionFromName parses the leading digits of "NNNN_name.sql".
func versionFromName(name string) (int, error) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("migration file %q must start with a version number", name)
	}
	return strconv.Atoi(name[:i])
}
