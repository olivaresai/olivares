// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func f2aPhysicalProof(t *testing.T, cfg store.Config) (directoryWriterControlState, userAuthorityCoverage, map[model.TenantID]int64) {
	t.Helper()
	ctx := context.Background()
	db, err := openDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dia, _ := dialect.New(cfg.Engine)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := readDirectoryWriterControlState(ctx, tx, dia)
	if err != nil {
		t.Fatal(err)
	}
	if err := dia.BindTenant(ctx, tx, model.SystemTenantID); err != nil {
		t.Fatal(err)
	}
	h, err := readUserAuthorityCoverage(ctx, tx, dia)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's tenant list is discoverable through membership targets under
	// SYSTEM; G itself is still read on each exact tenant binding.
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT target_tenant_id FROM "+directoryWriterRelation(dia, membershipDescriptor.Table))
	if err != nil {
		t.Fatal(err)
	}
	var tenants []model.TenantID
	for rows.Next() {
		var id model.TenantID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		tenants = append(tenants, id)
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		t.Fatal(err)
	}
	g := map[model.TenantID]int64{}
	for _, tenant := range tenants {
		if err := dia.BindTenant(ctx, tx, tenant); err != nil {
			t.Fatal(err)
		}
		epoch, found, err := readDirectoryEpochRow(ctx, tx, dia, tenant)
		if err != nil || !found {
			t.Fatalf("fixture G: %v", err)
		}
		g[tenant] = epoch.Version
	}
	return state, h, g
}

