// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B-03 — the SEAT of workspace confinement, tested where it lives.
//
// The HTTP regressions (modules/governance, modules/sourcescope) prove two real
// routes are confined. These tests pin the seam itself, because the seam has two
// properties no single route exercises:
//
//  1. a module also holds the BOOT-time ModuleData handle from UseData, and 48 of
//     the 610 module HTTP handlers reach the store through a method that uses it
//     instead of mc.Data. Both handles must honor the same request mark, or the
//     List class closes and the Get-by-id class stays open;
//  2. the mark is keyed by TENANT. A confinement recorded for one tenant must not
//     apply to a unit of work in another — carrying it over would confine (or fail
//     to confine) on the strength of an unrelated membership.

// Two module entities registered by the fixture: one that DECLARES workspace
// lineage and one that does not. They are what makes the Ext half of the seat
// testable at all — the core catalog has no lineage-less module entity to reach.
const (
	linedKind    model.Kind = "s575.lined"
	unlinedKind  model.Kind = "s575.unlined"
	colTestWS               = "workspace_id"
	colTestLabel            = "label"
)

func registerConfineTestEntities(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  linedKind,
		Table: "s575_lined",
		Fields: []model.FieldSpec{
			{Name: colTestLabel, Kind: model.KindText},
			{Name: colTestWS, Kind: model.KindText, Nullable: true, Indexed: true},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   colTestWS,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  unlinedKind,
		Table: "s575_unlined",
		Fields: []model.FieldSpec{
			{Name: colTestLabel, Kind: model.KindText},
		},
	})
}

// confineTestFixture opens an in-memory store with two workspaces and one agent in
// each, and returns the tenant, the two workspace ids and the store.
func confineTestFixture(t *testing.T) (store.Store, model.TenantID, model.ID, model.ID) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, registerConfineTestEntities)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "confineco", Slug: "confineco"})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var wsA, wsB model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, e := sc.Workspaces().Create(ctx, model.Workspace{Name: "WA", Slug: "wa", Status: model.StatusActive})
		if e != nil {
			return e
		}
		b, e := sc.Workspaces().Create(ctx, model.Workspace{Name: "WB", Slug: "wb", Status: model.StatusActive})
		if e != nil {
			return e
		}
		wsA, wsB = a.ID, b.ID
		if _, e := sc.Agents().Create(ctx, model.Agent{
			Name: "a-bot", Kind: "service", Status: model.StatusActive, WorkspaceID: wsA,
		}); e != nil {
			return e
		}
		_, e = sc.Agents().Create(ctx, model.Agent{
			Name: "b-bot", Kind: "service", Status: model.StatusActive, WorkspaceID: wsB,
		})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return st, tenant, wsA, wsB
}

