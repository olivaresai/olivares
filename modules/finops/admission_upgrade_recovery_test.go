// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// RECOVERY AFTER THE REAL UPGRADE. A store an earlier build wrote is upgraded in place,
// and the jobs then run as they would after a restart: both skip their first interval, so
// the first recovery pass runs a minute after the upgrade, callers retry their keys, and
// the first reconciliation — recovery, then the sweep — runs five minutes after it. What
// the earlier writers' holds become depends on the one fact no schema step can observe:
// whether the operator stated that those writers stopped, and when.
// -----------------------------------------------------------------------------

// earlierRow is one admission row an earlier build left, with the request whose key it
// is.
type earlierRow struct {
	req           AdmissionRequest
	state         string
	handle, spend holdID
	stateAt       time.Time
}

// TestUpgradeFromEarlierAdmissionRecovers upgrades a store an earlier build populated in
// every state it left rows in, for each stop the operator may have stated: none, one five
// seconds before the upgrade, and one 100 seconds before it, which a legacy claim dated
// ten seconds before the upgrade contradicts.
//
// The first recovery pass settles both owes_release rows under any stop — withholding
// rows released with an actual of zero, lapsed rows left to the sweep — and leaves the
// three legacy claims. The retries that follow are answered as each row allows: a live
// pair and a live seat-only row are replayed and then committed by either hold; a row
// published outside its window, or undated, is taken over under a new hold, the undated
// row's withholding hold released; a seat-only row released and then committed keeps the
// release's instant. The first reconciliation retires the three legacy claims only under
// the stop that is usable by then, and sweeps the two lapsed holds.
func TestUpgradeFromEarlierAdmissionRecovers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopBefore time.Duration
		atFirst    legacyStopState
		atSecond   legacyStopState
	}{
		{"no stop", 0, legacyStopAbsent, legacyStopAbsent},
		{"a stop five seconds before the upgrade", 5 * time.Second, legacyStopWaiting, legacyStopUsable},
		{"a stop 100 seconds before the upgrade", 100 * time.Second, legacyStopContradicted, legacyStopContradicted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachUpgradeEngine(t, func(t *testing.T, cfg store.Config) {
				runUpgradeFromEarlierAdmissionRecovers(t, cfg, tc.stopBefore, tc.atFirst, tc.atSecond)
			})
		})
	}
}