func TestUserAuthorityMaintenanceCASAndAcknowledgement(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, mode := range []string{"staged", "enforced"} {
			for _, scenario := range []string{"cas_mode", "cas_protocol", "ack_commit", "ack_rollback"} {
				t.Run(fmt.Sprintf("%s/%s/%s", engine, mode, scenario), func(t *testing.T) {
					ctx := context.Background()
					cfg, f, _ := F2AOldFixtureForTest(t, engine, mode, false)
					// Preparation is allowed to migrate v9, but must never serve an incomplete
					// enforced predecessor. The private path below performs its ceremony.
					raw, err := Open(ctx, cfg, nil)
					if raw != nil {
						raw.Close()
					}
					if err != nil && mode == "staged" {
						t.Fatal(err)
					}
					injected := errors.New("fixture acknowledgement lost")
					if strings.HasPrefix(scenario, "cas_") {
						directoryActivationBeforeCASTestHook = func(ctx context.Context, tx *sql.Tx) error {
							column, value := "mode", "enforced"
							if mode == "enforced" {
								value = "staged"
							}
							if scenario == "cas_protocol" {
								column, value = "coverage_protocol", coverageProtocolTarget
							}
							dia, _ := dialect.New(engine)
							_, err := tx.ExecContext(ctx, dia.Rebind("UPDATE "+directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)+" SET "+column+"=?"), value)
							return err
						}
					} else {
						directoryActivationCommitTestHook = func(tx *sql.Tx) error {
							if scenario == "ack_commit" {
								if err := tx.Commit(); err != nil {
									return err
								}
							} else {
								if err := tx.Rollback(); err != nil {
									return err
								}
							}
							return injected
						}
					}
					_, after, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, f.Generation)
					directoryActivationBeforeCASTestHook = nil
					directoryActivationCommitTestHook = nil
					state, h, g := f2aPhysicalProof(t, cfg)
					if scenario == "ack_commit" {
						if err != nil || !changed || after.CoverageProtocol != coverageProtocolTarget || state.ExpectedGeneration != f.Generation+1 || len(h.Missing) != 0 {
							t.Fatalf("committed acknowledgement: changed=%t after=%+v state=%+v missing=%d err=%v", changed, after, state, len(h.Missing), err)
						}
						for _, tenant := range f.Tenants {
							if g[tenant] != 3 {
								t.Fatalf("committed G=%d", g[tenant])
							}
						}
					} else {
						if err == nil || changed || errors.Is(err, ErrDirectoryWriterActivationIndeterminate) {
							t.Fatalf("settled rollback: changed=%t err=%v", changed, err)
						}
						if state.Mode != directoryWriterMode(mode) || state.ExpectedGeneration != f.Generation || state.CoverageProtocol != coverageProtocolLegacy || len(h.Rows) != 0 || len(h.Missing) != 2 {
							t.Fatalf("rollback changed H/control: %+v rows=%d missing=%d", state, len(h.Rows), len(h.Missing))
						}
						for _, tenant := range f.Tenants {
							if g[tenant] != 2 {
								t.Fatalf("rollback G=%d", g[tenant])
							}
						}
						if strings.HasPrefix(scenario, "cas_") && !errors.Is(err, store.ErrConflict) {
							t.Fatalf("CAS did not reject its changed concrete prestate: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestUserAuthorityAlreadyOpenV9WriterFenced(t *testing.T) {
	path := os.Getenv("OLIVARES_F2A_V9_BINARY")
	if path == "" {
		t.Skip("recorded source-native v9 fixture binary required")
	}
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "old-live.db")}
			if engine == store.EnginePostgres {
				pg := isolatedPGSplit(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
			}
			manifest := filepath.Join(t.TempDir(), "manifest.json")
			cmd := osexec.CommandContext(ctx, path, "-test.run=^TestF2AV9Fixture$", "-test.v")
			cmd.Env = f2aOldProcessEnv(cfg, "writer", manifest)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			var fixture F2AV9Fixture
			for {
				encoded, err := os.ReadFile(manifest)
				if err == nil && json.Unmarshal(encoded, &fixture) == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("old process did not establish fixture")
				case <-time.After(25 * time.Millisecond):
				}
			}
			stage, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			stage.Close()
			if _, err := stdin.Write([]byte("staged\n")); err != nil {
				t.Fatal(err)
			}
			for {
				if _, err := os.Stat(manifest + ".staged"); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("old staged protocol phase did not complete")
				case <-time.After(25 * time.Millisecond):
				}
			}
			if _, _, _, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err != nil {
				t.Fatalf("maintenance while old process idle: %v", err)
			}
			if _, err := stdin.Write([]byte("write\n")); err != nil {
				t.Fatal(err)
			}
			stdin.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("old-writer adapter: %v %s", err, output.String())
			}
			if !strings.Contains(output.String(), "OLD_WRITE_REJECTED:") {
				t.Fatalf("old writer did not report refusal: %s", output.String())
			}
			want := "directory coverage protocol required"
			if engine == store.EngineSQLite {
				want = "coverage_protocol"
			}
			if !strings.Contains(output.String(), want) {
				t.Fatalf("old write failed for another cause: %s", output.String())
			}
			state, h, g := f2aPhysicalProof(t, cfg)
			if state.CoverageProtocol != coverageProtocolTarget || len(h.Missing) != 0 {
				t.Fatal("old writer damaged target")
			}
			for _, tenant := range fixture.Tenants {
				want := int64(3)
				if engine == store.EnginePostgres && tenant == fixture.Tenants[0] {
					want = 4
				}
				if g[tenant] != want {
					t.Fatalf("old writer G=%d want%d", g[tenant], want)
				}
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if err := raw.AuthView(ctx, func(as store.AuthScope) error {
				u, err := as.Users().Get(ctx, fixture.Users[0].ID)
				if err == nil && u.Version != fixture.Users[0].Version+map[bool]int64{true: 1, false: 0}[engine == store.EnginePostgres] {
					t.Error("old source mutation survived")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserAuthorityAcknowledgementRequiresFullFreshWitness(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, changedWitness := range []string{"protocol", "G", "H"} {
			t.Run(fmt.Sprintf("%s/%s", engine, changedWitness), func(t *testing.T) {
				ctx := context.Background()
				cfg, f, _ := F2AOldFixtureForTest(t, engine, "enforced", false)
				// A separate connection changes the durable postcondition only after the
				// real commit. This is neither a mocked SELECT nor a fabricated success.
				directoryActivationCommitTestHook = func(tx *sql.Tx) error {
					if err := tx.Commit(); err != nil {
						return err
					}
					authorityCfg := cfg
					if engine == store.EnginePostgres {
						authorityCfg.DSN = cfg.OwnerDSN
					}
					db, err := openDB(authorityCfg)
					if err != nil {
						return err
					}
					defer db.Close()
					dia, _ := dialect.New(engine)
					if changedWitness == "protocol" {
						_, err = db.ExecContext(ctx, "UPDATE "+directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)+" SET coverage_protocol='membership-union-v1'")
						if err != nil {
							return err
						}
					} else {
						another, err := db.BeginTx(ctx, nil)
						if err != nil {
							return err
						}
						defer another.Rollback()
						state, err := acquireDirectoryWriter(ctx, another, dia)
						if err != nil {
							return err
						}
						tenant := model.SystemTenantID
						if changedWitness == "G" {
							tenant = f.Tenants[0]
						}
						if err := bindDirectoryTenant(ctx, another, dia, tenant); err != nil {
							return err
						}
						if err := armDirectoryWriter(ctx, another, dia, state); err != nil {
							return err
						}
						if changedWitness == "G" {
							_, err = another.ExecContext(ctx, dia.Rebind("UPDATE "+directoryWriterRelation(dia, directoryEpochDescriptor.Table)+" SET version=version+1 WHERE id=?"), tenant.String())
						} else {
							_, err = another.ExecContext(ctx, dia.Rebind("UPDATE "+directoryWriterRelation(dia, userAuthorityDescriptor.Table)+" SET id=? WHERE id=?"), model.NewID().String(), f.Users[0].ID.String())
						}
						if err != nil {
							return err
						}
						if err := finishDirectoryWriter(ctx, another, dia); err != nil {
							return err
						}
						if err := another.Commit(); err != nil {
							return err
						}
					}
					return errors.New("ack lost after changed durable witness")
				}
				_, _, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, f.Generation)
				directoryActivationCommitTestHook = nil
				if changed || !errors.Is(err, ErrDirectoryWriterActivationIndeterminate) {
					t.Fatalf("changed %s witness classified success: changed=%t err=%v", changedWitness, changed, err)
				}
			})
		}
	}
}

func TestUserAuthorityMaintenanceEpochOverflowRollsBackH(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			cfg, f, _ := F2AOldFixtureForTest(t, engine, "staged", false)
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := raw.(*sqlStore)
			if err := raw.Mutate(ctx, f.Tenants[1], func(sc store.Scope) error {
				_, err := sc.(*tenantScope).tx.ExecContext(ctx, s.dia.Rebind("UPDATE "+directoryWriterRelation(s.dia, directoryEpochDescriptor.Table)+" SET version=? WHERE id=?"), int64(math.MaxInt64), f.Tenants[1].String())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			if _, _, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1); err == nil || changed {
				t.Fatalf("overflow activated: %t %v", changed, err)
			}
			state, h, g := f2aPhysicalProof(t, cfg)
			if state.Mode != directoryWriterStaged || state.CoverageProtocol != coverageProtocolLegacy || state.ExpectedGeneration != 1 || len(h.Rows) != 0 || len(h.Missing) != 2 || g[f.Tenants[0]] != 2 || g[f.Tenants[1]] != math.MaxInt64 {
				t.Fatalf("overflow failed to rollback whole ceremony: state=%+v H=%d G=%v", state, len(h.Rows), g)
			}
		})
	}
}

func TestUserAuthorityAcknowledgementClassifierPinsProtocolAndCoverage(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, _, tenants := f2aFreshTarget(t, engine)
			authority, err := openDirectoryActivationAuthority(ctx, s, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if authority.closeOwner {
				defer authority.ownerDB.Close()
			}
			observed, err := runDirectoryActivation(ctx, s, authority, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			attempt := observed
			attempt.prestate = directoryWriterControlState{Mode: directoryWriterStaged, ExpectedGeneration: 1, CoverageProtocol: coverageProtocolLegacy}
			if !directoryActivationTargetMatches(attempt, observed) {
				t.Fatal("real locked target proof did not reconcile")
			}
			for _, part := range []string{"protocol", "H", "G"} {
				changed := observed
				switch part {
				case "protocol":
					changed.state.CoverageProtocol = coverageProtocolLegacy
				case "H":
					changed.users.Missing = []model.ID{model.NewID()}
				case "G":
					changed.inventory.Epochs = maps.Clone(observed.inventory.Epochs)
					changed.inventory.Epochs[tenants[0]]++
				}
				if directoryActivationTargetMatches(attempt, changed) {
					t.Fatalf("tuple-only classifier ignored %s", part)
				}
			}
		})
	}
}
