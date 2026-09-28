// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openRevisionStore opens a store with this module's schema on the engine; the
// PostgreSQL leg skips without a configured server.
func openRevisionStore(t *testing.T, eng store.Engine) (store.Store, model.TenantID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg := store.Config{Engine: eng, DSN: ":memory:", Debug: true}
	if eng == store.EnginePostgres {
		dsns := enginetest.IsolatedPostgresSplitOwner(t)
		cfg = store.Config{Engine: eng, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, Debug: true, MaxConns: 8}
	}
	st, err := engine.Open(ctx, cfg, New().RegisterSchema)
	if err != nil {
		t.Fatalf("open %s: %v", eng, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Surfaces", Slug: "surfaces", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st, tenant
}

// TestAnUnregisteredSurfaceIsRefusedAtTheWriter: the revision store's only writer
// accepts exactly the surfaces its registry lists, and the surface checks of the
// two authoring consoles answer from the same registry.
func TestAnUnregisteredSurfaceIsRefusedAtTheWriter(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			ctx := context.Background()
			st, tenant := openRevisionStore(t, eng)
			write := func(surface, content string) error {
				return st.Mutate(ctx, tenant, func(sc store.Scope) error {
					_, _, err := appendRevision(ctx, sc, surface, content, "test", true, true, "")
					return err
				})
			}
			stored := func(surface string) int {
				var n int
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(revisionKind)
					if err != nil {
						return err
					}
					recs, err := listAll(ctx, repo, eq(colRevSurface, surface))
					n = len(recs)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return n
			}

			if err := write("future-surface", "permit(principal, action, resource);"); !errors.Is(err, model.ErrUnknownKind) {
				t.Errorf("appendRevision with an unlisted surface = %v, want the unknown-kind refusal", err)
			}
			if n := stored("future-surface"); n != 0 {
				t.Errorf("an unlisted surface was stored (%d rows)", n)
			}
			managedActivation := surfaceManagedSettings + activationSurfaceSuffix
			if err := write(managedActivation, "1"); !errors.Is(err, model.ErrUnknownKind) {
				t.Errorf("appendRevision with the activation stream of a surface that is not activatable = %v, want the unknown-kind refusal", err)
			}
			if n := stored(managedActivation); n != 0 {
				t.Errorf("a non-activatable activation stream was stored (%d rows)", n)
			}
			// Only the policy editor's engines are activated, so only theirs have an
			// activation stream; the managed projection and the adopted snapshot
			// select by their own active revision.
			var streams []string
			for _, kind := range surfaces.Kinds() {
				if strings.HasSuffix(kind, activationSurfaceSuffix) {
					streams = append(streams, kind)
				}
			}
			if want := []string{surfaceCedar + activationSurfaceSuffix, surfaceOPA + activationSurfaceSuffix}; !slices.Equal(streams, want) {
				t.Errorf("the registry derives the activation streams %v, want only the engines' %v", streams, want)
			}
			for _, surface := range []string{surfaceCedarManaged, surfaceCedarDDIL} {
				stream := surface + activationSurfaceSuffix
				if err := write(stream, "1"); !errors.Is(err, model.ErrUnknownKind) {
					t.Errorf("appendRevision with the activation stream of %s = %v, want the unknown-kind refusal", surface, err)
				}
				if n := stored(stream); n != 0 {
					t.Errorf("the activation stream of %s was stored (%d rows)", surface, n)
				}
			}
			for _, kind := range surfaces.Kinds() {
				content := "{}"
				if v, _ := surfaces.Variant(kind); v == activationContent {
					content = "1"
				}
				if err := write(kind, content); err != nil {
					t.Errorf("appendRevision with the listed surface %q = %v", kind, err)
				}
			}

			for _, e := range surfaces.entries {
				if got := validSurface(e.name); got != (e.family == surfaceFamilyManaged) {
					t.Errorf("validSurface(%q) = %t, want %t", e.name, got, e.family == surfaceFamilyManaged)
				}
				if _, got := normEngine(e.name); got != e.engine {
					t.Errorf("normEngine(%q) = %t, want %t", e.name, got, e.engine)
				}
			}
			if validSurface("future-surface") {
				t.Errorf("validSurface accepts an unlisted surface")
			}
			if _, ok := normEngine("future-surface"); ok {
				t.Errorf("normEngine accepts an unlisted surface")
			}
		})
	}
}
