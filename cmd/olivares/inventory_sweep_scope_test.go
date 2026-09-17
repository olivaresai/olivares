// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/modules/inventory"
)

// The adapter must satisfy the module's seam without the module importing it.
var _ inventory.SweepScopeSource = inventorySweepScopeSource{}

// sweepScopeEstate opens a real store with the inventory schema and provisions
// the four-way directory every composition-frontier assertion needs: an active
// tenant this instance serves, a suspended one, an active one pinned to another
// region, and the reserved system tenant.
type sweepScopeEstate struct {
	st                     store.Store
	local, suspended, away model.TenantID
	system                 model.TenantID
	reg                    *residency.Registry
}

func newSweepScopeEstate(t *testing.T, cfg store.Config) sweepScopeEstate {
	t.Helper()
	ctx := context.Background()
	inv := inventory.New()
	st, err := coreengine.Open(ctx, cfg, inv.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatalf("residency registry: %v", err)
	}
	e := sweepScopeEstate{st: st, reg: reg}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		sysOrg, err := sys.EnsureSystemTenant(ctx)
		if err != nil {
			return err
		}
		e.system = sysOrg.TenantID
		local, err := sys.CreateOrg(ctx, model.Org{Name: "local", Slug: "local", Status: model.StatusActive, DataRegion: "eu"})
		if err != nil {
			return err
		}
		e.local = local.TenantID
		suspended, err := sys.CreateOrg(ctx, model.Org{Name: "suspended", Slug: "suspended", Status: model.StatusActive, DataRegion: "eu"})
		if err != nil {
			return err
		}
		e.suspended = suspended.TenantID
		if _, err := sys.SetOrgStatus(ctx, suspended.TenantID, model.StatusSuspended); err != nil {
			return err
		}
		away, err := sys.CreateOrg(ctx, model.Org{Name: "away", Slug: "away", Status: model.StatusActive, DataRegion: "us"})
		if err != nil {
			return err
		}
		e.away = away.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision the directory: %v", err)
	}
	return e
}

func sqliteEstate(t *testing.T) sweepScopeEstate {
	t.Helper()
	return newSweepScopeEstate(t, store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "sweepscope.db"),
	})
}

// TestInventorySweepScopeReturnsOnlyServedActiveBusinessTenants is the whole
// composition frontier in one assertion: the snapshot is exactly the tenants
// this instance may sweep, and the ordering/uniqueness the seam promises.
func TestInventorySweepScopeReturnsOnlyServedActiveBusinessTenants(t *testing.T) {
	e := sqliteEstate(t)
	src := inventorySweepScopeSource{st: e.st, reg: e.reg}

	got, err := src.ListSweepTenants(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if want := []model.TenantID{e.local}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want only the active locally-served business tenant %v", got, want)
	}
	for _, excluded := range []struct {
		name   string
		tenant model.TenantID
	}{
		{"suspended", e.suspended},
		{"pinned to another region", e.away},
		{"the system partition", e.system},
		{"the zero tenant", model.TenantID("")},
	} {
		for _, id := range got {
			if id == excluded.tenant {
				t.Fatalf("%s tenant %s is in the sweep snapshot", excluded.name, id)
			}
		}
	}
}

// TestInventorySweepScopeSnapshotIsNotAuthorization: a tenant that was a
// legitimate candidate when the snapshot was taken and lost service before its
// turn is refused by the store, not by the list. That is why the adapter may
// hand out ids at all.
func TestInventorySweepScopeSnapshotIsNotAuthorization(t *testing.T) {
	ctx := context.Background()
	e := sqliteEstate(t)
	src := inventorySweepScopeSource{st: e.st, reg: e.reg}

	snapshot, err := src.ListSweepTenants(ctx)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(snapshot) != 1 || snapshot[0] != e.local {
		t.Fatalf("snapshot = %v, want the active local tenant", snapshot)
	}

	// The composed store the engine gives the module: the same service-withdrawal
	// guard boot.go wraps around it.
	data := api.NewModuleData(suspension.Guard(e.st, slog.New(slog.DiscardHandler)))
	if err := data.Mutate(ctx, snapshot[0], func(store.Scope) error { return nil }); err != nil {
		t.Fatalf("the candidate must be writable while it is still served: %v", err)
	}
	if err := e.st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(ctx, snapshot[0], model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("withdraw service: %v", err)
	}
	err = data.Mutate(ctx, snapshot[0], func(store.Scope) error {
		t.Fatal("the turn's callback ran for a tenant whose service was withdrawn after the snapshot")
		return nil
	})
	if err == nil {
		t.Fatal("a mutation on a tenant suspended after the snapshot was allowed")
	}
}

