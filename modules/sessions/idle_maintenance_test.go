// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The delegate remains the real engine's ModuleData on both backends. Its
// PostgreSQL View is a database-read-only transaction; entering Mutate would
// instead persist enrollment even when the callback finds no work.
type idleMaintenanceData struct {
	api.ModuleData
	mutations int
	postView  func() error
}

func (d *idleMaintenanceData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if err := d.ModuleData.View(ctx, tenant, fn); err != nil {
		return err
	}
	if after := d.postView; after != nil {
		d.postView = nil
		return after()
	}
	return nil
}

func (d *idleMaintenanceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mutations++
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

func maintenanceWAL(t *testing.T, cfg store.Config) []byte {
	t.Helper()
	if cfg.Engine != store.EngineSQLite {
		return nil
	}
	wal, err := os.ReadFile(cfg.DSN + "-wal")
	if err != nil {
		t.Fatalf("read fixture WAL: %v", err)
	}
	return wal
}

func TestIdleWorkOutboxDoesNotWrite(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			cfg := be.config(t)
			if cfg.Engine == store.EngineSQLite {
				cfg.DSN = filepath.Join(t.TempDir(), "idle-outbox.db")
			}
			f := newWorkFixture(t, cfg.DSN, func(c *store.Config) { *c = cfg })
			t.Cleanup(func() { _ = f.st.Close() })
			WithWorkEventSink(&recordingWorkSink{})(f.m)
			data := &idleMaintenanceData{ModuleData: f.m.Data}
			f.m.UseData(data)
			before := maintenanceWAL(t, cfg)
			for i := 0; i < 3; i++ {
				if err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 200, nil); err != nil {
					t.Fatalf("idle drain: %v", err)
				}
			}
			if data.mutations != 0 {
				t.Errorf("empty ticks entered %d write transactions", data.mutations)
			}
			if after := maintenanceWAL(t, cfg); !bytes.Equal(before, after) {
				t.Error("empty ticks changed the file-backed SQLite WAL")
			}
		})
	}
}

func TestWorkOutboxDiscoveryResamplesDueAndAuthority(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, mode := range []string{"postponed", "authority_withdrawn", "canceled", "positive"} {
				t.Run(mode, func(t *testing.T) {
					cfg := be.config(t)
					if cfg.Engine == store.EngineSQLite {
						cfg.DSN = filepath.Join(t.TempDir(), "outbox-discovery.db")
					}
					f := newWorkFixture(t, cfg.DSN, func(c *store.Config) { *c = cfg })
					t.Cleanup(func() { _ = f.st.Close() })
					sink := &recordingWorkSink{}
					WithWorkEventSink(sink)(f.m)
					item := applyCreate(t, f, "discovery race")
					eventID := insertOutboxEventForTest(t, f, workItemKind, item.ResultID, 2, "work.handoff.offered")
					authority := &recordingOutboxAuthority{allowClaim: true, allowEffect: true}
					func() { f.m.WorkOutboxAuthority = authority; f.m.normalize() }()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					discovered := false
					data := &idleMaintenanceData{ModuleData: f.m.Data}
					data.postView = func() error {
						discovered = true
						switch mode {
						case "postponed":
							row := outboxRowForTest(t, f, eventID)
							row[colOutboxNextAttemptAt] = model.NewTimestamp(time.Now().Add(time.Hour)).String()
							return f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
								repo, err := sc.Ext(workOutboxKind)
								if err != nil {
									return err
								}
								_, err = repo.Update(ctx, row)
								return err
							})
						case "authority_withdrawn":
							authority.mu.Lock()
							authority.allowClaim = false
							authority.mu.Unlock()
						case "canceled":
							cancel()
						}
						return nil
					}
					f.m.UseData(data)
					err := f.m.DrainWorkOutboxWithPolicy(ctx, f.tenant, 1, nil)
					if mode == "canceled" {
						if !errors.Is(err, context.Canceled) || data.mutations != 0 {
							t.Fatalf("canceled discovery: err=%v writes=%d", err, data.mutations)
						}
					} else if err != nil {
						t.Fatalf("drain: %v", err)
					}
					if !discovered {
						t.Fatal("the discovery transaction was not exercised")
					}
					row := outboxRowForTest(t, f, eventID)
					if mode == "positive" {
						if row.String(colOutboxState) != "published" || sinkAttemptCount(sink, eventID) != 1 || data.mutations != 2 {
							t.Fatalf("positive delivery: row=%v attempts=%d writes=%d", row, sinkAttemptCount(sink, eventID), data.mutations)
						}
					} else {
						assertHeldRow(t, f, eventID, mode)
						if sinkAttemptCount(sink, eventID) != 0 {
							t.Fatal("delivery crossed a changed due/authority/cancellation boundary")
						}
					}
				})
			}
		})
	}
}

