// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func stopSnapshot(t *testing.T, st store.Store, m *Module, tenant model.TenantID) KillSwitchSnapshot {
	t.Helper()
	var out KillSwitchSnapshot
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		out, err = m.LockKillSwitchState(context.Background(), sc)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func engageForGeneration(ctx context.Context, m *Module, sc store.Scope, ref string) error {
	_, err := m.engageKillSwitchLocked(ctx, sc, ksEngageParams{
		ScopeKind: ksScopeAgent, ScopeRef: ref, Reason: "generation test", Source: ksSourceOperator,
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
	}, model.NewTimestamp(intBase))
	return err
}

// These are real-store tests on both engines, not mocked generation counters.
func TestKillSwitchGenerationDomainAndRollback(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			st, tenant := openRevisionStore(t, eng)
			ctx := context.Background()
			m := New()
			first := stopSnapshot(t, st, m, tenant)
			if first.Tenant() != tenant || first.Generation() != 1 || first.State().Any() {
				t.Fatalf("initial = %+v", first)
			}
			var other model.TenantID
			if err := st.System(ctx, func(sys store.SystemScope) error {
				org, err := sys.CreateOrg(ctx, model.Org{Name: "Other", Slug: "other", Status: model.StatusActive})
				other = model.TenantID(org.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return engageForGeneration(ctx, m, sc, "agent-a") }); err != nil {
					t.Fatal(err)
				}
				current := stopSnapshot(t, st, m, tenant)
				if current.Generation() != 2 || !current.State().Any() {
					t.Fatalf("new/idempotent engage %d = %+v", i, current)
				}
			}
			copied := stopSnapshot(t, st, m, tenant)
			state := copied.State()
			delete(state.AgentRefs, "agent-a")
			state.AgentRefs["invented"] = model.NewID()
			if _, ok := copied.State().Stopped("agent-a"); !ok {
				t.Fatal("caller changed snapshot")
			}
			if _, ok := copied.State().Stopped("invented"); ok {
				t.Fatal("caller added stop to snapshot")
			}
			if otherState := stopSnapshot(t, st, m, other); otherState.Generation() != 1 || otherState.State().Any() {
				t.Fatal("cross-tenant posture/generation")
			}

			rollback := errors.New("rollback after returned snapshot")
			var uncommitted KillSwitchSnapshot
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				if err := engageForGeneration(ctx, m, sc, "rolled-back"); err != nil {
					return err
				}
				var err error
				uncommitted, err = m.LockKillSwitchState(ctx, sc)
				if err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) || uncommitted.Generation() != 3 {
				t.Fatalf("rollback observation = %+v, %v", uncommitted, err)
			}
			// A snapshot can describe uncommitted state: it is deliberately no commit receipt.
			durable := stopSnapshot(t, st, m, tenant)
			if durable.Generation() != 2 {
				t.Fatal("rollback advanced durable generation")
			}
			if _, stopped := durable.State().Stopped("rolled-back"); stopped {
				t.Fatal("rollback left stop")
			}
			failedCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			err = st.Mutate(failedCtx, tenant, func(sc store.Scope) error {
				if err := engageForGeneration(failedCtx, m, sc, "failed-commit"); err != nil {
					return err
				}
				uncommitted, err = m.LockKillSwitchState(failedCtx, sc)
				if err != nil {
					return err
				}
				cancel() // database/sql must refuse Commit even though the callback returns nil.
				return nil
			})
			if err == nil || uncommitted.Generation() != 3 {
				t.Fatalf("canceled commit = %v, snapshot %+v", err, uncommitted)
			}
			durable = stopSnapshot(t, st, m, tenant)
			if durable.Generation() != 2 {
				t.Fatal("failed commit advanced generation")
			}
			if _, stopped := durable.State().Stopped("failed-commit"); stopped {
				t.Fatal("failed commit left stop")
			}
		})
	}
}

