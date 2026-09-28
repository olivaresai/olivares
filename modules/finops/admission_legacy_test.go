// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// ROWS AN EARLIER BUILD LEFT. An owes_release row names money that build owed back,
// and is settled whatever the operator has said. A claim that build staged, a pending
// row naming no hold, is retired only once the operator's stated stop of those writers
// is usable: stated, valid, in the past, not contradicted by a legacy row dated after
// it, and far enough behind that every hold such a writer took has lapsed. Nothing
// about the age of a claim or the silence of a key says those writers stopped.
// -----------------------------------------------------------------------------

// mustLegacyStop parses raw, which the test knows to be a valid stop.
func mustLegacyStop(t *testing.T, raw string) LegacyWriterStop {
	t.Helper()
	stop, err := ParseLegacyWriterStop(raw)
	if err != nil {
		t.Fatalf("parse the stop %q: %v", raw, err)
	}
	return stop
}

// stopText is the operator's text for the instant at.
func stopText(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// legacyStopAt reads the tenant's stop state at now, as a recovery pass reads it.
func legacyStopAt(t *testing.T, m *Module, tenant model.TenantID, stop LegacyWriterStop, now time.Time) legacyStopState {
	t.Helper()
	var state legacyStopState
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		state, err = tenantLegacyStop(context.Background(), sc, stop, model.NewTimestamp(now))
		return err
	}); err != nil {
		t.Fatalf("read the stop state: %v", err)
	}
	return state
}

// TestParseLegacyWriterStop: the stop is an RFC 3339 instant in UTC, written with the Z
// designator, or nothing. Nothing states no stop. Any other text is an error and a stop
// whose state is invalid at every instant, and the error does not repeat the text. A
// valid stop in the future is future until its instant, then waits until every hold of
// a writer that stopped then has lapsed.
func TestParseLegacyWriterStop(t *testing.T) {
	now := model.NewTimestamp(baseTime)

	t.Run("nothing states no stop", func(t *testing.T) {
		stop, err := ParseLegacyWriterStop("")
		if err != nil {
			t.Fatalf("an empty stop: %v", err)
		}
		if got := legacyStopStateFor(stop, now, model.Timestamp{}); got != legacyStopAbsent {
			t.Fatalf("an empty stop reads %q, want %q", got, legacyStopAbsent)
		}
	})

	for _, raw := range []string{"2026-06-10T11:00:00Z", "2026-06-10T11:00:00.5Z", "2026-06-10T11:00:00.123456789Z"} {
		t.Run("valid "+raw, func(t *testing.T) {
			stop := mustLegacyStop(t, raw)
			if got := legacyStopStateFor(stop, now, model.Timestamp{}); got != legacyStopUsable {
				t.Fatalf("a stop an hour past reads %q, want %q", got, legacyStopUsable)
			}
			early := model.NewTimestamp(time.Date(2026, 6, 10, 11, 4, 59, 0, time.UTC))
			if got := legacyStopStateFor(stop, early, model.Timestamp{}); got != legacyStopWaiting {
				t.Fatalf("a stop under five minutes past reads %q, want %q", got, legacyStopWaiting)
			}
		})
	}

	for _, raw := range []string{
		"yesterday",
		"2026-06-10 11:00:00Z",
		"2026-06-10T11:00:00",
		"2026-06-10T11:00:00+02:00",
		"2026-06-10T11:00:00+00:00",
		" 2026-06-10T11:00:00Z",
		"2026-06-10T11:00:00Z\n",
		"2026-13-10T11:00:00Z",
		"0001-01-01T00:00:00Z",
	} {
		t.Run("invalid "+strings.TrimSpace(raw), func(t *testing.T) {
			stop, err := ParseLegacyWriterStop(raw)
			if err == nil {
				t.Fatalf("the stop %q parsed; want an error", raw)
			}
			if strings.Contains(err.Error(), strings.TrimSpace(raw)) {
				t.Fatalf("the error repeats the operator's text: %v", err)
			}
			for _, at := range []time.Time{baseTime, baseTime.Add(24 * time.Hour)} {
				if got := legacyStopStateFor(stop, model.NewTimestamp(at), model.Timestamp{}); got != legacyStopInvalid {
					t.Fatalf("an unparsable stop reads %q at %s, want %q", got, at, legacyStopInvalid)
				}
			}
		})
	}

	t.Run("future", func(t *testing.T) {
		at := baseTime.Add(time.Second)
		stop := mustLegacyStop(t, stopText(at))
		for _, c := range []struct {
			now  time.Time
			want legacyStopState
		}{
			{baseTime, legacyStopFuture},
			{at, legacyStopWaiting},
			{at.Add(5*time.Minute - time.Nanosecond), legacyStopWaiting},
			{at.Add(5 * time.Minute), legacyStopUsable},
		} {
			if got := legacyStopStateFor(stop, model.NewTimestamp(c.now), model.Timestamp{}); got != c.want {
				t.Fatalf("a stop at %s reads %q at %s, want %q", at, got, c.now, c.want)
			}
		}
	})
}

