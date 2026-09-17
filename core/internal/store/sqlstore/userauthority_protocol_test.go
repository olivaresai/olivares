// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This adapter consumes an independently compiled, source-native v9 binary.
// The committed producer adapter and its base SHA are recorded in testdata.
type F2AV9Fixture struct {
	Users      []model.User
	Sessions   []model.AuthSession
	Tenants    []model.TenantID
	Mode       string
	Generation int64
}

func F2AOldFixtureForTest(t *testing.T, engine store.Engine, mode string, noAdmin bool) (store.Config, F2AV9Fixture, string) {
	t.Helper()
	path := os.Getenv("OLIVARES_F2A_V9_BINARY")
	if path == "" {
		t.Skip("set OLIVARES_F2A_V9_BINARY to the recorded ae19349 source-native fixture binary")
	}
	cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "historical.db")}
	var super string
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
		super = pg.Superuser
	}
	manifest := filepath.Join(t.TempDir(), "v9-manifest.json")
	cmd := osexec.Command(path, "-test.run=^TestF2AV9Fixture$", "-test.v")
	cmd.Env = f2aOldProcessEnv(cfg, mode, manifest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source-native v9 producer failed: %v\n%s", err, output)
	}
	encoded, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var fixture F2AV9Fixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	if noAdmin {
		cfg.AdminDSN = ""
	}
	return cfg, fixture, super
}

func f2aOldProcessEnv(cfg store.Config, mode, manifest string) []string {
	return append(os.Environ(), "OLIVARES_F2A_ENGINE="+string(cfg.Engine), "OLIVARES_F2A_DSN="+cfg.DSN, "OLIVARES_F2A_OWNER_DSN="+cfg.OwnerDSN, "OLIVARES_F2A_ADMIN_DSN="+cfg.AdminDSN, "OLIVARES_F2A_V9_MODE="+mode, "OLIVARES_F2A_MANIFEST="+manifest)
}

func TestUserAuthorityV9MaintenancePredecessors(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, mode := range []string{"staged", "enforced"} {
			for _, noAdmin := range []bool{false, true} {
				if engine == store.EngineSQLite && noAdmin {
					continue
				}
				name := string(engine) + "/" + mode
				if noAdmin {
					name += "/no_admin"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					cfg, fixture, super := F2AOldFixtureForTest(t, engine, mode, noAdmin)
					// The v9 fixture bytes are what the archived producer wrote. Every step
					// below may advance the schema to v10, and nothing else: the prestate
					// (mode, generation, compiled legacy protocol), H and G are compared at
					// that schema cut after each preparation, refusal and ordinary Open.
					wantPrestate := func(label string) {
						t.Helper()
						state, h, g := f2aPhysicalProof(t, cfg)
						if string(state.Mode) != fixture.Mode || state.ExpectedGeneration != fixture.Generation || state.CoverageProtocol != coverageProtocolLegacy || len(h.Rows) != 0 || len(h.Missing) != 2 {
							t.Fatalf("%s changed prestate: state=%+v H=%d missing=%d", label, state, len(h.Rows), len(h.Missing))
						}
						for _, tenant := range fixture.Tenants {
							if g[tenant] != 2 {
								t.Fatalf("%s changed G=%d", label, g[tenant])
							}
						}
						t.Logf("V9_PRESTATE_UNCHANGED|%s|mode=%s|generation=%d|protocol=%s|H=0|missing_H=2|G=2,2", label, state.Mode, state.ExpectedGeneration, state.CoverageProtocol)
					}
					if noAdmin {
						// Maintenance from either legacy prestate without the closed
						// routine is the typed refusal, never a preparation that could be
						// mistaken for readiness.
						_, _, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, fixture.Generation)
						if err == nil || changed || !errors.Is(err, store.ErrDirectoryUnavailable) || !strings.Contains(err.Error(), "closed directory inventory routine is required") {
							t.Fatalf("first v9 noAdmin attempt without inventory: changed=%t err=%v", changed, err)
						}
						wantPrestate("rejected noAdmin preparation")
					}
					raw, openErr := Open(ctx, cfg, nil)
					if mode == "enforced" {
						// Enforced legacy is never published by an ordinary Open. The
						// reason names what is missing: the closed routine when there is no
						// AdminDSN, otherwise the H coverage the ceremony has not built.
						reason := "enforced User authority coverage is incomplete"
						if noAdmin {
							reason = "closed directory inventory routine is required"
						}
						if openErr == nil {
							raw.Close()
							t.Fatal("ordinary Open published an enforced legacy store missing H")
						}
						if !errors.Is(openErr, store.ErrDirectoryUnavailable) || !strings.Contains(openErr.Error(), reason) {
							t.Fatalf("enforced legacy Open err = %v, want ErrDirectoryUnavailable with %q", openErr, reason)
						}
					} else {
						if openErr != nil {
							t.Fatalf("staged v9 upgrade: %v", openErr)
						}
						// Staged legacy stays serviceable: the witness is exact about what
						// it could prove. Without an AdminDSN and without the routine the
						// inventory is unknown, so the reason is the missing routine, the
						// counts stay zero and no coverage is claimed; with an authority the
						// inventory is complete while H coverage is honestly incomplete.
						want := store.DirectoryStatus{
							EpochCoverageComplete: true, ControlMode: store.DirectoryControlStaged, WriterPosture: store.DirectoryWriterSQLiteCapability,
							ExpectedGeneration: fixture.Generation, CoverageProtocol: coverageProtocolLegacy, InventoryAuthority: "sqlite",
							InventoryOrgCount: 3, InventoryBusinessOrgCount: 2, InventoryEpochCount: 2,
						}
						if engine == store.EnginePostgres {
							want.WriterPosture, want.InventoryAuthority = store.DirectoryWriterSplitOwner, "admin_dsn"
						}
						if noAdmin {
							want = store.DirectoryStatus{
								ControlMode: store.DirectoryControlStaged, WriterPosture: store.DirectoryWriterSplitOwner,
								ExpectedGeneration: fixture.Generation, CoverageProtocol: coverageProtocolLegacy, InventoryUnavailableReason: "closed_routine_missing",
							}
						}
						directoryEpochTestWantStatus(t, raw, want)
					}
					if raw != nil {
						raw.Close()
					}
					// v10 must preserve the original mode/generation, including enforced,
					// and must leave H backfill to the guarded ceremony.
					wantPrestate("v10 ordinary Open")
					if noAdmin {
						f2aInstallInventoryForConfig(t, cfg, super)
					}
					before, after, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, fixture.Generation)
					if err != nil {
						t.Fatalf("maintenance from %s: %v", mode, err)
					}
					if !changed || before.UserAuthorityCoverageComplete || !after.UserAuthorityCoverageComplete || after.CoverageProtocol != coverageProtocolTarget || after.ExpectedGeneration != fixture.Generation+1 || after.InventoryOrgCount != 3 || after.InventoryEpochCount != 2 {
						t.Fatalf("before=%+v after=%+v changed=%t", before, after, changed)
					}
					reopened, err := Open(ctx, cfg, nil)
					if err != nil {
						t.Fatalf("target reopen: %v", err)
					}
					for _, user := range fixture.Users {
						if got := F2AUserAuthorityVersionForTest(t, reopened, user.ID); got != 1 {
							t.Fatalf("backfilled H=%d want1", got)
						}
					}
					for _, tenant := range fixture.Tenants {
						if got := f2aEpochForTest(t, reopened, tenant); got != 3 {
							t.Fatalf("G=%d want v9 membership2 + cutover1", got)
						}
					}
					reopened.Close()
					_, retry, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, fixture.Generation)
					if err != nil || changed || retry != after {
						t.Fatalf("retry=%+v changed=%t err=%v", retry, changed, err)
					}
					old := osexec.Command(os.Getenv("OLIVARES_F2A_V9_BINARY"), "-test.run=^TestF2AV9Fixture$")
					old.Env = f2aOldProcessEnv(cfg, "open", "")
					output, err := old.CombinedOutput()
					if err == nil || !strings.Contains(string(output), "core") {
						t.Fatalf("old v9 reopen was not refused by core preflight: %v %s", err, output)
					}
				})
			}
		}
	}
}