func TestKillSwitchGenerationBootstrapsExistingStops(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			st, tenant := openRevisionStore(t, eng)
			ctx := context.Background()
			var stopID model.ID
			// Explicit legacy fixture: an active row predates the generation relation.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(killSwitchKind)
				if err != nil {
					return err
				}
				row, err := repo.Create(ctx, model.Record{
					colKSScopeKind: ksScopeEstate, colKSStatus: ksStatusActive, colKSReason: "legacy",
					colKSSource: ksSourceOperator, colKSEngagedBy: model.ActorSystem,
					colKSEngagedAAL: int64(0), colKSEngagedAt: model.NewTimestamp(intBase).String(),
					colKSEngageSeq: int64(0), colKSRevokedCount: int64(0), colKSReviewed: false,
					colKSActiveGuard: "stop:estate",
				})
				stopID = model.ID(row.String(model.ColID))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			got := stopSnapshot(t, st, New(), tenant)
			if got.Generation() != 1 || !got.State().EstateStopped || got.State().EstateStopID != stopID {
				t.Fatalf("legacy bootstrap = %+v", got)
			}
		})
	}
}

// Embedding Scope deliberately removes its optional transaction capability.
type noStopLocker struct{ store.Scope }
type stopTenantOverride struct {
	store.Scope
	tenant model.TenantID
}

func (s stopTenantOverride) Tenant() model.TenantID { return s.tenant }

type failedStopLocker struct {
	store.Scope
	err error
}

func (s failedStopLocker) LockTransaction(context.Context, string) error { return s.err }

type stopGenerationRows struct {
	store.GenericRepo
	rows []model.Record
}

func (r stopGenerationRows) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return r.rows, model.Page{}, nil
}

type stopGenerationScope struct {
	store.Scope
	rows []model.Record
}

func (s stopGenerationScope) LockTransaction(ctx context.Context, key string) error {
	return s.Scope.(store.TransactionLocker).LockTransaction(ctx, key)
}
func (s stopGenerationScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == killSwitchGenerationKind {
		return stopGenerationRows{GenericRepo: repo, rows: s.rows}, nil
	}
	return repo, err
}

