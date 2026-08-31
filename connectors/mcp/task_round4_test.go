// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// task_round4_test.go — Stage 4, review ROUND-4 regressions (R4-01…R4-07)
// plus the operator reconciliation surface the round-4 review made a release
// blocker. Each test names the finding it closes and states, in its doc comment,
// what the ROUND-3 code did instead — so the assertion is a statement about a
// concrete defect, not a restatement of the implementation.

// ledgerSize reports the number of records the ledger currently holds (the
// quantity round-4 R4-05 says was unbounded).
func ledgerSize(l *taskLedger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byID)
}

// retainedLedgerSize reports the records that CONSUME the retention bound.
//
// ROUND-7: that is now every row. Round-6 exempted the retired terminal-result
// handoffs because retirement kept them alive in a separately-bounded FIFO cache;
// proof-of-delivery retirement DELETES a retired record, so no row sits outside
// the count and this is once again `len(byID)` by a different name — deliberately
// computed through the production predicate so the two cannot drift.
func retainedLedgerSize(l *taskLedger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retainedTotalLocked()
}

// --- R4-01 (P0) ----------------------------------------------------------------

// TestCancellationPinHoldsUntilTheUnconfirmedStateIsInstalled is round-4 R4-01,
// the P0. Round-3 released the generation lease when the DISPATCH HELPER
// returned — a `defer` inside enforceTaskDispatch/dispatchAnchoredCancel — and
// only then did the caller record the delivered bar, the `cancel_requested`
// status or the quarantine. A concurrent lookup/insert in that window ran
// evictExpiredLocked against a record that was still `working`, no longer pinned
// and not yet cancellation-unconfirmed: it was deleted and tombstoned, the later
// generation-CAS status write failed, and a task whose cancellation had been
// ACKNOWLEDGED but never proven terminal vanished from every later sweep — the
// exact R3-02 fail-open, reopened one instruction later.
func TestCancellationPinHoldsUntilTheUnconfirmedStateIsInstalled(t *testing.T) {
	newPinned := func(t *testing.T, id string, now *time.Time) (*taskLedger, TaskRecord) {
		t.Helper()
		l := newTaskLedger(0, func() time.Time { return *now })
		ttl := int64(60000)
		rec, err := l.insert(TaskRecord{
			TaskID: id, Subject: "agent:a", Tenant: "t", Tool: "search",
			Status: taskStatusWorking, CreatedAt: *now, TTLMs: &ttl,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if lerr := l.acquireEffectLease(rec.TaskID, rec.Generation, taskEffectClientCancel); lerr != nil {
			t.Fatalf("acquireEffectLease: %v", lerr)
		}
		*now = now.Add(2 * time.Minute) // the TTL elapses while the cancel is on the wire
		if _, ok := l.lookup(rec.TaskID); !ok {
			t.Fatal("a PINNED record must never be TTL-evicted mid-flight (R3-03 substrate)")
		}
		return l, rec
	}

	// The causally exact proof, reproducing the reviewer's own probe: the ONE
	// atomic operation installs the cancellation-unconfirmed state and drops the
	// last pin under a single mutex, so no eviction can interleave.
	t.Run("the atomic settlement installs the state and unpins under ONE mutex", func(t *testing.T) {
		now := rsClock()
		l, rec := newPinned(t, "task-atomic", &now)
		l.settleCancelAttempt(rec.TaskID, rec.Generation, taskCancelSettlement{
			taskCancelBookkeeping: taskCancelBookkeeping{
				status:       taskCancelRequestedStatus,
				statusReason: "upstream acknowledged the cooperative cancellation",
			},
			dispatched: true, acked: true, releaseLease: true,
		})
		got, ok := l.lookup(rec.TaskID)
		if !ok {
			t.Fatal("R4-01: the record was evicted the instant the pin dropped — the ACK did not prove the task stopped")
		}
		if got.Status != taskCancelRequestedStatus || !got.CancelUnconfirmed {
			t.Errorf("record after the atomic settlement = %+v, want cancel_requested AND CancelUnconfirmed", got)
		}
		if taskExpired(got, now.Add(24*time.Hour)) {
			t.Error("a cancellation-unconfirmed record must be TTL-immune")
		}
	})

	// The negative control that proves the window is REAL: performing the same
	// steps in the round-3 ORDER (unpin first, bookkeeping after) loses the record
	// and makes the late compare-and-swap fail. If this control ever stops
	// failing, the test above proves nothing.
	t.Run("control: the round-3 order — unpin first, bookkeeping after — loses the record", func(t *testing.T) {
		now := rsClock()
		l, rec := newPinned(t, "task-control", &now)
		l.releaseEffectLease(rec.Generation) // the round-3 deferred release
		if _, ok := l.lookup(rec.TaskID); ok {
			t.Fatal("control invalid: the unpinned, still-`working` record was expected to be TTL-evicted here")
		}
		// The bookkeeping that follows is a compare-and-swap on a generation the
		// eviction already tombstoned, so it writes NOTHING: the ACK is lost and the
		// task is absent from every later sweep. That is the R4-01 fail-open, and it
		// is why the state install and the unpin must share one mutex.
		l.settleCancelAttempt(rec.TaskID, rec.Generation, taskCancelSettlement{
			taskCancelBookkeeping: taskCancelBookkeeping{status: taskCancelRequestedStatus},
			dispatched:            true, acked: true,
		})
		if _, ok := l.lookup(rec.TaskID); ok {
			t.Fatal("control invalid: the late bookkeeping was expected to be a no-op on an evicted record")
		}
		if n := len(l.active(nil)); n != 0 {
			t.Fatalf("control invalid: %d records visible to a later sweep, want 0 (the task was lost)", n)
		}
	})

	// End-to-end and deterministic: an AMBIGUOUS cancellation writes no status at
	// all — round-3 therefore left the record plainly `working` and TTL-evictable
	// even though the effect may have reached the upstream. The record must stay.
	t.Run("an ambiguous client cancellation makes the record TTL-immune end to end", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		now := rsClock()
		ttl := int64(60000)
		up := &taskUpstream{shaped: func(req UpstreamRequest) (UpstreamResult, error) {
			if req.Method == methodTasksCancel {
				// The production `unknown` leg: the request may have been transmitted
				// and nothing observed can confirm the outcome.
				return UpstreamResult{State: DispatchUnknown}, errors.New("connection reset after write")
			}
			return UpstreamResult{Result: json.RawMessage(normativeCompleteResult), State: DispatchCompleted}, nil
		}}
		rs := newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, func() time.Time { return now })
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-amb", TTLMs: &ttl})

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-amb"}`))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("ambiguous tasks/cancel status = %d, want 503 (indeterminate); body=%s", w.Code, w.Body.String())
		}
		now = now.Add(24 * time.Hour)
		got, ok := rs.taskLedger.lookup("task-amb")
		if !ok {
			t.Fatal("R4-01: an ambiguous cancellation whose effect MAY have landed was forgotten by its TTL")
		}
		if !got.CancelUnconfirmed {
			t.Errorf("record = %+v, want CancelUnconfirmed (the status shapes cannot express an ambiguous attempt)", got)
		}
		if n := len(rs.taskLedger.active(nil)); n != 1 {
			t.Errorf("sweep-visible tasks after the TTL = %d, want 1", n)
		}
	})

	// The barrier the review asked for: an evictor released the instant the
	// dispatch settles and hammering the ledger for the whole remainder of the
	// request. Honest characterization: this is a CONCURRENCY probe (it cannot
	// place itself between two specific instructions), so the deterministic causal
	// proof is the first two subtests and the persisted RED mutation run; this one
	// pins that no interleaving of the post-dispatch bookkeeping can lose the
	// record.
	for _, tc := range []struct {
		name   string
		action string
		drive  func(rs *ResourceServer, token string)
	}{
		{
			name: "client", action: taskActionCancelPrefix + opIDKindRequestInstance,
			drive: func(rs *ResourceServer, token string) {
				rs.ServeHTTP(httptest.NewRecorder(), taskReq(token, methodTasksCancel, `{"taskId":"task-barrier"}`))
			},
		},
		{
			name: "sweep", action: taskActionSweep,
			drive: func(rs *ResourceServer, token string) {
				_, _ = rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop")
			},
		},
	} {
		t.Run("barrier after the dispatch helper returns ("+tc.name+")", func(t *testing.T) {
			token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
			var mu sync.Mutex
			now := rsClock()
			clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
			ttl := int64(60000)
			up := &taskUpstream{}
			stop := make(chan struct{})
			var evictorRan, vanished bool
			var wg sync.WaitGroup
			base := &taskAuditor{}
			var once sync.Once
			var rs *ResourceServer
			aud := &hookAuditor{taskAuditor: base, onSettleDone: func(action string) {
				if action != tc.action {
					return
				}
				once.Do(func() {
					// The TTL elapses exactly as the dispatch settles, and an evictor
					// hammers the ledger for the whole post-dispatch bookkeeping.
					mu.Lock()
					now = now.Add(2 * time.Minute)
					mu.Unlock()
					evictorRan = true
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							select {
							case <-stop:
								return
							default:
							}
							if _, ok := rs.taskLedger.lookup("task-barrier"); !ok {
								vanished = true
								return
							}
						}
					}()
				})
			}}
			rs = newTaskEvidenceRSAt(t, jwks, up, aud, clock)
			mustInsertTask(t, rs, TaskRecord{TaskID: "task-barrier", TTLMs: &ttl})

			tc.drive(rs, token)
			close(stop)
			wg.Wait()

			if !evictorRan {
				t.Fatal("the post-dispatch barrier was never exercised")
			}
			if vanished {
				t.Error("R4-01: the record disappeared during the post-dispatch bookkeeping — an ACKed cancellation is not proof the task stopped")
			}
			if _, ok := rs.taskLedger.lookup("task-barrier"); !ok {
				t.Error("R4-01: the record is gone after the cancellation completed")
			}
		})
	}
}

