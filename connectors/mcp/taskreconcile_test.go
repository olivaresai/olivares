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

	"github.com/olivaresai/olivares/sdk"
)

// taskreconcile_test.go — Stage 4, review ROUND-4: the OPERATOR
// RECONCILIATION SURFACE the review made a release blocker.
//
// The reviewer's judgement (b): stage 4 retains cancellation-unconfirmed records
// indefinitely and can create ambiguous cancellation bars, yet
// `reconciliationRecords` and `clearCancelBar` had NO production caller and no
// operator surface at all — a repository-wide search found them only in
// taskledger.go and in tests. A retention rule with no exit is not a design.
// Automatic TTL deletion is explicitly not an acceptable substitute.
//
// These tests pin the four properties the review demanded of that surface:
// authenticated + permission-gated, generation- AND owner-bound, able to (i)
// list, (ii) query authoritative status, (iii) clear/retry a bar and (iv) retire
// a proven-terminal record — with every MUTATING action itself evidence-bound
// like the rest of stage 4.

const reconcileScopes = "tools:read " + scopeTasksReconcile

// reconcileParams renders the generation- and owner-bound params of one action.
func reconcileParams(rec TaskRecord, extra string) string {
	body := `{"taskId":"` + rec.TaskID + `","generation":"` + rec.Generation +
		`","ownerDigest":"` + rec.owner().digest() + `"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// newReconcileRS builds an RS holding ONE cancellation-unconfirmed record: a
// client tasks/cancel was acknowledged, so the task is retained indefinitely,
// carries a DELIVERED bar that suppresses every automatic cancellation, and can
// only leave the ledger through reconciliation.
func newReconcileRS(t *testing.T, jwks []byte, up *taskUpstream, aud GateAuditor, token, taskID string) (*ResourceServer, TaskRecord) {
	t.Helper()
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	stored := mustInsertTask(t, rs, TaskRecord{TaskID: taskID})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"`+taskID+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("seed tasks/cancel status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	return rs, stored
}

// TestTaskReconcileSurfaceIsPermissionGated pins the PERMISSION CONSTANT: the
// reconciliation family is deny-closed behind its own privileged scope, and a
// token that merely governs tasks can never reach it — not even to discover
// whether a record exists.
func TestTaskReconcileSurfaceIsPermissionGated(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	stored := mustInsertTask(t, rs, TaskRecord{TaskID: "task-recon-scope"})

	for _, method := range []string{
		methodTasksReconcileList, methodTasksReconcileStatus,
		methodTasksReconcileClear, methodTasksReconcileRetire,
	} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, method, reconcileParams(stored, "")))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without %q = %d, want 403; body=%s", method, scopeTasksReconcile, w.Code, w.Body.String())
		}
		if ch := w.Header().Get("WWW-Authenticate"); !strings.Contains(ch, scopeTasksReconcile) {
			t.Errorf("%s challenge = %q, want the %q step-up", method, ch, scopeTasksReconcile)
		}
	}
	if got := up.total(); got != 0 {
		t.Errorf("upstream calls from an unauthorized reconciliation attempt = %d, want 0", got)
	}
}

