// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// task_round9_test.go — Stage 4, review ROUND-9 regressions.
//
// Round 9 returned exactly two blocking findings, and both are about an identity
// the rest of the design leans on being weaker than it claims:
//
//	R9-01 the finalized registration and the RELAY PIN were two transitions with a
//	      window between them, the handle was written even when the second one
//	      refused, the provenance compare-and-swap result was discarded, and an
//	      abnormal unwind through the writer left an operable row that claimed the
//	      handle had certainly NEVER been delivered;
//	R9-02 the canonical report tree was not INJECTIVE: two materially different
//	      reports the gateway accepted and relayed raw could produce the same digest,
//	      so an owner's collection proof for one satisfied retirement of the other.

// --- R9-01: the handle relay is one pinned, custodied transition ---------------

// round9AdvancingClock returns a clock that moves forward on EVERY read. It is how
// a test crosses a TTL boundary "between" two ledger calls without any sleeping or
// scheduling assumption: whatever the handler does next, it does it later.
func round9AdvancingClock(step time.Duration) func() time.Time {
	now := rsClock()
	return func() time.Time {
		now = now.Add(step)
		return now
	}
}

// round9Pinned reports whether a governed effect currently pins this GENERATION.
// The key matters: `leases` is generation-keyed, so passing a task id here can never
// match and the assertion built on it can never fail (round-10 N10-03).
func round9Pinned(l *taskLedger, generation string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leasedLocked(generation)
}

// round9LeaseCount is the TOTAL number of generation pins the ledger holds. A
// full-handler test whose row was EVICTED cannot look its generation up in `byID`,
// and "no pin was stranded anywhere" is the claim those tests actually make.
func round9LeaseCount(l *taskLedger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, n := range l.leases {
		total += n
	}
	return total
}

// round9OnlyRetiredGeneration returns the single tombstoned generation of a ledger
// that has retired exactly one. A TTL eviction tombstones the generation it removes
// (`evictExpiredLocked`), so a test whose row was evicted can still NAME the exact
// generation its no-pin assertion is about instead of asserting on a key that cannot
// match (round-10 N10-03).
func round9OnlyRetiredGeneration(t *testing.T, l *taskLedger) string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.retiredOrder) != 1 {
		t.Fatalf("want exactly ONE tombstoned generation for this scenario, got %d: %v",
			len(l.retiredOrder), l.retiredOrder)
	}
	return l.retiredOrder[0]
}

// panicResponseWriter panics INSIDE the body write, which is the abnormal unwind
// round-9 R9-01 names: the headers and the status are already out, an unknown
// number of body bytes may be on the wire, and the handler never reaches either of
// its classified outcomes.
type panicResponseWriter struct {
	hdr    http.Header
	status int
	writes int
}

func (p *panicResponseWriter) Header() http.Header {
	if p.hdr == nil {
		p.hdr = http.Header{}
	}
	return p.hdr
}

func (p *panicResponseWriter) Write([]byte) (int, error) {
	p.writes++
	panic("mcp-test: the response writer exploded mid-relay")
}

func (p *panicResponseWriter) WriteHeader(code int) { p.status = code }

