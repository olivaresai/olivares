// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// D02 ingest cut. The causal controls for the transactional extraction of cost
// ingestion and the writer lock the FinOps ledger writers now share:
//
//   - an exact replay and a grown / re-settled / signed replacement of the same
//     bucket keep ONE canonical CostRecord (the legacy natural key and upsert
//     semantics are what this proves are unchanged);
//   - an insert conflict arriving AFTER the CostRecord was written rolls back
//     every row and publishes nothing, instead of being reported as a successful
//     de-duplication;
//   - a sample that omits its instant is stamped with the instant BEFORE the
//     transaction was entered, so waiting for admission cannot move its accounting
//     period;
//   - two ingestions of the same tenant each take ONE key before their decisive
//     read and produce the serial outcome — measured with a finite two-transaction
//     barrier whose every wait is bounded and cancellable, and WITHOUT comparing
//     markers that are recorded after the transactions they describe have ended;
//   - that last point has a control of its own: a deterministic schedule in which
//     the post-return commit marker trails a rival's lock even though the run is
//     correctly serialized — the reason no assertion rests on that comparison;
//   - two tenants do not serialize against each other;
//   - the reservation writers take that same key before their decisive read, and
//     the scope they get it from is now part of their SUPPORTED CONTRACT: a
//     decorator that forwards the capability keeps every legacy outcome (headroom,
//     exhausted limit, settlement, expiry sweep), and one that hides it is refused
//     — a NEW boundary, pinned on both sides rather than declared preserved.
//
// Every case runs on SQLite and, when a server is configured, on a real isolated
// PostgreSQL — the engine whose READ COMMITTED snapshots and aborted-transaction
// semantics make the defects observable. No SQL is simulated: the fixtures wrap
// repositories and forward the store's own optional capabilities, and a fixture
// never pretends to have acquired a lock.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// -----------------------------------------------------------------------------
// Backends
// -----------------------------------------------------------------------------

// eachIngestBackend runs fn against SQLite and, when a Postgres server is
// configured, against a private database of its own. An absent server SKIPS the
// Postgres leg loudly: not run is not a pass.
func eachIngestBackend(t *testing.T, fn func(t *testing.T, cfg store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		fn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: the PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		fn(t, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 4})
	})
}

// -----------------------------------------------------------------------------
// The transaction probe
// -----------------------------------------------------------------------------

// probeRoleKey carries which concurrent caller a transaction belongs to, so the
// probe can attribute a lock, a read and a commit to the goroutine that made them.
type probeRoleKey struct{}

func withProbeRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, probeRoleKey{}, role)
}

func probeRoleOf(ctx context.Context) string {
	if s, ok := ctx.Value(probeRoleKey{}).(string); ok {
		return s
	}
	return "solo"
}

// txProbe observes the transaction seam of the FinOps writers: the keys each
// transaction locks, the order of that lock against the first repository read and
// write, and the commit or rollback. It can also stage a failure and hold a
// transaction open at a chosen point (the barrier).
//
// It NEVER fabricates a lock: LockTransaction forwards to the scope the store
// produced, and hiding the capability hides it for real. A case that passes here
// passed with the store's own serialization, not the fixture's.
type txProbe struct {
	mu     sync.Mutex
	seq    int
	events []probeEvent

	// hideLock drops the optional capability, which is exactly what a decorator
	// written without thinking about optional methods produces.
	hideLock bool
	// watch is the module-registered entity whose repository reads/writes are
	// marked; ledger (CostRecord) writes are always marked.
	watch model.Kind
	// createErr, when set, fails the watched repository's Create with it — the
	// conflict that arrives after the ledger row of the same transaction exists.
	createErr error
	// afterLock runs right after a role really acquired the lock (the barrier).
	afterLock func(role string)
	// enterMutate runs as a role's Mutate is entered, BEFORE the store's own — so
	// another role can wait for the fact that this one is now contending.
	enterMutate func(role string)
	// beforeMark runs after a role's Mutate RETURNED and before its commit/rollback
	// marker is recorded. It exists to make the gap between the two deterministic,
	// which is the whole subject of TestTheCommitMarkerTrailsTheRelease.
	beforeMark func(role string)
}

type probeEvent struct {
	role string
	// lock | read | create | update | ledger | commit | rollback. The insert and the
	// update of the watched repository are marked apart on purpose: which of the two
	// a transaction performed is how a case can tell that it SAW another
	// transaction's committed row rather than merely finishing after it.
	what string
	key  string
	seq  int
}

func (p *txProbe) mark(role, what, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	p.events = append(p.events, probeEvent{role: role, what: what, key: key, seq: p.seq})
}

// at returns the sequence of the FIRST event of this kind for a role, or 0.
func (p *txProbe) at(role, what string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.events {
		if e.role == role && e.what == what {
			return e.seq
		}
	}
	return 0
}

// lockKeys returns every key a role locked, in order.
func (p *txProbe) lockKeys(role string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.events {
		if e.role == role && e.what == "lock" {
			out = append(out, e.key)
		}
	}
	return out
}

func (p *txProbe) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := ""
	for _, e := range p.events {
		out += e.role + ":" + e.what + " "
	}
	return out
}

type probeData struct {
	api.ModuleData
	p *txProbe
}

func (d probeData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(ctx, sc)) })
}

func (d probeData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	role := probeRoleOf(ctx)
	if d.p.enterMutate != nil {
		d.p.enterMutate(role)
	}
	err := d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(ctx, sc)) })
	// THIS MARKER IS NOT AN OBSERVATION OF THE COMMIT, and no assertion in this file
	// compares it across roles because of that. It is recorded after Mutate RETURNED:
	// the database commit and the release of every transaction lock already happened,
	// an unbounded amount of scheduling ago. Within one role it still orders correctly
	// against that role's own earlier marks (program order in one goroutine), and that
	// is all it is used for.
	if d.p.beforeMark != nil {
		d.p.beforeMark(role)
	}
	if err == nil {
		d.p.mark(role, "commit", "")
	} else {
		d.p.mark(role, "rollback", "")
	}
	return err
}

func (d probeData) wrap(ctx context.Context, sc store.Scope) store.Scope {
	if d.p.hideLock {
		return noLockScope{Scope: sc}
	}
	return probeScope{Scope: sc, p: d.p, role: probeRoleOf(ctx)}
}