// TestTaskReconcileActionsAreGenerationAndOwnerBound pins the binding the review
// demanded: every mutating action must name the exact record GENERATION and the
// canonical OWNER digest, and both are re-verified against the live record. It is
// what makes a delayed or mistargeted reconciliation impossible (the round-3
// R3-07 class) and what keeps the surface from actuating a REPLACEMENT task that
// merely reuses the same textual identifier.
func TestTaskReconcileActionsAreGenerationAndOwnerBound(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-recon-bind")
	before := up.total()

	for name, tc := range map[string]struct {
		params string
		want   int
	}{
		"wrong generation": {`{"taskId":"task-recon-bind","generation":"deadbeef","ownerDigest":"` +
			stored.owner().digest() + `"}`, http.StatusForbidden},
		"wrong owner digest": {`{"taskId":"task-recon-bind","generation":"` + stored.Generation +
			`","ownerDigest":"not-the-owner"}`, http.StatusForbidden},
		"unknown task": {`{"taskId":"nope","generation":"` + stored.Generation +
			`","ownerDigest":"` + stored.owner().digest() + `"}`, http.StatusForbidden},
		"missing generation": {`{"taskId":"task-recon-bind","ownerDigest":"` +
			stored.owner().digest() + `"}`, http.StatusBadRequest},
		"missing owner digest": {`{"taskId":"task-recon-bind","generation":"` +
			stored.Generation + `"}`, http.StatusBadRequest},
		"case-variant alias of taskId": {`{"TaskId":"task-recon-bind","taskId":"task-recon-bind","generation":"` +
			stored.Generation + `","ownerDigest":"` + stored.owner().digest() + `"}`, http.StatusBadRequest},
		"case-variant alias of generation": {`{"taskId":"task-recon-bind","Generation":"x","generation":"` +
			stored.Generation + `","ownerDigest":"` + stored.owner().digest() + `"}`, http.StatusBadRequest},
		"whitespace-ambiguous generation": {`{"taskId":"task-recon-bind","generation":" ` +
			stored.Generation + `","ownerDigest":"` + stored.owner().digest() + `"}`, http.StatusBadRequest},
	} {
		for _, method := range []string{
			methodTasksReconcileStatus, methodTasksReconcileClear, methodTasksReconcileRetire,
		} {
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, method, tc.params))
			if w.Code != tc.want {
				t.Errorf("%s / %s = %d, want %d; body=%s", method, name, w.Code, tc.want, w.Body.String())
			}
		}
	}
	if got := up.total() - before; got != 0 {
		t.Errorf("upstream calls from mis-targeted reconciliation attempts = %d, want 0", got)
	}
	// The bar is untouched by every refusal above.
	if bar, _, _ := rs.taskLedger.cancelIntentState(stored.Generation); bar != taskCancelBarDelivered {
		t.Errorf("cancellation bar after the refusals = %q, want it INTACT (%q)", bar, taskCancelBarDelivered)
	}
}

// TestTaskReconcileListInventoriesEveryRetainedRecord pins (i): the inventory
// reports every record that needs reconciliation — including the normal governed
// cancellation-unconfirmed record round-3 omitted (R4-04) — with the exact
// generation and owner digest the mutating actions require, and without leaking
// raw principal identifiers.
func TestTaskReconcileListInventoriesEveryRetainedRecord(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-recon-list")
	// ...plus a quarantined orphan and a parked ambiguous duplicate.
	q := rs.taskLedger.quarantine(TaskRecord{
		TaskID: "task-recon-orphan", Tenant: rs.tenant, Issuer: rsIssuer, Subject: "agent:claude",
		Tool: "search", RequiredScope: "tools:read", Status: taskStatusWorking, CreatedAt: rs.clock(),
	}, sdk.EvidenceBinding{}, "compensating cancel never confirmed")
	if !q.retained() {
		t.Fatalf("orphan not retained: %+v", q)
	}

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileList, `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation list status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Result struct {
			Records   []taskReconcileView `json:"records"`
			Truncated bool                `json:"truncated"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode inventory: %v; body=%s", err, w.Body.String())
	}
	byID := map[string]taskReconcileView{}
	for _, v := range env.Result.Records {
		byID[v.TaskID] = v
	}
	unconf, ok := byID["task-recon-list"]
	if !ok {
		t.Fatal("R4-04: the cancellation-unconfirmed record is absent from the reconciliation inventory")
	}
	if unconf.Generation != stored.Generation || unconf.OwnerDigest != stored.owner().digest() {
		t.Errorf("inventory row = %+v, want the exact generation/owner the mutating actions require", unconf)
	}
	if unconf.CancelBar != string(taskCancelBarDelivered) || !unconf.CancelUnconfirmed {
		t.Errorf("inventory row = %+v, want the delivered bar and the unconfirmed cancellation reported", unconf)
	}
	if unconf.Retirable {
		t.Error("a record with no CONFIRMED terminal status must not be reported as retirable")
	}
	if _, ok := byID["task-recon-orphan"]; !ok {
		t.Error("the quarantined orphan is absent from the reconciliation inventory")
	}
	// Minimal data: the owner appears only as its stable digest.
	if strings.Contains(w.Body.String(), "agent:claude") || strings.Contains(w.Body.String(), rsIssuer) {
		t.Errorf("the inventory leaked raw principal identifiers: %s", w.Body.String())
	}
}