// TestRound9HandleRelayRequiresItsGenerationLease is the reviewer's TTL
// counterexample, driven through the full handler.
//
// Round 8 finalized the registration under the ledger mutex, RELEASED it, and only
// then asked for the generation lease the relay is supposed to hold. The
// acquisition's own TTL sweep could evict the row in that window — and the caller
// treated the acquisition error as "there is no defer to install", so it wrote the
// handle ANYWAY:
//
//	if rs.taskLedger.acquireEffectLease(...) == nil {
//	        defer rs.taskLedger.releaseEffectLease(...)
//	}
//	writeResult(...)   // ← unconditional
//
// The client then held a handle for which this gateway has no governance row at
// all, and the post-write provenance compare-and-swap failed silently.
//
// Finalization and pin are now ONE transition: either the handler holds an operable
// record AND its pin, or it writes no handle.
func TestRound9HandleRelayRequiresItsGenerationLease(t *testing.T) {
	const id = "task-r9-lease"
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			// A 1 ms handle TTL: every later clock read is past this row's deadline.
			return round8TaskHandle(id, "1"), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, round9AdvancingClock(2*time.Millisecond))

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))

	if w.Code == http.StatusOK {
		t.Errorf("R9-01: the handle was written after generation-lease acquisition failed and evicted its record; "+
			"status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), id) {
		t.Errorf("R9-01: the task identifier reached the client although the relay transition refused; body=%s",
			w.Body.String())
	}
	// Nothing may claim the owner holds this handle, and no pin may be stranded.
	if rec, ok := rs.taskLedger.lookup(id); ok && rec.HandleRelayed {
		t.Errorf("R9-01: handle-relay provenance was installed for a refused relay: %+v", rec)
	}
	// ROUND-10 N10-03: this used to pass the TASK ID to a GENERATION-keyed probe, so
	// it could never match and the assertion could never fail. The evicted row's
	// generation is recoverable from its tombstone, so the assertion now names exactly
	// what its message claims — and the total pin count catches a strand on any other
	// generation too.
	evicted := round9OnlyRetiredGeneration(t, rs.taskLedger)
	if round9Pinned(rs.taskLedger, evicted) {
		t.Errorf("R9-01: a pin on generation %s was stranded by the refused relay", evicted)
	}
	if n := round9LeaseCount(rs.taskLedger); n != 0 {
		t.Errorf("R9-01: %d generation pin(s) stranded by the refused relay", n)
	}
}

// TestRound9FinalizationAndRelayPinAreOneTransition is the same rule at the ledger,
// one axis at a time — including the concurrent-state axis the full handler cannot
// schedule deterministically (a sweep that quarantines the row in the window round 8
// left open).
func TestRound9FinalizationAndRelayPinAreOneTransition(t *testing.T) {
	ms := func(n int64) *int64 { return &n }
	mk := func(t *testing.T, clock func() time.Time, ttl *int64) (*taskLedger, TaskRecord) {
		t.Helper()
		l := newTaskLedger(0, clock)
		rec, err := l.insert(TaskRecord{
			TaskID: "task-r9-pin", Subject: "agent:a", Tenant: "t", Tool: "search",
			Status: taskStatusWorking, CreatedAt: clock(), Pending: true, TTLMs: ttl,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return l, rec
	}
	t.Run("a clean finalization also PINS the generation", func(t *testing.T) {
		l, rec := mk(t, rsClock, ms(60000))
		got, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead)
		if !ok || got.Pending {
			t.Fatalf("settleRegistrationAndPin = %+v, %t; want the finalized record", got, ok)
		}
		if !round9Pinned(l, rec.Generation) {
			t.Error("R9-01: the finalization did not take the relay pin, so the relay window is open again")
		}
		// The positive control for both probes: they report a pin when one exists, so
		// the no-pin assertions elsewhere in this file are falsifiable (N10-03).
		if n := round9LeaseCount(l); n != 1 {
			t.Errorf("total generation pins after a clean finalization = %d, want exactly 1", n)
		}
		// The pin is a real one: the row cannot be released while it is held.
		if testReleaseUnlessPinned(l, rec.TaskID, rec.Generation) {
			t.Error("R9-01: a pinned generation was released underneath the relay")
		}
	})
	t.Run("a row that crossed its TTL is neither finalized nor pinned", func(t *testing.T) {
		l, rec := mk(t, round9AdvancingClock(5*time.Millisecond), ms(1))
		if got, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); ok {
			t.Errorf("R9-01: finalized a registration whose row had already expired: %+v", got)
		}
		if round9Pinned(l, rec.Generation) {
			t.Error("R9-01: a refused finalization left a pin behind")
		}
	})
	t.Run("a row quarantined in the old window is neither finalized nor pinned", func(t *testing.T) {
		l, rec := mk(t, rsClock, ms(60000))
		if !l.markQuarantine(rec.TaskID, rec.Generation, "a sweep could not confirm this registration") {
			t.Fatal("the scenario needs the quarantine to land")
		}
		if got, ok := l.settleRegistrationAndPin(rec.TaskID, rec.Generation, taskEffectClientRead); ok {
			t.Errorf("R9-01: finalized a quarantined registration: %+v", got)
		}
		if round9Pinned(l, rec.Generation) {
			t.Error("R9-01: a refused finalization left a pin behind")
		}
		// A refusal must not half-finalize: the row is still pending, so no other
		// path can mistake it for an operable, relayable registration.
		if after, ok := l.lookup(rec.TaskID); !ok || !after.Pending {
			t.Errorf("record after a refused finalization = %+v ok=%t, want it still PENDING", after, ok)
		}
	})
}