// --- R4-02 ---------------------------------------------------------------------

// TestTaskUpdateAckIsTheNormativeSEP2663Success is round-4 R4-02 on the FULL
// path. Round-3 read SEP-2663's "empty acknowledgement" as "an empty object" and
// made `resultType` FORBIDDEN — so a CONFORMANT upstream, which must send
// `{"resultType":"complete"}` because `UpdateTaskResult = Result`, was answered
// 502, while the non-conformant `{}` was blessed. The green suite entrenched the
// interoperability failure because all three fakes encoded `{}`.
func TestTaskUpdateAckIsTheNormativeSEP2663Success(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for name, tc := range map[string]struct {
		result string
		want   int
	}{
		"normative ack":            {normativeCompleteResult, http.StatusOK},
		"normative ack with _meta": {`{"resultType":"complete","_meta":{"x":1}}`, http.StatusOK},
		"round-3 bare object":      {`{}`, http.StatusBadGateway},
		"state-reporting body":     {`{"resultType":"complete","status":"cancelled"}`, http.StatusBadGateway},
		"aliased discriminator":    {`{"ResultType":"complete"}`, http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
				if req.Method == methodTasksUpdate {
					return json.RawMessage(tc.result), nil
				}
				return json.RawMessage(normativeCompleteResult), nil
			}}
			rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
			mustInsertTask(t, rs, TaskRecord{TaskID: "task-ack-shape"})
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-ack-shape","inputResponses":{}}`))
			if w.Code != tc.want {
				t.Errorf("tasks/update status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			if got := up.count(methodTasksUpdate); got != 1 {
				t.Errorf("upstream tasks/update forwards = %d, want 1 (the shape is judged AFTER the settled dispatch)", got)
			}
		})
	}
}