// TestTaskReconcileStatusThenRetireDrainsARetainedRecord pins (ii) and (iv), and
// the rule that makes them safe: an operator may NOT assert terminality. Only the
// authoritative upstream read may confirm it, and only a confirmed terminal
// status makes the record retirable.
func TestTaskReconcileStatusThenRetireDrainsARetainedRecord(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	terminal := false
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet && terminal {
			// ROUND-5 R5-01: a conforming GetTaskResult (Result & DetailedTask) — the
			// abbreviated round-4 body is no longer authoritative proof of anything.
			return json.RawMessage(conformingGetTaskResult("task-recon-drain", taskStatusCanceled)), nil
		}
		if req.Method == methodTasksGet {
			return json.RawMessage(conformingGetTaskResult("task-recon-drain", taskStatusWorking)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	aud := &taskAuditor{}
	rs, stored := newReconcileRS(t, jwks, up, aud, token, "task-recon-drain")

	// Retirement is REFUSED while the terminal status is unproven.
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if w.Code != http.StatusConflict {
		t.Errorf("retire without a confirmed terminal status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	if _, ok := rs.taskLedger.lookup("task-recon-drain"); !ok {
		t.Fatal("a refused retirement must never delete the record")
	}

	// An authoritative read that reports the task STILL WORKING confirms nothing
	// terminal, so the record stays retained.
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if rec, _ := rs.taskLedger.lookup("task-recon-drain"); rec.retirable() {
		t.Error("a non-terminal upstream report must not make the record retirable")
	}

	// The upstream now reports the terminal status; the read confirms it and the
	// retirement is accepted.
	terminal = true
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("confirming reconciliation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	rec, ok := rs.taskLedger.lookup("task-recon-drain")
	if !ok || !rec.retirable() {
		t.Fatalf("record after the confirming read = %+v ok=%t, want a CONFIRMED terminal status", rec, ok)
	}
	// ROUND-7: the drain additionally requires PROOF OF DELIVERY. The record is
	// client-readable, so retirement is refused until its owner has actually
	// collected the terminal result — round-6 retired first and kept a bounded
	// handoff cache instead, which could evict an unread result under FIFO pressure
	// (R7-02). The assertions here are STRICTLY STRONGER than round-6's: the
	// uncollected row is refused AND stays counted, and the retirement that follows
	// really removes it.
	wu := httptest.NewRecorder()
	rs.ServeHTTP(wu, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wu.Code != http.StatusConflict {
		t.Fatalf("retire before the owner collected = %d, want 409; body=%s", wu.Code, wu.Body.String())
	}
	if _, still := rs.taskLedger.lookup("task-recon-drain"); !still {
		t.Fatal("a refused retirement must never delete the record")
	}
	wc := httptest.NewRecorder()
	rs.ServeHTTP(wc, taskReq(token, methodTasksGet, `{"taskId":"task-recon-drain"}`))
	if wc.Code != http.StatusOK {
		t.Fatalf("the owner's collecting read = %d, want 200; body=%s", wc.Code, wc.Body.String())
	}
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("retire status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if drained, still := rs.taskLedger.lookup("task-recon-drain"); still {
		t.Fatalf("record after retirement = %+v, want it gone (its owner had already collected the result)", drained)
	}
	if got := rs.taskLedger.admissionState(stored.owner()).OwnerRetained; got != 0 {
		t.Errorf("owner retention after the drain = %d, want 0", got)
	}
	// A retired generation is stale to every AUTOMATIC cancellation: no late sweep,
	// compensation or bar-clearing operation may act on it.
	if res := rs.taskLedger.beginCancelAttempt("task-recon-drain", stored.Generation); res.ok || res.bar != taskCancelBarStale {
		t.Errorf("reservation on a retired generation = %+v, want the stale-generation refusal", res)
	}
	if rs.taskLedger.clearCancelBar("task-recon-drain", stored.Generation) {
		t.Error("clearing the cancellation bar of a RETIRED record must be refused")
	}
	// A second retirement of the same generation is refused. ROUND-7: the row is
	// GONE (retirement deletes it once its owner has collected), so the refusal now
	// comes from target resolution — 403, the surface's uniform "no live record
	// under this taskId/generation/ownerDigest" — rather than from the ledger's
	// idempotence check. Both are deny-closed and neither mutates anything; the
	// assertion is on the refusal, not on which layer produced it.
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusForbidden {
		t.Errorf("retiring an already-retired record = %d, want 403; body=%s", wr.Code, wr.Body.String())
	}
	// Every MUTATING reconciliation action is itself evidence-bound.
	actions := map[string]bool{}
	for _, d := range aud.decisions {
		if d.Allowed && strings.HasPrefix(d.EffectAction, "mcp.task.reconcile.") {
			actions[d.EffectAction] = true
		}
	}
	for _, want := range []string{
		taskActionReconcileStatusPrefix + opIDKindRequestInstance,
		taskActionReconcileRetirePrefix + opIDKindRequestInstance,
	} {
		if !actions[want] {
			t.Errorf("no claim was anchored for %q; claimed reconciliation actions = %v", want, actions)
		}
	}
}

// TestTaskReconcileClearRetriesASuppressedCancellation pins (iii): an ambiguous
// or delivered bar suppresses every automatic cancellation of that generation
// forever, so the surface must be able to re-arm it. Round-3 had `clearCancelBar`
// with no caller at all, which made a barred generation permanently
// uncancellable by any automatic path.
func TestTaskReconcileClearRetriesASuppressedCancellation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-recon-clear")

	// The delivered bar suppresses the sweep: the steady state, not a fault.
	if n, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 1"); n != 0 || err != nil {
		t.Fatalf("suppressed sweep = %d, %v; want 0, nil", n, err)
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Fatalf("upstream cancels after the suppressed sweep = %d, want 1 (the client's own)", got)
	}

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileClear, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation clear status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if bar, _, _ := rs.taskLedger.cancelIntentState(stored.Generation); bar != taskCancelBarNone {
		t.Errorf("bar after reconciliation = %q, want it cleared", bar)
	}
	// The re-armed generation is cancellable again.
	if n, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 2"); n != 1 || err != nil {
		t.Errorf("post-reconciliation sweep = %d, %v; want 1, nil", n, err)
	}
	if got := up.count(methodTasksCancel); got != 2 {
		t.Errorf("upstream cancels after reconciliation = %d, want 2", got)
	}
}

// TestTaskReconcileMutationsAreDenyClosedOnEvidenceRefusal pins the doctrine on
// the new surface: the mutation runs ONLY after a fresh claim has anchored and
// the leadership fence has passed. A refused claim mutates nothing — the operator
// gets the evidence refusal, not a silent local edit nobody can audit.
func TestTaskReconcileMutationsAreDenyClosedOnEvidenceRefusal(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	for name, tc := range map[string]struct {
		method string
		fault  bool
		fence  bool
	}{
		"claim refused": {methodTasksReconcileClear, true, false},
		"fence refused": {methodTasksReconcileClear, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			aud := &taskAuditor{}
			isReconcile := func(action string) bool { return strings.HasPrefix(action, "mcp.task.reconcile.") }
			if tc.fault {
				aud.fakeEvidenceJournal.recordFaultFn = func(d ToolDecision, _ sdk.EvidenceBinding) sdk.EvidenceFault {
					if isReconcile(d.EffectAction) {
						return sdk.EvidenceFaultLedgerUnwired
					}
					return ""
				}
			}
			if tc.fence {
				aud.fakeEvidenceJournal.fenceFaultFn = func(action string, _ GateRecord) sdk.EvidenceFault {
					if isReconcile(action) {
						return sdk.EvidenceFaultLedgerUnavailable
					}
					return ""
				}
			}
			up := &taskUpstream{}
			rs, stored := newReconcileRS(t, jwks, up, aud, token, "task-recon-deny")
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, tc.method, reconcileParams(stored, "")))
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s under a refused claim/fence = %d, want 503; body=%s", tc.method, w.Code, w.Body.String())
			}
			if bar, _, _ := rs.taskLedger.cancelIntentState(stored.Generation); bar != taskCancelBarDelivered {
				t.Errorf("bar after an unanchored reconciliation = %q, want it UNCHANGED (%q)", bar, taskCancelBarDelivered)
			}
		})
	}
}