func runUpgradeFromEarlierAdmissionRecovers(t *testing.T, cfg store.Config, stopBefore time.Duration, atFirst, atSecond legacyStopState) {
	ctx := context.Background()
	m := New()
	m.host = &fakeHost{}
	tu := baseTime // the upgrade
	clk := &fakeClock{t: tu}
	m.clock = clk
	if stopBefore > 0 {
		WithLegacyWriterStop(mustLegacyStop(t, stopText(tu.Add(-stopBefore))))(m)
	}
	ago := func(d time.Duration) time.Time { return tu.Add(-d) }

	// 1. The store an earlier build wrote: eight-column admission rows, and the money
	//    each names, under a budget and a seat limit.
	old := openAlertStore(t, cfg, registerAdmissionWithout(m, colAdmOwedHandles))
	tenant := provisionTenant(t, old)
	budget, limit := seedBudgetAndSeatLimit(t, old, tenant)
	seated := func(key string) AdmissionRequest { return seatRequest("model_gateway/upgraded-" + key) }
	pooled := func(key string) AdmissionRequest {
		return AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: 2 * oneUSD, IdempotencyKey: "model_gateway/upgraded-" + key}
	}
	pairBudget, pairSeat, lapsedBudget, committedBudget, committedSeat := newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID()
	owedBudget, owedSeat, owedLapsedBudget, liveSeat, releasedSeat, undatedBudget := newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID()
	rows := map[string]earlierRow{
		"fresh-claim":    {pooled("fresh-claim"), admStatePending, "", "", ago(10 * time.Second)},
		"old-claim":      {pooled("old-claim"), admStatePending, "", "", ago(600 * time.Second)},
		"undated-claim":  {pooled("undated-claim"), admStatePending, "", "", time.Time{}},
		"live-pair":      {seated("live-pair"), admStateReserved, pairBudget, pairSeat, ago(60 * time.Second)},
		"lapsed-hold":    {pooled("lapsed-hold"), admStateReserved, lapsedBudget, "", ago(400 * time.Second)},
		"committed-pair": {seated("committed-pair"), admStateCommitted, committedBudget, committedSeat, ago(1000 * time.Second)},
		"released":       {pooled("released"), admStateReleased, "", "", ago(1000 * time.Second)},
		"owed-pair":      {seated("owed-pair"), admStateOwesRelease, owedBudget, owedSeat, ago(20 * time.Second)},
		"owed-lapsed":    {pooled("owed-lapsed"), admStateOwesRelease, owedLapsedBudget, "", ago(500 * time.Second)},
		"live-seat":      {seated("live-seat"), admStateReserved, "", liveSeat, ago(30 * time.Second)},
		"seat-released":  {seated("seat-released"), admStateReserved, "", releasedSeat, ago(30 * time.Second)},
		"undated-hold":   {pooled("undated-hold"), admStateReserved, undatedBudget, "", time.Time{}},
	}
	for _, r := range rows {
		seedAdmission(t, old, tenant, admissionRecordFor(r.req, r.state, r.handle, r.spend, r.stateAt))
	}
	seqs := map[model.ID]int64{}
	ledger := func(policy model.ID, component string, h holdID, created time.Time, state string) {
		seqs[policy]++
		rec := ledgerRow(policy, component, h, seqs[policy], 2*oneUSD, created, created.Add(5*time.Minute), resvStateActive)
		if state != resvStateActive {
			rec[colResvState] = state
			rec[colResvSettledAt] = model.NewTimestamp(created.Add(time.Second)).String()
			rec[colResvActual] = measured
		}
		seedReservation(t, old, tenant, rec)
	}
	ledger(budget, "b", pairBudget, ago(60*time.Second), resvStateActive)
	ledger(limit, "s", pairSeat, ago(60*time.Second), resvStateActive)
	ledger(budget, "b", lapsedBudget, ago(400*time.Second), resvStateActive)
	ledger(budget, "b", committedBudget, ago(1000*time.Second), resvStateCommitted)
	ledger(limit, "s", committedSeat, ago(1000*time.Second), resvStateCommitted)
	ledger(budget, "b", owedBudget, ago(20*time.Second), resvStateActive)
	ledger(limit, "s", owedSeat, ago(20*time.Second), resvStateActive)
	ledger(budget, "b", owedLapsedBudget, ago(500*time.Second), resvStateActive)
	ledger(limit, "s", liveSeat, ago(30*time.Second), resvStateActive)
	ledger(limit, "s", releasedSeat, ago(30*time.Second), resvStateActive)
	ledger(budget, "b", undatedBudget, ago(90*time.Second), resvStateActive)
	before := rowsByID(t, old, tenant, admissionIdempotencyKind)
	idOf := map[string]string{}
	for name, r := range rows {
		for id, rec := range before {
			if rec.String(colAdmKey) == r.req.IdempotencyKey {
				idOf[name] = id
			}
		}
	}
	if len(before) != len(rows) || len(idOf) != len(rows) {
		t.Fatalf("fixture: %d admission rows seeded for %d keys", len(before), len(rows))
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the earlier-build store: %v", err)
	}

	// 2. The current schema.
	st := openAlertStore(t, cfg, m.RegisterSchema)
	defer func() { _ = st.Close() }()
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(st), st: st})
	unchangedSince := func(t *testing.T, when string, baseline map[string]model.Record, names ...string) {
		t.Helper()
		after := rowsByID(t, st, tenant, admissionIdempotencyKind)
		for _, name := range names {
			if moved := changedColumns(baseline[idOf[name]], after[idOf[name]]); len(moved) > 0 {
				t.Errorf("%s: the %s row changed %v", when, name, moved)
			}
		}
	}
	unchanged := func(t *testing.T, when string, names ...string) {
		t.Helper()
		unchangedSince(t, when, before, names...)
	}
	settledRow := func(t *testing.T, when, name, state string, h holdID) {
		t.Helper()
		row := admissionRowOf(t, m, tenant, rows[name].req.IdempotencyKey)
		if row.state != state || row.handle != h || len(row.owed) != 0 {
			t.Fatalf("%s: the %s row is %s naming %q owing %v, want %s naming %q", when, name, row.state, row.handle, row.owed, state, h)
		}
	}

	// 3. The first recovery pass.
	clk.t = tu.Add(60 * time.Second)
	firstPass := clk.t
	assertRecovery(t, "the first recovery pass", recoverOK(t, m, tenant),
		AdmissionRecovery{LegacyPending: 3, LegacyRetired: 2, OwedReleased: 2, LegacyStop: string(atFirst)})
	settledRow(t, "the first pass", "owed-pair", admStateReleased, "")
	assertRowsUnder(t, st, tenant, owedBudget, 1, resvStateReleased, 0, firstPass)
	assertRowsUnder(t, st, tenant, owedSeat, 1, resvStateReleased, 0, firstPass)
	settledRow(t, "the first pass", "owed-lapsed", admStateReleased, "")
	assertRowsUnder(t, st, tenant, owedLapsedBudget, 1, resvStateActive, 0, time.Time{})
	unchanged(t, "the first pass", "fresh-claim", "old-claim", "undated-claim", "live-pair", "lapsed-hold", "committed-pair", "released", "live-seat", "seat-released", "undated-hold")
	afterFirstPass := rowsByID(t, st, tenant, admissionIdempotencyKind)

	// 4. The follow-up calls.
	clk.t = tu.Add(100 * time.Second)
	followUp := clk.t
	for _, c := range []struct {
		name string
		h    holdID
	}{{"live-pair", pairBudget}, {"live-seat", liveSeat}} {
		res, err := m.Reserve(ctx, tenant, rows[c.name].req)
		if err != nil || !res.Allowed || !res.Replayed || res.Handle != c.h.String() {
			t.Fatalf("the retry of the %s row was answered %+v err=%v, want a replay of %s", c.name, res, err, c.h)
		}
		if err := m.Commit(ctx, tenant, c.h.String(), measured); err != nil {
			t.Fatalf("commit of the %s row: %v", c.name, err)
		}
	}
	row := admissionRowOf(t, m, tenant, rows["live-pair"].req.IdempotencyKey)
	if row.state != admStateCommitted || row.handle != pairBudget || row.spendHandle != pairSeat {
		t.Fatalf("the live pair is %s naming %q/%q, want it committed", row.state, row.handle, row.spendHandle)
	}
	assertRowsUnder(t, st, tenant, pairBudget, 1, resvStateCommitted, measured, clk.t)
	assertRowsUnder(t, st, tenant, pairSeat, 1, resvStateCommitted, measured, clk.t)
	if row := admissionRowOf(t, m, tenant, rows["live-seat"].req.IdempotencyKey); row.state != admStateCommitted || row.spendHandle != liveSeat {
		t.Fatalf("the live seat-only row is %s naming %q, want it committed", row.state, row.spendHandle)
	}
	assertRowsUnder(t, st, tenant, liveSeat, 1, resvStateCommitted, measured, clk.t)

	for _, c := range []struct {
		name string
		old  holdID
		kept string
	}{{"lapsed-hold", lapsedBudget, resvStateActive}, {"undated-hold", undatedBudget, resvStateReleased}} {
		res, err := m.Reserve(ctx, tenant, rows[c.name].req)
		if err != nil || !res.Allowed || res.Replayed || res.Handle == "" || res.Handle == c.old.String() {
			t.Fatalf("the retry of the %s row was answered %+v err=%v, want a new hold", c.name, res, err)
		}
		settledRow(t, "the follow-up calls", c.name, admStateReserved, holdID(res.Handle))
		if n := len(activeRowsUnder(t, st, tenant, holdID(res.Handle))); n != 1 {
			t.Fatalf("the new hold of the %s row has %d active row(s), want its budget row", c.name, n)
		}
		at := time.Time{}
		if c.kept == resvStateReleased {
			at = clk.t
		}
		assertRowsUnder(t, st, tenant, c.old, 1, c.kept, 0, at)
	}

	released := clk.t
	if err := m.Release(ctx, tenant, releasedSeat.String()); err != nil {
		t.Fatalf("release of the seat-only row: %v", err)
	}
	if row := admissionRowOf(t, m, tenant, rows["seat-released"].req.IdempotencyKey); row.state != admStateReleased {
		t.Fatalf("the seat-only row is %s after the release, want released", row.state)
	}
	assertRowsUnder(t, st, tenant, releasedSeat, 1, resvStateReleased, 0, released)
	clk.advance(time.Second)
	if err := m.Commit(ctx, tenant, releasedSeat.String(), measured); err != nil {
		t.Fatalf("late commit of the seat-only row: %v", err)
	}
	if row := admissionRowOf(t, m, tenant, rows["seat-released"].req.IdempotencyKey); row.state != admStateCommitted || row.spendHandle != releasedSeat {
		t.Fatalf("the released seat-only row is %s naming %q, want it committed", row.state, row.spendHandle)
	}
	assertRowsUnder(t, st, tenant, releasedSeat, 1, resvStateCommitted, measured, released)
	unchanged(t, "the follow-up calls", "fresh-claim", "old-claim", "undated-claim", "committed-pair", "released")
	unchangedSince(t, "the follow-up calls", afterFirstPass, "owed-pair")
	assertRowsUnder(t, st, tenant, owedBudget, 1, resvStateReleased, 0, firstPass)
	assertRowsUnder(t, st, tenant, owedSeat, 1, resvStateReleased, 0, firstPass)
	afterFollowUp := rowsByID(t, st, tenant, admissionIdempotencyKind)

	// 5. The first reconciliation: recovery, then the sweep.
	clk.t = tu.Add(300 * time.Second)
	job := reconcileOK(t, m, tenant)
	if job.LegacyStop != string(atSecond) || job.SweptExpired != 2 || job.PendingRetired != 0 || job.Unresolved != 0 || job.Corrupt != 0 {
		t.Fatalf("the first reconciliation reported %+v, want the stop %s and the two lapsed holds swept", job, atSecond)
	}
	for _, h := range []holdID{lapsedBudget, owedLapsedBudget} {
		assertRowsUnder(t, st, tenant, h, 1, resvStateExpired, 0, clk.t)
	}
	if atSecond == legacyStopUsable {
		if job.LegacyRetired != 3 || job.LegacyPending != 0 {
			t.Fatalf("under a usable stop the reconciliation reported %d retired and %d pending legacy claims, want 3 and 0",
				job.LegacyRetired, job.LegacyPending)
		}
		for _, name := range []string{"fresh-claim", "old-claim", "undated-claim"} {
			settledRow(t, "the first reconciliation", name, admStateReleased, "")
		}
	} else {
		if job.LegacyRetired != 0 || job.LegacyPending != 3 {
			t.Fatalf("under a stop that is %s the reconciliation reported %d retired and %d pending legacy claims, want 0 and 3",
				atSecond, job.LegacyRetired, job.LegacyPending)
		}
		unchanged(t, "the first reconciliation", "fresh-claim", "old-claim", "undated-claim")
	}
	unchanged(t, "the first reconciliation", "committed-pair", "released")
	assertRowsUnder(t, st, tenant, committedBudget, 1, resvStateCommitted, measured, time.Time{})
	assertRowsUnder(t, st, tenant, committedSeat, 1, resvStateCommitted, measured, time.Time{})
	unchangedSince(t, "the first reconciliation", afterFollowUp, "owed-pair", "live-pair", "live-seat", "seat-released")
	assertRowsUnder(t, st, tenant, owedBudget, 1, resvStateReleased, 0, firstPass)
	assertRowsUnder(t, st, tenant, owedSeat, 1, resvStateReleased, 0, firstPass)
	assertRowsUnder(t, st, tenant, pairBudget, 1, resvStateCommitted, measured, followUp)
	assertRowsUnder(t, st, tenant, pairSeat, 1, resvStateCommitted, measured, followUp)
	assertRowsUnder(t, st, tenant, liveSeat, 1, resvStateCommitted, measured, followUp)
	assertRowsUnder(t, st, tenant, releasedSeat, 1, resvStateCommitted, measured, released)
}