func TestIdleRunLineageDoesNotWrite(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, name := range []string{"no_runs", "already_lineaged"} {
				t.Run(name, func(t *testing.T) {
					cfg := be.config(t)
					if cfg.Engine == store.EngineSQLite {
						cfg.DSN = filepath.Join(t.TempDir(), "idle-lineage.db")
					}
					f := newWorkFixture(t, cfg.DSN, func(c *store.Config) { *c = cfg })
					t.Cleanup(func() { _ = f.st.Close() })
					cursor := RunLineageRepairCursor{}
					if name == "already_lineaged" {
						if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
							repo, err := sc.Ext(runKind)
							if err != nil {
								return err
							}
							row, err := repo.Create(context.Background(), model.Record{
								colRunRef: "lineaged-" + model.NewID().String(), colTransport: string(TransportStreamJSON),
								colPermissionMode: "",
								colIsolation:      string(IsolationNative), colState: stateStopped, colLastEventSeq: int64(0),
								colRunAuthzWorkspaceID: f.workspace.String(),
							})
							cursor.UpperBound = model.ID(row.String(model.ColID))
							return err
						}); err != nil {
							t.Fatalf("seed lineaged run: %v", err)
						}
					}
					data := &idleMaintenanceData{ModuleData: f.m.Data}
					f.m.UseData(data)
					before := maintenanceWAL(t, cfg)
					for i := 0; i < 3; i++ {
						got, err := f.m.RepairRunLineage(context.Background(), f.tenant, cursor)
						if err != nil || got != (RunLineageRepairResult{Exhausted: true}) {
							t.Fatalf("idle repair: %+v, %v", got, err)
						}
						cursor = RunLineageRepairCursor{}
					}
					if data.mutations != 0 || !bytes.Equal(before, maintenanceWAL(t, cfg)) {
						t.Fatalf("empty repair committed writes: Mutate=%d", data.mutations)
					}
				})
			}
		})
	}
}

func TestRunLineageDiscoveryResamplesCandidatesAndIdentity(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, mode := range []string{"candidate_changed", "identity_changed", "new_bound", "positive"} {
				t.Run(mode, func(t *testing.T) {
					f := newManagedStopFixture(t, be.config(t))
					dto, live := f.launch("lineage-discovery-" + mode)
					if stopped, err := m2stop(context.Background(), f, dto.RunRef); err != nil || stopped.State != stateStopped {
						t.Fatalf("stop the fixture before measuring maintenance: %+v, %v", stopped, err)
					}
					f.clearRunLineage(dto.RunRef)
					discovered := false
					data := &idleMaintenanceData{ModuleData: f.m.Data}
					data.postView = func() error {
						discovered = true
						switch mode {
						case "candidate_changed":
							return f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
								repo, err := sc.Ext(runKind)
								if err != nil {
									return err
								}
								row, err := findRunRec(context.Background(), repo, dto.RunRef)
								if err != nil {
									return err
								}
								row[colRunAuthzWorkspaceID] = f.workspace.String()
								_, err = repo.Update(context.Background(), row)
								return err
							})
						case "identity_changed":
							f.mergeIdentity(t, live.claim.SID, "osn_"+model.NewID().String())
						case "new_bound":
							time.Sleep(5 * time.Millisecond)
							f.insertLegacyRuns(1)
						}
						return nil
					}
					f.m.UseData(data)
					got, err := f.m.RepairRunLineage(context.Background(), f.tenant, RunLineageRepairCursor{})
					if err != nil || !discovered || data.mutations != 1 {
						t.Fatalf("repair after discovery: %+v, %v, discovered=%t writes=%d", got, err, discovered, data.mutations)
					}
					want := RunLineageRepairResult{Scanned: 1, Repaired: 1, Exhausted: true}
					switch mode {
					case "candidate_changed":
						want = RunLineageRepairResult{Exhausted: true}
					case "identity_changed":
						want = RunLineageRepairResult{Scanned: 1, Unresolved: 1, Exhausted: true}
					case "new_bound":
						want.Scanned, want.Unresolved = 2, 1
					}
					if got != want {
						t.Fatalf("repair = %+v, want %+v", got, want)
					}
					lineage, null := f.runLineage(dto.RunRef)
					if mode == "identity_changed" {
						if !null {
							t.Fatalf("changed identity acquired stale lineage %q", lineage)
						}
					} else if null || lineage != f.workspace.String() {
						t.Fatalf("persisted lineage = %q, null=%t", lineage, null)
					}
				})
			}
		})
	}
}