// TestTaskReconcileStatusNeverForwardsWithoutAnAnchor pins the exploit-matrix row
// on the one reconciliation action that performs an EXTERNAL effect: an
// unanchored authoritative read must never reach the upstream.
func TestTaskReconcileStatusNeverForwardsWithoutAnAnchor(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	// A seed cancel is needed before the auditor turns hostile, so the RS is built
	// with a healthy auditor and the fault is installed afterwards.
	aud := &taskAuditor{}
	rs, stored := newReconcileRS(t, jwks, up, aud, token, "task-recon-unanchored")
	before := up.count(methodTasksGet)
	aud.fakeEvidenceJournal.mu.Lock()
	aud.fakeEvidenceJournal.recordFaultFn = func(d ToolDecision, _ sdk.EvidenceBinding) sdk.EvidenceFault {
		if strings.HasPrefix(d.EffectAction, "mcp.task.reconcile.") {
			return sdk.EvidenceFaultLedgerUnwired
		}
		return ""
	}
	aud.fakeEvidenceJournal.mu.Unlock()

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unanchored reconciliation status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksGet) - before; got != 0 {
		t.Errorf("upstream tasks/get calls from an unanchored reconciliation = %d, want 0 "+
			"(the effect must NEVER precede the anchor)", got)
	}
}