// TestLegacyStopStates: the stop state of a tenant, read from its rows at the first
// recovery pass after an upgrade (upgrade + 60 s) and at the first reconciliation
// (upgrade + 300 s), for the three stops an operator may have stated. With no stop it is
// absent at both. A stop five seconds before the upgrade waits at the first pass and is
// usable at the second. A stop 100 seconds before the upgrade is contradicted at both,
// by a legacy claim dated ten seconds before the upgrade: a writer was still writing
// after the instant the operator named. Only legacy claims and owes_release rows date
// the earlier writers; a claim or an admission this build wrote later contradicts
// nothing.
func TestLegacyStopStates(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyStopStates)
}

func runLegacyStopStates(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	tu := baseTime // the upgrade
	ago := func(d time.Duration) time.Time { return tu.Add(-d) }
	for _, r := range []struct {
		key, state    string
		handle, spend holdID
		at            time.Time
	}{
		{"model_gateway/legacy-claim-ten-seconds-old", admStatePending, "", "", ago(10 * time.Second)},
		{"model_gateway/legacy-claim-ten-minutes-old", admStatePending, "", "", ago(600 * time.Second)},
		{"model_gateway/legacy-claim-undated", admStatePending, "", "", time.Time{}},
		{"model_gateway/owes-release-pair", admStateOwesRelease, newHoldID(), newHoldID(), ago(20 * time.Second)},
		{"model_gateway/owes-release-lapsed", admStateOwesRelease, newHoldID(), "", ago(500 * time.Second)},
		{"model_gateway/current-claim", admStatePending, newHoldID(), "", tu.Add(30 * time.Second)},
		{"model_gateway/current-published", admStateReserved, newHoldID(), "", tu.Add(40 * time.Second)},
	} {
		seedAdmission(t, st, tenant, admissionRecord(r.key, r.state, r.handle, r.spend, r.at))
	}

	firstPass, firstReconciliation := tu.Add(60*time.Second), tu.Add(300*time.Second)
	for _, c := range []struct {
		name                  string
		stop                  LegacyWriterStop
		atFirstPass           legacyStopState
		atFirstReconciliation legacyStopState
	}{
		{"no stop", LegacyWriterStop{}, legacyStopAbsent, legacyStopAbsent},
		{"a stop five seconds before the upgrade", mustLegacyStop(t, stopText(ago(5*time.Second))), legacyStopWaiting, legacyStopUsable},
		{"a stop 100 seconds before the upgrade", mustLegacyStop(t, stopText(ago(100*time.Second))), legacyStopContradicted, legacyStopContradicted},
	} {
		if got := legacyStopAt(t, m, tenant, c.stop, firstPass); got != c.atFirstPass {
			t.Errorf("%s: %q at the first recovery pass, want %q", c.name, got, c.atFirstPass)
		}
		if got := legacyStopAt(t, m, tenant, c.stop, firstReconciliation); got != c.atFirstReconciliation {
			t.Errorf("%s: %q at the first reconciliation, want %q", c.name, got, c.atFirstReconciliation)
		}
	}

	// The edges. A stop is usable exactly when every hold its writers took has lapsed,
	// and a legacy row dated at the stop itself was written before it.
	nearStop := mustLegacyStop(t, stopText(ago(5*time.Second)))
	usableAt := ago(5 * time.Second).Add(5 * time.Minute)
	if got := legacyStopAt(t, m, tenant, nearStop, usableAt.Add(-time.Nanosecond)); got != legacyStopWaiting {
		t.Errorf("one nanosecond before the wait ends: %q, want %q", got, legacyStopWaiting)
	}
	if got := legacyStopAt(t, m, tenant, nearStop, usableAt); got != legacyStopUsable {
		t.Errorf("when the wait ends: %q, want %q", got, legacyStopUsable)
	}
	atNewest := mustLegacyStop(t, stopText(ago(10*time.Second)))
	if got := legacyStopAt(t, m, tenant, atNewest, firstReconciliation); got != legacyStopUsable {
		t.Errorf("a stop at the newest legacy date: %q, want %q", got, legacyStopUsable)
	}
}