// --- R4-03 ---------------------------------------------------------------------

// TestRegistrationCannotFinalizeWhileACancelIsOnTheWire is round-4 R4-03.
// settleRegistrationAndPin (round-4: settleRegistration) inspected only TaskRecord fields, and a cancellation intent
// and a generation pin live in SEPARATE maps — so a sweep that had already
// reserved the generation and was BLOCKED INSIDE the upstream forward left the
// record looking like a clean pending registration. Round-3 finalized it and
// relayed the handle while a kill-switch cancellation was on the wire; if that
// cancellation then failed or ended ambiguously, the client held a handle the
// finalizer meant to withhold.
//
// The round-3 regression could not see this: its settlement hook ran the sweep
// SYNCHRONOUSLY to completion, so it tested "sweep completed before
// finalization", never "sweep in flight during finalization".
func TestRegistrationCannotFinalizeWhileACancelIsOnTheWire(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	onWire := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-inflight")}
	up.gate = func(req UpstreamRequest) {
		if req.Method != methodTasksCancel {
			return
		}
		gateOnce.Do(func() {
			close(onWire)
			<-release // the cancel STAYS on the wire across the finalization
		})
	}
	var rs *ResourceServer
	var wg sync.WaitGroup
	var sweepOnce sync.Once
	base := &taskAuditor{}
	aud := &hookAuditor{taskAuditor: base, onSettle: func(action string) {
		if action != taskActionTrack {
			return
		}
		sweepOnce.Do(func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = rs.CancelActiveTasks(context.Background(), nil, "kill-switch during registration")
			}()
			<-onWire // the sweep has reserved the intent, taken the pin and is BLOCKED upstream
		})
	}}
	rs = newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	// The finalization ran while the cancel was still in flight; it must have
	// refused and the handle must be withheld.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("tools/call status = %d, want 503 (the handle is withheld while a cancel is on the wire); body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "task-inflight") {
		t.Errorf("R4-03: the task handle was relayed while a kill-switch cancellation was ON THE WIRE: %s", w.Body.String())
	}
	close(release)
	wg.Wait()

	rec, ok := rs.taskLedger.lookup("task-inflight")
	if !ok {
		t.Fatal("the created upstream task must stay visible for reconciliation")
	}
	if rec.operable() {
		t.Errorf("record after a refused finalization = %+v, want NON-operable", rec)
	}
	wafter := httptest.NewRecorder()
	rs.ServeHTTP(wafter, taskReq(token, methodTasksUpdate, `{"taskId":"task-inflight","inputResponses":{}}`))
	if wafter.Code != http.StatusForbidden {
		t.Errorf("tasks/update on the withheld task = %d, want 403", wafter.Code)
	}
}

