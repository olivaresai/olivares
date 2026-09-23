// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE TWO REPLAYS, AND NOTHING ELSE. An admission row answers for a retry of the call
// that wrote it in exactly two cases: it is committed inside the replay window, or it
// is reserved inside the window and its hold still withholds. Every other request is
// evaluated. A key reused with another payload is never a replay.
// -----------------------------------------------------------------------------

// errLedgerUnreadable is the read failure ledgerFaultScope serves.
var errLedgerUnreadable = errors.New("finops-test: the ledger could not be read")

// ledgerFaultScope serves the reservation ledger and the cost samples through a
// repository whose reads fail — or, with incomplete set, end on a page that promises
// more rows and gives no cursor. Everything else is the real scope. A replay that
// reads money it has no business reading fails through it.
type ledgerFaultScope struct {
	store.Scope
	incomplete bool
}

func (s ledgerFaultScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || (kind != budgetReservationKind && kind != costSampleKind) {
		return repo, err
	}
	return ledgerFaultRepo{GenericRepo: repo, incomplete: s.incomplete}, nil
}

type ledgerFaultRepo struct {
	store.GenericRepo
	incomplete bool
}

func (r ledgerFaultRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if !r.incomplete {
		return nil, model.Page{}, errLedgerUnreadable
	}
	recs, _, err := r.GenericRepo.List(ctx, q)
	return recs, model.Page{HasMore: true}, err
}

// errReadRefused is the failure readNothingScope and refusingData serve.
var errReadRefused = errors.New("finops-test: a read this answer must not make")

// readNothingScope refuses every read an answer could make beyond the admission row it
// was handed: every extension repository — the ledger, the cost samples, the admission
// rows themselves — and the policies, identities, agent groups and cost records. An
// answer that stands through it read nothing but that row and the clock.
type readNothingScope struct{ store.Scope }

func (readNothingScope) Ext(model.Kind) (store.GenericRepo, error) { return nil, errReadRefused }

func (readNothingScope) Policies() store.Repository[model.Policy] {
	return refusingRepo[model.Policy]{}
}

func (readNothingScope) Identities() store.MutableRepository[model.Identity] {
	return refusingRepo[model.Identity]{}
}

func (readNothingScope) AgentGroups() store.Repository[model.AgentGroup] {
	return refusingRepo[model.AgentGroup]{}
}

func (readNothingScope) AgentGroupMembers() store.Repository[model.AgentGroupMember] {
	return refusingRepo[model.AgentGroupMember]{}
}

func (readNothingScope) Costs() store.Repository[model.CostRecord] {
	return refusingRepo[model.CostRecord]{}
}

// refusingRepo is a repository whose every call fails with errReadRefused.
type refusingRepo[T any] struct{}

func (refusingRepo[T]) Get(context.Context, model.ID) (T, error) {
	var zero T
	return zero, errReadRefused
}

func (refusingRepo[T]) List(context.Context, model.Query) ([]T, model.Page, error) {
	return nil, model.Page{}, errReadRefused
}

func (refusingRepo[T]) Create(context.Context, T) (T, error) {
	var zero T
	return zero, errReadRefused
}

func (refusingRepo[T]) Update(context.Context, T) (T, error) {
	var zero T
	return zero, errReadRefused
}

func (refusingRepo[T]) Delete(context.Context, model.ID) error { return errReadRefused }

// refusingData opens no transaction and reads no directory, so an answer that goes back
// to the module's own data handle fails through it.
type refusingData struct{}

func (refusingData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return errReadRefused
}

func (refusingData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return errReadRefused
}

func (refusingData) AuthView(context.Context, func(store.AuthScope) error) error {
	return errReadRefused
}

// replayFixture is one tenant with a clock at t0.
type replayFixture struct {
	m      *Module
	st     store.Store
	tenant model.TenantID
	clk    *fakeClock
	policy model.ID
	seq    int64
}

func newReplayFixture(t *testing.T, cfg store.Config) *replayFixture {
	t.Helper()
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	return &replayFixture{m: m, st: st, tenant: tenant, clk: clk, policy: model.NewID()}
}

// admission writes one admission row whose payload hashes to "hash-"+key.
func (f *replayFixture) admission(t *testing.T, key, state string, handle, spend holdID, stateAt time.Time) {
	t.Helper()
	seedAdmission(t, f.st, f.tenant, admissionRecord(key, state, handle, spend, stateAt))
}

