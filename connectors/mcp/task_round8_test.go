// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// task_round8_test.go — Stage 4, review ROUND-8 regressions.
//
// Round 8 returned four blocking findings, and three of them are the same mistake
// in three places: a governance decision was being taken from a value that does not
// mean what the decision needs it to mean.
//
//	R8-01 the retirement rule read CURRENT state (`!Pending && !Quarantined &&
//	      !Reconciling`) as if it were HISTORY, so a record whose handle its owner
//	      already held could be quarantined afterwards and then deleted unread — and,
//	      in the other direction, a registration whose handle relay FAILED owed a
//	      collection its owner could never perform. A second, ungated deletion door
//	      (`release`) bypassed the check entirely;
//	R8-02 the collection proof was a bare boolean, so a later, DIFFERENT
//	      authoritative terminal report did not revoke it;
//	R8-03 the `GetTaskResult` validator refused the conforming `1e-0` and accepted
//	      timestamps outside its own declared profile (`+24:00`, `+23:60`, a comma
//	      fraction);
//	R8-04 a conforming report's `ttlMs` was validated and then discarded, so an
//	      EXTENDED upstream TTL still evicted the owner at the stale initial deadline.

// --- R8-01: immutable handle-relay provenance ---------------------------------

// round8TaskHandle is the upstream tools/call answer that creates one durable task.
func round8TaskHandle(taskID, ttl string) json.RawMessage {
	body := `{"resultType":"task","taskId":"` + taskID + `","status":"working"`
	if ttl != "" {
		body += `,"ttlMs":` + ttl
	}
	return json.RawMessage(body + "}")
}

// TestRound8AQuarantinedRecordKeepsItsDeliveryObligation is R8-01's first
// direction, driven entirely through production paths.
//
// The owner receives a normal task handle. A kill-switch sweep whose cancellation
// cannot be confirmed then QUARANTINES that existing record, which makes every
// client task method refuse it — and round-7 read exactly that current state as
// "this record was never client-readable, so nobody is waiting for its result".
// A privileged status read plus `retire` therefore returned 200 and deleted the
// owner's only authorization WITHOUT serving the result: the R6-02 harm, reached
// through a state transition instead of through the retirement rule.
//
// Provenance is a fact about the past, so no later transition can invert it.
func TestRound8AQuarantinedRecordKeepsItsDeliveryObligation(t *testing.T) {
	const id = "task-r8-quarantined"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return round8TaskHandle(id, "60000"), nil
		case methodTasksCancel:
			return nil, fmt.Errorf("mcp-test: the upstream refused the cancellation")
		case methodTasksGet:
			return json.RawMessage(conformingGetTaskResult(id, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	// (1) the handle really reaches the owner, through the one relay site.
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	stored, ok := rs.taskLedger.lookup(id)
	if !ok || !stored.HandleRelayed {
		t.Fatalf("record after a successful handle relay = %+v ok=%t, want HandleRelayed", stored, ok)
	}

	// (2) a sweep whose cancellation does not succeed quarantines the DELIVERED
	// record (production path: CancelActiveTasks → the sweep bookkeeping).
	if _, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop"); err == nil {
		t.Fatal("the scenario needs a sweep whose cancellation did NOT succeed")
	}
	rec, ok := rs.taskLedger.lookup(id)
	if !ok || !rec.Quarantined {
		t.Fatalf("record after the failed sweep = %+v ok=%t, want it quarantined", rec, ok)
	}
	if !rec.HandleRelayed {
		t.Fatal("R8-01: quarantine ERASED the handle-relay provenance; it records something that already happened")
	}
	// The owner is now denied every client task method — unchanged, deny-closed, and
	// exactly why the deletion must not be treated as harmless.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
	if wg.Code != http.StatusForbidden {
		t.Fatalf("the owner's read of a quarantined record = %d, want 403; body=%s", wg.Code, wg.Body.String())
	}

	// (3) the operator proves the task terminal...
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	confirmed, ok := rs.taskLedger.lookup(id)
	if !ok || !confirmed.retirable() {
		t.Fatalf("record after the confirming read = %+v ok=%t, want a CONFIRMED terminal status", confirmed, ok)
	}

	// (4) THE regression: retirement is refused, because the owner holds a handle to
	// a result it has not been served. Round-7 answered 200 here and deleted the row.
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("R8-01: retire of a previously-delivered quarantined record = %d, want 409; body=%s",
			wr.Code, wr.Body.String())
	}
	if !strings.Contains(wr.Body.String(), "has not yet collected") {
		t.Errorf("R8-01: the refusal must name the missing delivery; body=%s", wr.Body.String())
	}
	if _, still := rs.taskLedger.lookup(id); !still {
		t.Fatal("R8-01: a refused retirement must never delete the record")
	}

	// (5) ...and the inventory EXPLAINS it without the operator having to attempt the
	// action: the row's class is `quarantined-orphan` (the state to act on first),
	// while the two independent fields carry the delivery obligation.
	rows := listReconcileRows(t, rs, token, "")
	if len(rows.Records) != 1 {
		t.Fatalf("inventory = %+v, want the single retained row", rows.Records)
	}
	row := rows.Records[0]
	if !row.HandleRelayed || row.OwnerCollected || row.Retirable {
		t.Errorf("R8-01: row = %+v, want handleRelayed=true ownerCollected=false retirable=false", row)
	}
}

