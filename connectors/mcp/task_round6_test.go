// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// task_round6_test.go — Stage 4, review ROUND-6 regressions.
//
// Round 6 left exactly three blocking findings, and every one of them is a case
// where a previous round's fix went one step too far or one step short:
//
//	R6-01 (P1) the GetTaskResult validator became OVER-strict — it closed the base
//	           `Result`'s open extension namespace, so a CONFORMING report carrying
//	           an extension member was refused and its record became undrainable
//	           (the same interoperability class round 4 caught on tasks/update);
//	R6-02 (P1) operator retirement DELETED the row that was the owner's only
//	           authorization to read its own task, so the final tool result was lost
//	           through the gateway while its upstream TTL was still running;
//	R6-03 (P1) the pagination cursor was forgeable (bare base64 with a self-reported
//	           instance) and its traversal was incomplete under mutation: a row
//	           inserted after page 1 whose key sorted before that page's last key was
//	           never returned, while the code claimed no row could be skipped.

// --- R6-01 -------------------------------------------------------------------

// TestReconcileStatusAcceptsAConformingOpenResultExtension is R6-01 end to end,
// the reviewer's exact scenario: a conforming server answers the authoritative
// read for a Canceled task with every mandatory member plus
// `"com.example/resultExtension":{"version":1}`. `GetTaskResult = Result &
// DetailedTask` and the base `Result` declares `[key: string]: unknown`, so that
// member is legal. Round-5 called it "unknown", answered 502, refused to confirm
// anything — and the record could then never be drained against that server.
func TestReconcileStatusAcceptsAConformingOpenResultExtension(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(`{"resultType":"complete","taskId":"task-open-ext","status":"cancelled",` +
				`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,` +
				`"com.example/resultExtension":{"version":1}}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-open-ext")

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("R6-01: a conforming report carrying an open-Result extension member = %d, want 200; body=%s",
			w.Code, w.Body.String())
	}
	rec, ok := rs.taskLedger.lookup("task-open-ext")
	if !ok || !rec.retirable() {
		t.Fatalf("R6-01: record after a conforming extended report = %+v ok=%t, want a CONFIRMED terminal status", rec, ok)
	}
	// ...and the record therefore DRAINS, which is the property the over-strict
	// refusal destroyed. ROUND-7: the drain now runs through proof of delivery —
	// the owner collects its terminal result and only then may the row be retired.
	wc := httptest.NewRecorder()
	rs.ServeHTTP(wc, taskReq(token, methodTasksGet, `{"taskId":"task-open-ext"}`))
	if wc.Code != http.StatusOK {
		t.Fatalf("R6-01: the owner's collecting read = %d, want 200; body=%s", wc.Code, wc.Body.String())
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusOK {
		t.Errorf("R6-01: retire after the conforming extended report = %d, want 200; body=%s", wr.Code, wr.Body.String())
	}
}

// TestTaskValidationAuditPersistsAClassNotUpstreamText is R6-05: the strict
// decoder quotes upstream-controlled PROPERTY NAMES, and the audit sites used to
// concatenate the whole parser error into the persisted reason. A secret encoded
// as a JSON key therefore bypassed the round-5 response projection straight into
// the durable audit trail.
func TestTaskValidationAuditPersistsAClassNotUpstreamText(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	const secretKey = "ROUND6-SECRET-PROPERTY-NAME"
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			// A DUPLICATE key whose name is the secret: the strict decoder's error text
			// quotes it verbatim.
			return json.RawMessage(`{"resultType":"complete","taskId":"task-audit-leak","status":"cancelled",` +
				`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,` +
				`"` + secretKey + `":1,"` + secretKey + `":2}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	aud := &taskAuditor{}
	rs, stored := newReconcileRS(t, jwks, up, aud, token, "task-audit-leak")

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("ambiguous upstream report = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secretKey) {
		t.Errorf("R6-05: the response echoed the upstream property name: %s", w.Body.String())
	}
	classed := false
	for _, d := range aud.decisions {
		if strings.Contains(d.Reason, secretKey) {
			t.Errorf("R6-05: an audit reason carries the upstream property name: %q", d.Reason)
		}
		if strings.Contains(d.Reason, defectGetResultDecode) {
			classed = true
		}
	}
	if !classed {
		t.Errorf("R6-05: no audit reason carries the stable validation class %q; reasons=%v",
			defectGetResultDecode, auditReasons(aud))
	}
	// The class vocabulary is the gateway's own, closed, and derived from the error
	// rather than from the body.
	_, err := strictGetTaskResult("t", json.RawMessage(`{"resultType":"complete","taskId":"t"}`))
	if got := taskDefectClass(err); got != defectGetResultStatus {
		t.Errorf("validation class = %q, want %q", got, defectGetResultStatus)
	}
	if got := taskDefectClass(fmt.Errorf("some other error")); got != taskDefectUnclassified {
		t.Errorf("class of a foreign error = %q, want %q", got, taskDefectUnclassified)
	}
}

func auditReasons(aud *taskAuditor) []string {
	out := make([]string, 0, len(aud.decisions))
	for _, d := range aud.decisions {
		out = append(out, d.Reason)
	}
	return out
}

// --- R6-02 -------------------------------------------------------------------
//
// ROUND-7 DESIGN CHANGE, recorded here because it is what these two tests now
// pin. R6-02's protection — an operator drain must never destroy a final tool
// result the owner has not collected — is UNCHANGED and still enforced. What
// changed is the mechanism. Round-6 retired the row first and kept it alive in a
// bounded "handoff" FIFO; round-7 REFUSES the retirement until the owner has
// provably received the result, and then deletes the row outright.
//
// The three round-7 findings that lived in the cache (an unread oldest handoff
// evicted at the 513th retirement, a leased row escaping the bound, and a handoff
// discharged by a response write whose failure was discarded) are eliminated
// structurally rather than patched: there is no cache, no FIFO and no post-write
// forget. `TestRetiredHandoffRetentionIsBounded`, which positively REQUIRED an
// unread oldest handoff to disappear, is deleted with the design it pinned; its
// bound is now the ordinary retention cap, exercised by
// `TestRound7UncollectedTerminalRowsAreOrdinaryBoundedRows` in task_round7_test.go.

// TestRetirementNeverDestroysAnUncollectedResult is R6-02, the reviewer's exact
// sequence: an operator reads the authoritative status of a normal task, gets a
// conforming `completed` report and tries to retire the row BEFORE the owning
// client has performed its terminal poll.
//
// Round-5 deleted the row. The owner's next `tasks/get` then received 403 "task
// not tracked" with NO upstream call at all: the final tool result was lost
// through the gateway even though the upstream TTL had not elapsed, and the
// round-5 comment claiming the upstream serves it to "any holder of the handle"
// was false — the client holds a route to the PEP, not to the upstream.
//
// Round-7 refuses the retirement instead of compensating for it.
func TestRetirementNeverDestroysAnUncollectedResult(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	const finalResult = "ROUND6-FINAL-TOOL-RESULT"
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(`{"resultType":"complete","taskId":"task-handoff","status":"completed",` +
				`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,` +
				`"result":{"content":[{"type":"text","text":"` + finalResult + `"}]}}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-handoff")

	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	// THE regression: the drain is REFUSED while the owner has not collected, so
	// there is no moment at which an uncollected result could be destroyed.
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("R6-02: retire before the owner collected = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	if !strings.Contains(wr.Body.String(), "has not yet collected") {
		t.Errorf("R6-02: the refusal must name the missing precondition; body=%s", wr.Body.String())
	}
	// The row is an ORDINARY retained row meanwhile: counted, listed, actionable and
	// explicitly not retirable. Nothing about it is cached, exempt or invisible.
	if got := rs.taskLedger.admissionState(stored.owner()).OwnerRetained; got != 1 {
		t.Errorf("owner retention while the result is uncollected = %d, want 1 (the row counts like any other)", got)
	}
	rows := listReconcileRows(t, rs, token, "")
	if len(rows.Records) != 1 || rows.Records[0].Class != taskReconcileRowTerminalUncollected ||
		!rows.Records[0].Actionable || rows.Records[0].Retirable || rows.Records[0].OwnerCollected {
		t.Errorf("uncollected terminal row = %+v, want the actionable %q class, retirable=false, ownerCollected=false",
			rows.Records, taskReconcileRowTerminalUncollected)
	}
	// The owner collects its own final tool result, and the read really reaches the
	// upstream.
	before := up.count(methodTasksGet)
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"task-handoff"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("R6-02: the owner's terminal tasks/get = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}
	if got := up.count(methodTasksGet) - before; got != 1 {
		t.Errorf("R6-02: %d upstream reads served the owner's terminal poll, want 1", got)
	}
	if !strings.Contains(wg.Body.String(), finalResult) {
		t.Errorf("R6-02: the owner's terminal read did not carry the final tool result: %s", wg.Body.String())
	}
	// ...and only THEN may the record be retired — after which it is gone exactly
	// as round-5 made it gone, generation tombstoned.
	collected := listReconcileRows(t, rs, token, "")
	if len(collected.Records) != 1 || !collected.Records[0].OwnerCollected ||
		collected.Records[0].Class != taskReconcileRowTerminalConfirmed || !collected.Records[0].Retirable {
		t.Errorf("row after collection = %+v, want the retirable %q class", collected.Records, taskReconcileRowTerminalConfirmed)
	}
	wr2 := httptest.NewRecorder()
	rs.ServeHTTP(wr2, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr2.Code != http.StatusOK {
		t.Fatalf("retire after collection = %d, want 200; body=%s", wr2.Code, wr2.Body.String())
	}
	if rec, still := rs.taskLedger.lookup("task-handoff"); still {
		t.Errorf("record after a retirement its owner had collected = %+v, want it forgotten", rec)
	}
	if got := rs.taskLedger.admissionState(stored.owner()).OwnerRetained; got != 0 {
		t.Errorf("owner retention after the drain = %d, want 0", got)
	}
	if res := rs.taskLedger.beginCancelAttempt("task-handoff", stored.Generation); res.ok || res.bar != taskCancelBarStale {
		t.Errorf("reservation on the forgotten generation = %+v, want the stale-generation refusal", res)
	}
	// A later read is refused: the record is gone, and the owner already has its
	// result.
	wsecond := httptest.NewRecorder()
	rs.ServeHTTP(wsecond, taskReq(token, methodTasksGet, `{"taskId":"task-handoff"}`))
	if wsecond.Code != http.StatusForbidden {
		t.Errorf("a read after the drain = %d, want 403; body=%s", wsecond.Code, wsecond.Body.String())
	}
}

// TestALaterNonTerminalReportRevokesTheCollectionProof pins the OTHER direction of
// the R6-02 protection under the round-7 design. Round-6 asked whether a retired
// row could be REOPENED by a later upstream answer; with no retired row the
// equivalent question is whether the retirement precondition can go STALE.
//
// It cannot, and ROUND-8 R8-02 extends the question from one shape to three. Round
// 7 recorded the collection as a bare BOOLEAN that only a NON-terminal report
// cleared, so a later, DIFFERENT authoritative TERMINAL report left the proof
// standing and `retire` then deleted a row whose owner had never seen the answer
// that was now authoritative. The proof is bound to the report's canonical DIGEST,
// so all three of these revoke it:
//
//	(B) a different terminal STATUS (`canceled` → `completed`);
//	(D) the SAME terminal status carrying a DIFFERENT result payload — the case no
//	    status comparison can ever see;
//	(E) a confirmed NON-terminal status (the original round-7 scenario).
//
// Deny-closed throughout: SEP-2663 statuses are terminal, so an authoritative
// report that changes after one is a broken or hostile upstream, and the
// conservative reading is the only safe one.
func TestALaterNonTerminalReportRevokesTheCollectionProof(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	// The upstream's CURRENT answer, switched by the phases below.
	report := conformingGetTaskResult("task-reopen", taskStatusCanceled)
	reportWorking := false
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			if reportWorking {
				return json.RawMessage(conformingGetTaskResult("task-reopen", taskStatusWorking)), nil
			}
			return json.RawMessage(report), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-reopen")

	// confirm drives the OPERATOR's authoritative read; collect drives the OWNER's
	// own terminal read and requires the resulting proof to stand.
	confirm := func(phase string) {
		t.Helper()
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: reconciliation status = %d, want 200; body=%s", phase, w.Code, w.Body.String())
		}
	}
	collect := func(phase string) {
		t.Helper()
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"task-reopen"}`))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: the owner's terminal read = %d, want 200; body=%s", phase, w.Code, w.Body.String())
		}
		rec, ok := rs.taskLedger.lookup("task-reopen")
		if !ok || !rec.ownerCollectedCurrentTerminalReport() || !rec.retireReady() {
			t.Fatalf("%s: record after the owner collected = %+v ok=%t, want it collected and retire-ready",
				phase, rec, ok)
		}
	}
	// revoked requires the proof to be GONE and the retirement refused, while the
	// record itself survives.
	revoked := func(phase string) {
		t.Helper()
		rec, still := rs.taskLedger.lookup("task-reopen")
		if !still {
			t.Fatalf("%s: a later report must never remove the record", phase)
		}
		if rec.ownerCollectedCurrentTerminalReport() {
			t.Errorf("%s: R8-02: the collection proof survived a DIFFERENT authoritative terminal report", phase)
		}
		if rec.retireReady() {
			t.Errorf("%s: record = %+v, want it NOT retire-ready", phase, rec)
		}
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
		if w.Code != http.StatusConflict {
			t.Errorf("%s: retire after the authoritative report changed = %d, want 409; body=%s",
				phase, w.Code, w.Body.String())
		}
	}

	// (A) the owner collects exactly the terminal report the operator confirmed.
	confirm("A")
	collect("A")

	// (B) a DIFFERENT terminal status. Round-7 left the boolean set here and retired.
	report = conformingGetTaskResult("task-reopen", taskStatusCompleted)
	confirm("B")
	revoked("B: a different terminal status")

	// (C) the owner collects the NEW authoritative answer...
	collect("C")

	// (D) ...and the SAME terminal status carrying a different payload revokes the
	// proof again. Only the report's identity can see this one.
	report = strings.Replace(conformingGetTaskResult("task-reopen", taskStatusCompleted),
		`"content":[]`, `"content":[{"type":"text","text":"a DIFFERENT final tool result"}]`, 1)
	if report == conformingGetTaskResult("task-reopen", taskStatusCompleted) {
		t.Fatal("the changed-payload fixture did not change the body")
	}
	confirm("D")
	revoked("D: the same terminal status with a changed payload")

	// (E) the original round-7 scenario: after a fresh collection the upstream
	// contradicts its own terminal report with a NON-terminal one.
	collect("E")
	reportWorking = true
	wsr := httptest.NewRecorder()
	rs.ServeHTTP(wsr, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if wsr.Code != http.StatusOK {
		t.Fatalf("second reconciliation status = %d, want 200; body=%s", wsr.Code, wsr.Body.String())
	}
	rec, still := rs.taskLedger.lookup("task-reopen")
	if !still {
		t.Fatal("a non-terminal report must never remove the record")
	}
	if rec.ownerCollectedCurrentTerminalReport() {
		t.Error("ROUND-7: a confirmed NON-terminal status must revoke the proof of delivery")
	}
	if rec.retirable() || rec.retireReady() {
		t.Errorf("record after a contradicting report = %+v, want it NOT retirable", rec)
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Errorf("retire after a contradicting report = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
}

// --- R6-03 -------------------------------------------------------------------

// TestReconcileCursorIsAuthenticated is R6-03's first half. Round-5's cursor was
// bare base64 JSON carrying a SELF-REPORTED instance, so any authorized caller who
// read `instance` out of one list response could hand-build a token for an
// arbitrary position — and an operator's "drain" would then skip whatever prefix
// the forged cursor named.
func TestReconcileCursorIsAuthenticated(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	rs := newTaskEvidenceRS(t, jwks, &taskUpstream{}, &taskAuditor{}, nil, nil, nil)
	for i := 0; i < 3; i++ {
		q := rs.taskLedger.quarantine(TaskRecord{
			TaskID: fmt.Sprintf("task-cur-%04d", i), Tenant: rs.tenant, Issuer: rsIssuer,
			Subject: "agent:claude", Tool: "search", RequiredScope: "tools:read",
			Status: taskStatusWorking, CreatedAt: rs.clock(),
		}, sdk.EvidenceBinding{}, "orphan")
		if !q.retained() {
			t.Fatalf("orphan %d not retained", i)
		}
	}
	// The round-5 token shape: base64 JSON with a self-reported instance, built by
	// a caller that learned the instance from an ordinary list response.
	instance := listReconcileRows(t, rs, token, "").Instance
	if instance == "" {
		t.Fatal("the inventory must name its instance")
	}
	forgedPayload, _ := json.Marshal(map[string]any{
		"i": instance, "s": 1, "t": "task-cur-0000", "g": "g",
	})
	valid, verr := rs.issueReconcileCursor(reconcileCursor{
		Instance: instance, Snapshot: 1, TaskID: "task-cur-0000", Generation: "g",
	})
	if verr != nil {
		t.Fatalf("issue cursor: %v", verr)
	}
	parts := strings.Split(valid, ".")
	if len(parts) != 3 {
		t.Fatalf("cursor shape = %q, want version.payload.mac", valid)
	}
	tamperedPayload, _ := json.Marshal(map[string]any{
		"i": instance, "s": 99, "t": "task-cur-0002", "g": "g",
	})
	extraMember, _ := json.Marshal(map[string]any{
		"i": instance, "s": 1, "t": "task-cur-0000", "g": "g", "x": "surprise",
	})
	for name, cursor := range map[string]string{
		"round-5 unauthenticated token": base64.RawURLEncoding.EncodeToString(forgedPayload),
		"manufactured position":         "v1." + base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[2],
		"stripped mac":                  parts[0] + "." + parts[1],
		"empty mac":                     parts[0] + "." + parts[1] + ".",
		"wrong version":                 "v2." + parts[1] + "." + parts[2],
		"extra payload member":          "v1." + base64.RawURLEncoding.EncodeToString(extraMember) + "." + parts[2],
		"not base64":                    "!!!not-base64!!!",
	} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksReconcileList, `{"cursor":"`+cursor+`"}`))
		if w.Code != http.StatusBadRequest {
			t.Errorf("R6-03 %s: cursor accepted with %d, want 400; body=%s", name, w.Code, w.Body.String())
		}
	}
	// The token this instance itself issued still works.
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileList, `{"cursor":"`+valid+`"}`))
	if w.Code != http.StatusOK {
		t.Errorf("a cursor issued by this instance = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// TestReconcileTraversalIsSnapshotCompleteUnderInsertion is R6-03's second half,
// the reviewer's exact scenario: after page 1, a new row whose (taskId,
// generation) key sorts BEFORE that page's last key is inserted. Round-5 started
// page 2 strictly after the old key, so that row was never returned by any page —
// while the code claimed "no row can be skipped or repeated" and the session file
// repeated the claim. An operator treating end-of-cursor as a completed drain lost
// it silently.
//
// The contract this test pins is the one the code now states: a traversal is a
// SNAPSHOT. Rows that arrive mid-traversal are excluded BY CONSTRUCTION and
// COUNTED (`newerThanSnapshot`), and a fresh traversal returns them. End-of-cursor
// means "this snapshot is fully traversed", never "the ledger is drained".
func TestReconcileTraversalIsSnapshotCompleteUnderInsertion(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	rs := newTaskEvidenceRS(t, jwks, &taskUpstream{}, &taskAuditor{}, nil, nil, nil)
	orphan := func(id string) {
		t.Helper()
		q := rs.taskLedger.quarantine(TaskRecord{
			TaskID: id, Tenant: rs.tenant, Issuer: rsIssuer, Subject: "agent:claude",
			Tool: "search", RequiredScope: "tools:read", Status: taskStatusWorking,
			CreatedAt: rs.clock(),
		}, sdk.EvidenceBinding{}, "orphan")
		if !q.retained() {
			t.Fatalf("orphan %s not retained: %+v", id, q)
		}
	}
	const total = maxReconcileListRecords + 5
	for i := 0; i < total; i++ {
		orphan(fmt.Sprintf("task-snap-%04d", i))
	}

	page1 := listReconcileRows(t, rs, token, "")
	if page1.NextCursor == "" {
		t.Fatal("the inventory must page: it holds more rows than one page")
	}
	if page1.Total != total || page1.NewerThanSnapshot != 0 {
		t.Errorf("page 1 = total %d / newer %d, want %d / 0", page1.Total, page1.NewerThanSnapshot, total)
	}
	// THE mutation, in both directions relative to the cursor position: one row
	// whose key sorts BEFORE page 1's last key (round-5 skipped it silently) and one
	// that sorts AFTER it (round-5's live-state traversal returned it mid-drain,
	// which is why "end of cursor" could not mean anything precise).
	orphan("task-snap-0000-a-late-prefix")
	orphan("task-snap-9999-a-late-suffix")

	seen := map[string]int{}
	for _, row := range page1.Records {
		seen[row.TaskID]++
	}
	cursor := page1.NextCursor
	pages := 1
	newerSeen := 0
	for cursor != "" {
		page := listReconcileRows(t, rs, token, cursor)
		pages++
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		newerSeen = page.NewerThanSnapshot
		for _, row := range page.Records {
			seen[row.TaskID]++
		}
		cursor = page.NextCursor
	}
	// The traversal is COMPLETE for its snapshot: every pre-existing row exactly
	// once, and no repeats.
	if len(seen) != total {
		t.Errorf("R6-03: %d distinct rows across %d pages, want the %d rows of the snapshot", len(seen), pages, total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s returned %d times: the traversal is not a stable total order", id, n)
		}
	}
	for _, late := range []string{"task-snap-0000-a-late-prefix", "task-snap-9999-a-late-suffix"} {
		if seen[late] != 0 {
			t.Errorf("R6-03: %s was inserted after the snapshot and appeared inside that traversal: "+
				"the traversal is a live view, so end-of-cursor states nothing precise", late)
		}
	}
	// ...and both late rows are REPORTED, not silently skipped.
	if newerSeen != 2 {
		t.Errorf("R6-03: newerThanSnapshot on the final page = %d, want 2 — a mid-traversal insertion must be VISIBLE, "+
			"or end-of-cursor reads as a completed drain", newerSeen)
	}
	// A fresh traversal (a new snapshot) returns them, which is the documented
	// restart-until-stable protocol.
	freshSeen := map[string]bool{}
	freshCursor := ""
	for pass := 0; ; pass++ {
		if pass > 10 {
			t.Fatal("the fresh traversal did not terminate")
		}
		fresh := listReconcileRows(t, rs, token, freshCursor)
		if fresh.Total != total+2 || fresh.NewerThanSnapshot != 0 {
			t.Errorf("fresh traversal page %d = total %d / newer %d, want %d / 0",
				pass, fresh.Total, fresh.NewerThanSnapshot, total+2)
		}
		for _, row := range fresh.Records {
			freshSeen[row.TaskID] = true
		}
		if fresh.NextCursor == "" {
			break
		}
		freshCursor = fresh.NextCursor
	}
	for _, late := range []string{"task-snap-0000-a-late-prefix", "task-snap-9999-a-late-suffix"} {
		if !freshSeen[late] {
			t.Errorf("R6-03: %s inserted mid-traversal is not returned by a FRESH traversal either", late)
		}
	}
}