// legacyFixture is one tenant with its clock at baseTime and two policy identities for
// the ledger rows an earlier build left.
func legacyFixture(t *testing.T, cfg store.Config) (*Module, store.Store, model.TenantID, *fakeClock, model.ID, model.ID) {
	t.Helper()
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	return m, st, tenant, clk, model.NewID(), model.NewID()
}

// seedOwesRelease writes an owes_release row as an earlier build left it, dated created
// and naming h1 and h2, and returns it as read. Every hold it names has a budget row (h1)
// or a seat row (h2) of 2 000 000 µUSD, created with the row and expiring with the
// earlier build's five-minute TTL.
func seedOwesRelease(t *testing.T, m *Module, st store.Store, tenant model.TenantID, budget, limit model.ID, h1, h2 holdID, created time.Time) admissionRow {
	t.Helper()
	const key = "model_gateway/owes-release"
	expires := created.Add(5 * time.Minute)
	seedAdmission(t, st, tenant, admissionRecord(key, admStateOwesRelease, h1, h2, created))
	seedReservation(t, st, tenant, ledgerRow(budget, "b", h1, 1, 2*oneUSD, created, expires, resvStateActive))
	if !h2.isZero() {
		seedReservation(t, st, tenant, ledgerRow(limit, "s", h2, 1, 2*oneUSD, created, expires, resvStateActive))
	}
	return admissionRowOf(t, m, tenant, key)
}

// assertLegacySettled asserts that the row of key was written once, by a settlement at
// at: released, naming nothing, owing nothing.
func assertLegacySettled(t *testing.T, m *Module, tenant model.TenantID, read admissionRow, at time.Time) {
	t.Helper()
	row := admissionRowOf(t, m, tenant, read.key)
	if row.state != admStateReleased || !row.handle.isZero() || !row.spendHandle.isZero() || len(row.owed) != 0 || row.owedErr != nil {
		t.Fatalf("the row is %s naming %q/%q owing %v, want released naming nothing", row.state, row.handle, row.spendHandle, row.owed)
	}
	if row.version != read.version+1 || row.stateAt.String() != model.NewTimestamp(at).String() {
		t.Fatalf("the row is at version %d dated %s, want one write, at %d dated %s", row.version, row.stateAt, read.version+1, model.NewTimestamp(at))
	}
}