func TestKillSwitchGenerationRefusesUnavailableOrCorrupt(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			st, tenant := openRevisionStore(t, eng)
			ctx := context.Background()
			m := New()
			if got, err := m.LockKillSwitchState(ctx, nil); err == nil || got.Generation() != 0 {
				t.Fatal("nil scope accepted")
			}
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				got, err := m.LockKillSwitchState(ctx, sc)
				if !errors.Is(err, store.ErrReadOnly) || got.Generation() != 0 {
					t.Fatalf("View = %+v, %v", got, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				refused := errors.New("lock failed")
				cases := []store.Scope{noStopLocker{sc}, failedStopLocker{sc, refused}}
				for _, invalid := range []model.TenantID{"", model.SystemTenantID, "invalid", model.TenantID(strings.ToUpper(tenant.String()))} {
					// UUIDs containing no letters are canonical in either case.
					if invalid == tenant {
						continue
					}
					cases = append(cases, stopTenantOverride{sc, invalid})
				}
				for _, candidate := range cases {
					if got, err := m.LockKillSwitchState(ctx, candidate); err == nil || got.Generation() != 0 {
						t.Fatalf("unavailable scope accepted: %T", candidate)
					}
				}
				if _, err := m.LockKillSwitchState(ctx, failedStopLocker{sc, refused}); !errors.Is(err, refused) {
					t.Fatal("lock error lost")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stopSnapshot(t, st, m, tenant)
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(killSwitchGenerationKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Limit: 2})
				if err != nil {
					return err
				}
				good := rows[0]
				for name, change := range map[string]func(model.Record){
					"zero generation":     func(r model.Record) { r[colKSGeneration] = int64(0) },
					"negative generation": func(r model.Record) { r[colKSGeneration] = int64(-1) },
					"missing generation":  func(r model.Record) { delete(r, colKSGeneration) },
					"wrong numeric shape": func(r model.Record) { r[colKSGeneration] = "1" },
					"zero version":        func(r model.Record) { r[model.ColVersion] = int64(0) },
					"wrong tenant":        func(r model.Record) { r[model.ColTenantID] = model.NewID().String() },
					"bad id":              func(r model.Record) { r[model.ColID] = "not-an-id" },
				} {
					row := model.Record{}
					for k, v := range good {
						row[k] = v
					}
					change(row)
					if got, err := m.LockKillSwitchState(ctx, stopGenerationScope{sc, []model.Record{row}}); err == nil || got.Generation() != 0 {
						t.Errorf("%s accepted", name)
					}
				}
				if _, err := m.LockKillSwitchState(ctx, stopGenerationScope{sc, []model.Record{good, good}}); err == nil {
					t.Error("duplicate singleton accepted")
				}
				versionExhausted := model.Record{}
				for k, v := range good {
					versionExhausted[k] = v
				}
				versionExhausted[model.ColVersion] = int64(math.MaxInt64)
				if err := engageForGeneration(ctx, m, stopGenerationScope{sc, []model.Record{versionExhausted}}, "version-overflow"); err == nil {
					t.Error("version overflow accepted")
				}
				// Read an old version, then advance the real row version. The writer
				// must propagate the actual generic repository's CAS conflict.
				stale := model.Record{}
				for k, v := range good {
					stale[k] = v
				}
				good, err = repo.Update(ctx, good)
				if err != nil {
					return err
				}
				if err := engageForGeneration(ctx, m, stopGenerationScope{sc, []model.Record{stale}}, "stale-cas"); !errors.Is(err, store.ErrConflict) {
					t.Errorf("stale generation CAS = %v", err)
				}
				// This corruption is persisted through the actual repository, not a fake.
				good[colKSGeneration] = int64(0)
				_, err = repo.Update(ctx, good)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { _, err := m.LockKillSwitchState(ctx, sc); return err }); err == nil {
				t.Fatal("stored zero accepted")
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(killSwitchGenerationKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Limit: 2})
				if err != nil {
					return err
				}
				rows[0][colKSGeneration] = int64(math.MaxInt64)
				_, err = repo.Update(ctx, rows[0])
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error { return engageForGeneration(ctx, m, sc, "overflow") }); err == nil {
				t.Fatal("generation overflow accepted")
			}
			state := stopSnapshot(t, st, m, tenant)
			if state.Generation() != math.MaxInt64 || state.State().Any() {
				t.Fatal("overflow changed posture or wrapped generation")
			}
		})
	}
}

// The probe forwards the REAL transaction lock. Protected repo access before
// the first fence is an error, so deleting an early guardian/tier lock fails
// even when the engage primitive still takes its later lock.
type stopOrderScope struct {
	store.Scope
	locked bool
	refuse bool
	locks  *int
}

func (s *stopOrderScope) LockTransaction(ctx context.Context, key string) error {
	if s.refuse {
		return errors.New("quarantine unexpectedly took stop fence")
	}
	if key != "governance.killswitch:"+s.Tenant().String() {
		return errors.New("wrong stop fence key")
	}
	if err := s.Scope.(store.TransactionLocker).LockTransaction(ctx, key); err != nil {
		return err
	}
	s.locked = true
	*s.locks++
	return nil
}
func (s *stopOrderScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if !s.refuse && !s.locked {
		switch kind {
		case guardianActionKind, tierFloorSignalKind, killSwitchKind, approvalKind:
			return nil, errors.New("stop writer accessed protected repo before fence")
		}
	}
	return s.Scope.Ext(kind)
}