// TestSettleRegistrationRefusesAReservedOrPinnedGeneration pins the ledger-level
// rule R4-03 introduced, one axis at a time.
func TestSettleRegistrationRefusesAReservedOrPinnedGeneration(t *testing.T) {
	mk := func(t *testing.T) (*taskLedger, TaskRecord) {
		t.Helper()
		l := newTaskLedger(0, rsClock)
		rec, err := l.insert(TaskRecord{
			TaskID: "task-pend", Subject: "agent:a", Tenant: "t", Tool: "search",
			Status: taskStatusWorking, CreatedAt: rsClock(), Pending: true,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return l, rec
	}
	t.Run("a clean pending registration finalizes", func(t *testing.T) {
		l, rec := mk(t)
		if got, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); !ok || got.Pending {
			t.Errorf("settleRegistrationAndPin = %+v, %t; want the finalized record", got, ok)
		}
	})
	t.Run("an IN-FLIGHT cancellation reservation refuses it", func(t *testing.T) {
		l, rec := mk(t)
		if res := l.beginCancelAttempt(rec.TaskID, rec.Generation); !res.ok {
			t.Fatalf("reservation = %+v, want ok", res)
		}
		if _, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); ok {
			t.Error("R4-03: finalized a registration whose cancellation was already reserved and in flight")
		}
	})
	t.Run("a BARRED generation refuses it", func(t *testing.T) {
		l, rec := mk(t)
		l.barCancelIntent(rec.Generation, taskCancelBarDelivered, "a cancellation was already delivered")
		if _, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); ok {
			t.Error("R4-03: finalized a registration whose generation already carries a cancellation bar")
		}
	})
	t.Run("a PINNED generation refuses it", func(t *testing.T) {
		l, rec := mk(t)
		if err := l.acquireEffectLease(rec.TaskID, rec.Generation, taskEffectServerCancel); err != nil {
			t.Fatalf("acquireEffectLease: %v", err)
		}
		if _, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); ok {
			t.Error("R4-03: finalized a registration with a governed effect still pinned to its generation")
		}
	})
}

