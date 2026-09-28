// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
)

// admissionJobNames are the loop's two jobs, in the order a table over them runs.
var admissionJobNames = []string{admissionRecoverJobName, admissionReconcileJobName}

// stagedHoldMicroUSD is what every staged ledger row withholds: two dollars.
const stagedHoldMicroUSD = int64(2_000_000)

// forEachServedEngine runs body once on SQLite and once on PostgreSQL. The loop finds its
// tenants through a cross-tenant System read, which on PostgreSQL needs the admin role, so
// that leg configures it as a deployment does. Without a PostgreSQL server the leg is
// skipped by name, and the skip says it is not a pass.
func forEachServedEngine(t *testing.T, body func(*testing.T, store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		body(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: this PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		pg := enginetest.IsolatedPostgres(t)
		body(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, OwnerDSN: pg.Owner, MaxConns: 8})
	})
}

// bootConfigOn is the composition root's configuration for the store cfg names.
func bootConfigOn(t *testing.T, cfg store.Config, log *slog.Logger) bootConfig {
	t.Helper()
	return bootConfig{
		DataDir: t.TempDir(), Engine: string(cfg.Engine), DSN: cfg.DSN, AdminDSN: cfg.AdminDSN,
		OwnerDSN: cfg.OwnerDSN, Version: "test", Logger: log,
	}
}

// addFinOpsOrg provisions one more active business org on an open store, so a pass has
// more than one tenant to visit.
func addFinOpsOrg(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision %s: %v", slug, err)
	}
	return tenant
}

// setOrgStatus withdraws or restores a tenant's service through the System path.
func setOrgStatus(t *testing.T, st store.Store, tenant model.TenantID, status model.LifecycleStatus) {
	t.Helper()
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(context.Background(), tenant, status)
		return err
	}); err != nil {
		t.Fatalf("set %s to %s: %v", tenant, status, err)
	}
}

// heldRow is one active ledger row of hold under a policy of its own, withholding
// stagedHoldMicroUSD until five minutes after claimed.
func heldRow(hold, kind, dimension, scopeKey, period string, claimed time.Time) model.Record {
	start := time.Date(claimed.Year(), claimed.Month(), 1, 0, 0, 0, 0, time.UTC)
	if period == "daily" {
		start = time.Date(claimed.Year(), claimed.Month(), claimed.Day(), 0, 0, 0, 0, time.UTC)
	}
	return model.Record{
		"policy_ref":       model.NewID().String(),
		"policy_kind":      kind,
		"dimension":        dimension,
		"dim_key":          scopeKey,
		"period":           period,
		"period_start":     model.NewTimestamp(start).String(),
		"seq":              int64(1),
		"amount_micro_usd": stagedHoldMicroUSD,
		"actual_micro_usd": int64(0),
		"state":            "active",
		"handle":           hold,
		"expires_at":       model.NewTimestamp(claimed.Add(5 * time.Minute)).String(),
	}
}

// stagedRow is one row written straight through the store, as the writer that left it
// did.
type stagedRow struct {
	kind model.Kind
	rec  model.Record
}

// stageRows writes rows into the tenant's store in one transaction, so no pass can see a
// part of them.
func stageRows(t *testing.T, st store.Store, tenant model.TenantID, rows ...stagedRow) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, row := range rows {
			repo, err := sc.Ext(row.kind)
			if err != nil {
				return err
			}
			if _, err := repo.Create(ctx, row.rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("stage rows: %v", err)
	}
}

// stageStaleClaim writes what a caller leaves when it stops after creating its hold and
// before publishing it: the claim of key, pending and naming the hold, taken a minute ago —
// past the thirty seconds after which a claim is no longer a caller in flight — and the
// hold's two ledger rows, a budget and a spend limit, withholding until five minutes after
// the claim. Nobody retries the key. It returns the hold.
func stageStaleClaim(t *testing.T, st store.Store, tenant model.TenantID, key string) string {
	t.Helper()
	claimed := time.Now().UTC().Add(-time.Minute)
	hold := model.NewID().String()
	stageRows(t, st, tenant,
		stagedRow{finopsAdmissionKind, model.Record{
			"idempotency_key":    key,
			"payload_hash":       "hash-" + key,
			"handle":             hold,
			"scope":              finops.AdmissionScopeModelGateway,
			"estimate_micro_usd": stagedHoldMicroUSD,
			"state":              "pending",
			"state_at":           model.NewTimestamp(claimed).String(),
		}},
		stagedRow{finopsReservationKind, heldRow(hold, "budget", "global", "", "monthly", claimed)},
		stagedRow{finopsReservationKind, heldRow(hold, "spend_limit", "spend_limit", "a", "daily", claimed)})
	return hold
}

// stageLapsedHold writes one active ledger row whose TTL passed an hour ago: the durable
// trace of a caller that took headroom and died before settling it.
func stageLapsedHold(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	stageRows(t, st, tenant, stagedRow{finopsReservationKind,
		heldRow(model.NewID().String(), "budget", "global", "", "monthly", time.Now().UTC().Add(-65*time.Minute))})
}

// stageLegacyClaim writes a claim the earlier admission build staged and left: pending,
// naming no hold, dated ten minutes ago.
func stageLegacyClaim(t *testing.T, st store.Store, tenant model.TenantID, key string) {
	t.Helper()
	stageRows(t, st, tenant, stagedRow{finopsAdmissionKind, model.Record{
		"idempotency_key":    key,
		"payload_hash":       "hash-" + key,
		"handle":             "",
		"scope":              finops.AdmissionScopeModelGateway,
		"estimate_micro_usd": stagedHoldMicroUSD,
		"state":              "pending",
		"state_at":           model.NewTimestamp(time.Now().UTC().Add(-10 * time.Minute)).String(),
	}})
}