// TestTaskReconcileDrainsASaturatedInventory ties the surface to round-4 R4-05:
// the retention bound is only legitimate because there is a bounded, OPERATOR-
// DRIVEN way OUT of it. A saturated owner is refused new task-producing forwards;
// after the operator reconciles a retained record, admission resumes.
//
// ROUND-5 R5-03, said plainly: that exit is not DURABLE. The ledger is
// process-local and in-memory (see the `taskLedger` type comment), so the drain
// must happen on the instance that holds it, before that instance restarts;
// durable tenant-keyed task persistence is owned by a separate, later work
// item.
func TestTaskReconcileDrainsASaturatedInventory(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	next := 0
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			next++
			return json.RawMessage(fmt.Sprintf(
				`{"resultType":"task","taskId":"task-drain-%d","status":"working"}`, next)), nil
		case methodTasksGet:
			// The authoritative report of whichever task the reconciliation names.
			var p struct {
				TaskID string `json:"taskId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			return json.RawMessage(conformingGetTaskResult(p.TaskID, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)

	// Fill the retention bound: one governed task plus one cap-denied orphan the
	// quarantine path deliberately retains.
	for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	}
	saturated := httptest.NewRecorder()
	rs.ServeHTTP(saturated, toolsCallReq(token, "search", `{}`))
	if saturated.Code != http.StatusTooManyRequests {
		t.Fatalf("saturated tools/call = %d, want 429; body=%s", saturated.Code, saturated.Body.String())
	}
	// The governed task is then canceled cooperatively, which is what makes it a
	// RETAINED, cancellation-unconfirmed record (R4-04): it now needs the operator
	// workflow exactly like the cap-denied orphan does.
	wc := httptest.NewRecorder()
	rs.ServeHTTP(wc, taskReq(token, methodTasksCancel, `{"taskId":"task-drain-1"}`))
	if wc.Code != http.StatusOK {
		t.Fatalf("tasks/cancel status = %d, want 200; body=%s", wc.Code, wc.Body.String())
	}
	before := retainedLedgerSize(rs.taskLedger)

	// Drain EVERY retained record through the operator workflow: an authoritative
	// read confirms the terminal status, and the record then leaves the ledger —
	// a reconciliation artifact is released by the confirmation itself, a normal
	// governed record by an explicit retirement.
	//
	// ROUND-7: a CLIENT-READABLE record additionally needs its owner to have
	// collected the terminal result before it may be retired, so the drain of such a
	// row is the two-party sequence the design intends: the operator proves the task
	// finished, the owner collects its result, the operator removes the row. The
	// refusal in between is asserted, not skipped — it is the property that makes
	// retirement non-destructive. A record that was never client-readable (the
	// cap-denied quarantined orphan below) has no owner delivery to wait for and
	// retires on the operator's proof alone.
	for _, rec := range rs.taskLedger.reconciliationRecords() {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(rec, "")))
		if w.Code != http.StatusOK {
			t.Fatalf("reconciliation status of %s = %d, want 200; body=%s", rec.TaskID, w.Code, w.Body.String())
		}
		current, still := rs.taskLedger.lookup(rec.TaskID)
		if !still {
			// A reconciliation Artifact whose terminal status is now confirmed is
			// released by the confirmation itself — which requires the generation pin
			// to have ended with the dispatch, not to still be held by the read.
			continue
		}
		if current.operable() {
			wu := httptest.NewRecorder()
			rs.ServeHTTP(wu, taskReq(token, methodTasksReconcileRetire, reconcileParams(rec, "")))
			if wu.Code != http.StatusConflict {
				t.Fatalf("retire of the uncollected %s = %d, want 409; body=%s", rec.TaskID, wu.Code, wu.Body.String())
			}
			wg := httptest.NewRecorder()
			rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+rec.TaskID+`"}`))
			if wg.Code != http.StatusOK {
				t.Fatalf("owner collection of %s = %d, want 200; body=%s", rec.TaskID, wg.Code, wg.Body.String())
			}
		}
		w = httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksReconcileRetire, reconcileParams(rec, "")))
		if w.Code != http.StatusOK {
			t.Fatalf("retire of %s = %d, want 200; body=%s", rec.TaskID, w.Code, w.Body.String())
		}
	}
	if after := retainedLedgerSize(rs.taskLedger); after >= before {
		t.Fatalf("bound-consuming records after the drain = %d, want fewer than %d", after, before)
	}
	after := httptest.NewRecorder()
	rs.ServeHTTP(after, toolsCallReq(token, "search", `{}`))
	if after.Code != http.StatusOK {
		t.Errorf("R4-05: tools/call after the operator drain = %d, want 200 — the retention bound must have a bounded operator-driven way OUT; body=%s",
			after.Code, after.Body.String())
	}
}