// TestRound8ADeliveredArtifactIsNotReleasedByATerminalReport is R8-01's SECOND
// deletion door. `syncTaskStatusFromResult` released any `Reconciling` record whose
// terminal status was confirmed, with no delivery check at all — and a normal,
// handle-delivered record REACHES `Reconciling` (a sweep that quarantines it, then
// a later sweep whose cancellation the upstream acknowledges). The gated retirement
// was therefore bypassable by driving the record through that state.
//
// Both deletions now run the ONE compare-and-delete predicate.
func TestRound8ADeliveredArtifactIsNotReleasedByATerminalReport(t *testing.T) {
	const id = "task-r8-artifact"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	cancelFails := true
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return round8TaskHandle(id, "60000"), nil
		case methodTasksCancel:
			if cancelFails {
				return nil, fmt.Errorf("mcp-test: the upstream refused the cancellation")
			}
			return json.RawMessage(normativeCompleteResult), nil
		case methodTasksGet:
			return json.RawMessage(conformingGetTaskResult(id, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	stored, ok := rs.taskLedger.lookup(id)
	if !ok || !stored.HandleRelayed {
		t.Fatalf("record after the relay = %+v ok=%t, want HandleRelayed", stored, ok)
	}
	// A failed sweep quarantines it; a second sweep whose cancellation IS
	// acknowledged turns the quarantined record into a reconciliation artifact.
	if _, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 1"); err == nil {
		t.Fatal("the scenario needs a first sweep that did NOT succeed")
	}
	cancelFails = false
	if _, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 2"); err != nil {
		t.Fatalf("second sweep = %v, want the acknowledged cancellation", err)
	}
	artifact, ok := rs.taskLedger.lookup(id)
	if !ok || !artifact.Reconciling {
		t.Fatalf("record after the acknowledged sweep = %+v ok=%t, want a reconciliation artifact", artifact, ok)
	}
	if !artifact.HandleRelayed {
		t.Fatal("R8-01: the reconciliation transition ERASED the handle-relay provenance")
	}

	// THE regression: the authoritative terminal report confirms the status and does
	// NOT delete the row — its owner still has that result to collect. Round-7's
	// ungated `release` deleted it here, bypassing the retirement rule entirely.
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	survivor, still := rs.taskLedger.lookup(id)
	if !still {
		t.Fatal("R8-01: a confirmed terminal report RELEASED a record whose owner had not collected its result")
	}
	if !survivor.retirable() {
		t.Errorf("record after the confirming read = %+v, want a CONFIRMED terminal status", survivor)
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Errorf("R8-01: retire of the uncollected artifact = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
}

// TestRound8AFailedHandleRelayIsNeverDeliveredAndDrainable is R8-01's OPPOSITE
// direction, and it is the reviewer's exact counterexample.
//
// The registration settles durably — the record is operable — and then the response
// carrying the task handle fails to write. Round-7 ignored that error at
// `rs.go:901`: the record kept an obligation to serve an owner that had never
// learned the unguessable task id, so with `ttlMs:null` it answered `retire` 409
// FOREVER and permanently consumed one retention slot.
//
// The relay outcome is now recorded. A handle that was never delivered leaves no
// delivery to protect, and the row is retained as an operator-drainable
// never-delivered artifact instead of as a live governed task nobody can address.
func TestRound8AFailedHandleRelayIsNeverDeliveredAndDrainable(t *testing.T) {
	const id = "task-r8-undelivered"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			// No ttlMs: the handle declares no expiry, so nothing can quietly forget
			// this row — the reviewer's `ttlMs:null` case.
			return round8TaskHandle(id, ""), nil
		case methodTasksGet:
			return json.RawMessage(conformingGetTaskResult(id, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	fw := &failingResponseWriter{}
	rs.ServeHTTP(fw, toolsCallReq(token, "search", `{}`))
	if fw.writes == 0 {
		t.Fatal("the handler never attempted to write the handle; the scenario did not run")
	}
	rec, ok := rs.taskLedger.lookup(id)
	if !ok {
		t.Fatal("the upstream task exists: its record must NEVER be forgotten (F-03)")
	}
	if rec.HandleRelayed {
		t.Fatal("R8-01: a FAILED handle relay was recorded as a delivery")
	}
	if !rec.Quarantined || rec.QuarantineReason != taskQuarantineHandleUndelivered {
		t.Errorf("R8-01: record after a failed relay = %+v, want the never-delivered reconciliation state", rec)
	}
	// Nobody can address it — the owner never learned the identifier, and the record
	// is not client-operable either.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
	if wg.Code != http.StatusForbidden {
		t.Errorf("a read of a never-delivered record = %d, want 403; body=%s", wg.Code, wg.Body.String())
	}
	rows := listReconcileRows(t, rs, token, "")
	if len(rows.Records) != 1 || rows.Records[0].HandleRelayed || !rows.Records[0].Actionable {
		t.Fatalf("inventory = %+v, want one actionable row with handleRelayed=false", rows.Records)
	}

	// THE regression: the operator proves the task terminal and the row DRAINS.
	// Round-7 refused this retirement for ever, because it demanded proof of a
	// delivery that could never happen.
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(rec, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(rec, "")))
	if wr.Code != http.StatusOK {
		t.Fatalf("R8-01: retire of a never-delivered record = %d, want 200; body=%s", wr.Code, wr.Body.String())
	}
	if survivor, still := rs.taskLedger.lookup(id); still {
		t.Errorf("record after the drain = %+v, want it forgotten", survivor)
	}
	if got := rs.taskLedger.admissionState(rec.owner()).OwnerRetained; got != 0 {
		t.Errorf("owner retention after the drain = %d, want 0 (the slot was consumed for ever)", got)
	}
}

// --- R8-04: a conforming report's TTL is APPLIED ------------------------------

// TestRound8AnExtendedTTLCannotBeEvictedByTheStaleInitialValue is R8-04 over the
// full handler. SEP-2663 measures `Task.ttlMs` from creation and says it MAY change
// over the task's lifetime; round-7 validated the reported value and then updated
// neither the TTL nor any effective expiry, so at local age 1500ms an owner whose
// task had been EXTENDED to 60s was evicted and answered 403 — without asking an
// upstream that still held the result.
func TestRound8AnExtendedTTLCannotBeEvictedByTheStaleInitialValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  string
	}{
		{"an extended ttlMs", "60000"},
		{"a null ttlMs (the task never expires)", "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "task-r8-ttl"
			token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
			now := rsClock()
			up := &taskUpstream{}
			up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
				switch req.Method {
				case "tools/call":
					return round8TaskHandle(id, "1000"), nil
				case methodTasksGet:
					return json.RawMessage(conformingGetTaskResultTTL(id, taskStatusWorking, tc.ttl)), nil
				}
				return json.RawMessage(normativeCompleteResult), nil
			}
			rs := newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, func() time.Time { return now })

			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			if w.Code != http.StatusOK {
				t.Fatalf("tools/call = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			if rec, ok := rs.taskLedger.lookup(id); !ok || rec.TTLMs == nil || *rec.TTLMs != 1000 {
				t.Fatalf("stored record = %+v ok=%t, want the initial ttlMs 1000", rec, ok)
			}
			// BEFORE the stale deadline, the upstream reports the CURRENT ttlMs.
			now = now.Add(500 * time.Millisecond)
			wg := httptest.NewRecorder()
			rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
			if wg.Code != http.StatusOK {
				t.Fatalf("the owner's read = %d, want 200; body=%s", wg.Code, wg.Body.String())
			}
			rec, ok := rs.taskLedger.lookup(id)
			if !ok {
				t.Fatal("the record disappeared before its own initial deadline")
			}
			switch tc.ttl {
			case "null":
				if rec.TTLMs != nil {
					t.Errorf("R8-04: ttlMs after a null report = %d, want UNBOUNDED", *rec.TTLMs)
				}
			default:
				if rec.TTLMs == nil {
					t.Errorf("R8-04: ttlMs after an extending report = UNBOUNDED, want 60000")
				} else if *rec.TTLMs != 60000 {
					t.Errorf("R8-04: ttlMs after an extending report = %d, want 60000", *rec.TTLMs)
				}
			}
			// PAST the stale deadline, and well before the current one.
			now = now.Add(1 * time.Second)
			if _, still := rs.taskLedger.lookup(id); !still {
				t.Fatal("R8-04: the record was evicted at the STALE initial deadline")
			}
			wg2 := httptest.NewRecorder()
			rs.ServeHTTP(wg2, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
			if wg2.Code != http.StatusOK {
				t.Fatalf("R8-04: the owner's read past the stale deadline = %d, want 200 — the gateway is the owner's "+
					"only route to a result the upstream still holds; body=%s", wg2.Code, wg2.Body.String())
			}
		})
	}
}