// admissionRowOf is the stored admission row of key.
func admissionRowOf(t *testing.T, st store.Store, tenant model.TenantID, key string) model.Record {
	t.Helper()
	for _, r := range listFinOpsRows(t, st, tenant, finopsAdmissionKind) {
		if r.String("idempotency_key") == key {
			return r
		}
	}
	t.Fatalf("no admission row for %s in %s", key, tenant)
	return nil
}

// rowsUnder lists the ledger rows of hold.
func rowsUnder(t *testing.T, st store.Store, tenant model.TenantID, hold string) []model.Record {
	t.Helper()
	var out []model.Record
	for _, r := range listFinOpsRows(t, st, tenant, finopsReservationKind) {
		if r.String("handle") == hold {
			out = append(out, r)
		}
	}
	return out
}

// timestampOf reads a timestamp column of a stored row.
func timestampOf(t *testing.T, r model.Record, col string) time.Time {
	t.Helper()
	ts, err := model.ParseTimestamp(r.String(col))
	if err != nil {
		t.Fatalf("%s of row %s is %q: %v", col, r.String(model.ColID), r.String(col), err)
	}
	return ts.Time()
}

// assertClaimRetired checks the rows a stale claim's recovery leaves: the claim released,
// naming and owing nothing, and both rows of its hold released with an actual of zero,
// settled between from and to and before they would have lapsed.
func assertClaimRetired(t *testing.T, st store.Store, tenant model.TenantID, key, hold string, from, to time.Time) {
	t.Helper()
	row := admissionRowOf(t, st, tenant, key)
	if row.String("state") != "released" || row.String("handle") != "" || row.String("spend_handle") != "" || row.String("owed_handles") != "" {
		t.Fatalf("the claim of %s is %s naming %q/%q owing %q; want released, naming and owing nothing",
			key, row.String("state"), row.String("handle"), row.String("spend_handle"), row.String("owed_handles"))
	}
	rows := rowsUnder(t, st, tenant, hold)
	if len(rows) != 2 {
		t.Fatalf("%d ledger rows under the hold of %s, want its 2", len(rows), key)
	}
	for _, r := range rows {
		settled, expires := timestampOf(t, r, "settled_at"), timestampOf(t, r, "expires_at")
		if r.String("state") != "released" || r.Int("actual_micro_usd") != 0 {
			t.Fatalf("a %s row of the hold of %s is %s with actual %d; want released with actual 0",
				r.String("policy_kind"), key, r.String("state"), r.Int("actual_micro_usd"))
		}
		if settled.Before(from) || settled.After(to) || !settled.Before(expires) {
			t.Fatalf("a %s row of the hold of %s was settled at %s; want within the pass [%s, %s] and before it lapses at %s",
				r.String("policy_kind"), key, settled, from, to, expires)
		}
	}
}

// assertClaimPending checks that nothing touched a stale claim: still pending and naming
// its hold, and both rows of the hold still active.
func assertClaimPending(t *testing.T, st store.Store, tenant model.TenantID, key, hold string) {
	t.Helper()
	row := admissionRowOf(t, st, tenant, key)
	if row.String("state") != "pending" || row.String("handle") != hold {
		t.Fatalf("the claim of %s is %s naming %q; want it untouched, pending and naming %s", key, row.String("state"), row.String("handle"), hold)
	}
	rows := rowsUnder(t, st, tenant, hold)
	if len(rows) != 2 {
		t.Fatalf("%d ledger rows under the hold of %s, want its 2", len(rows), key)
	}
	for _, r := range rows {
		if r.String("state") != "active" || r.String("settled_at") != "" {
			t.Fatalf("a %s row of the untouched hold of %s is %s settled %q; want active", r.String("policy_kind"), key, r.String("state"), r.String("settled_at"))
		}
	}
}

// inspectOK is the read route's report for the tenant.
func inspectOK(t *testing.T, fin *finops.Module, tenant model.TenantID) finops.AdmissionReconciliation {
	t.Helper()
	report, err := fin.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect %s: %v", tenant, err)
	}
	return report
}