// assertLegacyUnchanged asserts that the row of key is exactly as read.
func assertLegacyUnchanged(t *testing.T, m *Module, tenant model.TenantID, read admissionRow) {
	t.Helper()
	row := admissionRowOf(t, m, tenant, read.key)
	if row.version != read.version || row.state != read.state || row.handle != read.handle ||
		row.spendHandle != read.spendHandle || row.stateAt.String() != read.stateAt.String() {
		t.Fatalf("the row is %s at version %d naming %q/%q dated %s, want it untouched: %s at %d naming %q/%q dated %s",
			row.state, row.version, row.handle, row.spendHandle, row.stateAt,
			read.state, read.version, read.handle, read.spendHandle, read.stateAt)
	}
}

// TestLegacyOwesReleaseSettled: an owes_release row is settled in one write whatever
// the stop says: every row under the holds it names that still withholds is released,
// with an actual of zero and settled now, and the row is released naming nothing. A row
// that already lapsed is left as it is — its expiry is what happened to it, and the
// sweep records it — and no actual amount is ever written.
func TestLegacyOwesReleaseSettled(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyOwesReleaseSettled)
}

func runLegacyOwesReleaseSettled(t *testing.T, cfg store.Config) {
	for _, stop := range []legacyStopState{
		legacyStopAbsent, legacyStopInvalid, legacyStopFuture, legacyStopContradicted, legacyStopWaiting, legacyStopUsable,
	} {
		t.Run("both holds withholding, stop "+string(stop), func(t *testing.T) {
			m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
			h1, h2 := newHoldID(), newHoldID()
			read := seedOwesRelease(t, m, st, tenant, budget, limit, h1, h2, baseTime.Add(-20*time.Second))
			step, released := m.settleLegacy(context.Background(), tenant, read, stop)
			if step != recoveryDone || released != 2 {
				t.Fatalf("the settlement came to %v releasing %d row(s), want done releasing the two", step, released)
			}
			assertLegacySettled(t, m, tenant, read, clk.t)
			assertRowsUnder(t, st, tenant, h1, 1, resvStateReleased, 0, clk.t)
			assertRowsUnder(t, st, tenant, h2, 1, resvStateReleased, 0, clk.t)
			assertWithheld(t, st, tenant, clk.t, 0, 0)
		})
	}

	t.Run("a lapsed hold is left to the sweep", func(t *testing.T) {
		m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
		h1 := newHoldID()
		read := seedOwesRelease(t, m, st, tenant, budget, limit, h1, "", baseTime.Add(-500*time.Second))
		step, released := m.settleLegacy(context.Background(), tenant, read, legacyStopAbsent)
		if step != recoveryDone || released != 0 {
			t.Fatalf("the settlement came to %v releasing %d row(s), want done releasing none", step, released)
		}
		assertLegacySettled(t, m, tenant, read, clk.t)
		assertRowsUnder(t, st, tenant, h1, 1, resvStateActive, 0, time.Time{})
		for _, r := range ledgerRowsUnder(t, st, tenant, h1) {
			if r.String(colResvSettledAt) != "" {
				t.Fatalf("the lapsed row was stamped settled at %q", r.String(colResvSettledAt))
			}
		}
	})
}

// TestLegacyPendingRetiresOnlyWhenUsable: a claim an earlier build staged is retired —
// released, naming nothing, and no ledger row written — only under a usable stop. Absent,
// invalid, future, contradicted or still waiting, the row stays exactly as it is however
// old it is. Across the wait: a claim staged at 0 whose writer created two holds at
// 320 s that no row names, under a stop at 400 s, is left alone at 650 s and retired at
// 700 s, and those holds are not touched: they lapsed at 620 s and are the sweep's.
func TestLegacyPendingRetiresOnlyWhenUsable(t *testing.T) {
	forEachAdmissionEngine(t, runLegacyPendingRetiresOnlyWhenUsable)
}