// -----------------------------------------------------------------------------
// THE UPGRADE OF THE STATE-ENTRY COLUMN. A store is created with the admission table as it
// was before state_at existed, admissions are written through it by the module's own
// Reserve and Commit, and the SAME storage is reopened with the current descriptor, which
// adds the column as NULL. An undated row neither orphans the live hold it recorded nor
// carries an unbounded stale admission.
// -----------------------------------------------------------------------------

func runAdmissionStateEntryUpgrade(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	m := New()
	m.host = &fakeHost{}
	m.clock = &fakeClock{t: baseTime}

	// 1. The storage as it was BEFORE the column: two admissions written by the real
	//    Reserve/Commit path, one still holding money and one already settled.
	old := openAlertStore(t, cfg, registerAdmissionWithout(m, colAdmStateAt, colAdmOwedHandles))
	tenant := provisionTenant(t, old)
	createBudget(t, old, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(old), st: old})

	live := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/pre-upgrade-live", EstimateMicroUSD: oneUSD}
	settled := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/pre-upgrade-settled", EstimateMicroUSD: oneUSD}
	preLive, err := m.Reserve(ctx, tenant, live)
	if err != nil || !preLive.Allowed || preLive.Handle == "" {
		t.Fatalf("pre-upgrade live admission: %+v %v", preLive, err)
	}
	preSettled, err := m.Reserve(ctx, tenant, settled)
	if err != nil || !preSettled.Allowed || preSettled.Handle == "" {
		t.Fatalf("pre-upgrade settled admission: %+v %v", preSettled, err)
	}
	if err := m.Commit(ctx, tenant, preSettled.Handle, oneUSD); err != nil {
		t.Fatalf("pre-upgrade commit: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the pre-upgrade store: %v", err)
	}

	// 2. Reopen the SAME storage with the current descriptor. The reconciler adds the
	//    nullable column and back-fills nothing, so both rows are undated.
	upgraded := openAlertStore(t, cfg, m.RegisterSchema)
	defer func() { _ = upgraded.Close() }()
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(upgraded), st: upgraded})

	for _, req := range []AdmissionRequest{live, settled} {
		row, found, lerr := m.readRow(ctx, tenant, req.IdempotencyKey)
		if lerr != nil || !found {
			t.Fatalf("read %s after the upgrade: found=%v err=%v", req.IdempotencyKey, found, lerr)
		}
		if !row.stateAt.IsZero() {
			t.Fatalf("%s came out of the upgrade dated %v; the fixture is not testing an undated row",
				req.IdempotencyKey, row.stateAt)
		}
	}

	// 3. RECOVERY OF THE LIVE ONE. It is re-evaluated, because an undated row cannot be
	//    shown to be a young retry — and the hold it recorded is released, so the call
	//    that reserved before the upgrade does not end up holding twice.
	recovered, err := m.Reserve(ctx, tenant, live)
	if err != nil || !recovered.Allowed {
		t.Fatalf("the undated live admission was not recovered: %+v %v", recovered, err)
	}
	if recovered.Replayed || recovered.Handle == preLive.Handle {
		t.Fatalf("an undated row was replayed instead of re-evaluated: %+v", recovered)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 || report.IdempotencyOrphans != 0 || report.Drift {
		t.Fatalf("the upgrade left the ledger drifted: %+v (pre=%s recovered=%s)",
			report, preLive.Handle, recovered.Handle)
	}

	// 4. RECOVERY OF THE SETTLED ONE, which is the other half: an undated row must not
	//    carry its old verdict for ever. Blow the cap and the retry is refused by the
	//    ledger, not answered from a row nobody can date.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 50*oneUSD, baseTime))
	stale, err := m.Reserve(ctx, tenant, settled)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Allowed || stale.Replayed {
		t.Fatalf("an undated settled row granted an unbounded stale admission over a blown cap: %+v", stale)
	}
	if stale.Action != "block" {
		t.Fatalf("Action = %q, want the budget's block", stale.Action)
	}
}

// TestAdmissionStateEntryUpgradeSQLite runs the upgrade against a real SQLite file: a
// :memory: database cannot be reopened, and creating the table from the new descriptor
// would prove nothing about a migration.
func TestAdmissionStateEntryUpgradeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission-state-entry-upgrade.db")
	runAdmissionStateEntryUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true})
}

// TestAdmissionStateEntryUpgradePostgres runs the SAME upgrade against a real, isolated
// PostgreSQL database, whose dialect runs different DDL. It SKIPS without the backend:
// a leg that returned normally after logging would produce a PASS marker for a leg that
// never ran.
func TestAdmissionStateEntryUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL upgrade leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	dsns := enginetest.IsolatedPostgres(t)
	runAdmissionStateEntryUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
}