// ledgerState is every admission and ledger row of the tenant as stored — id, version,
// state, hold, actual and settlement — in a stable order, to show that a pass wrote nothing.
func ledgerState(t *testing.T, st store.Store, tenant model.TenantID) string {
	t.Helper()
	var lines []string
	for _, kind := range []model.Kind{finopsAdmissionKind, finopsReservationKind} {
		for _, r := range listFinOpsRows(t, st, tenant, kind) {
			lines = append(lines, fmt.Sprintf("%s %s v%d %s %q %d %q", kind, r.String(model.ColID), r.Int(model.ColVersion),
				r.String("state"), r.String("handle"), r.Int("actual_micro_usd"), r.String("settled_at")))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// runJob runs one tick of the named job of l.
func runJob(ctx context.Context, l *admissionReconciler, job string) error {
	switch job {
	case admissionRecoverJobName:
		return l.recoverOnce(ctx)
	case admissionReconcileJobName:
		return l.reconcileOnce(ctx)
	}
	return fmt.Errorf("no admission job %q", job)
}

// recordsOf is what log captured, in order.
func recordsOf(log *loopLog) []loopRecord {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]loopRecord(nil), log.recs...)
}

// admissionTick runs one tick of job over st and fin and returns what it logged and
// returned.
func admissionTick(t *testing.T, st store.Store, fin *finops.Module, job string) ([]loopRecord, error) {
	t.Helper()
	log := &loopLog{}
	l := newAdmissionReconciler(st, fin, slog.New(log))
	if l == nil {
		t.Fatal("the loop must be built when FinOps is composed")
	}
	err := runJob(context.Background(), l, job)
	return recordsOf(log), err
}

// admissionTickOK is admissionTick for a tick that must not fail.
func admissionTickOK(t *testing.T, st store.Store, fin *finops.Module, job string) []loopRecord {
	t.Helper()
	recs, err := admissionTick(t, st, fin, job)
	if err != nil {
		t.Fatalf("%s tick: %v", job, err)
	}
	return recs
}

// passLine is the one line a tick logged for the tenant's completed pass.
func passLine(t *testing.T, recs []loopRecord, tenant model.TenantID) loopRecord {
	t.Helper()
	var found []loopRecord
	for _, r := range recs {
		if r.attrs["tenant"] == tenant.String() && strings.HasSuffix(r.msg, ": tenant pass completed") {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d completed-pass lines for %s, want 1; the tick logged %v", len(found), tenant, recs)
	}
	return found[0]
}

// linesAt are the records of recs at level.
func linesAt(recs []loopRecord, level slog.Level) []loopRecord {
	var out []loopRecord
	for _, r := range recs {
		if r.level == level {
			out = append(out, r)
		}
	}
	return out
}

// assertCounts checks the named counts of a pass line.
func assertCounts(t *testing.T, line loopRecord, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if line.attrs[k] != v {
			t.Fatalf("%s = %v (%T), want %v (%T); line %q %v", k, line.attrs[k], line.attrs[k], v, v, line.msg, line.attrs)
		}
	}
}

// assertCountsOnly fails when a record carries any of the stored values: a key or a hold
// is row content, and the loop's lines carry counts only.
func assertCountsOnly(t *testing.T, recs []loopRecord, stored ...string) {
	t.Helper()
	for _, r := range recs {
		text := r.msg + " " + fmt.Sprint(r.attrs)
		for _, v := range stored {
			if strings.Contains(text, v) {
				t.Fatalf("a line carries the stored value %q: %q %v", v, r.msg, r.attrs)
			}
		}
	}
}

// TestAdmissionReconciler_CompletesIdleTenant: recovery reaches the tenants no request
// reaches. Two tenants each hold the claim of a caller that stopped after creating its hold
// and before publishing it, and nobody retries either key. One tick of the recover job
// retires both claims and releases both holds, settled before they would have lapsed. The
// two reconcile passes over each tenant that follow are quiet, so neither leaves anything
// outstanding, and the read route agrees.
func TestAdmissionReconciler_CompletesIdleTenant(t *testing.T) {
	forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
		fin, st, tenantA := openFinOpsEngineOn(t, cfg)
		tenantB := addFinOpsOrg(t, st, "admission-idle-b")
		keys := map[model.TenantID]string{tenantA: "model_gateway/idle-a", tenantB: "model_gateway/idle-b"}
		holds := map[model.TenantID]string{}
		for tenant, key := range keys {
			holds[tenant] = stageStaleClaim(t, st, tenant, key)
		}

		from := time.Now().UTC()
		recs := admissionTickOK(t, st, fin, admissionRecoverJobName)
		to := time.Now().UTC()
		for tenant, key := range keys {
			assertCounts(t, passLine(t, recs, tenant), map[string]any{
				"pending_retired": int64(1), "owed_released": int64(2), "outstanding": int64(0),
			})
			assertClaimRetired(t, st, tenant, key, holds[tenant], from, to)
		}

		for pass := 1; pass <= 2; pass++ {
			if recs := admissionTickOK(t, st, fin, admissionReconcileJobName); len(recs) != 0 {
				t.Fatalf("reconcile pass %d after the recovery logged %v; with nothing done and nothing outstanding it is quiet", pass, recs)
			}
			for tenant := range keys {
				if report := inspectOK(t, fin, tenant); report.Outstanding() != 0 || !report.Quiet() {
					t.Fatalf("after reconcile pass %d the read route reports %+v for %s; want nothing outstanding", pass, report, tenant)
				}
			}
		}
		for tenant, key := range keys {
			assertClaimRetired(t, st, tenant, key, holds[tenant], from, to)
		}
	})
}

// errLostAcknowledgment is the failure lostRetirementData reports: a lost connection, as
// the store reports one.
var errLostAcknowledgment = fmt.Errorf("admission-reconcile-test: the acknowledgment was lost: %w", store.ErrStoreUnavailable)

// lostRetirementData loses the acknowledgment of the tenant's first write, after the
// write ran and rolled back, or after it committed. Every other call reaches the store.
// fired says the fault decided that write's outcome.
type lostRetirementData struct {
	api.ModuleData
	tenant    model.TenantID
	committed bool
	writes    atomic.Int64
	fired     atomic.Bool
}

func (d *lostRetirementData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant != d.tenant || d.writes.Add(1) != 1 {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	if d.committed {
		if err := d.ModuleData.Mutate(ctx, tenant, fn); err != nil {
			return err
		}
		d.fired.Store(true)
		return errLostAcknowledgment
	}
	err := d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		return errLostAcknowledgment
	})
	if errors.Is(err, errLostAcknowledgment) {
		d.fired.Store(true)
	}
	return err
}