func runLegacyPendingRetiresOnlyWhenUsable(t *testing.T, cfg store.Config) {
	for _, stop := range []legacyStopState{
		legacyStopAbsent, legacyStopInvalid, legacyStopFuture, legacyStopContradicted, legacyStopWaiting,
	} {
		t.Run("an old claim stays, stop "+string(stop), func(t *testing.T) {
			m, st, tenant, _, _, _ := legacyFixture(t, cfg)
			for _, at := range []time.Time{baseTime.Add(-600 * time.Second), {}} {
				key := "model_gateway/legacy-claim-dated"
				if at.IsZero() {
					key = "model_gateway/legacy-claim-undated"
				}
				seedAdmission(t, st, tenant, admissionRecord(key, admStatePending, "", "", at))
				read := admissionRowOf(t, m, tenant, key)
				if step, _ := m.settleLegacy(context.Background(), tenant, read, stop); step != recoveryNone {
					t.Fatalf("%s: the claim came to %v under a stop that is %s, want nothing written", key, step, stop)
				}
				assertLegacyUnchanged(t, m, tenant, read)
			}
		})
	}

	t.Run("a usable stop retires it", func(t *testing.T) {
		m, st, tenant, clk, _, _ := legacyFixture(t, cfg)
		for _, at := range []time.Time{baseTime.Add(-600 * time.Second), {}} {
			key := "model_gateway/legacy-claim-dated"
			if at.IsZero() {
				key = "model_gateway/legacy-claim-undated"
			}
			seedAdmission(t, st, tenant, admissionRecord(key, admStatePending, "", "", at))
			read := admissionRowOf(t, m, tenant, key)
			if step, released := m.settleLegacy(context.Background(), tenant, read, legacyStopUsable); step != recoveryDone || released != 0 {
				t.Fatalf("%s: the claim came to %v releasing %d row(s), want retired releasing none", key, step, released)
			}
			assertLegacySettled(t, m, tenant, read, clk.t)
		}
		if n := len(countReservations(t, st, tenant)); n != 0 {
			t.Fatalf("retiring a claim wrote %d ledger row(s)", n)
		}
	})

	t.Run("across the stop's wait", func(t *testing.T) {
		m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
		t0 := baseTime
		const key = "model_gateway/legacy-claim-across-the-wait"
		seedAdmission(t, st, tenant, admissionRecord(key, admStatePending, "", "", t0))
		created := t0.Add(320 * time.Second)
		seedReservation(t, st, tenant, ledgerRow(budget, "b", newHoldID(), 1, 2*oneUSD, created, created.Add(reservationTTL), resvStateActive))
		seedReservation(t, st, tenant, ledgerRow(limit, "s", newHoldID(), 1, 2*oneUSD, created, created.Add(reservationTTL), resvStateActive))
		stop := mustLegacyStop(t, stopText(t0.Add(400*time.Second)))
		ledger := rowsByID(t, st, tenant, budgetReservationKind)

		clk.t = t0.Add(650 * time.Second)
		read := admissionRowOf(t, m, tenant, key)
		state := legacyStopAt(t, m, tenant, stop, clk.t)
		if state != legacyStopWaiting {
			t.Fatalf("the stop reads %q at 650 s, want %q", state, legacyStopWaiting)
		}
		if step, _ := m.settleLegacy(context.Background(), tenant, read, state); step != recoveryNone {
			t.Fatalf("the claim came to %v at 650 s, want nothing written while the stop waits", step)
		}
		assertLegacyUnchanged(t, m, tenant, read)
		if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 2, ActiveLapsed: 2}) {
			t.Fatalf("the ledger at 650 s is %+v, want the two unnamed holds active and lapsed", got)
		}

		clk.t = t0.Add(700 * time.Second)
		state = legacyStopAt(t, m, tenant, stop, clk.t)
		if state != legacyStopUsable {
			t.Fatalf("the stop reads %q at 700 s, want %q", state, legacyStopUsable)
		}
		if step, released := m.settleLegacy(context.Background(), tenant, read, state); step != recoveryDone || released != 0 {
			t.Fatalf("the claim came to %v releasing %d row(s) at 700 s, want retired releasing none", step, released)
		}
		assertLegacySettled(t, m, tenant, read, clk.t)
		assertRowsUnchanged(t, "the unnamed holds", ledger, rowsByID(t, st, tenant, budgetReservationKind))
	})
}