// --- R4-04 ---------------------------------------------------------------------

// TestCancellationUnconfirmedIsAFirstClassReconciliationState is round-4 R4-04.
// `operable()` ignores status, so a NORMAL governed record whose cancellation was
// merely acknowledged (`cancel_requested`) — neither quarantined nor reconciling
// — still accepted tasks/update, while the delivered bar suppressed every later
// automatic cancellation. An upstream that never honored the cooperative request
// could therefore be fed new input and keep working with no automatic
// cancellation path left. Round-3 also omitted such records from
// `reconciliationRecords`, so the connector's own reconciliation view was blind
// to the records the R3-02 retention rule created.
func TestCancellationUnconfirmedIsAFirstClassReconciliationState(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	newCanceled := func(t *testing.T, id string) (*ResourceServer, *taskUpstream) {
		t.Helper()
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: id})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"`+id+`"}`))
		if w.Code != http.StatusOK {
			t.Fatalf("tasks/cancel status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		return rs, up
	}

	t.Run("tasks/update is DENIED after an acknowledged cancellation", func(t *testing.T) {
		rs, up := newCanceled(t, "task-unconf-update")
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-unconf-update","inputResponses":{}}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("R4-04: tasks/update on a cancellation-unconfirmed task = %d, want 403; body=%s", w.Code, w.Body.String())
		}
		if got := up.count(methodTasksUpdate); got != 0 {
			t.Errorf("upstream tasks/update forwards = %d, want 0 (new input to a task nobody could stop)", got)
		}
	})

	t.Run("the AUTHORITATIVE tasks/get stays permitted", func(t *testing.T) {
		rs, up := newCanceled(t, "task-unconf-get")
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"task-unconf-get"}`))
		if w.Code != http.StatusOK {
			t.Errorf("tasks/get on a cancellation-unconfirmed task = %d, want 200 (reading is how the ambiguity is resolved); body=%s",
				w.Code, w.Body.String())
		}
		if got := up.count(methodTasksGet); got != 1 {
			t.Errorf("upstream tasks/get forwards = %d, want 1", got)
		}
	})

	t.Run("the record is IN the reconciliation inventory", func(t *testing.T) {
		rs, _ := newCanceled(t, "task-unconf-inv")
		found := false
		for _, rec := range rs.taskLedger.reconciliationRecords() {
			if rec.TaskID == "task-unconf-inv" {
				found = true
			}
		}
		if !found {
			t.Error("R4-04: an indefinitely retained cancellation-unconfirmed record is absent from the reconciliation inventory")
		}
	})

	t.Run("the update denial covers every cancellation-unconfirmed shape", func(t *testing.T) {
		for name, rec := range map[string]TaskRecord{
			"cancel_requested":    {Status: taskCancelRequestedStatus},
			"cancel_failed":       {Status: taskCancelFailedStatus},
			"inferred terminal":   {Status: taskStatusCanceled, TerminalUnconfirmed: true},
			"ambiguous dispatch":  {Status: taskStatusWorking, CancelUnconfirmed: true},
			"working (permitted)": {Status: taskStatusWorking},
		} {
			want := name != "working (permitted)"
			if got := !rec.updatable(); got != want {
				t.Errorf("%s: update denied = %t, want %t", name, got, want)
			}
		}
	})
}

// --- R4-05 ---------------------------------------------------------------------