// TestAdmissionReconciler_ResolvesUncertainRetire: a retirement whose outcome is unknown
// never ends recovery. When it rolled back after it ran, the tick reports it unresolved and
// outstanding, the claim and its hold are as they were, and the next tick retires the
// claim. When it committed and its acknowledgment was lost, the tick finds it done by the
// row alone, and the next tick is quiet. Either way the rows end as a stale claim's
// recovery leaves them.
func TestAdmissionReconciler_ResolvesUncertainRetire(t *testing.T) {
	for _, c := range []struct {
		name      string
		committed bool
		first     map[string]any
	}{
		{"the retirement rolled back after it ran", false, map[string]any{
			"pending_retired": int64(0), "owed_released": int64(0), "unresolved": int64(1), "outstanding": int64(1),
		}},
		{"the retirement committed and its acknowledgment was lost", true, map[string]any{
			"pending_retired": int64(1), "owed_released": int64(2), "unresolved": int64(0), "outstanding": int64(0),
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
				fin, st, tenant := openFinOpsEngineOn(t, cfg)
				const key = "model_gateway/uncertain-retirement"
				hold := stageStaleClaim(t, st, tenant, key)

				fault := &lostRetirementData{ModuleData: api.NewModuleData(st), tenant: tenant, committed: c.committed}
				fin.UseData(fault)
				from := time.Now().UTC()
				recs := admissionTickOK(t, st, fin, admissionRecoverJobName)
				if !fault.fired.Load() {
					t.Fatal("the injected fault never fired; the test proves nothing")
				}
				assertCounts(t, passLine(t, recs, tenant), c.first)
				if !c.committed {
					assertClaimPending(t, st, tenant, key, hold)
				}

				next := admissionTickOK(t, st, fin, admissionRecoverJobName)
				if c.committed {
					if len(next) != 0 {
						t.Fatalf("the tick after a retirement resolved as done logged %v; want quiet", next)
					}
				} else {
					assertCounts(t, passLine(t, next, tenant), map[string]any{
						"pending_retired": int64(1), "owed_released": int64(2), "unresolved": int64(0), "outstanding": int64(0),
					})
				}
				assertClaimRetired(t, st, tenant, key, hold, from, time.Now().UTC())
			})
		})
	}
}

// recordedJob is one registration a scheduler received.
type recordedJob struct {
	interval  time.Duration
	immediate bool
	run       func(context.Context) error
}

// jobRecorder is a scheduler that records, by name, what it is asked to run.
type jobRecorder struct {
	jobs  map[string]recordedJob
	calls int
}

func (r *jobRecorder) SchedulePeriodic(name string, interval time.Duration, runImmediately bool, job func(context.Context) error) error {
	r.calls++
	r.jobs[name] = recordedJob{interval: interval, immediate: runImmediately, run: job}
	return nil
}

// TestAdmissionReconciler_RegistersBothJobs pins the two jobs and their owner. The recover
// job runs recovery every minute and the reconcile job the sweep and the inspection every
// five minutes, neither at start, and each runs its own pass: recovery never sweeps. The
// runtime's own scheduler accepts both, and the composition root registers them: the booted
// engine retires a stale claim nobody retried, while the lapsed hold beside it waits for
// the reconcile job.
func TestAdmissionReconciler_RegistersBothJobs(t *testing.T) {
	t.Run("the jobs", func(t *testing.T) {
		forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
			ctx := context.Background()
			fin, st, tenant := openFinOpsEngineOn(t, cfg)
			l := newAdmissionReconciler(st, fin, discardLog())
			rec := &jobRecorder{jobs: map[string]recordedJob{}}
			if err := l.register(rec); err != nil {
				t.Fatalf("register: %v", err)
			}
			want := map[string]time.Duration{
				"finops-admission-recover":   time.Minute,
				"finops-admission-reconcile": 5 * time.Minute,
			}
			if rec.calls != len(want) || len(rec.jobs) != len(want) {
				t.Fatalf("%d registrations under %d names, want the %d jobs %v", rec.calls, len(rec.jobs), len(want), want)
			}
			for name, every := range want {
				got, ok := rec.jobs[name]
				if !ok || got.interval != every || got.immediate || got.run == nil {
					t.Fatalf("job %s = %+v (registered %t); want every %s, not at start", name, got, ok, every)
				}
			}

			stageLapsedHold(t, st, tenant)
			if err := rec.jobs["finops-admission-recover"].run(ctx); err != nil {
				t.Fatalf("the recover job: %v", err)
			}
			if report := inspectOK(t, fin, tenant); report.ActiveLapsed != 1 || report.ExpiredUnsettled != 0 {
				t.Fatalf("after the recover job: %+v; recovery never sweeps a lapsed hold", report)
			}
			if err := rec.jobs["finops-admission-reconcile"].run(ctx); err != nil {
				t.Fatalf("the reconcile job: %v", err)
			}
			if report := inspectOK(t, fin, tenant); report.ActiveLapsed != 0 || report.ExpiredUnsettled != 1 {
				t.Fatalf("after the reconcile job: %+v; it sweeps the lapsed hold", report)
			}

			rt := runtime.New(runtime.Options{Logger: discardLog()})
			if err := l.register(rt); err != nil {
				t.Fatalf("register on the runtime: %v", err)
			}
			if err := rt.Start(ctx); err != nil {
				t.Fatalf("start: %v", err)
			}
			if err := rt.Stop(ctx); err != nil {
				t.Fatalf("stop: %v", err)
			}
		})
	})

	t.Run("the composition root", func(t *testing.T) {
		forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
			saved := admissionRecoverInterval
			admissionRecoverInterval = 150 * time.Millisecond
			t.Cleanup(func() { admissionRecoverInterval = saved })

			ctx := context.Background()
			eng, err := boot(ctx, bootConfigOn(t, cfg, discardLog()))
			if err != nil {
				t.Fatalf("boot the composition root: %v", err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			fin, ok := eng.finops.(*finops.Module)
			if !ok {
				t.Fatalf("the booted engine's FinOps is %T, want *finops.Module", eng.finops)
			}
			tenant := addFinOpsOrg(t, eng.store, "admission-boot")

			const key = "model_gateway/boot-stale-claim"
			from := time.Now().UTC()
			hold := stageStaleClaim(t, eng.store, tenant, key)
			stageLapsedHold(t, eng.store, tenant)
			deadline := time.Now().Add(30 * time.Second)
			for admissionRowOf(t, eng.store, tenant, key).String("state") != "released" {
				if time.Now().After(deadline) {
					t.Fatal("the booted scheduler never retired a stale claim nobody retried")
				}
				time.Sleep(50 * time.Millisecond)
			}
			assertClaimRetired(t, eng.store, tenant, key, hold, from, time.Now().UTC())
			if report := inspectOK(t, fin, tenant); report.ActiveLapsed != 1 {
				t.Fatalf("the booted engine swept before its reconcile job was due: %+v", report)
			}
		})
	})
}