type probeScope struct {
	store.Scope
	p    *txProbe
	role string
}

// LockTransaction forwards the REAL capability and marks the acquisition — after
// it succeeded, so the recorded instant is when this transaction actually held the
// key, not when it started waiting for it.
func (s probeScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	if err := locker.LockTransaction(ctx, key); err != nil {
		return err
	}
	s.p.mark(s.role, "lock", key)
	if s.p.afterLock != nil {
		s.p.afterLock(s.role)
	}
	return nil
}

func (s probeScope) Costs() store.Repository[model.CostRecord] {
	return probeCostRepo{Repository: s.Scope.Costs(), p: s.p, role: s.role}
}

func (s probeScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != s.p.watch {
		return repo, err
	}
	return probeRepo{GenericRepo: repo, p: s.p, role: s.role}, nil
}

type probeCostRepo struct {
	store.Repository[model.CostRecord]
	p    *txProbe
	role string
}

func (r probeCostRepo) Create(ctx context.Context, v model.CostRecord) (model.CostRecord, error) {
	r.p.mark(r.role, "ledger", "")
	return r.Repository.Create(ctx, v)
}

type probeRepo struct {
	store.GenericRepo
	p    *txProbe
	role string
}

func (r probeRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.p.mark(r.role, "read", "")
	return r.GenericRepo.List(ctx, q)
}

func (r probeRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	r.p.mark(r.role, "create", "")
	if r.p.createErr != nil {
		return nil, r.p.createErr
	}
	return r.GenericRepo.Create(ctx, rec)
}

func (r probeRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	r.p.mark(r.role, "update", "")
	return r.GenericRepo.Update(ctx, rec)
}

// -----------------------------------------------------------------------------
// Finite waiting
// -----------------------------------------------------------------------------

// barrierBound is the bound on EVERY wait in the concurrency controls below, and the
// deadline of the context the participating calls run under. A barrier that cannot be
// reached therefore ends as a FAILED case with its calls cancelled and collected —
// never as a package that hangs until the global test timeout.
const barrierBound = 30 * time.Second

// waitSlack is how much longer a case waits for a call to come back than the call's
// own context lives, so a cancelled ingestion reports ITS error instead of the test
// reporting a deadline.
const waitSlack = 10 * time.Second

// bgCall is one participating call running in its own goroutine, with a finite
// completion handshake and a cleanup that collects it even when the case failed at a
// barrier. It is the reason no case here ends on an unbounded WaitGroup.
type bgCall struct {
	role string
	ch   chan error
	done bool
}

// startIngest runs one ingestion under ctx and registers its collection.
func startIngest(t *testing.T, ctx context.Context, m *Module, tenant model.TenantID, role string, c sdkmodel.CostSample) *bgCall {
	t.Helper()
	b := &bgCall{role: role, ch: make(chan error, 1)}
	go func() {
		b.ch <- m.onCost(withProbeRole(ctx, role), tenant, c, nil)
		close(b.ch)
	}()
	t.Cleanup(func() {
		if b.done {
			return
		}
		select {
		case <-b.ch:
		case <-time.After(waitSlack):
			t.Errorf("the %s ingestion never returned after its context was cancelled", b.role)
		}
	})
	return b
}

// wait is the finite completion handshake.
func (b *bgCall) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-b.ch:
		b.done = true
		return err
	case <-time.After(barrierBound + waitSlack):
		t.Fatalf("the %s call did not return within %s", b.role, barrierBound+waitSlack)
		return nil
	}
}

// awaitLock is the finite FIRST-LOCK handshake: the call reaches its lock, or it
// finishes first (which is a failure with the call's own error in it), or the bound
// expires. None of the three waits forever.
func (b *bgCall) awaitLock(t *testing.T, locked <-chan struct{}, probe *txProbe) {
	t.Helper()
	select {
	case <-locked:
	case err := <-b.ch:
		b.done = true
		t.Fatalf("the %s call finished (%v) before it ever held the writer lock: %s", b.role, err, probe)
	case <-time.After(barrierBound):
		t.Fatalf("the %s call did not reach its writer lock within %s: %s", b.role, barrierBound, probe)
	}
}

// -----------------------------------------------------------------------------
// Causal 1 — replay, growth and a signed replacement keep ONE canonical record
// -----------------------------------------------------------------------------

// TestCostIngestReplayAndReplacementKeepOneCanonicalRecord is the preservation
// control for the extraction: the natural key, the upsert semantics, the
// estimated-vs-billed split and the signed ledger value must all come through the
// helper exactly as they went into onCost.
func TestCostIngestReplayAndReplacementKeepOneCanonicalRecord(t *testing.T) {
	eachIngestBackend(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, host := openFinCfg(t, cfg)
		ctx := context.Background()

		first := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 600, baseTime)
		m.ingest(t, tenant, first)
		m.ingest(t, tenant, first) // exact replay: byte-identical re-delivery
		if n := countCosts(t, st, tenant); n != 1 {
			t.Fatalf("cost records after an exact replay = %d, want 1", n)
		}

		// The SAME bucket re-pulled after it grew: one row, replaced in place.
		m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 160, 80, 1000, baseTime))
		// And re-settled DOWNWARD past zero: a credit is a legitimate signed cost,
		// so the ledger must carry the negative figure rather than clamp it.
		m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 160, 80, -250, baseTime))

		// A BILLED sample of the same bucket is reconciliation data: a distinct
		// natural key (provenance is in it) and NO canonical ledger entry.
		billed := mkCost("anthropic", "claude-opus-4-8", "", 160, 80, 1000, baseTime)
		billed.Provenance = sdkmodel.ProvenanceBilled
		m.ingest(t, tenant, billed)

		if n := countCosts(t, st, tenant); n != 1 {
			t.Fatalf("cost records = %d, want exactly 1 canonical entry for the bucket", n)
		}
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			costs, _, err := sc.Costs().List(ctx, model.Query{})
			if err != nil {
				return err
			}
			if got := costs[0].CostMicroUSD; got != -250 {
				t.Errorf("ledger cost = %d, want the signed replacement -250", got)
			}
			if costs[0].InputTokens != 160 || costs[0].OutputTokens != 80 {
				t.Errorf("ledger tokens = %d/%d, want the replaced 160/80",
					costs[0].InputTokens, costs[0].OutputTokens)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		rows := costSampleRows(t, st, tenant)
		if len(rows) != 2 {
			t.Fatalf("read-model rows = %d, want 2 (one estimated bucket + one billed)", len(rows))
		}
		for _, r := range rows {
			switch r.String(colProvenance) {
			case string(sdkmodel.ProvenanceEstimated):
				if r.Int(colCostMicroUSD) != -250 {
					t.Errorf("estimated row cost = %d, want -250", r.Int(colCostMicroUSD))
				}
				if r.String(colCostRecordID) == "" {
					t.Errorf("estimated row lost its link to the canonical ledger")
				}
			case string(sdkmodel.ProvenanceBilled):
				if r.String(colCostRecordID) != "" {
					t.Errorf("a billed row was linked to a ledger entry: %q", r.String(colCostRecordID))
				}
			default:
				t.Errorf("unexpected provenance %q", r.String(colProvenance))
			}
		}
		if f := host.findings(); len(f) != 0 {
			t.Errorf("no budget exists and %d findings were published", len(f))
		}
	})
}