func TestKillSwitchGenerationGuardianAndTierWriterOrder(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			st, tenant := openRevisionStore(t, eng)
			ctx := context.Background()
			clk := &intClock{t: intBase}
			m := New(WithClock(clk))
			host := &capturingHost{}
			if err := m.Init(ctx, host); err != nil {
				t.Fatal(err)
			}
			locks := 0
			m.UseData(cedarEpochModuleData{st: st, mutateWrap: func(sc store.Scope) store.Scope { return &stopOrderScope{Scope: sc, locks: &locks} }})
			f := &guardianFixture{t: t, m: m, st: st, host: host, tenant: tenant}
			generation := func(want int64) {
				t.Helper()
				if got := stopSnapshot(t, st, m, tenant).Generation(); got != want {
					t.Fatalf("generation = %d, want %d", got, want)
				}
			}
			generation(1)
			f.createRule("auto", "auto-finding", "high", gaActionStopAgent, gaModeAuto)
			f.fire("auto-finding", sdkmodel.SeverityHigh, "agent", "auto-agent", "a")
			generation(2)
			f.fire("auto-finding", sdkmodel.SeverityHigh, "agent", "auto-agent", "a")
			generation(2)
			f.createRule("approved", "approval-finding", "high", gaActionStopEstate, gaModeApproval)
			f.fire("approval-finding", sdkmodel.SeverityHigh, "agent", "approval-agent", "b")
			generation(2)
			if _, err := m.GuardianSweep(ctx, tenant); err != nil {
				t.Fatal(err)
			}
			generation(2)
			var action model.Record
			for _, a := range f.actions() {
				if a.String(colGAStatus) == gaStatusPending {
					action = a
				}
			}
			if action == nil {
				t.Fatal("pending writer failed before action")
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(approvalKind)
				if err != nil {
					return err
				}
				row, err := repo.Get(ctx, model.ID(action.String(colGAApprovalID)))
				if err != nil {
					return err
				}
				// Explicit approval fixture with the one human a decision records; sweep itself is real.
				row[colStatus], row[colApproveCount] = statusApproved, int64(1)
				_, err = repo.Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if res, err := m.GuardianSweep(ctx, tenant); err != nil || res.Executed != 1 {
				t.Fatalf("approved sweep = %+v %v", res, err)
			}
			generation(3)
			if _, err := m.GuardianSweep(ctx, tenant); err != nil {
				t.Fatal(err)
			}
			generation(3)
			tf := &tierFloorFixture{t: t, st: st, m: m, clk: clk, host: host, tenant: tenant}
			tf.createAgent(tenant, "high-agent", "high-ref", string(RiskTierHigh))
			tf.fire(tenant, sdkmodel.SeverityHigh, "high-ref", "one")
			generation(3)
			tf.fire(tenant, sdkmodel.SeverityHigh, "high-ref", "two")
			generation(4)
			tf.fire(tenant, sdkmodel.SeverityHigh, "high-ref", "two")
			generation(4)
			tf.createAgent(tenant, "critical-agent", "critical-ref", string(RiskTierCritical))
			tf.fire(tenant, sdkmodel.SeverityHigh, "critical-ref", "critical")
			generation(5)
			if locks < 6 {
				t.Fatalf("writers did not reach real fence: %d", locks)
			}

			// Quarantine is an identity arm: it must never obtain the governance lock.
			f.createRule("quarantine", "quarantine-finding", "high", gaActionQuarantineNHI, gaModeAuto)
			m.UseData(cedarEpochModuleData{st: st, mutateWrap: func(sc store.Scope) store.Scope { return &stopOrderScope{Scope: sc, refuse: true, locks: &locks} }})
			f.fire("quarantine-finding", sdkmodel.SeverityHigh, "identity", "test-identity", "q")
			found := false
			for _, a := range f.actions() {
				if a.String(colGAAction) == gaActionQuarantineNHI && a.String(colGAStatus) == gaStatusExecuted {
					found = true
				}
			}
			if !found {
				t.Fatal("quarantine did not execute its identity arm")
			}
			generation(5)
		})
	}
}