// errTenantUnreachable is the failure unreachableTenantData reports for its tenant.
var errTenantUnreachable = fmt.Errorf("admission-reconcile-test: the tenant's store is unreachable: %w", store.ErrStoreUnavailable)

// unreachableTenantData fails every read and write of one tenant; the others reach the
// store.
type unreachableTenantData struct {
	api.ModuleData
	tenant model.TenantID
}

func (d unreachableTenantData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant == d.tenant {
		return errTenantUnreachable
	}
	return d.ModuleData.View(ctx, tenant, fn)
}

func (d unreachableTenantData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant == d.tenant {
		return errTenantUnreachable
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// cancelledPassData ends the tick's context during the first read it serves, and that read
// fails with the context's error, as a store call does when its context ends. Every later
// call reaches the store. fired says it did.
type cancelledPassData struct {
	api.ModuleData
	cancel context.CancelFunc
	reads  atomic.Int64
	fired  atomic.Bool
}

func (d *cancelledPassData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if d.reads.Add(1) != 1 {
		return d.ModuleData.View(ctx, tenant, fn)
	}
	d.cancel()
	d.fired.Store(true)
	return fmt.Errorf("admission-reconcile-test: the read ended with its context: %w", ctx.Err())
}

// TestAdmissionReconciler_NamesFailedTenants: a tenant whose pass fails does not end the
// tick. Its failure is logged with the tenant and the error, every other tenant — those
// after it included — is recovered, and once every tenant has run the tick returns an
// error that says how many passes failed, for the scheduler to log. When the tenant is
// reachable again the next tick recovers it and returns nil. A tick whose context is
// cancelled during a pass is not a failure: it ends with the cancellation, visits no
// further tenant, and neither logs nor counts a failed pass. Both jobs.
func TestAdmissionReconciler_NamesFailedTenants(t *testing.T) {
	for _, job := range admissionJobNames {
		t.Run(job, func(t *testing.T) {
			forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
				fin, st, _ := openFinOpsEngineOn(t, cfg)
				addFinOpsOrg(t, st, "admission-failed-b")
				addFinOpsOrg(t, st, "admission-failed-c")
				tenants, err := servedBusinessTenants(context.Background(), st)
				if err != nil || len(tenants) != 3 {
					t.Fatalf("served tenants = %v, %v; want the 3 provisioned", tenants, err)
				}
				down := tenants[0] // the first a tick visits, so every other comes after it
				const key = "model_gateway/failed-tenant"
				holds := map[model.TenantID]string{}
				for _, tenant := range tenants {
					holds[tenant] = stageStaleClaim(t, st, tenant, key)
				}

				fin.UseData(unreachableTenantData{ModuleData: api.NewModuleData(st), tenant: down})
				from := time.Now().UTC()
				recs, err := admissionTick(t, st, fin, job)
				if err == nil || !strings.Contains(err.Error(), "1 of 3 tenant passes failed") {
					t.Fatalf("the tick returned %v; want an error saying 1 of 3 tenant passes failed", err)
				}
				warns := linesAt(recs, slog.LevelWarn)
				if len(warns) != 1 || warns[0].attrs["tenant"] != down.String() ||
					!strings.Contains(fmt.Sprint(warns[0].attrs["err"]), "unreachable") {
					t.Fatalf("warnings %v; want one naming the failed tenant %s and its error", warns, down)
				}
				for _, tenant := range tenants[1:] {
					assertClaimRetired(t, st, tenant, key, holds[tenant], from, time.Now().UTC())
				}
				assertClaimPending(t, st, down, key, holds[down])

				fin.UseData(api.NewModuleData(st))
				from = time.Now().UTC()
				recs = admissionTickOK(t, st, fin, job)
				assertCounts(t, passLine(t, recs, down), map[string]any{"pending_retired": int64(1), "owed_released": int64(2)})
				assertClaimRetired(t, st, down, key, holds[down], from, time.Now().UTC())
			})

			t.Run("a tick cancelled during a pass", func(t *testing.T) {
				forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
					fin, st, _ := openFinOpsEngineOn(t, cfg)
					addFinOpsOrg(t, st, "admission-cancelled-b")
					tenants, err := servedBusinessTenants(context.Background(), st)
					if err != nil || len(tenants) != 2 {
						t.Fatalf("served tenants = %v, %v; want the 2 provisioned", tenants, err)
					}
					const key = "model_gateway/cancelled-tick"
					holds := map[model.TenantID]string{}
					for _, tenant := range tenants {
						holds[tenant] = stageStaleClaim(t, st, tenant, key)
					}

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					fault := &cancelledPassData{ModuleData: api.NewModuleData(st), cancel: cancel}
					fin.UseData(fault)
					log := &loopLog{}
					err = runJob(ctx, newAdmissionReconciler(st, fin, slog.New(log)), job)
					if !fault.fired.Load() {
						t.Fatal("the context was never cancelled during a pass; the test proves nothing")
					}
					if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "tenant passes failed") {
						t.Fatalf("the cancelled tick returned %v; want the cancellation, not a count of failed passes", err)
					}
					if warns := linesAt(recordsOf(log), slog.LevelWarn); len(warns) != 0 {
						t.Fatalf("the cancelled tick warned %v; a pass cut short by cancellation is not a tenant failure", warns)
					}
					for _, tenant := range tenants {
						assertClaimPending(t, st, tenant, key, holds[tenant])
					}
				})
			})
		})
	}
}