// TestTaskReconcileStatusReleasesAConfirmedArtifact pins the pin lifetime of the
// authoritative reconciliation read: a RECONCILIATION Artifact (an ungoverned
// orphan whose compensating cancellation was acknowledged) is released by the
// confirmation ITSELF — `syncTaskStatusFromResult` calls `release`, and `release`
// refuses a pinned generation, so a read that still held its pin would leave the
// record alive despite its own proof of termination.
func TestTaskReconcileStatusReleasesAConfirmedArtifact(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(conformingGetTaskResult("task-artifact", taskStatusCanceled)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	q := rs.taskLedger.quarantine(TaskRecord{
		TaskID: "task-artifact", Tenant: rs.tenant, Issuer: rsIssuer, Subject: "agent:claude",
		Tool: "search", RequiredScope: "tools:read", Status: taskStatusWorking, CreatedAt: rs.clock(),
	}, sdk.EvidenceBinding{}, "compensating cancel acknowledged, terminal status unconfirmed")
	if !q.retained() {
		t.Fatalf("orphan not retained: %+v", q)
	}
	// Make it a reconciliation Artifact the way the compensation path does.
	rs.taskLedger.settleCancelAttempt(q.record.TaskID, q.record.Generation, taskCancelSettlement{
		taskCancelBookkeeping: taskCancelBookkeeping{
			status: taskCancelRequestedStatus, reconcileIfQuarantined: true,
		},
		dispatched: true, acked: true,
	})
	rec, ok := rs.taskLedger.lookup("task-artifact")
	if !ok || !rec.Reconciling {
		t.Fatalf("record = %+v ok=%t, want a reconciliation artifact", rec, ok)
	}

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(rec, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if _, still := rs.taskLedger.lookup("task-artifact"); still {
		t.Error("the confirmed-terminal artifact survived its own proof of termination — the read still held the generation pin")
	}
}