// TestRetainedTaskInventoryIsBounded is round-4 R4-05. Quarantine deliberately
// BYPASSES the active caps (forgetting a live external task is the failure being
// prevented) and quarantined/reconciling/cancellation-unconfirmed records never
// expire — so with no pre-forward bound, a caller sitting at its cap produced one
// fresh, non-expiring orphan per tools/call and `byID` grew without limit. The
// reviewer's probe reached 513 entries under a cap of 1.
//
// The fix must NOT be to drop a live orphan: the bound is enforced where a NEW
// task can still be PREVENTED, and the retained records leave only through a
// proven terminal confirmation or an explicit operator retirement.
func TestRetainedTaskInventoryIsBounded(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	next := 0
	var mu sync.Mutex
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			mu.Lock()
			next++
			id := next
			mu.Unlock()
			return json.RawMessage(fmt.Sprintf(`{"resultType":"task","taskId":"task-%03d","status":"working"}`, id)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)

	// The reviewer's probe, verbatim in shape: repeat the cap-denied call with
	// fresh identifiers many times over.
	statuses := map[int]int{}
	for i := 0; i < 512; i++ {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		statuses[w.Code]++
	}
	bound := rs.taskLedger.retainedCapPerOwner()
	if got := ledgerSize(rs.taskLedger); got > bound {
		t.Errorf("R4-05: retained records = %d after 512 calls, want at most the retention bound %d", got, bound)
	}
	if got := up.count("tools/call"); got > bound {
		t.Errorf("R4-05: task-producing forwards = %d, want at most %d (a saturated owner must be refused BEFORE the upstream can create anything)",
			got, bound)
	}
	if statuses[http.StatusTooManyRequests] == 0 {
		t.Errorf("no call was refused; statuses = %v", statuses)
	}
	// NOTHING was dropped: every retained record is still addressable for
	// reconciliation.
	if got := len(rs.taskLedger.reconciliationRecords()); got == 0 {
		t.Error("R4-05: the retained orphans must stay visible to reconciliation — never silently dropped")
	}
}

// TestSaturationRefusesBeforeTheUpstreamCanCreateATask pins the ORDER: the
// refusal happens before the forward, which is the only point at which a new
// external task can still be prevented.
func TestSaturationRefusesBeforeTheUpstreamCanCreateATask(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-sat")}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)
	owner := taskOwner{Tenant: rs.tenant, Issuer: rsIssuer, Subject: "agent:claude"}
	// Fill the RETENTION bound with quarantined orphans (the states that bypass
	// the active caps).
	for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
		q := rs.taskLedger.quarantine(TaskRecord{
			TaskID: fmt.Sprintf("orphan-%02d", i), Tenant: owner.Tenant, Issuer: owner.Issuer,
			Subject: owner.Subject, Tool: "search", RequiredScope: "tools:read",
			Status: taskStatusWorking, CreatedAt: rs.clock(),
		}, sdk.EvidenceBinding{}, "unreconciled orphan")
		if !q.retained() {
			t.Fatalf("orphan %d not retained: %+v", i, q)
		}
	}
	before := up.total()
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("saturated tools/call = %d, want 429; body=%s", w.Code, w.Body.String())
	}
	if got := up.total() - before; got != 0 {
		t.Errorf("R4-05: %d upstream forwards ran while the retained inventory was saturated, want 0", got)
	}
}

// --- R4-06 ---------------------------------------------------------------------