// TestAdmissionReconciler_LogsOnlyWhenNotQuiet: a tick logs a tenant's pass only when the
// pass did something or left something, and the line carries counts only. A live published
// hold, under a budget and a seat limit of the caller's actor, is no work: both jobs pass over
// it in silence and write nothing, and its caller then commits both of its rows at the actual
// amount. A stale claim retired and a lapsed hold swept are logged; once swept, expired
// rows are history, and a pass that finds only history is silent. A legacy claim waiting
// for the operator's stop is outstanding, so every pass logs it.
func TestAdmissionReconciler_LogsOnlyWhenNotQuiet(t *testing.T) {
	forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()
		fin, st, tenant := openFinOpsEngineOn(t, cfg)
		createBudgetPolicy(t, st, tenant, "admission-cap", map[string]any{
			"dimension": "global", "period": "monthly",
			"limit_micro_usd": int64(20_000_000), "action": "block",
		})
		createSeatSpendLimit(t, st, tenant, "a", 10_000_000)

		const live = "model_gateway/live-hold"
		res, err := fin.Reserve(ctx, tenant, finops.AdmissionRequest{
			Scope: finops.AdmissionScopeModelGateway, ActorRef: "a", EstimateMicroUSD: stagedHoldMicroUSD, IdempotencyKey: live,
		})
		if err != nil || !res.Allowed || res.Handle == "" {
			t.Fatalf("reserve = %+v, %v; want an allowed hold", res, err)
		}
		before := ledgerState(t, st, tenant)
		for _, job := range admissionJobNames {
			if recs := admissionTickOK(t, st, fin, job); len(recs) != 0 {
				t.Fatalf("a %s tick over a live published hold logged %v; want silence", job, recs)
			}
		}
		if after := ledgerState(t, st, tenant); after != before {
			t.Fatalf("the ticks over a live published hold wrote:\nbefore\n%s\nafter\n%s", before, after)
		}
		committing := time.Now().UTC()
		if err := fin.Commit(ctx, tenant, res.Handle, 1_500_000); err != nil {
			t.Fatalf("commit the live hold: %v", err)
		}
		committed := time.Now().UTC()
		if row := admissionRowOf(t, st, tenant, live); row.String("state") != "committed" || row.String("handle") != res.Handle || row.String("owed_handles") != "" {
			t.Fatalf("the committed admission is %s naming %q owing %q; want committed naming %s, owing nothing",
				row.String("state"), row.String("handle"), row.String("owed_handles"), res.Handle)
		}
		rows := rowsUnder(t, st, tenant, res.Handle)
		kinds := map[string]bool{}
		for _, r := range rows {
			kinds[r.String("policy_kind")] = true
		}
		if len(rows) != 2 || !kinds["budget"] || !kinds["spend_limit"] {
			t.Fatalf("the committed hold has %d rows of kinds %v; want its budget row and its seat-limit row", len(rows), kinds)
		}
		for _, r := range rows {
			settled := timestampOf(t, r, "settled_at")
			if r.String("state") != "committed" || r.Int("amount_micro_usd") != stagedHoldMicroUSD || r.Int("actual_micro_usd") != 1_500_000 ||
				settled.Before(committing) || settled.After(committed) {
				t.Fatalf("the %s row of the committed hold is %s, amount %d, actual %d, settled at %s; want committed, amount %d, actual 1500000, settled by the commit",
					r.String("policy_kind"), r.String("state"), r.Int("amount_micro_usd"), r.Int("actual_micro_usd"), settled, stagedHoldMicroUSD)
			}
		}

		const stale = "model_gateway/stale-claim"
		hold := stageStaleClaim(t, st, tenant, stale)
		recs := admissionTickOK(t, st, fin, admissionRecoverJobName)
		assertCounts(t, passLine(t, recs, tenant), map[string]any{
			"pending_retired": int64(1), "owed_released": int64(2), "outstanding": int64(0),
		})
		assertCountsOnly(t, recs, stale, live, hold, res.Handle)
		for _, job := range admissionJobNames {
			if recs := admissionTickOK(t, st, fin, job); len(recs) != 0 {
				t.Fatalf("a %s tick after the retirement logged %v; want silence", job, recs)
			}
		}

		stageLapsedHold(t, st, tenant)
		assertCounts(t, passLine(t, admissionTickOK(t, st, fin, admissionReconcileJobName), tenant), map[string]any{
			"swept_expired": int64(1), "expired_unsettled": int64(1), "drift": true, "outstanding": int64(0),
		})
		if recs := admissionTickOK(t, st, fin, admissionReconcileJobName); len(recs) != 0 {
			t.Fatalf("a reconcile tick that found only swept rows logged %v; expired rows are history, not work", recs)
		}
		if report := inspectOK(t, fin, tenant); report.ExpiredUnsettled != 1 || !report.Drift {
			t.Fatalf("the read route reports %+v; the silent tick must have seen the expired row", report)
		}

		stageLegacyClaim(t, st, tenant, "model_gateway/legacy-claim")
		for pass := 1; pass <= 2; pass++ {
			for _, job := range admissionJobNames {
				recs := admissionTickOK(t, st, fin, job)
				assertCounts(t, passLine(t, recs, tenant), map[string]any{
					"legacy_pending": int64(1), "legacy_stop": "absent", "outstanding": int64(1),
				})
				assertCountsOnly(t, recs, "model_gateway/legacy-claim")
			}
		}
	})
}