// hold writes one active ledger row under h, created at created, so it withholds
// until created plus the TTL.
func (f *replayFixture) hold(t *testing.T, h holdID, created time.Time) {
	t.Helper()
	f.seq++
	seedReservation(t, f.st, f.tenant, ledgerRow(f.policy, "b", h, f.seq, 2*oneUSD, created, created.Add(reservationTTL), resvStateActive))
}

// replay reads the row of key and asks it about a request hashing to hash, in one
// read transaction; wrap, when set, replaces the scope the question is asked through.
func (f *replayFixture) replay(t *testing.T, key, hash string, wrap func(store.Scope) store.Scope) (replayOutcome, holdID, error) {
	t.Helper()
	ctx := context.Background()
	var (
		outcome replayOutcome
		h       holdID
		asked   error
	)
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		row, found, err := rowOfKey(ctx, sc, key)
		if err != nil || !found {
			t.Fatalf("the row of %q: found=%v err=%v", key, found, err)
		}
		if wrap != nil {
			sc = wrap(sc)
		}
		outcome, h, asked = f.m.replayFor(ctx, sc, row, hash)
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	return outcome, h, asked
}

// wantReplay fails unless the row answered with exactly h.
func wantReplay(t *testing.T, what string, outcome replayOutcome, got, h holdID, err error) {
	t.Helper()
	if err != nil || outcome != replayAnswer || got != h {
		t.Errorf("%s: outcome=%v hold=%q err=%v; want a replay of %q", what, outcome, got, err, h)
	}
}

// wantEvaluate fails unless the row did not answer and the request goes to evaluation.
func wantEvaluate(t *testing.T, what string, outcome replayOutcome, got holdID, err error) {
	t.Helper()
	if err != nil || outcome != replayEvaluate || !got.isZero() {
		t.Errorf("%s: outcome=%v hold=%q err=%v; want an evaluation", what, outcome, got, err)
	}
}

// TestReplayIgnoresHoldFreeRow: an admission that holds nothing has nothing to hand
// back, so its row never answers — every repeat of its key is evaluated afresh, which
// is how new spend and a new budget reach a stable key. Neither does a released or
// pending row. A row that names a hold under which a complete read finds no ledger row
// holds nothing either, in its handle slot or, for a spend-only row, in its spend slot.
// The controls answer: a row whose hold withholds, and a pair one of whose holds has no
// ledger row while the other withholds — a slot with no rows neither justifies a replay
// nor stops one.
func TestReplayIgnoresHoldFreeRow(t *testing.T) {
	forEachAdmissionEngine(t, runReplayIgnoresHoldFreeRow)
}