// --- refusals ----------------------------------------------------------------

// enumerationErrorStore returns rows ALONGSIDE an error, the exact shape
// SystemScope.ListOrgs has on Postgres without a BYPASSRLS admin pool.
type enumerationErrorStore struct {
	store.Store
	rows   []model.Org
	err    error
	opened bool
}

type enumerationErrorScope struct {
	store.SystemScope
	owner *enumerationErrorStore
}

func (s *enumerationErrorStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	s.opened = true
	return s.Store.System(ctx, func(sys store.SystemScope) error {
		return fn(enumerationErrorScope{SystemScope: sys, owner: s})
	})
}

func (s enumerationErrorScope) ListOrgs(context.Context) ([]model.Org, error) {
	return s.owner.rows, s.owner.err
}

// TestInventorySweepScopeDiscardsRowsFromANonAuthoritativeEnumeration: rows that
// arrive with an error are not a smaller estate, they are an unread one.
func TestInventorySweepScopeDiscardsRowsFromANonAuthoritativeEnumeration(t *testing.T) {
	e := sqliteEstate(t)
	var visible []model.Org
	if err := e.st.System(context.Background(), func(sys store.SystemScope) error {
		var err error
		visible, err = sys.ListOrgs(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read the real directory for the fixture: %v", err)
	}
	if len(visible) == 0 {
		t.Fatal("the fixture directory is empty, so this test would prove nothing")
	}

	failing := &enumerationErrorStore{
		Store: e.st, rows: visible,
		err: errors.New("engine \"postgres\" holds no BYPASSRLS admin pool: " + store.ErrEnumerationNotAuthoritative.Error()),
	}
	failing.err = errors.Join(store.ErrEnumerationNotAuthoritative, failing.err)
	src := inventorySweepScopeSource{st: failing, reg: e.reg}

	got, err := src.ListSweepTenants(context.Background())
	if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("enumerate = (%v, %v), want ErrEnumerationNotAuthoritative", got, err)
	}
	if got != nil {
		t.Fatalf("candidates = %v, want none: the rows came with the refusal", got)
	}
}

// sweepScopeStandbyStore is a real, reachable store that reports this node as a
// standby (reusing leader_readyz_test.go's standbyLeader), and notices whether
// the privileged System transaction was opened at all.
type sweepScopeStandbyStore struct {
	store.Store
	systemOpened bool
}

func (s *sweepScopeStandbyStore) Leader() store.LeaderElector { return standbyLeader{} }

func (s *sweepScopeStandbyStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	s.systemOpened = true
	return s.Store.System(ctx, fn)
}

// TestInventorySweepScopeRefusesOnAFollower: a standby does not enumerate the
// estate. The store's write gate would refuse its turns anyway; stopping here
// spends nothing on an estate this node is not serving.
func TestInventorySweepScopeRefusesOnAFollower(t *testing.T) {
	e := sqliteEstate(t)
	standby := &sweepScopeStandbyStore{Store: e.st}
	src := inventorySweepScopeSource{st: standby, reg: e.reg}

	got, err := src.ListSweepTenants(context.Background())
	if !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("enumerate on a follower = (%v, %v), want store.ErrNotLeader", got, err)
	}
	if got != nil {
		t.Fatalf("a follower produced candidates: %v", got)
	}
	if standby.systemOpened {
		t.Fatal("a follower opened the privileged System transaction")
	}
}