// TestAdmissionReconciler_SkipsSuspendedTenants: a tenant whose service is withdrawn is not
// visited. Its stale claim and the hold it names stay as they are, no line names it and the
// tick does not fail, while the served tenant beside it is recovered. Once its service
// resumes, the next tick recovers it. Both jobs.
func TestAdmissionReconciler_SkipsSuspendedTenants(t *testing.T) {
	for _, job := range admissionJobNames {
		t.Run(job, func(t *testing.T) {
			forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
				fin, st, served := openFinOpsEngineOn(t, cfg)
				suspended := addFinOpsOrg(t, st, "admission-suspended")
				const key = "model_gateway/withdrawn-service"
				servedHold := stageStaleClaim(t, st, served, key)
				suspendedHold := stageStaleClaim(t, st, suspended, key)
				setOrgStatus(t, st, suspended, model.StatusSuspended)

				from := time.Now().UTC()
				recs, err := admissionTick(t, st, fin, job)
				if err != nil {
					t.Fatalf("the tick failed: %v", err)
				}
				for _, r := range recs {
					if r.attrs["tenant"] == suspended.String() {
						t.Fatalf("a line names the suspended tenant: %q %v", r.msg, r.attrs)
					}
				}
				assertClaimRetired(t, st, served, key, servedHold, from, time.Now().UTC())
				assertClaimPending(t, st, suspended, key, suspendedHold)

				setOrgStatus(t, st, suspended, model.StatusActive)
				from = time.Now().UTC()
				recs = admissionTickOK(t, st, fin, job)
				assertCounts(t, passLine(t, recs, suspended), map[string]any{"pending_retired": int64(1), "owed_released": int64(2)})
				assertClaimRetired(t, st, suspended, key, suspendedHold, from, time.Now().UTC())
			})
		})
	}
}

// TestAdmissionReconciler_ParsesLegacyStopOnce: the operator's stop of the earlier admission
// writers is read once, by the composition root, and every pass reads that value. Reading
// asks for the variable once; text that is not an RFC 3339 instant in UTC is reported by
// one ERROR line that does not repeat it. Both jobs then report the stop the module was
// built with on every pass, though the environment has since changed, and log no error.
// The booted engine's module carries the stop the variable stated, reported once, and boot
// warns nothing about the variable: it is a key the engine reads.
func TestAdmissionReconciler_ParsesLegacyStopOnce(t *testing.T) {
	const notUTC = "2026-09-01T02:00:00+02:00"

	t.Run("the variable", func(t *testing.T) {
		for _, c := range []struct {
			name, value string
			errors      int
		}{
			{"unset", "", 0},
			{"an instant in UTC", "2026-09-01T00:00:00Z", 0},
			{"an instant not in UTC", notUTC, 1},
			{"not an instant", "yesterday", 1},
		} {
			t.Run(c.name, func(t *testing.T) {
				asked := 0
				getenv := func(k string) string {
					if k != admissionLegacyWriterStopEnv {
						t.Errorf("read %s; want only %s", k, admissionLegacyWriterStopEnv)
					}
					asked++
					return c.value
				}
				log := &loopLog{}
				admissionLegacyWriterStop(getenv, slog.New(log))
				if asked != 1 {
					t.Fatalf("the variable was read %d times, want once", asked)
				}
				recs := recordsOf(log)
				if errs := linesAt(recs, slog.LevelError); len(errs) != c.errors || len(recs) != c.errors {
					t.Fatalf("logged %v; want %d ERROR line(s) and nothing else", recs, c.errors)
				}
				for _, r := range recs {
					if !strings.Contains(r.msg, admissionLegacyWriterStopEnv) {
						t.Fatalf("the error line %q does not name the variable", r.msg)
					}
				}
				if c.value != "" {
					assertCountsOnly(t, recs, c.value)
				}
			})
		}
	})

	t.Run("every pass", func(t *testing.T) {
		forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
			_, st, tenant := openFinOpsEngineOn(t, cfg)
			const legacy = "model_gateway/legacy-claim"
			stageLegacyClaim(t, st, tenant, legacy)

			t.Setenv(admissionLegacyWriterStopEnv, "2999-01-01T00:00:00Z")
			fin := finops.New(finops.WithLegacyWriterStop(admissionLegacyWriterStop(osGetenv, discardLog())))
			fin.UseData(api.NewModuleData(st))
			t.Setenv(admissionLegacyWriterStopEnv, notUTC)

			for pass := 1; pass <= 2; pass++ {
				for _, job := range admissionJobNames {
					recs := admissionTickOK(t, st, fin, job)
					assertCounts(t, passLine(t, recs, tenant), map[string]any{
						"legacy_stop": "future", "legacy_pending": int64(1), "outstanding": int64(1),
					})
					if errs := linesAt(recs, slog.LevelError); len(errs) != 0 {
						t.Fatalf("%s pass %d logged %v; the stop is read once, at composition", job, pass, errs)
					}
				}
			}
			if row := admissionRowOf(t, st, tenant, legacy); row.String("state") != "pending" {
				t.Fatalf("the legacy claim is %s; a future stop retires nothing", row.String("state"))
			}
		})
	})

	t.Run("the composition root", func(t *testing.T) {
		forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
			ctx := context.Background()
			t.Setenv(admissionLegacyWriterStopEnv, notUTC)
			sink := &syncBuffer{}
			eng, err := boot(ctx, bootConfigOn(t, cfg, slog.New(slog.NewTextHandler(sink, nil))))
			if err != nil {
				t.Fatalf("boot the composition root: %v", err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			fin, ok := eng.finops.(*finops.Module)
			if !ok {
				t.Fatalf("the booted engine's FinOps is %T, want *finops.Module", eng.finops)
			}
			tenant := addFinOpsOrg(t, eng.store, "admission-stop")
			report, err := fin.RecoverAdmissions(ctx, tenant)
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if report.LegacyStop != "invalid" {
				t.Fatalf("the booted module reports the stop %q; want invalid, as the variable stated", report.LegacyStop)
			}
			var errs, warns []string
			for _, line := range strings.Split(sink.String(), "\n") {
				if strings.Contains(line, notUTC) {
					t.Fatalf("boot logged the stop's text: %q", line)
				}
				if !strings.Contains(line, admissionLegacyWriterStopEnv) {
					continue
				}
				switch {
				case strings.Contains(line, "level=ERROR"):
					errs = append(errs, line)
				case strings.Contains(line, "level=WARN"):
					warns = append(warns, line)
				}
			}
			if len(errs) != 1 {
				t.Fatalf("boot logged %q; want one ERROR line about the stop", errs)
			}
			if len(warns) != 0 {
				t.Fatalf("boot warned %q; the stop is a key it reads, so it warns about none", warns)
			}
		})
	})
}