// TestRound8ReportedTTLIsMonotoneAndNeverWraps pins the exact rule the effective
// retention update follows, including the two directions it deliberately does NOT
// take: it never SHORTENS (a broken or hostile upstream would otherwise hold the
// trigger for deleting an owner's only authorization early — R6-02 with a remote
// hand on it), and a value outside this process's duration range means UNBOUNDED
// rather than a wrapped `time.Duration` (round-2 N-05).
func TestRound8ReportedTTLIsMonotoneAndNeverWraps(t *testing.T) {
	ms := func(n int64) *int64 { return &n }
	for name, tc := range map[string]struct {
		start    *int64
		reported *int64
		want     *int64
	}{
		"an extension applies":              {ms(1000), ms(60000), ms(60000)},
		"null becomes unbounded":            {ms(1000), nil, nil},
		"a shorter value is ignored":        {ms(60000), ms(1000), ms(60000)},
		"an equal value is a no-op":         {ms(1000), ms(1000), ms(1000)},
		"unbounded is never re-bounded":     {nil, ms(1000), nil},
		"an unrepresentable value unbounds": {ms(1000), nil, nil},
	} {
		rec := TaskRecord{TTLMs: tc.start}
		rec.applyReportedTTL(tc.reported)
		switch {
		case tc.want == nil && rec.TTLMs != nil:
			t.Errorf("%s: ttlMs = %d, want UNBOUNDED", name, *rec.TTLMs)
		case tc.want != nil && rec.TTLMs == nil:
			t.Errorf("%s: ttlMs = UNBOUNDED, want %d", name, *tc.want)
		case tc.want != nil && *rec.TTLMs != *tc.want:
			t.Errorf("%s: ttlMs = %d, want %d", name, *rec.TTLMs, *tc.want)
		}
	}
	// The unrepresentable case end to end: the validator reports UNBOUNDED for a
	// conforming value it cannot hold, so nothing ever multiplies it.
	rep, err := strictGetTaskResult("t", json.RawMessage(
		`{"resultType":"complete","taskId":"t","status":"working","createdAt":"2026-06-08T12:00:00Z",`+
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":1e2000000000}`))
	if err != nil {
		t.Fatalf("a conforming huge ttlMs was refused: %v", err)
	}
	if rep.TTLMs != nil {
		t.Errorf("R8-04: ttlMs 1e2000000000 = %d, want UNBOUNDED (never materialized, never wrapped)", *rep.TTLMs)
	}
}

// --- R8-03: the timestamp profile is an explicit grammar ----------------------

// TestRound8TimestampProfileIsAnExplicitPredicate is the HELPER table for R8-03's
// second half. The layouts round-7 called an "exhaustive membership" were not a
// predicate at all: `time.Parse` range-checks the clock fields but not the zone
// designator, and it accepts a comma as the fractional separator. Three spellings
// the profile's own prose excludes were therefore accepted inside the one body the
// deletion rule calls proof.
func TestRound8TimestampProfileIsAnExplicitPredicate(t *testing.T) {
	for ts, want := range map[string]bool{
		// The offset RANGES the profile states, at their boundaries.
		"2026-06-08T12:00:00+23:59": true,
		"2026-06-08T12:00:00-23:59": true,
		"2026-06-08T12:00:00+00:00": true,
		"2026-06-08T12:00:00+2359":  true,
		"2026-06-08T12:00:00+24:00": false,
		"2026-06-08T12:00:00+23:60": false,
		"2026-06-08T12:00:00-24:00": false,
		"2026-06-08T12:00:00+2400":  false,
		"2026-06-08T12:00:00+2360":  false,
		// The fractional separator is EXACTLY `.` — W3C-DTF names no other, and Go's
		// parser accepted `,`.
		"2026-06-08T12:00:00.5Z":          true,
		"2026-06-08T12:00:00.123456789Z":  true,
		"2026-06-08T12:00:00,5Z":          false,
		"2026-06-08T12:00:00.1234567890Z": false, // the declared 9-digit limit
		"2026-06-08T12:00:00.Z":           false,
		// A fraction belongs to SECONDS, not to a reduced precision.
		"2026-06-08T12:00.5Z": false,
		// The forms the profile does declare.
		"2026-06-08T12:00Z":        true,
		"2026-06-08T12:00:00Z":     true,
		"2026-06-08T12:00:00":      true,
		"20260608T120000Z":         true,
		"20260608T1200Z":           true,
		"20260608T120000.5-0500":   true,
		"20260608T120000+02:00":    true,
		"2026-06-08T12:00:00+0200": true,
		// ...and the ones it excludes.
		"2026-06-08":             false,
		"20260608":               false,
		" 2026-06-08T12:00:00Z ": false,
		"2026-06-08t12:00:00Z":   false,
		"2026-159T12:00:00Z":     false,
		"2026-W24-1T12:00:00Z":   false,
		"2026-06-08T25:00:00Z":   false,
		"2026-06-08T12:60:00Z":   false,
		"2026-06-08T12:00:60Z":   false,
		"2026-02-30T12:00:00Z":   false,
		"2026-06-08T12:0:00Z":    false,
		"2026-06-08T120000Z":     false,
		"20260608T12:00:00Z":     false,
		"":                       false,
		"t":                      false,
	} {
		if got := isoTimestamp(ts); got != want {
			t.Errorf("R8-03: isoTimestamp(%q) = %t, want %t", ts, got, want)
		}
	}
}

// TestRound8AllZeroExponentIsExponentZero is the HELPER table for R8-03's first
// half: RFC 8259 permits an exponent sign followed by digits, so `1e-0` is a
// conforming spelling of the integer 1. Trimming the all-zero exponent produced the
// empty string, `Atoi` failed, and the failure branch SATURATED the exponent
// negative — refusing a conforming report over its spelling.
func TestRound8AllZeroExponentIsExponentZero(t *testing.T) {
	for lexeme, want := range map[string]bool{
		"1e0": true, "1e+0": true, "1e-0": true, "1E-0": true,
		"1e-00": true, "1e00": true, "0e-0": true, "1.5e-0": false,
		"10e-0": true, "1e-1": false, "10e-1": true,
	} {
		if _, _, ok := jsonNumberNonNegativeInteger(lexeme); ok != want {
			t.Errorf("R8-03: jsonNumberNonNegativeInteger(%q) ok = %t, want %t", lexeme, ok, want)
		}
	}
	// The decomposition is still exact where the value is STORED.
	digits, shift, ok := jsonNumberNonNegativeInteger("1e-0")
	if !ok || digits != "1" || shift != 0 {
		t.Errorf("R8-03: jsonNumberNonNegativeInteger(\"1e-0\") = (%q, %d, %t), want (\"1\", 0, true)", digits, shift, ok)
	}
}