// TestLostLegacySettleRetried: a settlement of an owes_release row that fails is known
// by the row alone. A write that never ran, or ran and rolled back, leaves the row as it
// was read: unresolved, and the next settlement completes it. A write that committed and
// lost its acknowledgment is found by the row, one version on and released naming
// nothing, and counted as done with the rows it released; the next call finds nothing
// left to settle. A caller that cannot read the row after the fault reports the write
// unresolved and writes nothing more; the next call decides from the row as it stands.
func TestLostLegacySettleRetried(t *testing.T) {
	forEachAdmissionEngine(t, runLostLegacySettleRetried)
}

func runLostLegacySettleRetried(t *testing.T, cfg store.Config) {
	for _, c := range []struct {
		name  string
		mode  faultMode
		blind bool
		want  recoveryStep
	}{
		{"the write never opened", faultBeforeCallback, false, recoveryUnresolved},
		{"the write rolled back after it ran", faultAfterRollback, false, recoveryUnresolved},
		{"the write committed and its acknowledgment was lost", faultAfterCommit, false, recoveryDone},
		{"the write committed and the row could not be read", faultAfterCommit, true, recoveryUnresolved},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, st, tenant, clk, budget, limit := legacyFixture(t, cfg)
			h1, h2 := newHoldID(), newHoldID()
			read := seedOwesRelease(t, m, st, tenant, budget, limit, h1, h2, baseTime.Add(-20*time.Second))

			base := m.data
			fault := &faultData{ModuleData: base, nth: 1, mode: c.mode}
			m.data = fault
			if c.blind {
				m.data = blindAfterFault{fault}
			}
			step, released := m.settleLegacy(pausedCtx(context.Background()), tenant, read, legacyStopAbsent)
			m.data = base
			if !fault.fired.Load() {
				t.Fatal("the injected fault never fired; the test proves nothing")
			}
			if step != c.want {
				t.Fatalf("the settlement came to %v, want %v", step, c.want)
			}

			committed := c.mode == faultAfterCommit
			if !committed {
				if released != 0 {
					t.Fatalf("a settlement that did not commit reported %d released row(s)", released)
				}
				assertLegacyUnchanged(t, m, tenant, read)
				assertWithheld(t, st, tenant, clk.t, 2*oneUSD, 2*oneUSD)
				clk.advance(time.Second)
				if step, released := m.settleLegacy(context.Background(), tenant, read, legacyStopAbsent); step != recoveryDone || released != 2 {
					t.Fatalf("the next settlement came to %v releasing %d row(s), want done releasing the two", step, released)
				}
			} else if !c.blind && released != 2 {
				t.Fatalf("the committed settlement was found with %d released row(s), want the two", released)
			}
			assertLegacySettled(t, m, tenant, read, clk.t)
			assertRowsUnder(t, st, tenant, h1, 1, resvStateReleased, 0, clk.t)
			assertRowsUnder(t, st, tenant, h2, 1, resvStateReleased, 0, clk.t)
			assertWithheld(t, st, tenant, clk.t, 0, 0)

			if committed {
				// The next call reads the row as it now stands: nothing is left to settle.
				settled := admissionRowOf(t, m, tenant, read.key)
				if step, _ := m.settleLegacy(context.Background(), tenant, settled, legacyStopAbsent); step != recoveryNone {
					t.Fatalf("a settled row came to %v on the next call, want nothing written", step)
				}
				assertLegacyUnchanged(t, m, tenant, settled)
			}
		})
	}
}