// countAgents lists agents through whatever Scope it is given.
func countAgents(t *testing.T, ctx context.Context, sc store.Scope) int {
	t.Helper()
	agents, _, err := sc.Agents().List(ctx, model.Query{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	return len(agents)
}

func TestWorkspaceConfinementIsIdempotentAndCannotBeRetargeted(t *testing.T) {
	st, tenant, wsA, wsB := confineTestFixture(t)
	ctx := context.WithValue(context.Background(), ctxKeyModuleBoundary,
		moduleRequestBoundary{tenant: tenant, workspace: wsB})

	if err := NewModuleData(st).View(ctx, tenant, func(first store.Scope) error {
		// Both fixture workspaces are non-default. This is the exact shape a
		// request-marked ModuleData callback hands to a module before the module's
		// narrower service seam applies the same boundary again.
		second, err := store.ConfineWorkspace(ctx, first, wsB)
		if err != nil {
			t.Fatalf("repeat same workspace confinement: %v", err)
		}
		if second != first {
			t.Fatal("repeat confinement replaced the established capability wrapper")
		}
		if got := countAgents(t, ctx, second); got != 1 {
			t.Fatalf("agents after repeat confinement = %d, want one", got)
		}

		if got, err := store.ConfineWorkspace(ctx, first, wsA); got != nil ||
			!errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Fatalf("retargeted confinement = %#v, %v; want nil/ErrWorkspaceConfinement", got, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect idempotent confinement: %v", err)
	}
}

// The boot-time ModuleData handle honors the request mark. Without this, a
// handler that reaches the store through the module's own data field (rather than
// mc.Data) keeps a tenant-wide view inside an authorized, confined request.
func TestBootModuleDataHandleHonoursRequestConfinement(t *testing.T) {
	st, tenant, wsA, _ := confineTestFixture(t)
	md := NewModuleData(st)
	base := context.Background()

	// Unmarked: engine authority, both agents — this is the pumps' path, and it
	// must stay byte-identical.
	if err := md.View(base, tenant, func(sc store.Scope) error {
		if n := countAgents(t, base, sc); n != 2 {
			t.Errorf("unmarked boot handle must keep engine authority, saw %d agents, want 2", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("unmarked view: %v", err)
	}

	// Marked: the same handle, the same tenant, one agent.
	marked := context.WithValue(base, ctxKeyModuleBoundary,
		moduleRequestBoundary{tenant: tenant, workspace: wsA})
	if err := md.View(marked, tenant, func(sc store.Scope) error {
		if n := countAgents(t, marked, sc); n != 1 {
			t.Errorf("marked boot handle must be confined, saw %d agents, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("marked view: %v", err)
	}
}

// ScopedData — the mc.Data handle — confines on both View and Mutate. Mutate
// matters on its own: several governed READS run inside a committed transaction
// so the read can self-audit, so a Mutate that skipped the decorator would leave
// exactly those reads (the recon-relevant ones) unconfined.
func TestScopedDataConfinesBothViewAndMutate(t *testing.T) {
	st, tenant, wsA, _ := confineTestFixture(t)
	base := context.Background()
	marked := context.WithValue(base, ctxKeyModuleBoundary,
		moduleRequestBoundary{tenant: tenant, workspace: wsA})
	sd := NewScopedData(st, tenant)

	if err := sd.View(marked, func(sc store.Scope) error {
		if n := countAgents(t, marked, sc); n != 1 {
			t.Errorf("View: saw %d agents, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if err := sd.Mutate(marked, func(sc store.Scope) error {
		if n := countAgents(t, marked, sc); n != 1 {
			t.Errorf("Mutate: saw %d agents, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
}

// A confinement recorded for another tenant does not apply here.
func TestRequestConfinementIsKeyedByTenant(t *testing.T) {
	st, tenant, wsA, _ := confineTestFixture(t)
	base := context.Background()
	otherTenant := model.TenantID("00000000-0000-7000-8000-00000000ffff")
	if otherTenant == tenant {
		t.Fatal("fixture: the other tenant id must differ")
	}
	// A mark that belongs to a DIFFERENT tenant's unit of work.
	foreign := context.WithValue(base, ctxKeyModuleBoundary,
		moduleRequestBoundary{tenant: otherTenant, workspace: wsA})

	if _, ok := moduleBoundaryFrom(foreign, tenant); ok {
		t.Fatal("a boundary recorded for another tenant must not apply")
	}
	if err := NewScopedData(st, tenant).View(foreign, func(sc store.Scope) error {
		if n := countAgents(t, foreign, sc); n != 2 {
			t.Errorf("a foreign-tenant mark must not confine, saw %d agents, want 2", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// An entity with NO declared workspace lineage is refused to a confined caller,
// and refused as a typed error — not served tenant-wide, and not served as an
// empty page. An empty page would assert the collection was inspected in full.
func TestConfinedScopeRefusesEntitiesWithoutLineage(t *testing.T) {
	st, tenant, wsA, _ := confineTestFixture(t)
	ctx := context.Background()

	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, wsA)
		if err != nil {
			return err
		}
		// Providers carry no workspace axis at all.
		if _, _, err := sc.Providers().List(ctx, model.Query{}); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("providers list = %v, want ErrWorkspaceLineageRequired", err)
		}
		// The tenant-wide audit chain is readable only in the append direction.
		if _, _, err := sc.Audit().Head(ctx); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("audit head = %v, want ErrWorkspaceLineageRequired", err)
		}
		// The workspace list is confined by identity: only the caller's own.
		wss, _, err := sc.Workspaces().List(ctx, model.Query{})
		if err != nil {
			t.Fatalf("workspaces list: %v", err)
		}
		if len(wss) != 1 || wss[0].ID != wsA {
			t.Errorf("a confined caller must see only its own workspace, got %d rows", len(wss))
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// The MODULE half of the seat: Ext refuses an entity that declares no lineage,
// and filters one that does — with no code in the module.
func TestConfinedExtFiltersDeclaredAndRefusesUndeclared(t *testing.T) {
	st, tenant, wsA, wsB := confineTestFixture(t)
	ctx := context.Background()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(linedKind)
		if err != nil {
			return err
		}
		if _, err := repo.Create(ctx, model.Record{colTestLabel: "own", colTestWS: wsA.String()}); err != nil {
			return err
		}
		if _, err := repo.Create(ctx, model.Record{colTestLabel: "foreign", colTestWS: wsB.String()}); err != nil {
			return err
		}
		un, err := sc.Ext(unlinedKind)
		if err != nil {
			return err
		}
		_, err = un.Create(ctx, model.Record{colTestLabel: "tenant-wide"})
		return err
	}); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, wsA)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(linedKind)
		if err != nil {
			t.Fatalf("a declared entity must be reachable: %v", err)
		}
		rows, _, err := repo.List(ctx, model.Query{})
		if err != nil {
			t.Fatalf("list lined: %v", err)
		}
		if len(rows) != 1 || rows[0].String(colTestLabel) != "own" {
			t.Errorf("declared lineage must filter to the caller's row, got %d rows", len(rows))
		}
		// The undeclared entity is refused at Ext, BEFORE a repo exists: a handler
		// never gets a handle whose empty answer it would read as "nothing to see".
		if _, err := sc.Ext(unlinedKind); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("ext(unlined) = %v, want ErrWorkspaceLineageRequired", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestCreateWithIDIsStrictUnderWorkspaceConfinement pins the ordering at the
// generic decorator: lineage is checked before the raw repository can validate
// or insert the chosen id, and an allowed row keeps that id exactly.
func TestCreateWithIDIsStrictUnderWorkspaceConfinement(t *testing.T) {
	st, tenant, wsA, wsB := confineTestFixture(t)
	ctx := context.Background()
	allowedID := model.ID("018f22e2-79b0-7cc3-8a1b-2c3d4e5f6790")
	foreignID := model.ID("018f22e2-79b0-7cc3-8a1b-2c3d4e5f6791")

	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, wsA)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(linedKind)
		if err != nil {
			return err
		}

		// Both the id and lineage are invalid. A confinement error proves the
		// decorator checked the incoming row before delegating id validation.
		_, err = repo.CreateWithID(ctx, "", model.Record{
			colTestLabel: "foreign-invalid-id", colTestWS: wsB.String(),
		})
		if !errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Errorf("foreign row with invalid id = %v, want workspace confinement first", err)
		}

		got, err := repo.CreateWithID(ctx, allowedID, model.Record{
			colTestLabel: "own-chosen-id", colTestWS: wsA.String(),
		})
		if err != nil {
			return err
		}
		if got.String(model.ColID) != allowedID.String() {
			t.Errorf("confined CreateWithID id = %q, want %q", got.String(model.ColID), allowedID)
		}

		_, err = repo.CreateWithID(ctx, foreignID, model.Record{
			colTestLabel: "foreign", colTestWS: wsB.String(),
		})
		if !errors.Is(err, store.ErrWorkspaceConfinement) {
			t.Errorf("foreign CreateWithID = %v, want ErrWorkspaceConfinement", err)
		}
		rawRepo, err := raw.Ext(linedKind)
		if err != nil {
			return err
		}
		if _, err := rawRepo.Get(ctx, foreignID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("foreign id reached the raw repo: Get error = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("confined CreateWithID: %v", err)
	}
}

// The back-compat rule that a plain equality filter cannot express: a row with NO
// workspace belongs to the tenant's DEFAULT workspace. An operator confined to
// the default must see those rows; one confined elsewhere must not. Getting this
// wrong in one direction hides every pre-FASE-X row from the operator who owns
// them; in the other, it shows them to somebody who does not.
func TestUnsetWorkspaceBelongsToTheDefaultWorkspaceOnly(t *testing.T) {
	st, tenant, wsA, _ := confineTestFixture(t)
	ctx := context.Background()

	var defaultWS model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		defaultWS = def.ID
		// An agent with NO workspace at all — the shape a pre-FASE-X row has.
		_, err = sc.Agents().Create(ctx, model.Agent{
			Name: "legacy-bot", Kind: "service", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if defaultWS == wsA {
		t.Fatal("fixture: wsA must not be the default workspace")
	}

	names := func(ws model.ID) []string {
		var out []string
		if err := st.View(ctx, tenant, func(raw store.Scope) error {
			sc, err := store.ConfineWorkspace(ctx, raw, ws)
			if err != nil {
				return err
			}
			agents, _, err := sc.Agents().List(ctx, model.Query{})
			if err != nil {
				return err
			}
			for _, a := range agents {
				out = append(out, a.Name)
			}
			return nil
		}); err != nil {
			t.Fatalf("list for %s: %v", ws, err)
		}
		return out
	}

	got := names(defaultWS)
	if len(got) != 1 || got[0] != "legacy-bot" {
		t.Errorf("the default-workspace operator must see the unset row, got %v", got)
	}
	for _, n := range names(wsA) {
		if n == "legacy-bot" {
			t.Fatalf("a non-default operator must NOT see the unset row, got %v", names(wsA))
		}
	}
}

// A foreign row fetched BY ID is ErrNotFound, never the row and never a
// distinguishable "forbidden" — which would make the handle an oracle for ids in
// other workspaces.
func TestConfinedScopeHidesForeignRowOnGet(t *testing.T) {
	st, tenant, wsA, wsB := confineTestFixture(t)
	ctx := context.Background()

	var foreignAgent model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "workspace_id", Op: model.OpEq, Value: wsB.String()}},
		})
		if err != nil {
			return err
		}
		if len(agents) != 1 {
			t.Fatalf("fixture: want 1 agent in wsB, got %d", len(agents))
		}
		foreignAgent = agents[0].ID
		return nil
	}); err != nil {
		t.Fatalf("fixture read: %v", err)
	}

	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, wsA)
		if err != nil {
			return err
		}
		if _, err := sc.Agents().Get(ctx, foreignAgent); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("get foreign agent = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}