// TestRound9HandleRelayProvenanceCASIsEnforced pins the second half of R9-01: the
// relay custodian REPORTS whether the provenance compare-and-swap held, and the
// caller audits a refusal instead of discarding it.
//
// Stated honestly: under the pin taken with the finalization this refusal is
// unreachable through production paths (nothing can evict, release or retire the
// pinned generation), so this is a defense-in-depth contract test at the custodian
// rather than a full-handler scenario. What it forbids is the round-8 shape, where
// the boolean was dropped on the floor and a client holding a handle with no
// governance row behind it produced no signal at all.
func TestRound9HandleRelayProvenanceCASIsEnforced(t *testing.T) {
	l := newTaskLedger(0, rsClock)
	rec, err := l.insert(TaskRecord{
		TaskID: "task-r9-cas", Subject: "agent:a", Tenant: "t", Tool: "search",
		Status: taskStatusWorking, CreatedAt: rsClock(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	ok := (&taskHandleRelayGuard{ledger: l, taskID: rec.TaskID, generation: rec.Generation}).delivered()
	if !ok {
		t.Fatal("the guard refused a provenance install against the live generation")
	}
	if stored, _ := l.lookup(rec.TaskID); !stored.HandleRelayed {
		t.Error("R9-01: a successful relay installed no handle-relay provenance")
	}
	stale := &taskHandleRelayGuard{ledger: l, taskID: rec.TaskID, generation: "generation-that-no-longer-holds-this-id"}
	if stale.delivered() {
		t.Error("R9-01: the provenance compare-and-swap accepted a generation the record does not hold")
	}
	// The guard closes out EXACTLY ONCE: a second outcome may never rewrite the first.
	closed := &taskHandleRelayGuard{ledger: l, taskID: rec.TaskID, generation: rec.Generation}
	closed.undelivered(taskQuarantineHandleUndelivered)
	if closed.delivered() {
		t.Error("R9-01: an already-closed relay custodian accepted a second, contradicting outcome")
	}
}

// TestRound9AnAbnormalRelayUnwindAssumesPossibleDelivery is the reviewer's panic
// counterexample, and its assertion is the semantic one that matters.
//
// Round 8's only defer at the relay was the pin releaser. A panic in
// `ResponseWriter.Write` released the pin and executed NEITHER the success nor the
// error transition, so the row was left `Pending:false`, `Quarantined:false`,
// `HandleRelayed:false` — an operable record that positively asserts the handle was
// never delivered. That assertion authorizes an operator to retire (delete) a result
// its owner may be entitled to read, because `ownerCollectionSatisfied` treats
// `!HandleRelayed` as "there is no delivery to protect".
//
// An abnormal unwind is DELIVERY-AMBIGUOUS. The custodian therefore assumes POSSIBLE
// relay (`HandleRelayed:true`) and quarantines the row, and the panic keeps
// propagating untouched.
func TestRound9AnAbnormalRelayUnwindAssumesPossibleDelivery(t *testing.T) {
	const id = "task-r9-panic"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return round8TaskHandle(id, "60000"), nil
		case methodTasksGet:
			return json.RawMessage(conformingGetTaskResult(id, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	pw := &panicResponseWriter{}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("R9-01: the panic must be PRESERVED and re-raised, not swallowed by the relay custodian")
			}
		}()
		rs.ServeHTTP(pw, toolsCallReq(token, "search", `{}`))
	}()
	if pw.writes == 0 {
		t.Fatal("the handler never attempted to write the handle; the scenario did not run")
	}

	rec, ok := rs.taskLedger.lookup(id)
	if !ok {
		t.Fatal("the upstream task exists: its record must NEVER be forgotten (F-03)")
	}
	if !rec.HandleRelayed {
		t.Error("R9-01: an AMBIGUOUS unwind was recorded as certainly-never-delivered, which authorizes deleting a " +
			"result the owner may be entitled to read")
	}
	if !rec.Quarantined || rec.QuarantineReason != taskQuarantineHandleAmbiguous {
		t.Errorf("R9-01: record after an abnormal relay unwind = %+v, want the ambiguous reconciliation state", rec)
	}
	if rec.operable() {
		t.Error("R9-01: an unprovable registration stayed client-operable after its relay unwound abnormally")
	}
	if round9Pinned(rs.taskLedger, rec.Generation) {
		t.Error("R9-01: the generation pin was LEAKED by the panic")
	}

	// The consequence the semantics exist for: an operator who proves the task
	// terminal still may NOT delete it, because the owner may hold the handle.
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(rec, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(rec, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("R9-01: retire after an ambiguous relay unwind = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	if _, still := rs.taskLedger.lookup(id); !still {
		t.Error("R9-01: a refused retirement must never delete the record")
	}
}

// --- R9-02: the accepted canonical report tree is injective --------------------

// round9Report is one complete, schema-shaped terminal `GetTaskResult` whose single
// interesting difference is the RAW JSON string in its result payload.
func round9Report(taskID, rawString string) string {
	return `{"resultType":"complete","taskId":"` + taskID + `","status":"completed",` +
		`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,` +
		`"result":{"content":[{"type":"text","text":"` + rawString + `"}]}}`
}

// TestRound9AliasingReportsCannotSatisfyRetirement is the reviewer's exact R9-02
// lifecycle, over the full handler.
//
// The owner is served terminal report A, whose result string is the raw escape
// `\ud800`. A privileged read then confirms report B, identical except for
// `\ud801`. Both were ACCEPTED, and both raw bodies were relayed verbatim — but
// `json.Decoder` maps every unpaired surrogate escape to U+FFFD, so their canonical
// digests were EQUAL. The owner's collection proof for A therefore survived B, and
// `retire` deleted a row whose owner had never received B's bytes.
//
// The refusal is now BEFORE the lossy conversion, so neither body can become the
// authoritative terminal report and nothing is confirmed from either: the record is
// retained, has no confirmed terminal status, and retirement is REFUSED.
func TestRound9AliasingReportsCannotSatisfyRetirement(t *testing.T) {
	const id = "task-r9-alias"
	bodyA, bodyB := round9Report(id, `\ud800`), round9Report(id, `\ud801`)
	if bodyA == bodyB {
		t.Fatal("the scenario needs two MATERIALLY different report bodies")
	}
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	report := bodyA
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return round8TaskHandle(id, "60000"), nil
		case methodTasksGet:
			return json.RawMessage(report), nil
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

	// (1) the OWNER reads report A. The gateway still relays the upstream's own bytes
	// unchanged — it refuses to BELIEVE the body, it does not rewrite it — but the
	// body is not a report this validator accepts, so NOTHING is confirmed and no
	// collection proof exists.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("the owner's read = %d, want 200 (the body is relayed); body=%s", wg.Code, wg.Body.String())
	}
	afterA, ok := rs.taskLedger.lookup(id)
	if !ok {
		t.Fatal("the record must be retained: nothing was confirmed")
	}
	if afterA.TerminalReportDigest != "" || afterA.OwnerCollectedDigest != "" {
		t.Errorf("R9-02: an unpaired-surrogate report was accepted as authoritative: %+v", afterA)
	}
	if afterA.retirable() {
		t.Error("R9-02: an unpaired-surrogate report made the record retirable")
	}

	// (2) a privileged read confirms report B — the ALIASING twin. It is refused as
	// an upstream protocol fault, so it cannot become authoritative either.
	report = bodyB
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusBadGateway {
		// Deliberately NOT fatal: the retirement below is the harm this finding is
		// about, and the evidence must show it rather than stop one step short of it.
		t.Errorf("R9-02: the aliasing report was accepted by the authoritative read = %d, want 502; body=%s",
			ws.Code, ws.Body.String())
	}

	// (3) THE regression: retirement is REFUSED and the row survives. Before the fix
	// both bodies were accepted, their digests compared equal, the proof recorded for
	// A satisfied B, and this call returned 200 and deleted the record.
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("R9-02: retire after two ALIASING reports = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	if _, still := rs.taskLedger.lookup(id); !still {
		t.Fatal("R9-02: the row was deleted although its owner never received the report that was made authoritative")
	}
}

// TestRound9ADistinctAcceptedReportRevokesTheCollectionProof is the same lifecycle
// inside the ACCEPTED domain, and it is what injectivity has to buy: two reports
// that differ only in one string value must have DIFFERENT digests, so a proof bound
// to the first cannot discharge the second.
func TestRound9ADistinctAcceptedReportRevokesTheCollectionProof(t *testing.T) {
	const id = "task-r9-distinct"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	// U+FFFD written as a CONFORMING escape: it is a perfectly valid scalar, and the
	// gateway must keep accepting it — the refusal above is about escapes that encode
	// no character, not about this one.
	report := round9Report(id, `\ufffd`)
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return round8TaskHandle(id, "60000"), nil
		case methodTasksGet:
			return json.RawMessage(report), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	stored, _ := rs.taskLedger.lookup(id)

	// (1) the owner collects report A — a conforming terminal report.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("the owner's read = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}
	collected, ok := rs.taskLedger.lookup(id)
	if !ok || !collected.ownerCollectedCurrentTerminalReport() {
		t.Fatalf("record after the owner's collection = %+v ok=%t, want the collection proof", collected, ok)
	}

	// (2) a DIFFERENT accepted terminal report becomes authoritative.
	report = round9Report(id, `a different answer`)
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	moved, ok := rs.taskLedger.lookup(id)
	if !ok {
		t.Fatal("the record must be retained")
	}
	if moved.ownerCollectedCurrentTerminalReport() {
		t.Error("R9-02: a DIFFERENT authoritative report left the previous collection proof standing")
	}

	// (3) retirement is refused: the owner has never seen the answer that is now
	// authoritative.
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("R9-02: retire after the authoritative report changed = %d, want 409; body=%s",
			wr.Code, wr.Body.String())
	}
}

// TestRound9CanonicalTreeRefusesLossyStrings is the helper table under the two
// lifecycles: what a conforming server may send is accepted, and exactly the two
// contents whose standard-library decode is LOSSY are refused.
//
// Primary sources (accessed 2026-07-25): the MCP transport binding requires UTF-8
// encoded JSON-RPC messages; RFC 8259 §8.1 requires UTF-8 for interchange, §8.2
// warns that unpaired surrogates encode no character and behave unpredictably, and
// §9 permits a parser to limit "the length and character contents of strings".
func TestRound9CanonicalTreeRefusesLossyStrings(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want bool // accepted
	}{
		"plain ASCII":                             {`{"a":"hello"}`, true},
		"a conforming BMP escape":                 {`{"a":"\u00e9"}`, true},
		"a conforming replacement character":      {`{"a":"\ufffd"}`, true},
		"a literal replacement character":         {"{\"a\":\"�\"}", true},
		"a well-formed surrogate PAIR":            {`{"a":"\ud83d\ude00"}`, true},
		"an escaped quote inside a string":        {`{"a":"say \"hi\" \\u0041"}`, true},
		"a non-ASCII object KEY":                  {`{"😀":"x"}`, true},
		"the escape that looks like one":          {`{"a":"\\ud800"}`, true},
		"a lone HIGH surrogate":                   {`{"a":"\ud800"}`, false},
		"a lone LOW surrogate":                    {`{"a":"\udc00"}`, false},
		"a high surrogate then a BMP escape":      {`{"a":"\ud800A"}`, false},
		"a high surrogate PAIRED with a lone one": {`{"a":"\ud83d\ude00\udc00"}`, false},
		"a lone surrogate in a KEY":               {`{"\ud800":"x"}`, false},
		"a truncated \\u escape":                  {`{"a":"\u00"}`, false},
	} {
		err := rejectLossyJSONStrings([]byte(tc.raw))
		if (err == nil) != tc.want {
			t.Errorf("R9-02: rejectLossyJSONStrings(%s) [%s] err = %v, accepted want %t", tc.raw, name, err, tc.want)
		}
	}
	// Invalid UTF-8 cannot be written as a Go source string literal by accident, so
	// it is built from bytes: `0xff` is not a valid UTF-8 sequence, and
	// `0xed 0xa0 0x80` is the CESU-8 spelling of the same lone surrogate.
	for name, raw := range map[string][]byte{
		"a raw invalid UTF-8 byte":       append(append([]byte(`{"a":"`), 0xff), []byte(`"}`)...),
		"a surrogate encoded as CESU-8":  append(append([]byte(`{"a":"`), 0xed, 0xa0, 0x80), []byte(`"}`)...),
		"an overlong two-byte encoding":  append(append([]byte(`{"a":"`), 0xc0, 0xaf), []byte(`"}`)...),
		"a truncated multi-byte encoded": append(append([]byte(`{"a":"`), 0xe2, 0x82), []byte(`"}`)...),
	} {
		if err := rejectLossyJSONStrings(raw); err == nil {
			t.Errorf("R9-02: invalid UTF-8 (%s) was accepted; the decode would have replaced it with U+FFFD", name)
		}
	}
}

// TestRound9AcceptedReportsHaveDistinctDigests is the injectivity property itself,
// stated over `strictGetTaskResult` — the one validator whose digest is used as
// deletion proof.
func TestRound9AcceptedReportsHaveDistinctDigests(t *testing.T) {
	digest := func(t *testing.T, body string) string {
		t.Helper()
		rep, err := strictGetTaskResult("t", json.RawMessage(body))
		if err != nil {
			t.Fatalf("a conforming report was refused: %v; body=%s", err, body)
		}
		return rep.Digest
	}
	// Distinct MEANINGS must have distinct identities.
	seen := map[string]string{}
	for _, body := range []string{
		round9Report("t", `\ufffd`),
		round9Report("t", `a`),
		round9Report("t", `b`),
		round9Report("t", `\ud83d\ude00`),
		round9Report("t", ``),
	} {
		d := digest(t, body)
		if prev, dup := seen[d]; dup {
			t.Errorf("R9-02: two distinct accepted reports share a canonical digest:\n  %s\n  %s", prev, body)
		}
		seen[d] = body
	}
	// ...and two spellings of the SAME Unicode string are the SAME report. That is
	// semantic equality, not a collision: the reports mean one thing.
	if a, b := digest(t, round9Report("t", `A`)), digest(t, round9Report("t", `\u0041`)); a != b {
		t.Errorf("R9-02: two spellings of the same string produced different digests (%s vs %s)", a, b)
	}
	// The two lossy contents never reach a digest at all.
	for _, body := range []string{round9Report("t", `\ud800`), round9Report("t", `\ud801`)} {
		if _, err := strictGetTaskResult("t", json.RawMessage(body)); err == nil {
			t.Errorf("R9-02: an unpaired-surrogate report was accepted and given an identity: %s", body)
		}
	}
}