// -----------------------------------------------------------------------------
// Causal 2 — a conflict after the ledger write rolls everything back
// -----------------------------------------------------------------------------

// TestCostIngestConflictAfterLedgerWriteRollsBackEverything is the defect this cut
// closes. The read-model INSERT conflicts AFTER the canonical CostRecord of the same
// transaction was written; the old path returned nil and let the caller commit a
// ledger row that no read-model row points at.
//
// WHAT KIND OF CONFLICT THIS IS, stated because the two kinds are not the same fact
// (R4 of the independent review). The fixture returns store.ErrConflict from the
// watched repository BEFORE the real INSERT is issued, so the transaction underneath
// — a real PostgreSQL transaction on that leg — REMAINS USABLE. That is exactly the
// branch being exercised: with the old return in place the ingestion commits the
// orphan prefix, which is what mutant M1 showed. It is a synthetic repository failure
// inside a real transaction and it is NOT a native unique violation: no SQLSTATE
// 23505 is produced here, the transaction is never put into the aborted state, and
// nothing in this file establishes what a native violation would report to an HTTP
// caller. See the propagation comment in ingest.go for what is and is not claimed
// about that other path.
//
// The audit hook is the privileged HTTP one, so the audited fact is inside the
// claim and not beside it.
func TestCostIngestConflictAfterLedgerWriteRollsBackEverything(t *testing.T) {
	eachIngestBackend(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, host := openFinCfg(t, cfg)
		m.clock = &fakeClock{t: baseTime}
		ctx := context.Background()
		// A budget that the sample crosses, so the alert row is part of what must
		// disappear too.
		createBudget(t, st, tenant, "conflicting-"+uniqueSlugSuffix(t), budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
			Action: "block", Thresholds: []float64{1},
		})
		probe := &txProbe{watch: costSampleKind, createErr: store.ErrConflict}
		m.UseData(probeData{ModuleData: m.data, p: probe})

		err := m.onCost(ctx, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime), auditingIngest(t))
		t.Logf("err=%v events=%s", err, probe)
		if err == nil {
			t.Fatalf("the insert conflict was swallowed: the ingestion reported success")
		}
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("err = %v, want the conflict itself", err)
		}
		// The causal is the ORDER: the ledger row really was written first, which is
		// what makes "return nil here" a committed prefix rather than a no-op.
		ledgerAt, insertAt := probe.at("solo", "ledger"), probe.at("solo", "create")
		if ledgerAt == 0 || insertAt == 0 || ledgerAt > insertAt {
			t.Fatalf("ledger=%d sampleInsert=%d: the case did not reach the conflict AFTER the ledger write",
				ledgerAt, insertAt)
		}
		if probe.at("solo", "rollback") == 0 {
			t.Fatalf("the transaction did not roll back: %s", probe)
		}

		if n := countCosts(t, st, tenant); n != 0 {
			t.Errorf("%d canonical ledger rows survived the conflict", n)
		}
		if rows := costSampleRows(t, st, tenant); len(rows) != 0 {
			t.Errorf("%d read-model rows survived the conflict", len(rows))
		}
		if rows := alertRows(t, st, tenant); len(rows) != 0 {
			t.Errorf("%d alert rows survived the conflict", len(rows))
		}
		if n := countAuditAction(t, st, tenant, "finops.cost.ingest"); n != 0 {
			t.Errorf("%d audit events survived the conflict", n)
		}
		if f := host.findings(); len(f) != 0 {
			t.Errorf("a rolled-back ingestion published %d findings", len(f))
		}
	})
}

// TestCostIngestAuditFailureLeavesNothing pins the step-2 position of the audit
// hook: it now runs UNDER the writer lock, and a hook that refuses still aborts the
// whole ingestion before any row is written.
func TestCostIngestAuditFailureLeavesNothing(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	boom := errors.New("finops-test: the principal's audit could not be appended")
	probe := &txProbe{watch: costSampleKind}
	m.UseData(probeData{ModuleData: m.data, p: probe})

	err := m.onCost(context.Background(), tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, oneUSD, baseTime),
		func(context.Context, store.Scope) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the audit failure", err)
	}
	if probe.at("solo", "lock") == 0 {
		t.Errorf("the writer lock was not taken before the audit hook: %s", probe)
	}
	if probe.at("solo", "read") != 0 || probe.at("solo", "create") != 0 ||
		probe.at("solo", "update") != 0 || probe.at("solo", "ledger") != 0 {
		t.Errorf("a refused audit still touched the ledger: %s", probe)
	}
	if n := countCosts(t, st, tenant); n != 0 {
		t.Errorf("%d ledger rows survived a refused audit", n)
	}
	if rows := costSampleRows(t, st, tenant); len(rows) != 0 {
		t.Errorf("%d read-model rows survived a refused audit", len(rows))
	}
	if f := host.findings(); len(f) != 0 {
		t.Errorf("a refused ingestion published %d findings", len(f))
	}
}

// -----------------------------------------------------------------------------
// Causal 3 — the two-transaction barrier: one key, lock order, serial outcome
// -----------------------------------------------------------------------------