// countingElector answers Active() true for a fixed number of asks and false after that,
// so leadership can move BETWEEN two tenants without a seam inside the loop and without a
// real election. The loop asks once before enumerating the estate and once before each
// tenant, so activeFor = 1 + n lets exactly n tenants run. It wraps the store's real
// elector, so every other method still answers.
type countingElector struct {
	store.LeaderElector
	activeFor int64
	asked     atomic.Int64
}

func (e *countingElector) Active() bool { return e.asked.Add(1) <= e.activeFor }

// TestAdmissionReconciler_ChecksLeadershipBeforeEveryTenant pins the gate both jobs keep: a
// standby recovers nothing, a node demoted mid-pass stops where it is and records why, and
// a leader finishes. The middle arm is the one that matters: a pass over a large estate
// outlives an election, and a gate taken once per tick would let a demoted node keep
// writing for every remaining tenant beside the new leader.
func TestAdmissionReconciler_ChecksLeadershipBeforeEveryTenant(t *testing.T) {
	for _, job := range admissionJobNames {
		t.Run(job, func(t *testing.T) {
			forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
				fin, raw, tenantA := openFinOpsEngineOn(t, cfg)
				tenantB := addFinOpsOrg(t, raw, "admission-recon-b")
				const key = "model_gateway/leadership"
				tenants := []model.TenantID{tenantA, tenantB}
				for _, tenant := range tenants {
					stageStaleClaim(t, raw, tenant, key)
				}
				pending := func() int {
					t.Helper()
					n := 0
					for _, tenant := range tenants {
						if admissionRowOf(t, raw, tenant, key).String("state") == "pending" {
							n++
						}
					}
					return n
				}
				pass := func(activeFor int64) []loopRecord {
					t.Helper()
					log := &loopLog{}
					le := &countingElector{LeaderElector: raw.Leader(), activeFor: activeFor}
					l := newAdmissionReconciler(electedStore{Store: raw, le: le}, fin, slog.New(log))
					if err := runJob(context.Background(), l, job); err != nil {
						t.Fatalf("tick: %v", err)
					}
					return recordsOf(log)
				}

				standby := pass(0)
				if got := pending(); got != 2 {
					t.Fatalf("a standby recovered %d of the 2 tenants; a node that is not the active writer recovers none", 2-got)
				}
				if len(standby) != 1 || !strings.Contains(standby[0].msg, "standby") {
					t.Fatalf("a standby tick did not record why it did nothing: %v", standby)
				}

				demoted := pass(2) // the tick itself, then the first tenant
				if got := pending(); got != 1 {
					t.Fatalf("a node demoted after its first tenant recovered %d of the 2; the pass must stop where leadership did", 2-got)
				}
				var stopped bool
				for _, r := range demoted {
					stopped = stopped || r.attrs["stopped"] == "leadership"
				}
				if !stopped {
					t.Fatalf("the demotion was not reported as a leadership stop: %v", demoted)
				}

				leader := pass(64)
				if got := pending(); got != 0 {
					t.Fatalf("%d claim(s) survived a full pass by the active writer", got)
				}
				var completed int
				for _, r := range leader {
					if r.attrs["pending_retired"] == int64(1) {
						completed++
					}
				}
				if completed != 1 {
					t.Fatalf("the promoted node's tick logged %v; want the one remaining tenant's retirement", leader)
				}
			})
		})
	}
}

// TestAdmissionReconciler_SweepsALapsedHold: the reconcile job sweeps a hold left by a
// caller that died mid-call, without any request, and its line says what it swept. The
// lapsed row is staged rather than waited for: the module's clock is not a seam the
// composition root can move, and five real minutes is not a test.
func TestAdmissionReconciler_SweepsALapsedHold(t *testing.T) {
	forEachServedEngine(t, func(t *testing.T, cfg store.Config) {
		fin, st, tenant := openFinOpsEngineOn(t, cfg)
		stageLapsedHold(t, st, tenant)
		if before := inspectOK(t, fin, tenant); before.ActiveLapsed != 1 {
			t.Fatalf("the fixture did not stage a lapsed hold: %+v", before)
		}
		assertCounts(t, passLine(t, admissionTickOK(t, st, fin, admissionReconcileJobName), tenant),
			map[string]any{"swept_expired": int64(1)})
		if after := inspectOK(t, fin, tenant); after.ActiveLapsed != 0 || after.ExpiredUnsettled != 1 {
			t.Fatalf("the lapsed hold was not swept: %+v", after)
		}
	})
}

// TestAdmissionReconciler_NilWithoutFinOps keeps the loop off an install with no ledger to
// recover: a tick that enumerates tenants to call nothing is pure cost. It runs no
// statement, so one engine suffices.
func TestAdmissionReconciler_NilWithoutFinOps(t *testing.T) {
	_, st, _ := openFinOpsEngine(t)
	if l := newAdmissionReconciler(st, nil, discardLog()); l != nil {
		t.Fatal("no FinOps module means no reservation ledger and no loop")
	}
}