// TestPanicDuringCancellationNeverStrandsTheReservation is round-4 R4-06.
// Round-3 paired beginCancelAttempt with an endCancelAttempt on the NORMAL return
// path only. A panic in Upstream.Forward or GateAuditor.Settle unwound past it:
// the generation lease released (it was deferred) but the reservation stayed
// `inFlight` FOREVER, so every later client and sweep cancellation of that task
// was suppressed as "in flight" — and even clearCancelBar refuses an in-flight
// intent. Only a process restart recovered.
func TestPanicDuringCancellationNeverStrandsTheReservation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name  string
		drive func(rs *ResourceServer, token string)
	}{
		{
			name: "client tasks/cancel",
			drive: func(rs *ResourceServer, token string) {
				rs.ServeHTTP(httptest.NewRecorder(), taskReq(token, methodTasksCancel, `{"taskId":"task-panic"}`))
			},
		},
		{
			name: "server cancellation actuator",
			drive: func(rs *ResourceServer, _ string) {
				_, _ = rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &taskUpstream{}
			up.gate = func(req UpstreamRequest) {
				if req.Method == methodTasksCancel {
					panic("upstream transport exploded mid-cancellation")
				}
			}
			rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
			target := mustInsertTask(t, rs, TaskRecord{TaskID: "task-panic"})

			func() {
				defer func() {
					if r := recover(); r == nil {
						t.Error("R4-06: the panic must be PRESERVED and re-raised, not swallowed by the cleanup")
					}
				}()
				tc.drive(rs, token)
			}()

			// The reservation must be closed out, conservatively barred — never left
			// in flight (which would suppress every later cancellation forever).
			res := rs.taskLedger.beginCancelAttempt("task-panic", target.Generation)
			if res.ok {
				t.Error("R4-06: a cancellation attempt was permitted after an ambiguous panic; the attempt must be BARRED")
			}
			if res.bar == taskCancelBarInFlight {
				t.Error("R4-06: the reservation is STRANDED in flight — only a process restart would recover it")
			}
			if res.bar != taskCancelBarAmbiguous {
				t.Errorf("bar after a panic = %q, want %q", res.bar, taskCancelBarAmbiguous)
			}
			rec, ok := rs.taskLedger.lookup("task-panic")
			if !ok {
				t.Fatal("the record must survive: the cancellation may have reached the upstream")
			}
			if !rec.CancelUnconfirmed {
				t.Errorf("record after a panic = %+v, want CancelUnconfirmed (the effect may have landed)", rec)
			}
			// The generation pin must also have been dropped: a leaked pin would
			// block every later release/eviction of the identifier.
			if !testReleaseUnlessPinned(rs.taskLedger, "task-panic", target.Generation) {
				t.Error("R4-06: the generation pin was LEAKED by the panic")
			}
			// ...and the operator reconciliation path can still lift the bar, which a
			// stranded in-flight reservation would have made impossible.
			l := newTaskLedger(0, rsClock)
			fresh, err := l.insert(TaskRecord{
				TaskID: "task-panic", Subject: "agent:a", Tenant: "t", Tool: "search",
				Status: taskStatusWorking, CreatedAt: rsClock(),
			})
			if err != nil {
				t.Fatalf("insert: %v", err)
			}
			l.barCancelIntent(fresh.Generation, taskCancelBarAmbiguous, "panic")
			if !l.clearCancelBar(fresh.TaskID, fresh.Generation) {
				t.Error("an ambiguous (not in-flight) bar must be clearable by reconciliation")
			}
		})
	}
}

// --- R4-07 ---------------------------------------------------------------------

// TestActiveCapIsKeyedByTheCanonicalOwner is round-4 R4-07. The per-principal cap
// counted the bare `Subject`, while EVERY other part of this task model treats
// the canonical owner tuple (tenant, issuer, subject, act-as, client_id) as the
// principal. Two configured trusted issuers minting the same `sub` therefore
// aliased: issuer A could fill the cap and deny task admission to issuer B.
func TestActiveCapIsKeyedByTheCanonicalOwner(t *testing.T) {
	l := newTaskLedger(1, rsClock)
	mk := func(id, issuer, actAs, clientID string) error {
		_, err := l.insert(TaskRecord{
			TaskID: id, Tenant: "t", Issuer: issuer, Subject: "same-sub",
			ActAs: actAs, ClientID: clientID, Tool: "search",
			Status: taskStatusWorking, CreatedAt: rsClock(),
		})
		return err
	}
	if err := mk("t-a", "https://idp-a", "", ""); err != nil {
		t.Fatalf("issuer A first task: %v", err)
	}
	for name, tc := range map[string]struct{ id, issuer, actAs, clientID string }{
		"another trusted issuer": {"t-b", "https://idp-b", "", ""},
		"a different act-as":     {"t-c", "https://idp-a", "principal:x", ""},
		"a different OAuth client": {
			"t-d", "https://idp-a", "", "client-2",
		},
	} {
		if err := mk(tc.id, tc.issuer, tc.actAs, tc.clientID); err != nil {
			t.Errorf("R4-07: %s sharing the same `sub` was denied admission by another principal's cap: %v", name, err)
		}
	}
	// The cap still binds WITHIN one canonical owner.
	if err := mk("t-a2", "https://idp-a", "", ""); !errors.Is(err, errTaskSubjectCap) {
		t.Errorf("second task of the SAME owner = %v, want errTaskSubjectCap", err)
	}
}