func f2aInstallInventoryForConfig(t *testing.T, cfg store.Config, super string) {
	t.Helper()
	ctx := context.Background()
	db, err := openPGPinnedToEngineSchema(cfg.DSN, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var database, app, owner string
	if err := db.QueryRowContext(ctx, "SELECT current_database(),current_user").Scan(&database, &app); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT r.rolname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_roles r ON r.oid=c.relowner WHERE n.nspname='public' AND c.relname='orgs'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	result, err := ProvisionPostgres(ctx, super, store.PgProvisionSpec{Database: database, App: store.PgRole{Name: app}, Owner: store.PgRole{Name: owner}, InstallDirectoryInventory: true}, true)
	if err != nil || !result.DirectoryInventoryInstalled {
		t.Fatalf("install inventory: %v", err)
	}
}

func F2AUserAuthorityVersionForTest(t *testing.T, raw store.Store, id model.ID) int64 {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := raw.AuthView(ctx, func(as store.AuthScope) error {
		a := as.(*authScope)
		h, found, err := readUserAuthorityRow(ctx, a.ts.tx, a.ts.s.dia, id)
		if err == nil && found {
			version = h.Version
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return version
}

func f2aEpochForTest(t *testing.T, raw store.Store, tenant model.TenantID) int64 {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := raw.View(ctx, tenant, func(sc store.Scope) error {
		h, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		version = h.Version
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestUserAuthorityProtocolPresentationRefusesRawWrites(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, _ := f2aFreshTarget(t, engine)
			for _, protocol := range []string{"", coverageProtocolLegacy, "unknown-v1"} {
				err := s.AuthMutate(ctx, func(as store.AuthScope) error {
					a := as.(*authScope)
					if err := a.ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) { return nil, nil }); err != nil {
						return err
					}
					if engine == store.EnginePostgres {
						var got string
						if err := a.ts.tx.QueryRowContext(ctx, "SELECT pg_catalog.set_config('app.directory_coverage_protocol',$1,true)", protocol).Scan(&got); err != nil {
							return err
						}
					} else {
						if _, err := a.ts.tx.ExecContext(ctx, "UPDATE main.directory_writer_marker SET coverage_protocol=?", protocol); err != nil {
							return err
						}
					}
					_, err := a.ts.tx.ExecContext(ctx, s.dia.Rebind("UPDATE "+directoryWriterRelation(s.dia, userDescriptor.Table)+" SET display_name='untracked-protocol' WHERE id=?"), users[0].ID.String())
					return err
				})
				if err == nil {
					t.Fatalf("raw protocol %q wrote target User", protocol)
				}
			}
			if h := F2AUserAuthorityVersionForTest(t, s, users[0].ID); h != 1 {
				t.Fatalf("raw refusal changed H=%d", h)
			}
		})
	}
}
