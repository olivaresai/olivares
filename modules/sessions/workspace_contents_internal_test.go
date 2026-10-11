// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"maps"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A workspace nobody has used holds nothing a user made. The communication guard
// initializer still writes its two guard rows into every new workspace, so the
// contents read must leave the guard kinds out instead of listing a table name.
func TestFreshWorkspaceContentsListNoInternalKinds(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			descriptors := fixture.st.(store.CompositionCensus).CensusDescriptors()

			var guards int
			var contents map[model.Kind]store.KindCount
			if err := fixture.m.Data.View(ctx, fixture.tenant, func(sc store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, sc, fixture.workspace)
				if err != nil {
					return err
				}
				repo, err := confined.Ext(communicationGuardKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Limit: 10})
				if err != nil {
					return err
				}
				guards = len(rows)
				contents, err = store.ReadWorkspaceContents(ctx, sc, fixture.workspace, descriptors)
				return err
			}); err != nil {
				t.Fatalf("read fresh workspace: %v", err)
			}

			// The rows the report is about exist; the read must still not show them.
			if guards != 2 {
				t.Fatalf("fresh workspace guard rows = %d, want the initializer's 2", guards)
			}
			for kind, count := range contents {
				if count.Count != 0 {
					t.Errorf("fresh workspace contents list %s = %d, want nothing a user made", kind, count.Count)
				}
			}
			for _, kind := range []model.Kind{communicationGuardKind, workGuardKind, protocolReplayGuardKind} {
				if _, listed := contents[kind]; listed {
					t.Errorf("workspace contents list internal kind %s", kind)
				}
			}
		})
	}
}

// The guard kinds are the module's own counters, epochs and replay tokens.
// Dropping the flag from one would list its table name in every workspace
// that has used it; marking any other kind hides its rows from the contents
// read, so a new Internal kind must be reviewed and added here.
//
// ponytail: reads the core and sessions census only, upgrade when another
// module declares an Internal kind (pin it in that module's own test).
func TestOnlyGuardKindsAreInternal(t *testing.T) {
	t.Parallel()

	fixture := communicationOpenFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "internal-kinds.db"),
	})
	got := map[model.Kind]bool{}
	for _, d := range fixture.st.(store.CompositionCensus).CensusDescriptors() {
		if d.Internal {
			got[d.Kind] = true
		}
	}
	want := map[model.Kind]bool{communicationGuardKind: true, workGuardKind: true, protocolReplayGuardKind: true}
	if !maps.Equal(got, want) {
		t.Errorf("internal kinds in the census = %v, want exactly %v", got, want)
	}
}