// TestCostIngestSerializesOneTenantsWriters is the finite barrier. Two ingestions of
// the SAME natural key run concurrently; the first holds its transaction open until
// the second is provably contending for it, and only then commits.
//
// WHAT IT ASSERTS, and every one of these is causally sound (R3 of the independent
// review). Each claim is either read inside ONE goroutine, where program order is
// real, or read off the durable rows, where the database is the witness:
//
//   - ONE key per ingestion, and it is the key the alert writer has always used;
//   - within each transaction the key precedes the read that decides replay-vs-new,
//     and precedes that transaction's own outcome. This is the half that does NOT
//     depend on the store's own serialization: a lock taken after the natural-key
//     lookup (where it used to be, inside the evaluation) fails here on both engines;
//   - the outcome is the SERIAL one, and it is asserted as a CAUSAL fact rather than
//     as a timeline: the second ingestion UPDATED the row the first INSERTED. An
//     update can only happen if the second transaction's decisive read found a row
//     that the first had already committed — which is the serialization, observed
//     through the data instead of through a marker.
//
// WHAT IT DELIBERATELY DOES NOT ASSERT: any ordering between the two roles' commit
// markers and the other role's lock. Those markers are recorded after Mutate
// RETURNED, i.e. after the commit and after every transaction lock was released, so a
// correct run can record them in any order the scheduler likes. Comparing them would
// fail correct runs; TestTheCommitMarkerTrailsTheRelease produces exactly that
// schedule on purpose.
func TestCostIngestSerializesOneTenantsWriters(t *testing.T) {
	eachIngestBackend(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, _ := openFinCfg(t, cfg)

		// One context for both participating calls: it is the cancellation the barrier
		// needs and the deadline that makes every wait below finite.
		ctx, cancel := context.WithTimeout(context.Background(), barrierBound)
		defer cancel()

		firstLocked := make(chan struct{})
		secondContending := make(chan struct{})
		var lockedOnce, contendOnce sync.Once
		probe := &txProbe{watch: costSampleKind}
		// ENTERING Mutate is the last point the second ingestion can be observed at, on
		// BOTH engines, and the reason bounds what this case proves: the store
		// serializes a tenant's write transactions BEFORE the callback runs — SQLite by
		// admitting one writer, and sqlstore.Mutate on PostgreSQL by taking an exclusive
		// per-tenant advisory lock in lineageWriteTracker.start (lineage_writer.go).
		// A barrier that waited for the second transaction to reach the FinOps key would
		// simply time out, and did: that is a measured harness error from the first
		// version of this case, not a source defect. So the second ingestion blocks
		// inside the STORE, and what this case measures is the composed behaviour. It
		// does not claim the FinOps key is what produced the serialization on this store.
		probe.enterMutate = func(role string) {
			if role == "second" {
				contendOnce.Do(func() { close(secondContending) })
			}
		}
		probe.afterLock = func(role string) {
			if role != "first" {
				return
			}
			lockedOnce.Do(func() { close(firstLocked) })
			// Hold the transaction open until the second ingestion is really contending,
			// then commit. Bounded by the shared context: an unreachable barrier ends the
			// case with both calls cancelled, never with a parked goroutine.
			select {
			case <-secondContending:
			case <-ctx.Done():
			}
		}
		m.UseData(probeData{ModuleData: m.data, p: probe})

		first := startIngest(t, ctx, m, tenant, "first",
			mkCost("anthropic", "claude-opus-4-8", "sess-barrier", 10, 5, 600, baseTime))
		first.awaitLock(t, firstLocked, probe)
		second := startIngest(t, ctx, m, tenant, "second",
			mkCost("anthropic", "claude-opus-4-8", "sess-barrier", 20, 10, 1000, baseTime))

		firstErr, secondErr := first.wait(t), second.wait(t)
		t.Logf("events=%s", probe)
		if firstErr != nil || secondErr != nil {
			t.Fatalf("ingestion errors: first=%v second=%v (a serialized writer loses nothing)", firstErr, secondErr)
		}

		want := alertWriterLockKeyPrefix + tenant.String()
		for _, role := range []string{"first", "second"} {
			keys := probe.lockKeys(role)
			if len(keys) != 1 {
				t.Fatalf("%s locked %v, want exactly one key per ingestion", role, keys)
			}
			if keys[0] != want {
				t.Fatalf("%s lock key = %q, want %q", role, keys[0], want)
			}
			lockAt, readAt := probe.at(role, "lock"), probe.at(role, "read")
			if readAt == 0 || lockAt == 0 || lockAt > readAt {
				t.Fatalf("%s read the cost samples at %d, before locking at %d", role, readAt, lockAt)
			}
			if commitAt := probe.at(role, "commit"); commitAt == 0 || lockAt > commitAt {
				t.Fatalf("%s recorded lock=%d outside its own transaction (commit=%d)", role, lockAt, commitAt)
			}
		}

		// THE SERIALIZATION, read causally: the first transaction INSERTED the row and
		// the second UPDATED it. The second could only reach its update by finding a
		// committed row in its decisive read, so the first commit strictly precedes the
		// second read — a fact about the data, not about when a goroutine got scheduled.
		// Without the serialization both would have found nothing and both would have
		// inserted, and the loser's conflict is now propagated as an error.
		if probe.at("first", "create") == 0 {
			t.Fatalf("the first ingestion did not insert the bucket: %s", probe)
		}
		if probe.at("second", "update") == 0 {
			t.Fatalf("the second ingestion did not see the first's committed row (no update): %s", probe)
		}
		if probe.at("second", "create") != 0 {
			t.Fatalf("the second ingestion inserted a second row for the same bucket: %s", probe)
		}

		// And the durable outcome that follows from it: one bucket, one ledger entry,
		// the second value.
		if n := countCosts(t, st, tenant); n != 1 {
			t.Fatalf("cost records = %d, want 1 (both ingestions were the same bucket)", n)
		}
		if rows := costSampleRows(t, st, tenant); len(rows) != 1 {
			t.Fatalf("read-model rows = %d, want 1", len(rows))
		} else if rows[0].Int(colCostMicroUSD) != 1000 {
			t.Fatalf("read-model cost = %d, want the second ingestion's 1000", rows[0].Int(colCostMicroUSD))
		}
		if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			costs, _, err := sc.Costs().List(context.Background(), model.Query{})
			if err != nil {
				return err
			}
			if costs[0].CostMicroUSD != 1000 || costs[0].InputTokens != 20 {
				t.Errorf("ledger = %d/%d, want the second ingestion's 1000/20",
					costs[0].CostMicroUSD, costs[0].InputTokens)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestTheCommitMarkerTrailsTheRelease is the control for R3 itself: it shows, by
// SCHEDULING the interleaving rather than hoping for it, that a probe marker recorded
// after Mutate returns cannot be compared against a rival transaction's lock.
//
// The run it produces is correct in every way that matters — one key each, taken
// before each decisive read, and the serial outcome — and yet the second
// transaction's lock is recorded BEFORE the first's commit marker. An assertion of
// the form "secondLock > firstCommit" would fail this run. That is why the barrier
// above asserts serialization from the data instead.
//
// SQLite is enough here, and saying why matters: the claim is about WHERE the marker
// is recorded in this test harness, not about an engine's locking. The delay is
// deterministic — the marker is held until the second transaction's lock is observed
// — so the case either produces the schedule or fails on its bound; it never flakes.
func TestTheCommitMarkerTrailsTheRelease(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	ctx, cancel := context.WithTimeout(context.Background(), barrierBound)
	defer cancel()

	firstLocked := make(chan struct{})
	secondContending := make(chan struct{})
	secondLocked := make(chan struct{})
	var lockedOnce, contendOnce, secondOnce sync.Once
	probe := &txProbe{watch: costSampleKind}
	probe.enterMutate = func(role string) {
		if role == "second" {
			contendOnce.Do(func() { close(secondContending) })
		}
	}
	probe.afterLock = func(role string) {
		switch role {
		case "first":
			lockedOnce.Do(func() { close(firstLocked) })
			select {
			case <-secondContending:
			case <-ctx.Done():
			}
		case "second":
			secondOnce.Do(func() { close(secondLocked) })
		}
	}
	// THE SCHEDULE. The first transaction has already returned from Mutate here: it
	// committed and released its locks, and only its MARKER is still pending. Holding
	// it until the second transaction has taken the key reproduces, deterministically,
	// a descheduling the runtime is free to produce on its own.
	probe.beforeMark = func(role string) {
		if role != "first" {
			return
		}
		select {
		case <-secondLocked:
		case <-ctx.Done():
		}
	}
	m.UseData(probeData{ModuleData: m.data, p: probe})

	first := startIngest(t, ctx, m, tenant, "first",
		mkCost("anthropic", "claude-opus-4-8", "sess-marker", 10, 5, 600, baseTime))
	first.awaitLock(t, firstLocked, probe)
	second := startIngest(t, ctx, m, tenant, "second",
		mkCost("anthropic", "claude-opus-4-8", "sess-marker", 20, 10, 1000, baseTime))

	firstErr, secondErr := first.wait(t), second.wait(t)
	t.Logf("events=%s", probe)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("ingestion errors: first=%v second=%v", firstErr, secondErr)
	}

	// The run really is the correct, serialized one.
	if probe.at("first", "create") == 0 || probe.at("second", "update") == 0 {
		t.Fatalf("this schedule was not the serial one, so it proves nothing: %s", probe)
	}
	if n := countCosts(t, st, tenant); n != 1 {
		t.Fatalf("cost records = %d, want 1", n)
	}
	for _, role := range []string{"first", "second"} {
		lockAt, readAt := probe.at(role, "lock"), probe.at(role, "read")
		if lockAt == 0 || readAt == 0 || lockAt > readAt {
			t.Fatalf("%s: lock=%d read=%d, the run was not the correct one", role, lockAt, readAt)
		}
	}

	// And the marker order is inverted with respect to the discarded assertion.
	secondLock, firstCommit := probe.at("second", "lock"), probe.at("first", "commit")
	if secondLock == 0 || firstCommit == 0 {
		t.Fatalf("incomplete run: secondLock=%d firstCommit=%d", secondLock, firstCommit)
	}
	if !(secondLock < firstCommit) {
		t.Fatalf("the scheduled delay did not take effect (secondLock=%d firstCommit=%d): "+
			"this case must produce the trailing marker to be evidence of anything",
			secondLock, firstCommit)
	}
	t.Logf("a correctly serialized run recorded secondLock=%d BEFORE firstCommit=%d: "+
		"the discarded assertion secondLock>firstCommit would have failed it",
		secondLock, firstCommit)
}

// TestCostIngestDoesNotSerializeAcrossTenants is the counterpart: the key carries
// the tenant, so one tenant's ingestion must not hold another tenant's. It runs on
// PostgreSQL only, and that is a property of the ENGINES, not a gap: SQLite admits
// a single writer whatever the key is, so "tenant B proceeded while A held its
// lock" is not a statement SQLite can make either way.
//
// The barrier is the strict one — A holds its transaction open until B's whole
// ingestion has COMMITTED — and it is legitimate here precisely because the tenants
// differ: the store's own per-tenant lock (lineageWriteTracker.start) is taken on
// tenant A, so tenant B's callback is free to enter. A same-tenant case must never
// require that, and the one above does not. A key that did not carry the tenant would
// deadlock here and be caught by the bound rather than by a passing assertion.
//
// The one cross-role marker comparison this file makes lives here, and it is sound
// for a reason that has nothing to do with luck: B's commit marker is recorded before
// otherDone is closed, otherDone gates A's release, and A's marker is recorded after
// that. The order is established by the handshake, not observed after the fact.
func TestCostIngestDoesNotSerializeAcrossTenants(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the cross-tenant leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	m, st, tenantA, _ := openFinCfg(t, store.Config{
		Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 4,
	})
	// provisionTenant (alert_evidence_upgrade_test.go) makes the second business
	// tenant inside the same database.
	tenantB := provisionTenant(t, st)

	ctx, cancel := context.WithTimeout(context.Background(), barrierBound)
	defer cancel()

	holderLocked := make(chan struct{})
	otherDone := make(chan struct{})
	var lockedOnce sync.Once
	probe := &txProbe{watch: costSampleKind}
	probe.afterLock = func(role string) {
		if role != "holder" {
			return
		}
		lockedOnce.Do(func() { close(holderLocked) })
		select {
		case <-otherDone:
		case <-ctx.Done():
		}
	}
	m.UseData(probeData{ModuleData: m.data, p: probe})

	holder := startIngest(t, ctx, m, tenantA, "holder",
		mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 600, baseTime))
	// The finite first-lock handshake replaces the poll loop this case used to run:
	// either the holder reaches its lock, or it finished first and its own error is
	// the failure, or the bound expires.
	holder.awaitLock(t, holderLocked, probe)

	otherErr := m.onCost(withProbeRole(ctx, "other"), tenantB,
		mkCost("anthropic", "claude-opus-4-8", "", 1, 1, 900, baseTime), nil)
	close(otherDone)
	holderErr := holder.wait(t)

	t.Logf("events=%s", probe)
	if holderErr != nil || otherErr != nil {
		t.Fatalf("ingestion errors: holder=%v other=%v", holderErr, otherErr)
	}
	otherCommit, holderCommit := probe.at("other", "commit"), probe.at("holder", "commit")
	if otherCommit == 0 || holderCommit == 0 {
		t.Fatalf("incomplete run: otherCommit=%d holderCommit=%d", otherCommit, holderCommit)
	}
	if otherCommit > holderCommit {
		t.Fatalf("the second tenant committed after the first: the barrier did not overlap them: %s", probe)
	}
	if n := countCosts(t, st, tenantA); n != 1 {
		t.Errorf("tenant A ledger rows = %d, want 1", n)
	}
	if n := countCosts(t, st, tenantB); n != 1 {
		t.Errorf("tenant B ledger rows = %d, want 1", n)
	}
}

// -----------------------------------------------------------------------------
// Causal 3-bis — the accounting instant is fixed before the transaction
// -----------------------------------------------------------------------------

// clockAdvancingData advances a fake clock as a transaction is ENTERED. That is what
// waiting does to the wall clock: the store admits one write transaction per tenant
// at a time, so an ingestion can sit outside its callback for an arbitrary time
// before it runs. The decorator makes that wait deterministic and large enough to
// cross an accounting period, which no timing-dependent case could do reliably.
type clockAdvancingData struct {
	api.ModuleData
	clock *fakeClock
	by    time.Duration
}

func (d clockAdvancingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.clock.advance(d.by)
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// TestAnOmittedInstantIsTheOneFromBeforeTheTransaction is R2 of the independent
// review. A cost sample may omit occurred_at — the HTTP DTO accepts that and passes
// the zero value through — and the module then stamps it with its own clock. WHERE it
// reads that clock is a contract, not an implementation detail: read before the
// transaction, the stamp is the instant the sample was submitted at; read inside the
// callback, it is the instant the store finally admitted the writer, and the sample's
// natural key, its CostRecord timestamp and its accounting PERIOD all move with the
// wait.
//
// The delay here crosses a month boundary, so the difference is not a few
// milliseconds of drift but the month the spend is billed in.
func TestAnOmittedInstantIsTheOneFromBeforeTheTransaction(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clock := &fakeClock{t: baseTime}
	m.clock = clock
	const admissionDelay = 21 * 24 * time.Hour // baseTime is 2026-06-10: this lands in July
	m.UseData(clockAdvancingData{ModuleData: m.data, clock: clock, by: admissionDelay})

	submitted := baseTime
	delayed := baseTime.Add(admissionDelay)
	omitted := mkCost("anthropic", "claude-opus-4-8", "s-omitted", 10, 5, 600, time.Time{})
	if err := m.onCost(context.Background(), tenant, omitted, nil); err != nil {
		t.Fatalf("onCost: %v", err)
	}

	rows := costSampleRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("read-model rows = %d, want 1", len(rows))
	}
	got, err := model.ParseTimestamp(rows[0].String(colOccurredAt))
	if err != nil {
		t.Fatalf("stored occurred_at %q: %v", rows[0].String(colOccurredAt), err)
	}
	t.Logf("submitted=%s admitted=%s stored=%s", submitted.UTC(), delayed.UTC(), got.Time().UTC())
	if !got.Time().Equal(submitted) {
		t.Fatalf("stored occurred_at = %s, want the pre-transaction instant %s (the admission instant was %s)",
			got.Time().UTC(), submitted.UTC(), delayed.UTC())
	}
	if got.Time().UTC().Month() != submitted.UTC().Month() {
		t.Errorf("the sample was accounted to %s, want %s: the accounting period followed the wait",
			got.Time().UTC().Month(), submitted.UTC().Month())
	}

	// The canonical ledger entry carries the same instant: the read-model row and the
	// CostRecord are stamped from one value, and that value is the caller's.
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		costs, _, err := sc.Costs().List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(costs) != 1 {
			t.Fatalf("cost records = %d, want 1", len(costs))
		}
		if !costs[0].OccurredAt.Time().Equal(submitted) {
			t.Errorf("ledger occurred_at = %s, want %s", costs[0].OccurredAt.Time().UTC(), submitted.UTC())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A sample that DOES carry an instant is untouched by any of this: the fallback is
	// the only thing the clock decides.
	explicit := baseTime.Add(-2 * time.Hour)
	if err := m.onCost(context.Background(), tenant, mkCost("anthropic", "claude-opus-4-8", "s-explicit", 1, 1, 60, explicit), nil); err != nil {
		t.Fatalf("onCost (explicit instant): %v", err)
	}
	for _, r := range costSampleRows(t, st, tenant) {
		if r.String(colSessionRef) != "s-explicit" {
			continue
		}
		at, err := model.ParseTimestamp(r.String(colOccurredAt))
		if err != nil {
			t.Fatalf("stored occurred_at %q: %v", r.String(colOccurredAt), err)
		}
		if !at.Time().Equal(explicit) {
			t.Errorf("an explicit occurred_at was rewritten to %s, want %s", at.Time().UTC(), explicit.UTC())
		}
	}
}

// -----------------------------------------------------------------------------
// Causal 4 — the reservation writers take the same key before their decisive read
// -----------------------------------------------------------------------------

// TestReservationWritersTakeTheWriterLockBeforeTheirDecisiveRead covers the three
// reservation transactions: the reserve (whose decisive read is the seq probe that
// the INSERT then proves), the settlement and the sweep. Each takes ONE key, the
// shared one, before it reads the ledger it is about to decide on.
func TestReservationWritersTakeTheWriterLockBeforeTheirDecisiveRead(t *testing.T) {
	eachIngestBackend(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		clock := &fakeClock{t: baseTime}
		m.clock = clock
		createBudget(t, st, tenant, "reserving-"+uniqueSlugSuffix(t), budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		want := alertWriterLockKeyPrefix + tenant.String()

		for _, tc := range []struct {
			name string
			run  func(t *testing.T, role string) string // returns a handle when it made one
		}{
			{
				name: "reserve",
				run: func(t *testing.T, role string) string {
					res, err := m.ReserveBudget(withProbeRole(context.Background(), role), tenant, SpendDims{}, oneUSD)
					if err != nil {
						t.Fatalf("ReserveBudget: %v", err)
					}
					if !res.Allowed {
						t.Fatalf("reservation denied with headroom: %+v", res)
					}
					return res.Handle
				},
			},
			{
				name: "settle",
				run: func(t *testing.T, role string) string {
					res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
					if err != nil || !res.Allowed {
						t.Fatalf("seed reservation: %v %+v", err, res)
					}
					if err := m.CommitReservation(withProbeRole(context.Background(), role), tenant, res.Handle, oneUSD); err != nil {
						t.Fatalf("CommitReservation: %v", err)
					}
					return res.Handle
				},
			},
			{
				name: "sweep",
				run: func(t *testing.T, role string) string {
					res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
					if err != nil || !res.Allowed {
						t.Fatalf("seed reservation: %v %+v", err, res)
					}
					clock.advance(2 * reservationTTL)
					defer clock.advance(-2 * reservationTTL)
					if _, err := m.SweepExpiredReservations(withProbeRole(context.Background(), role), tenant); err != nil {
						t.Fatalf("SweepExpiredReservations: %v", err)
					}
					return res.Handle
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				probe := &txProbe{watch: budgetReservationKind}
				live := m.data
				m.UseData(probeData{ModuleData: live, p: probe})
				defer m.UseData(live)

				role := tc.name
				tc.run(t, role)

				t.Logf("events=%s", probe)
				keys := probe.lockKeys(role)
				if len(keys) != 1 {
					t.Fatalf("%s locked %v, want exactly one key per transaction", tc.name, keys)
				}
				if keys[0] != want {
					t.Fatalf("%s lock key = %q, want the shared FinOps writer key %q", tc.name, keys[0], want)
				}
				lockAt, readAt := probe.at(role, "lock"), probe.at(role, "read")
				if lockAt == 0 || readAt == 0 {
					t.Fatalf("%s did not reach both steps: %s", tc.name, probe)
				}
				if lockAt > readAt {
					t.Fatalf("%s read the reservation ledger at %d before locking at %d", tc.name, readAt, lockAt)
				}
			})
		}
	})
}

// -----------------------------------------------------------------------------
// Causal 5 — the SUPPORTED SCOPE of the participating monetary writers
// -----------------------------------------------------------------------------
//
// The two controls below replace a single case that asserted the new refusal against
// nothing but itself. They pin the boundary from both sides, because a boundary is
// only a contract if the supported side is shown to work:
//
//   - a decorator that FORWARDS store.TransactionLocker keeps every outcome the
//     reservation seam had before this cut — headroom, exhausted limit, settlement,
//     expiry sweep;
//   - a decorator that HIDES it is refused, and the refusal is shaped exactly as the
//     public GoDoc says: an error with Allowed:true and NO handle, nothing written,
//     nothing transitioned, and a sweep that reports zero rather than a count for
//     work it did not do.
//
// Both cases LOG the observed tuple of every operation before asserting on it, so a
// run of these same controls against the pre-cut sources records what that build
// actually did instead of leaving the difference to prose.

// forwardingScope is the decorator the new requirement asks a caller to write: it
// wraps a scope and FORWARDS the optional capability instead of dropping it. Nothing
// else is overridden — it is the minimal correct wrapper, and the same shape the
// GoDoc on ReserveBudget shows.
type forwardingScope struct{ store.Scope }

func (s forwardingScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

// decoratedData puts one of the two decorators on every scope the module is handed.
// noLockScope (alert_record_evidence_test.go) is the hiding one: it embeds only
// store.Scope, which is what a wrapper written without thinking about optional
// methods produces. Neither fixture fakes a lock.
type decoratedData struct {
	api.ModuleData
	hide bool
}

func (d decoratedData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func (d decoratedData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func (d decoratedData) wrap(sc store.Scope) store.Scope {
	if d.hide {
		return noLockScope{Scope: sc}
	}
	return forwardingScope{Scope: sc}
}

// handleState returns the state of the reservation rows carrying a handle.
func handleState(t *testing.T, st store.Store, tenant model.TenantID, handle string) []string {
	t.Helper()
	var out []string
	for _, r := range reservationRows(t, st, tenant) {
		if r.String(colResvHandle) == handle {
			out = append(out, r.String(colResvState))
		}
	}
	return out
}

// TestAForwardingDecoratorKeepsEveryReservationOutcome is the supported side of the
// boundary: through a scope decorator that forwards the capability, the reservation
// seam still does all four things it did before the writer lock was required of it.
func TestAForwardingDecoratorKeepsEveryReservationOutcome(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clock := &fakeClock{t: baseTime}
	m.clock = clock
	createBudget(t, st, tenant, "forwarded", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.UseData(decoratedData{ModuleData: m.data})
	ctx := context.Background()

	// 1. AVAILABLE HEADROOM: admitted, with a handle.
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	t.Logf("headroom reserve: allowed=%v handle=%q err=%v", res.Allowed, res.Handle, err)
	if err != nil {
		t.Fatalf("a reservation with headroom failed through a forwarding decorator: %v", err)
	}
	if !res.Allowed || res.Handle == "" {
		t.Fatalf("reserve with headroom = %+v, want an admission carrying a handle", res)
	}

	// 2. EXHAUSTED LIMIT: a normal DENY, not an error — the case the replaced control
	// never exercised at all.
	denied, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 20*oneUSD)
	t.Logf("exhausted reserve: allowed=%v action=%q reason=%q err=%v",
		denied.Allowed, denied.Action, denied.Reason, err)
	if err != nil {
		t.Fatalf("an exhausted budget returned an error instead of a denial: %v", err)
	}
	if denied.Allowed {
		t.Fatalf("reserve past the limit = %+v, want a denial", denied)
	}
	if denied.Reason == "" || denied.Action != "block" {
		t.Errorf("denial = %+v, want the block action and a reason", denied)
	}

	// 3. SETTLEMENT: the handle transitions.
	if err := m.CommitReservation(ctx, tenant, res.Handle, oneUSD); err != nil {
		t.Fatalf("CommitReservation through a forwarding decorator: %v", err)
	}
	if got := handleState(t, st, tenant, res.Handle); len(got) != 1 || got[0] != resvStateCommitted {
		t.Errorf("settled reservation state = %v, want [%s]", got, resvStateCommitted)
	}

	// 4. EXPIRY SWEEP: a second reservation, left to lapse, is swept and counted.
	lapsing, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	if err != nil || !lapsing.Allowed {
		t.Fatalf("second reservation: %v %+v", err, lapsing)
	}
	clock.advance(2 * reservationTTL)
	n, err := m.SweepExpiredReservations(ctx, tenant)
	t.Logf("expiry sweep: swept=%d err=%v", n, err)
	if err != nil {
		t.Fatalf("SweepExpiredReservations through a forwarding decorator: %v", err)
	}
	if n != 1 {
		t.Errorf("swept = %d, want the 1 lapsed reservation", n)
	}
	if got := handleState(t, st, tenant, lapsing.Handle); len(got) != 1 || got[0] != resvStateExpired {
		t.Errorf("lapsed reservation state = %v, want [%s]", got, resvStateExpired)
	}
}

// TestACapabilityHidingScopeIsUnsupportedForTheMonetaryWriters is the unsupported
// side, and it is a NEW boundary rather than a preserved posture: before this cut the
// same decorator reserved, denied, settled and swept exactly as the case above shows.
// It is documented as a break on the public methods, and this is what the break looks
// like.
//
// The shape of the refusal is the assertion that matters, and D02/R1 SUPERSEDED IT.
//
// BEFORE (this cut's original text, kept so the change is legible): "Reserve returns
// Allowed:true WITH the error and WITHOUT a handle: that is the seam's pre-existing
// outage posture (a failure that decided nothing), and turning it into a deny would
// be a new monetary guarantee invented here rather than adjudicated."
//
// AFTER: Reserve returns Allowed:FALSE with a typed error and no handle. What changed
// is not the appetite for monetary guarantees; it is that the same tenant may now
// carry a durable activation frontier, and a scope that cannot take the writer lock
// never reaches the guard that would read it. Allowed:true is a permission whatever
// accompanies it, and this path has established nothing — not that there is headroom,
// and not that there is no boundary to cross. The independent return named this route
// explicitly (R1: "opening Mutate and lock failure"), and root ratified it.
//
// The rest of the case is untouched, including the two facts it was written for: the
// refusal carries an error and no handle, and NOTHING is written.
func TestACapabilityHidingScopeIsUnsupportedForTheMonetaryWriters(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	clock := &fakeClock{t: baseTime}
	m.clock = clock
	createBudget(t, st, tenant, "unserializable", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	ctx := context.Background()
	// Seeded through the undecorated handle, so the settlement and the sweep below
	// have a real row to fail on rather than an absent one.
	seeded, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	if err != nil || !seeded.Allowed {
		t.Fatalf("seed reservation: %v %+v", err, seeded)
	}

	m.UseData(decoratedData{ModuleData: m.data, hide: true})

	// 1. AVAILABLE HEADROOM.
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	t.Logf("headroom reserve, capability hidden: allowed=%v handle=%q err=%v", res.Allowed, res.Handle, err)
	if err == nil {
		t.Fatalf("a reservation with headroom was decided through a capability-hiding scope (%+v): "+
			"that tuple is the PRE-CUT behaviour, not the supported-scope contract", res)
	}
	if res.Allowed {
		t.Errorf("the refusal admitted (%+v): a scope that cannot take the writer lock never read "+
			"this tenant's activation frontier, so it may not issue a permission", res)
	}
	if attemptCode(err) == "" {
		t.Errorf("the refusal is untyped (%v): a caller cannot classify it", err)
	}
	if res.Handle != "" {
		t.Errorf("the refusal carried handle %q: a refused reservation is not a reservation", res.Handle)
	}

	// 2. EXHAUSTED LIMIT — the difference the boundary makes, in the one case the
	// replaced control never covered. With the capability the same call DENIES
	// (Allowed:false, no error); without it there is no decision at all.
	exhausted, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 20*oneUSD)
	t.Logf("exhausted reserve, capability hidden: allowed=%v reason=%q err=%v",
		exhausted.Allowed, exhausted.Reason, err)
	if err == nil {
		t.Fatalf("an exhausted budget was decided through a capability-hiding scope (%+v): "+
			"that is the PRE-CUT deny, and the boundary must refuse instead of deciding", exhausted)
	}
	if exhausted.Allowed || exhausted.Handle != "" {
		t.Errorf("refusal = %+v, want a refusal with no handle: nothing was decided, and "+
			"nothing was confirmed about this tenant's frontier either", exhausted)
	}

	// 3. SETTLEMENT, both directions: nothing transitions.
	if err := m.CommitReservation(ctx, tenant, seeded.Handle, oneUSD); err == nil {
		t.Errorf("a settlement proceeded through a capability-hiding scope")
	} else {
		t.Logf("commit, capability hidden: err=%v", err)
	}
	if err := m.ReleaseReservation(ctx, tenant, seeded.Handle); err == nil {
		t.Errorf("a release proceeded through a capability-hiding scope")
	}

	// 4. EXPIRY SWEEP: zero AND an error, never a count for work it did not do.
	clock.advance(2 * reservationTTL)
	n, err := m.SweepExpiredReservations(ctx, tenant)
	t.Logf("expiry sweep, capability hidden: swept=%d err=%v", n, err)
	if err == nil {
		t.Errorf("a sweep proceeded through a capability-hiding scope (swept %d)", n)
	}
	if n != 0 {
		t.Errorf("a refused sweep reported %d swept", n)
	}

	// Nothing moved: the seeded reservation is untouched and no second row exists.
	rows := reservationRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("reservation rows = %d, want the seeded one only", len(rows))
	}
	if got := rows[0].String(colResvState); got != resvStateActive {
		t.Errorf("seeded reservation state = %q, want %q", got, resvStateActive)
	}
}

// reservationRows returns the tenant's reservation ledger rows.
func reservationRows(t *testing.T, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("reservationRows: %v", err)
	}
	return out
}