// TestInventorySweepScopeRefusesAMissingStore is the seam's other absent
// dependency: never a silent empty snapshot.
func TestInventorySweepScopeRefusesAMissingStore(t *testing.T) {
	got, err := inventorySweepScopeSource{}.ListSweepTenants(context.Background())
	if err == nil {
		t.Fatalf("an unwired adapter returned %v and no error", got)
	}
	if got != nil {
		t.Fatalf("an unwired adapter produced candidates: %v", got)
	}
}

// TestSweepableTenantsRefusesAMalformedDirectoryRow: a row this process cannot
// read makes the census incomplete, and an incomplete census is not a shorter
// one. Same rule as the promotion census (preparePDPReloadTenants).
func TestSweepableTenantsRefusesAMalformedDirectoryRow(t *testing.T) {
	good := model.Org{Status: model.StatusActive}
	good.ID = model.NewID()
	good.TenantID = model.TenantID(good.ID.String())

	mismatched := model.Org{Status: model.StatusActive}
	mismatched.ID = model.NewID()
	mismatched.TenantID = model.TenantID(model.NewID().String())

	unreadable := model.Org{Status: model.StatusActive}
	unreadable.ID = model.ID("not-a-uuid")
	unreadable.TenantID = model.TenantID("not-a-uuid")

	for _, tc := range []struct {
		name string
		orgs []model.Org
	}{
		{"an unreadable id", []model.Org{good, unreadable}},
		{"an id that disagrees with its tenant id", []model.Org{good, mismatched}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sweepableTenants(tc.orgs, nil)
			if err == nil {
				t.Fatalf("%s was accepted, yielding %v", tc.name, got)
			}
			if got != nil {
				t.Fatalf("%s still produced candidates: %v", tc.name, got)
			}
		})
	}

	// The control: the same good row alone is accepted, so the refusals above are
	// about the malformed row and not about the filter rejecting everything.
	got, err := sweepableTenants([]model.Org{good, good}, nil)
	if err != nil {
		t.Fatalf("a well-formed row was refused: %v", err)
	}
	if want := []model.TenantID{good.TenantID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want the single deduplicated %v", got, want)
	}
}

// --- the real PostgreSQL frontier --------------------------------------------

// TestInventorySweepScopePostgresAdminPoolDecidesAuthority is group 2's real-
// engine half: on PostgreSQL the authority to enumerate the directory is a
// property of the POOL, not of the DSN string being present.
//
// It is deliberately NOT skipped when no server is configured: a skipped check
// is not a passing check, so an unconfigured run fails and says which variable
// to set.
func TestInventorySweepScopePostgresAdminPoolDecidesAuthority(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Fatalf("%s unset: the required PostgreSQL leg of the sweep-scope frontier is NOT RUN", enginetest.EnvSuperuserDSN)
	}

	t.Run("without an admin pool the enumeration is refused", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		t.Logf("sweep-scope PostgreSQL fixture database=%s engine=postgres admin_dsn=absent", pg.Database)
		e := newSweepScopeEstate(t, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4, Debug: true,
		})
		src := inventorySweepScopeSource{st: e.st, reg: e.reg}
		got, err := src.ListSweepTenants(context.Background())
		if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
			t.Fatalf("enumerate without an admin pool = (%v, %v), want ErrEnumerationNotAuthoritative", got, err)
		}
		if got != nil {
			t.Fatalf("an RLS-limited read produced candidates: %v", got)
		}
	})

	t.Run("with the attested admin pool the legitimate candidates come back", func(t *testing.T) {
		pg := enginetest.IsolatedPostgres(t)
		t.Logf("sweep-scope PostgreSQL fixture database=%s engine=postgres admin_dsn=present", pg.Database)
		e := newSweepScopeEstate(t, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
			MaxConns: 4, Debug: true,
		})
		src := inventorySweepScopeSource{st: e.st, reg: e.reg}
		got, err := src.ListSweepTenants(context.Background())
		if err != nil {
			t.Fatalf("enumerate with the attested admin pool: %v", err)
		}
		if want := []model.TenantID{e.local}; !reflect.DeepEqual(got, want) {
			t.Fatalf("candidates = %v, want only the active locally-served business tenant %v", got, want)
		}
	})
}