func runReplayIgnoresHoldFreeRow(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	t0 := baseTime
	held, released, intent := newHoldID(), newHoldID(), newHoldID()
	f.admission(t, "session_launch/run-42", admStateReserved, "", "", t0)
	f.admission(t, "released", admStateReleased, released, "", t0)
	f.hold(t, released, t0)
	f.admission(t, "pending", admStatePending, intent, "", t0)
	f.admission(t, "control", admStateReserved, held, "", t0)
	f.hold(t, held, t0)
	empty, spendEmpty, pairLive, pairEmpty := newHoldID(), newHoldID(), newHoldID(), newHoldID()
	f.admission(t, "names-a-hold-without-rows", admStateReserved, empty, "", t0)
	f.admission(t, "spend-only-without-rows", admStateReserved, "", spendEmpty, t0)
	f.admission(t, "pair-with-an-empty-half", admStateReserved, pairLive, pairEmpty, t0)
	f.hold(t, pairLive, t0)
	f.clk.advance(time.Second)

	o, h, err := f.replay(t, "session_launch/run-42", "hash-session_launch/run-42", nil)
	wantEvaluate(t, "a published admission that holds nothing", o, h, err)
	o, h, err = f.replay(t, "released", "hash-released", nil)
	wantEvaluate(t, "a released admission", o, h, err)
	o, h, err = f.replay(t, "pending", "hash-pending", nil)
	wantEvaluate(t, "a claim in flight", o, h, err)
	o, h, err = f.replay(t, "control", "hash-control", nil)
	wantReplay(t, "control: a withholding hold", o, h, held, err)
	o, h, err = f.replay(t, "names-a-hold-without-rows", "hash-names-a-hold-without-rows", nil)
	wantEvaluate(t, "a hold with no ledger row", o, h, err)
	o, h, err = f.replay(t, "spend-only-without-rows", "hash-spend-only-without-rows", nil)
	wantEvaluate(t, "a spend-only row whose hold has no ledger row", o, h, err)
	o, h, err = f.replay(t, "pair-with-an-empty-half", "hash-pair-with-an-empty-half", nil)
	wantReplay(t, "control: a pair whose other hold withholds", o, h, pairLive, err)

	ctx := context.Background()
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		live, lerr := handlesLive(ctx, sc, "", "", f.m.clock.Now())
		if lerr != nil || live {
			t.Errorf(`handlesLive("", "") = %v, %v; want false: no hold is not a live hold`, live, lerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReplayReturnsLiveHold: a retry inside the window of a call whose hold still
// withholds is handed that hold, and spend recorded since does not change the answer —
// the hold is that call's money, and a retry must not hold twice. A hold that has
// lapsed inside the window is not handed back. The window is never shorter than the
// reservation TTL, so once it closes no row of the hold it handed out still withholds.
func TestReplayReturnsLiveHold(t *testing.T) {
	forEachAdmissionEngine(t, runReplayReturnsLiveHold)
}

func runReplayReturnsLiveHold(t *testing.T, cfg store.Config) {
	if admissionReplayWindow < reservationTTL {
		t.Fatalf("the replay window (%v) is shorter than the reservation TTL (%v): a key could be "+
			"taken over while the hold it handed out still withholds", admissionReplayWindow, reservationTTL)
	}
	f := newReplayFixture(t, cfg)
	t0 := baseTime
	createBudget(t, f.st, f.tenant, "B", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD, Action: "block",
	})
	h, lapsing := newHoldID(), newHoldID()
	f.admission(t, "model_gateway/held", admStateReserved, h, "", t0)
	f.hold(t, h, t0)
	f.hold(t, h, t0)
	// Published ten seconds after its rows were created: they lapse inside its window.
	f.admission(t, "model_gateway/lapsing", admStateReserved, lapsing, "", t0.Add(10*time.Second))
	f.hold(t, lapsing, t0)

	f.clk.advance(time.Minute)
	o, got, err := f.replay(t, "model_gateway/held", "hash-model_gateway/held", nil)
	wantReplay(t, "a retry of a held call", o, got, h, err)

	f.m.ingest(t, f.tenant, mkCost("anthropic", "model", "s1", 1, 1, 50*oneUSD, t0))
	o, got, err = f.replay(t, "model_gateway/held", "hash-model_gateway/held", nil)
	wantReplay(t, "a retry after spend was recorded", o, got, h, err)

	f.clk.advance(reservationTTL - time.Minute + time.Second)
	o, got, err = f.replay(t, "model_gateway/lapsing", "hash-model_gateway/lapsing", nil)
	wantEvaluate(t, "a hold that lapsed inside the window", o, got, err)
	if n := len(activeRowsUnder(t, f.st, f.tenant, h)); n != 2 {
		t.Fatalf("asking a row about a retry moved its hold: %d active row(s), want 2", n)
	}
}

// TestReplayReadsBothLegacySlots: a legacy pair names a budget hold and a seat hold, and
// a spend-only admission names its one hold in the seat slot. A replay reads both
// slots: a pair answers with its budget hold only while both halves withhold, and a
// spend-only row answers with its seat hold.
func TestReplayReadsBothLegacySlots(t *testing.T) {
	forEachAdmissionEngine(t, runReplayReadsBothLegacySlots)
}

func runReplayReadsBothLegacySlots(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	tu := baseTime
	c1, c2 := newHoldID(), newHoldID()
	j2 := newHoldID()
	p1, p2 := newHoldID(), newHoldID()
	q1, q2 := newHoldID(), newHoldID()
	// (c) both halves withholding; (j) spend-only, withholding.
	f.admission(t, "legacy-c", admStateReserved, c1, c2, tu.Add(-60*time.Second))
	f.hold(t, c1, tu.Add(-60*time.Second))
	f.hold(t, c2, tu.Add(-60*time.Second))
	f.admission(t, "legacy-j", admStateReserved, "", j2, tu.Add(-30*time.Second))
	f.hold(t, j2, tu.Add(-30*time.Second))
	// A pair whose seat half has lapsed, and one whose budget half has.
	f.admission(t, "legacy-p", admStateReserved, p1, p2, tu.Add(-60*time.Second))
	f.hold(t, p1, tu.Add(-60*time.Second))
	f.hold(t, p2, tu.Add(-400*time.Second))
	f.admission(t, "legacy-q", admStateReserved, q1, q2, tu.Add(-60*time.Second))
	f.hold(t, q1, tu.Add(-400*time.Second))
	f.hold(t, q2, tu.Add(-60*time.Second))

	o, h, err := f.replay(t, "legacy-c", "hash-legacy-c", nil)
	wantReplay(t, "(c) a withholding pair", o, h, c1, err)
	o, h, err = f.replay(t, "legacy-j", "hash-legacy-j", nil)
	wantReplay(t, "(j) a spend-only row", o, h, j2, err)
	o, h, err = f.replay(t, "legacy-p", "hash-legacy-p", nil)
	wantEvaluate(t, "a pair whose seat half lapsed", o, h, err)
	o, h, err = f.replay(t, "legacy-q", "hash-legacy-q", nil)
	wantEvaluate(t, "a pair whose budget half lapsed", o, h, err)
}

// TestUndatedRowIsOutsideWindow: a row written before state_at existed cannot be shown
// to be a young retry, so it never answers, whatever its hold is doing. The control is
// the same row, dated.
func TestUndatedRowIsOutsideWindow(t *testing.T) {
	forEachAdmissionEngine(t, runUndatedRowIsOutsideWindow)
}

func runUndatedRowIsOutsideWindow(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	tu := baseTime
	k1, e1, d1 := newHoldID(), newHoldID(), newHoldID()
	f.admission(t, "legacy-k", admStateReserved, k1, "", time.Time{})
	f.hold(t, k1, tu.Add(-90*time.Second))
	f.admission(t, "legacy-undated-commit", admStateCommitted, e1, "", time.Time{})
	f.admission(t, "dated", admStateReserved, d1, "", tu.Add(-90*time.Second))
	f.hold(t, d1, tu.Add(-90*time.Second))

	o, h, err := f.replay(t, "legacy-k", "hash-legacy-k", nil)
	wantEvaluate(t, "(k) an undated row with a withholding hold", o, h, err)
	o, h, err = f.replay(t, "legacy-undated-commit", "hash-legacy-undated-commit", nil)
	wantEvaluate(t, "an undated committed row", o, h, err)
	o, h, err = f.replay(t, "dated", "hash-dated", nil)
	wantReplay(t, "control: the same row, dated", o, h, d1, err)
}

// TestUnreadableHoldIsAnError: whether a reserved row's hold withholds is read from its
// ledger rows, and a read that did not complete is not "no longer withholding" — that
// answer would move a live, received hold to owed. It is an error, which the caller
// answers deny-closed, and its outcome is unknown, never "evaluate" — also for a hold
// that has no ledger row, which only a complete read may show. The control is the same
// row read completely.
func TestUnreadableHoldIsAnError(t *testing.T) {
	forEachAdmissionEngine(t, runUnreadableHoldIsAnError)
}

func runUnreadableHoldIsAnError(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	h := newHoldID()
	f.admission(t, "model_gateway/held", admStateReserved, h, "", baseTime)
	f.hold(t, h, baseTime)
	f.admission(t, "model_gateway/no-rows", admStateReserved, newHoldID(), "", baseTime)
	f.clk.advance(time.Minute)

	incomplete := func(sc store.Scope) store.Scope { return ledgerFaultScope{Scope: sc, incomplete: true} }
	o, got, err := f.replay(t, "model_gateway/held", "hash-model_gateway/held", incomplete)
	if !errors.Is(err, errReservationScanIncomplete) {
		t.Errorf("an incomplete read of the hold answered outcome=%v hold=%q err=%v; want the scan error", o, got, err)
	}
	if o != replayUnknown || !got.isZero() {
		t.Errorf("an incomplete read was answered: outcome=%v hold=%q; want unknown and no hold", o, got)
	}
	failing := func(sc store.Scope) store.Scope { return ledgerFaultScope{Scope: sc} }
	o, got, err = f.replay(t, "model_gateway/held", "hash-model_gateway/held", failing)
	if !errors.Is(err, errLedgerUnreadable) || o != replayUnknown || !got.isZero() {
		t.Errorf("a failed read of the hold answered outcome=%v hold=%q err=%v; want the read error, unknown, no hold", o, got, err)
	}
	o, got, err = f.replay(t, "model_gateway/held", "hash-model_gateway/held", nil)
	wantReplay(t, "control: the same hold, read completely", o, got, h, err)

	// A hold with no ledger row is evaluated only when a complete read says so: the same
	// read incomplete, or failed, is an error with an unknown outcome.
	o, got, err = f.replay(t, "model_gateway/no-rows", "hash-model_gateway/no-rows", incomplete)
	if !errors.Is(err, errReservationScanIncomplete) || o != replayUnknown || !got.isZero() {
		t.Errorf("an incomplete read of a hold with no rows answered outcome=%v hold=%q err=%v; want the scan error", o, got, err)
	}
	o, got, err = f.replay(t, "model_gateway/no-rows", "hash-model_gateway/no-rows", failing)
	if !errors.Is(err, errLedgerUnreadable) || o != replayUnknown || !got.isZero() {
		t.Errorf("a failed read of a hold with no rows answered outcome=%v hold=%q err=%v; want the read error", o, got, err)
	}
}

// TestReplayReturnsRecentCommit pins everything a recent commit's replay reads. A
// committed row inside the window answers with its hold, and its read set is the row's
// payload hash, state and state_at and the clock: the call already ran and was charged,
// so nothing else is read — not its ledger rows, not spend, not policies, identities or
// groups. The answer stands through a scope that refuses every one of those reads and
// with the module's data handle refusing every transaction, while a reserved row asked
// the same way does not. The window is open for less than its length after state_at; a
// legacy pair answers with its budget hold; and another payload under the key is a
// conflict, never a replay.
func TestReplayReturnsRecentCommit(t *testing.T) {
	forEachAdmissionEngine(t, runReplayReturnsRecentCommit)
}

func runReplayReturnsRecentCommit(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	t0 := baseTime
	h, e1, e2, r := newHoldID(), newHoldID(), newHoldID(), newHoldID()
	f.admission(t, "model_gateway/committed", admStateCommitted, h, "", t0)
	f.admission(t, "legacy-e", admStateCommitted, e1, e2, t0)
	f.admission(t, "model_gateway/reserved", admStateReserved, r, "", t0)
	f.hold(t, r, t0)
	readNothing := func(sc store.Scope) store.Scope { return readNothingScope{Scope: sc} }
	data := f.m.data
	f.m.data = refusingData{}

	f.clk.advance(2 * time.Minute)
	o, got, err := f.replay(t, "model_gateway/committed", "hash-model_gateway/committed", readNothing)
	wantReplay(t, "a recent commit, with every other read refused", o, got, h, err)
	if _, _, err := f.replay(t, "model_gateway/reserved", "hash-model_gateway/reserved", readNothing); !errors.Is(err, errReadRefused) {
		t.Fatalf("fixture: a reserved row asked through the refusing scope answered err=%v; the scope does not bite", err)
	}
	o, got, err = f.replay(t, "legacy-e", "hash-legacy-e", readNothing)
	wantReplay(t, "(e) a committed legacy pair, with every other read refused", o, got, e1, err)

	o, got, err = f.replay(t, "model_gateway/committed", "hash-another-payload", readNothing)
	if err != nil || o != replayConflict || !got.isZero() {
		t.Errorf("another payload under the key: outcome=%v hold=%q err=%v; want a conflict", o, got, err)
	}
	f.m.data = data

	f.clk.advance(admissionReplayWindow - 2*time.Minute - time.Second)
	o, got, err = f.replay(t, "model_gateway/committed", "hash-model_gateway/committed", nil)
	wantReplay(t, "one second before the window closes", o, got, h, err)
	f.clk.advance(time.Second)
	o, got, err = f.replay(t, "model_gateway/committed", "hash-model_gateway/committed", nil)
	wantEvaluate(t, "the instant the window closes", o, got, err)
	o, got, err = f.replay(t, "model_gateway/committed", "hash-another-payload", nil)
	if err != nil || o != replayConflict {
		t.Errorf("another payload after the window: outcome=%v err=%v; want a conflict", o, err)
	}
}

// holdUntil writes one active ledger row of component ("b" or "s") under h, created at
// created and expiring at expires.
func (f *replayFixture) holdUntil(t *testing.T, h holdID, component string, created, expires time.Time) {
	t.Helper()
	f.seq++
	seedReservation(t, f.st, f.tenant, ledgerRow(f.policy, component, h, f.seq, 2*oneUSD, created, expires, resvStateActive))
}

// holdsBack reads the row of key and asks whether it holds its key back at the clock's
// instant, in one read transaction; wrap, when set, replaces the scope it is asked through.
func (f *replayFixture) holdsBack(t *testing.T, key string, wrap func(store.Scope) store.Scope) (bool, error) {
	t.Helper()
	ctx := context.Background()
	var (
		back  bool
		asked error
	)
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		row, found, err := rowOfKey(ctx, sc, key)
		if err != nil || !found {
			t.Fatalf("the row of %q: found=%v err=%v", key, found, err)
		}
		if wrap != nil {
			sc = wrap(sc)
		}
		back, asked = pairHoldsBack(ctx, sc, row, f.m.clock.Now())
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	return back, asked
}

// TestLegacyPairHoldsBack: a pair an earlier build published holds its key back — no
// takeover, no release, no new hold — while the row is dated inside the replay window and
// a complete read finds a row that withholds under either of its holds, whichever lapsed
// first. It stops holding back once both holds have lapsed, and at the end of the window
// even if a row were still withholding. An undated row and a row with one slot never hold
// back. A read that does not complete is an error.
func TestLegacyPairHoldsBack(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyPairHoldsBack)
}

func runLegacyPairHoldsBack(t *testing.T, cfg store.Config) {
	f := newReplayFixture(t, cfg)
	tp := baseTime
	created := tp.Add(-time.Second)
	early, late := tp.Add(100*time.Second), tp.Add(290*time.Second)
	pair := func(key string, budgetEnd, seatEnd, stateAt time.Time) {
		h1, h2 := newHoldID(), newHoldID()
		f.admission(t, key, admStateReserved, h1, h2, stateAt)
		f.holdUntil(t, h1, "b", created, budgetEnd)
		f.holdUntil(t, h2, "s", created, seatEnd)
	}
	pair("order-1", early, late, tp)
	pair("order-2", late, early, tp)
	pair("undated", early, late, time.Time{})
	pair("past-the-window", tp.Add(admissionReplayWindow+time.Minute), early, tp)
	one, spend := newHoldID(), newHoldID()
	f.admission(t, "one-slot", admStateReserved, one, "", tp)
	f.holdUntil(t, one, "b", created, late)
	f.admission(t, "spend-only", admStateReserved, "", spend, tp)
	f.holdUntil(t, spend, "s", created, late)

	want := func(what, key string, wrap func(store.Scope) store.Scope, back bool) {
		t.Helper()
		got, err := f.holdsBack(t, key, wrap)
		if err != nil || got != back {
			t.Errorf("%s: holds back=%v err=%v; want %v", what, got, err, back)
		}
	}

	f.clk.advance(150 * time.Second)
	want("the budget hold lapsed first, the seat hold withholds", "order-1", nil, true)
	want("the seat hold lapsed first, the budget hold withholds", "order-2", nil, true)
	want("an undated pair", "undated", nil, false)
	want("a row with one hold", "one-slot", nil, false)
	want("a spend-only row", "spend-only", nil, false)
	incomplete := func(sc store.Scope) store.Scope { return ledgerFaultScope{Scope: sc, incomplete: true} }
	if back, err := f.holdsBack(t, "order-1", incomplete); !errors.Is(err, errReservationScanIncomplete) || back {
		t.Errorf("an incomplete read of the pair: holds back=%v err=%v; want the scan error", back, err)
	}

	f.clk.advance(141 * time.Second)
	want("both holds lapsed, order 1", "order-1", nil, false)
	want("both holds lapsed, order 2", "order-2", nil, false)
	want("a hold still withholding just inside the window", "past-the-window", nil, true)

	f.clk.t = tp.Add(admissionReplayWindow)
	want("the instant the window closes", "past-the-window", nil, false)
}