func TestKillSwitchGenerationGuardianRefusesMovedArm(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			for _, arms := range [][2]string{{gaActionQuarantineNHI, gaActionStopAgent}, {gaActionStopAgent, gaActionQuarantineNHI}} {
				t.Run(arms[0], func(t *testing.T) {
					st, tenant := openRevisionStore(t, eng)
					ctx := context.Background()
					m := New(WithClock(&intClock{t: intBase}))
					m.UseData(api.NewModuleData(st))
					host := &capturingHost{}
					if err := m.Init(ctx, host); err != nil {
						t.Fatal(err)
					}
					f := &guardianFixture{t: t, m: m, st: st, host: host, tenant: tenant}
					f.createRule("pending", "moved-arm", "high", arms[0], gaModeApproval)
					f.fire("moved-arm", sdkmodel.SeverityHigh, "agent", "agent", "m")
					actions := f.actions()
					if len(actions) != 1 {
						t.Fatal("missing pending action")
					}
					action := actions[0]
					changed := false
					m.UseData(cedarEpochModuleData{st: st, afterView: func() {
						if changed {
							return
						}
						changed = true
						if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
							repo, err := sc.Ext(guardianActionKind)
							if err != nil {
								return err
							}
							row, err := repo.Get(ctx, model.ID(action.String(model.ColID)))
							if err != nil {
								return err
							}
							row[colGAAction] = arms[1]
							if _, err = repo.Update(ctx, row); err != nil {
								return err
							}
							approvals, err := sc.Ext(approvalKind)
							if err != nil {
								return err
							}
							approval, err := approvals.Get(ctx, model.ID(action.String(colGAApprovalID)))
							if err != nil {
								return err
							}
							approval[colStatus] = statusApproved
							_, err = approvals.Update(ctx, approval)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}})
					if _, err := m.GuardianSweep(ctx, tenant); err == nil {
						t.Fatal("changed containment arm executed")
					}
					if got := stopSnapshot(t, st, m, tenant); got.Generation() != 1 || got.State().Any() {
						t.Fatalf("moved arm changed stops: %+v", got)
					}
					if got := f.actions(); got[0].String(colGAStatus) != gaStatusPending {
						t.Fatal("moved arm executed a containment")
					}
				})
			}
		})
	}
}

func TestKillSwitchSnapshotSerializesRealWriter(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			st, tenant := openRevisionStore(t, eng)
			m := New()
			stopSnapshot(t, st, m, tenant) // Bootstrap before the contention witness.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			held, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			readerDone, writerDone := make(chan error, 1), make(chan error, 1)
			go func() {
				readerDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
					before, err := m.LockKillSwitchState(ctx, sc)
					if err != nil {
						return err
					}
					close(held)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					after, err := m.LockKillSwitchState(ctx, sc)
					if err != nil {
						return err
					}
					if before.Generation() != 1 || after.Generation() != 1 || after.State().Any() {
						return errors.New("writer crossed held snapshot barrier")
					}
					return nil
				})
			}()
			select {
			case <-held:
			case err := <-readerDone:
				t.Fatalf("reader failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() {
				close(attempted)
				writerDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error { return engageForGeneration(ctx, m, sc, "concurrent") })
			}()
			<-attempted
			select {
			case err := <-writerDone:
				close(release)
				<-readerDone
				t.Fatalf("writer completed while barrier held: %v", err)
			case <-time.After(150 * time.Millisecond): // Bounded negative interval; final row assertions follow.
			}
			close(release)
			if err := <-readerDone; err != nil {
				t.Fatal(err)
			}
			if err := <-writerDone; err != nil {
				t.Fatal(err)
			}
			after := stopSnapshot(t, st, m, tenant)
			if after.Generation() != 2 || !after.State().Any() {
				t.Fatalf("released writer = %+v", after)
			}
		})
	}
}
