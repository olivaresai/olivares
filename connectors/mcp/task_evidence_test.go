// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// Error-code note: the evidence-gate codes moved from -3201x to -3101x because MCP
// 2026-07-28 puts -32000..-32019 in a LEGACY sub-range new implementations SHOULD NOT
// use, and tells implementations to allocate their own codes OUTSIDE the reserved
// -32768..-32000 range entirely (basic/index.mdx:117-155). Trailing digits preserved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// task_evidence_test.go — Stage 4 RED-first exploit suite for the q1-MCP
// evidence-mandatory task surface (task registration, tasks/get|cancel|update,
// the compensating cancel and the kill-switch sweep). Written and observed
// FAILING against the stage-3 code (task effects still fail-open: zero-binding
// best-effort audits, upstream cancels before any evidence anchor) BEFORE the
// stage-4 enforcement was implemented. The assertions pin the frozen S5
// contract (sdk/evidence.go) on every durable task effect: claim+anchor BEFORE
// the effect, single-use OperationID, refusal wire shapes per design §5, and
// the exploit-matrix row "cancel/compensation anchor failure ⇒ cancel upstream
// not called".

// newTaskEvidenceRS builds the task-surface RS under test with an EXPLICIT
// auditor (nil ⇒ the deny-closed nop default — the unwired-ledger exploits).
func newTaskEvidenceRS(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, gate ApprovalGate, tg TaskGate, med ElicitationMediator) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	if gate == nil {
		gate = fakeToolGate{StatusApproved}
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       gate,
		TaskGate:                   tg,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Upstream:                   up,
		Auditor:                    aud,
		ElicitationMediator:        med,
		Clock:                      rsClock,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// --- exploit A: task-method effect with the ledger unwired ---------------------

// TestTaskCancelEvidenceUnwiredRefusesBeforeForward: with NO auditor wired (the
// deny-closed nop default), an authorized tasks/cancel MUST refuse 503/-31010
// and the upstream cancel MUST NOT be called. Stage-3 fail-open: the cancel
// forwarded with a zero-binding best-effort audit.
func TestTaskCancelEvidenceUnwiredRefusesBeforeForward(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-ev-cancel"})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-ev-cancel"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ledger-unwired tasks/cancel status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if code, _, data, _ := rpcErrorEnvelope(t, w.Body.String()); code != -31010 || data["failure_class"] != "evidence_fault" {
		t.Errorf("ledger-unwired tasks/cancel envelope = code %d data %v, want -31010/evidence_fault", code, data)
	}
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("upstream tasks/cancel calls = %d, want 0 (the effect must NEVER precede the anchor)", got)
	}
}

// TestTaskGetEvidenceUnwiredRefusesBeforeForward: same exploit on tasks/get.
func TestTaskGetEvidenceUnwiredRefusesBeforeForward(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-ev-get"})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"task-ev-get"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ledger-unwired tasks/get status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksGet); got != 0 {
		t.Errorf("upstream tasks/get calls = %d, want 0", got)
	}
}

// --- exploit B: duplicate update — same op-key forwards exactly once -----------

const opKeyedTaskUpdate = `{"taskId":"task-up-1","inputResponses":{},"_meta":{"ai.olivares/operationId":"task-op-1"}}`

func TestTaskUpdateExactReplayForwardsOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-up-1"})

	w1 := httptest.NewRecorder()
	rs.ServeHTTP(w1, taskReq(token, methodTasksUpdate, opKeyedTaskUpdate))
	if w1.Code != http.StatusOK {
		t.Fatalf("first keyed tasks/update status = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, taskReq(token, methodTasksUpdate, opKeyedTaskUpdate))
	if got := up.count(methodTasksUpdate); got != 1 {
		t.Errorf("upstream tasks/update calls across exact replay = %d, want EXACTLY 1", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("exact replay status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	if code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String()); code != -31012 || data["state"] == nil {
		t.Errorf("exact replay envelope = code %d data %v, want -31012 + recorded state", code, data)
	}
}

// TestTaskUpdateConcurrentDuplicateClaimsForwardOnce proves the claim race with
// a REAL barrier (round-1 F-13: the first version had no start barrier and
// never blocked the winning dispatch, so both goroutines could execute serially
// — it proved ordinary replay, not an in-flight claim race). The winner is held
// INSIDE the upstream dispatch until the loser has finished its whole request,
// so the loser necessarily meets a claimed-but-unsettled operation.
func TestTaskUpdateConcurrentDuplicateClaimsForwardOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	up := &taskUpstream{
		gate: func(req UpstreamRequest) {
			if req.Method != methodTasksUpdate {
				return
			}
			first := false
			once.Do(func() { first = true })
			if !first {
				return
			}
			close(inFlight) // the winner is now dispatching
			<-release       // ... and stays there until the loser is done
		},
		fn: func(UpstreamRequest) (json.RawMessage, error) {
			return json.RawMessage(`{"taskId":"task-up-c","status":"working"}`), nil
		},
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-up-c"})
	body := `{"taskId":"task-up-c","inputResponses":{},"_meta":{"ai.olivares/operationId":"task-op-c"}}`

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, body))
	}()
	<-inFlight
	loser := httptest.NewRecorder()
	rs.ServeHTTP(loser, taskReq(token, methodTasksUpdate, body))
	close(release)
	wg.Wait()

	if got := up.count(methodTasksUpdate); got != 1 {
		t.Errorf("concurrent duplicate task updates produced %d upstream dispatches, want EXACTLY 1", got)
	}
	if loser.Code != http.StatusConflict {
		t.Errorf("loser status = %d, want 409 (an in-flight claim is never re-dispatched); body=%s",
			loser.Code, loser.Body.String())
	}
	if code, _, data, _ := rpcErrorEnvelope(t, loser.Body.String()); code != -31012 || data["state"] != "claimed" {
		t.Errorf("loser envelope = code %d data %v, want -31012 with state=claimed", code, data)
	}
}

// --- exploit C: rebind — same op-key, different update params ------------------

func TestTaskUpdateRebindRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-up-1"})
	w1 := httptest.NewRecorder()
	rs.ServeHTTP(w1, taskReq(token, methodTasksUpdate, opKeyedTaskUpdate))
	if w1.Code != http.StatusOK {
		t.Fatalf("first keyed tasks/update status = %d; body=%s", w1.Code, w1.Body.String())
	}
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-up-1","inputResponses":{"answer":{"text":"CHANGED"}},"_meta":{"ai.olivares/operationId":"task-op-1"}}`))
	if got := up.count(methodTasksUpdate); got != 1 {
		t.Errorf("upstream calls after rebind attempt = %d, want 1 (no second effect)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("rebind status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	if code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String()); code != -31011 || data["failure_class"] != "replay" {
		t.Errorf("rebind envelope = code %d data %v, want -31011/replay", code, data)
	}
}

// --- exploit D: taskId case-fold/duplicate smuggling refused pre-claim ---------

// TestTaskMethodCaseAliasSmugglingRefused: the stage-3 P0 class on the task
// surface — taskIDFromParams used a case-INSENSITIVE json.Unmarshal, so
// {"taskId":"tracked","TaskId":"evil"} authorized the tracked record while the
// forwarded bytes carried the alias for a first-wins/other-cased upstream.
// Strict canonicalization must refuse 400/-32602 BEFORE any claim or forward.
func TestTaskMethodCaseAliasSmugglingRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct{ name, params string }{
		{"case-variant alias beside exact key", `{"taskId":"task-cf-1","TaskId":"task-evil"}`},
		{"case-variant alias alone", `{"TASKID":"task-cf-1"}`},
		{"meta case-variant alias", `{"taskId":"task-cf-1","_Meta":{"x":1}}`},
		{"duplicate taskId key", `{"taskId":"task-cf-1","taskId":"task-evil"}`},
		{"duplicate nested key", `{"taskId":"task-cf-1","o":{"x":1,"x":2}}`},
	} {
		for _, method := range []string{methodTasksGet, methodTasksCancel, methodTasksUpdate} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				up := &taskUpstream{}
				rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
				mustInsertTask(t, rs, TaskRecord{TaskID: "task-cf-1"})
				w := httptest.NewRecorder()
				rs.ServeHTTP(w, taskReq(token, method, tc.params))
				if w.Code != http.StatusBadRequest {
					t.Errorf("smuggled params status = %d, want 400; body=%s", w.Code, w.Body.String())
				}
				if got := len(up.calls); got != 0 {
					t.Errorf("upstream calls = %d, want 0 (ambiguous params must never be forwarded)", got)
				}
			})
		}
	}
}

// --- exploit E: settlement failure withholds the task response -----------------

func TestTaskUpdateSettlementFailureWithholdsResponse(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{settleFail: true}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-up-1"})

	w1 := httptest.NewRecorder()
	rs.ServeHTTP(w1, taskReq(token, methodTasksUpdate, opKeyedTaskUpdate))
	if got := up.count(methodTasksUpdate); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the forward happened; the settlement failed)", got)
	}
	if w1.Code != http.StatusServiceUnavailable {
		t.Errorf("unsettled outcome status = %d, want 503 (response withheld); body=%s", w1.Code, w1.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w1.Body.String()); code != -31012 {
		t.Errorf("unsettled outcome code = %d, want -31012", code)
	}
	// The claim is burned: a same-operation retry is a status replay only.
	aud.fakeEvidenceJournal.settleFail = false
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, taskReq(token, methodTasksUpdate, opKeyedTaskUpdate))
	if got := up.count(methodTasksUpdate); got != 1 {
		t.Errorf("upstream calls after retry = %d, want EXACTLY 1 (claimed-never-settled is non-replayable)", got)
	}
	if w2.Code != http.StatusConflict {
		t.Errorf("retry status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
}

// --- exploit F: orphan task after admission denial -----------------------------

// TestTaskAdmissionDenyCompensationAnchorFailureBlocksCancel: the exploit-matrix
// row "cancel/compensation anchor failure ⇒ cancel upstream NOT called", plus
// the two round-1 corrections this test itself needed.
//
//   - F-12: the round-1 version REQUIRED the 503 evidence refusal here. That is
//     the defect: the frozen doctrine makes evidence mandatory for the
//     compensating ALLOW, never for the DENY, so a 402 budget denial must stay a
//     402 whatever the ledger did. The policy result may not vary with ledger
//     availability.
//   - F-03: the round-1 version REQUIRED the denied task to be absent from the
//     ledger. That is the permanent-orphan defect: the upstream task is alive and
//     was not canceled, so it must stay VISIBLE (quarantined) to reconciliation
//     and to every future kill-switch sweep.
func TestTaskAdmissionDenyCompensationAnchorFailureBlocksCancel(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-denied")}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
		// Only the task-correlated claims (compensation cancel) turn hostile; the
		// tools/call parent claim itself anchors.
		recordFaultFn: func(dec ToolDecision, binding sdk.EvidenceBinding) sdk.EvidenceFault {
			if dec.TaskID != "" && binding.Valid() {
				return sdk.EvidenceFaultLedgerUnavailable
			}
			return ""
		},
	}}
	gate := &taskGateFake{dec: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, gate, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("upstream tasks/cancel calls = %d, want 0 (compensation anchor refused ⇒ cancel not called)", got)
	}
	if w.Code != http.StatusPaymentRequired {
		t.Errorf("response status = %d, want 402 (F-12: the POLICY denial stands; it never becomes a ledger fault); body=%s",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "task-denied") {
		t.Errorf("denied handle leaked to the client: %s", w.Body.String())
	}
	// F-03: the live upstream task survives as a quarantine record — never a
	// successfully anchored registration, never forgotten.
	rec, ok := rs.taskLedger.get("task-denied")
	if !ok {
		t.Fatal("F-03: an uncanceled created task must remain VISIBLE for reconciliation, not vanish")
	}
	if !rec.Quarantined || rec.QuarantineReason == "" {
		t.Errorf("orphan record = %+v, want quarantined with a reason", rec)
	}
	if rec.Origin.Valid() {
		t.Errorf("a quarantine record must NOT claim an anchored mcp.task.track origin: %+v", rec.Origin)
	}
	// ... and a later sweep can still see it.
	if got := len(rs.taskLedger.active(nil)); got != 1 {
		t.Errorf("active tasks visible to the sweep = %d, want 1 (the orphan)", got)
	}
	// ... but the orphan is NOT a governance record: retaining it must not hand
	// the caller a usable handle for the task the gate DENIED.
	for _, method := range []string{methodTasksGet, methodTasksCancel, methodTasksUpdate} {
		wq := httptest.NewRecorder()
		rs.ServeHTTP(wq, taskReq(token, method, `{"taskId":"task-denied","inputResponses":{}}`))
		if wq.Code != http.StatusForbidden {
			t.Errorf("%s on a quarantined task = %d, want 403 (a denied task never becomes operable); body=%s",
				method, wq.Code, wq.Body.String())
		}
	}
	if got := up.count(methodTasksGet) + up.count(methodTasksUpdate); got != 0 {
		t.Errorf("upstream task-method forwards on a quarantined task = %d, want 0", got)
	}
}

// TestTaskAdmissionDenyOrphanIsCancellableByALaterSweep closes the F-03 loop:
// once the ledger recovers, the quarantined orphan is exactly what the
// kill-switch sweep discovers and cancels. Under the round-1 code the record did
// not exist, so no sweep could ever reach the live upstream task.
func TestTaskAdmissionDenyOrphanIsCancellableByALaterSweep(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-orphan")}
	hostile := true
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
		recordFaultFn: func(dec ToolDecision, binding sdk.EvidenceBinding) sdk.EvidenceFault {
			if hostile && dec.TaskID != "" && binding.Valid() {
				return sdk.EvidenceFaultLedgerUnavailable
			}
			return ""
		},
	}}
	gate := &taskGateFake{dec: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, gate, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("deny status = %d, want 402; body=%s", w.Code, w.Body.String())
	}
	hostile = false // the evidence ledger recovers

	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop after recovery")
	if err != nil || canceled != 1 {
		t.Fatalf("post-recovery sweep = %d, %v; want 1, nil (the orphan is reachable again)", canceled, err)
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream cancels = %d, want 1", got)
	}
	// Round-2 N-02 strengthening (the round-1 form of this check goes VACUOUS once
	// an acknowledged cancellation stops being terminal): the orphan is RETAINED
	// as a reconciliation artifact in the non-terminal cancel_requested state —
	// never deleted on a bare acknowledgement, never a tracked governance record.
	rec, ok := rs.taskLedger.lookup("task-orphan")
	if !ok {
		t.Fatal("the swept orphan must be retained until its terminal status is confirmed")
	}
	if rec.Status != taskCancelRequestedStatus || taskStatusTerminal(rec.Status) {
		t.Errorf("orphan status after the sweep = %q, want the non-terminal %q", rec.Status, taskCancelRequestedStatus)
	}
	if !rec.Reconciling || !rec.Quarantined {
		t.Errorf("swept orphan = %+v, want retained for reconciliation", rec)
	}
	if _, tracked := rs.taskLedger.get("task-orphan"); tracked {
		t.Error("an ungoverned, denied task must never become a tracked governance record")
	}
}

// TestTaskAdmissionDenyCompensationAnchoredCancelsOnce: the compensation cancel
// with a healthy ledger IS its own claimed+settled operation: the upstream
// cancel runs exactly once and the deny status reaches the client.
func TestTaskAdmissionDenyCompensationAnchoredCancelsOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-denied")}
	aud := &taskAuditor{}
	gate := &taskGateFake{dec: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, gate, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusPaymentRequired {
		t.Errorf("deny status = %d, want 402; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream tasks/cancel calls = %d, want EXACTLY 1", got)
	}
	if _, ok := rs.taskLedger.get("task-denied"); ok {
		t.Error("denied task must not remain registered")
	}
	// TWO distinct operations claimed: the tools/call parent AND the compensating
	// cancel (stage-3 code claimed only the parent — the cancel ran unclaimed).
	if got := aud.fakeEvidenceJournal.opCount(); got != 2 {
		t.Errorf("claimed operations = %d, want 2 (parent tools/call + compensation cancel)", got)
	}
	// The compensation cancel (the most recent claim) settled completed.
	comp := aud.fakeEvidenceJournal.lastBinding()
	if got := aud.fakeEvidenceJournal.settledState(comp.OperationID); got != DispatchCompleted {
		t.Errorf("compensation settled state = %q, want completed", got)
	}
}

// --- exploit G: track anchor failure withholds the task handle -----------------

// TestTrackAnchorFailureWithholdsTaskResult: the mcp.task.track child operation
// (registration of a returned durable handle) is evidence-mandatory: when its
// anchor refuses, the task is NOT registered, the handle is NOT relayed, and
// the compensating cancel is attempted (itself evidence-gated — here it also
// refuses, so the upstream cancel never runs either: deny-closed cascade).
func TestTrackAnchorFailureWithholdsTaskResult(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-track-1")}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
		recordFaultFn: func(dec ToolDecision, binding sdk.EvidenceBinding) sdk.EvidenceFault {
			if dec.TaskID != "" && binding.Valid() {
				return sdk.EvidenceFaultLedgerUnavailable
			}
			return ""
		},
	}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
	// F-03 correction: the round-1 version asserted the task was ABSENT. A
	// refused registration whose compensation also refused leaves a LIVE upstream
	// task; forgetting it is the permanent-orphan bug. It must be present and
	// QUARANTINED — visible, but never presented as a governed registration.
	rec, ok := rs.taskLedger.get("task-track-1")
	if !ok {
		t.Fatal("F-03: a track-refused, uncanceled task must remain visible for reconciliation")
	}
	if !rec.Quarantined {
		t.Errorf("track-refused task = %+v, want quarantined (NOT a governed registration)", rec)
	}
	if strings.Contains(w.Body.String(), "task-track-1") {
		t.Errorf("task handle relayed despite the track anchor refusal: %s", w.Body.String())
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("track-refused status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("upstream tasks/cancel calls = %d, want 0 (the compensation anchor refused too)", got)
	}
}

// TestTrackFenceRefusalQuarantinesInsteadOfRegistering is the F-09 exploit: the
// track claim commits at leader epoch E and leadership is lost BEFORE the
// registration mutation. Every other governed effect calls BeforeEffect
// immediately before acting; the round-1 registration went straight from
// MayEmit to taskLedger.insert, so a STALE node registered the task and the
// stale registration could later drive task methods or a sweep. The fence must
// refuse, the governed registration must not happen, and the live upstream task
// must survive as a quarantine record (never as a governed one).
func TestTrackFenceRefusalQuarantinesInsteadOfRegistering(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	// The tools/call parent already dispatched; only the TRACK fence fails.
	trackFenceOnly := func(action string, _ GateRecord) sdk.EvidenceFault {
		if action == taskActionTrack {
			return sdk.EvidenceFaultLedgerUnavailable
		}
		return ""
	}

	t.Run("the stale node never performs the governed registration", func(t *testing.T) {
		up := &taskUpstream{fn: taskHandleUpstreamFn("task-fence-1")}
		aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{fenceFaultFn: trackFenceOnly}}
		rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("track-fence-refused status = %d, want 503; body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "task-fence-1") {
			t.Errorf("handle relayed despite the refused registration fence: %s", w.Body.String())
		}
		// The compensation's own fence is valid, so the task IS canceled — and
		// only a PROVEN cancellation may release the record.
		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("compensating cancels = %d, want 1", got)
		}
		// Round-2 N-02 strengthening: an ACKNOWLEDGED cancellation is not proof of
		// a terminal one, so the record is retained (quarantined + reconciling,
		// non-terminal) instead of released — and it is never a governed record.
		rec, ok := rs.taskLedger.lookup("task-fence-1")
		if !ok {
			t.Fatal("a fence-refused, merely acknowledged task must stay retained for reconciliation")
		}
		if !rec.Quarantined || rec.Origin.Valid() {
			t.Errorf("record = %+v, want quarantined and WITHOUT an anchored track origin", rec)
		}
		if taskStatusTerminal(rec.Status) {
			t.Errorf("record status = %q, want non-terminal (the ack proves nothing)", rec.Status)
		}
		if _, tracked := rs.taskLedger.get("task-fence-1"); tracked {
			t.Error("a record whose governed registration never ran must not be a tracked governance record")
		}
		// Whatever happened, no mcp.task.track operation may have settled
		// completed: the registration effect never ran under a valid fence.
		if got := aud.fakeEvidenceJournal.settledCount(DispatchCompleted); got != 2 {
			// parent tools/call + compensation cancel; the track claim stays unsettled.
			t.Errorf("settled completed operations = %d, want 2 (parent + compensation, NOT the registration)", got)
		}
	})

	t.Run("with the compensation also refused the orphan stays visible", func(t *testing.T) {
		up := &taskUpstream{fn: taskHandleUpstreamFn("task-fence-2")}
		aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
			fenceFaultFn: func(action string, _ GateRecord) sdk.EvidenceFault {
				if action == taskActionTrack || action == taskActionCompensation {
					return sdk.EvidenceFaultLedgerUnavailable
				}
				return ""
			},
		}}
		rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503; body=%s", w.Code, w.Body.String())
		}
		if got := up.count(methodTasksCancel); got != 0 {
			t.Errorf("upstream cancels = %d, want 0 (the compensation fence refused)", got)
		}
		rec, ok := rs.taskLedger.get("task-fence-2")
		if !ok {
			t.Fatal("the live upstream task must remain visible after a refused registration fence")
		}
		if !rec.Quarantined {
			t.Errorf("record = %+v, want QUARANTINED: the governed mcp.task.track effect never ran under a valid fence", rec)
		}
	})
}

// TestTrackAnchorFailureCompensationAnchoredCancelsOnce: the finer cascade —
// ONLY the track claim refuses (EffectAction-selected), the compensation
// anchors: the upstream cancel runs exactly once, the handle is still withheld
// and the task untracked, and the compensation settles completed.
func TestTrackAnchorFailureCompensationAnchoredCancelsOnce(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-track-2")}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
		recordFaultFn: func(dec ToolDecision, _ sdk.EvidenceBinding) sdk.EvidenceFault {
			if dec.EffectAction == taskActionTrack {
				return sdk.EvidenceFaultLedgerUnavailable
			}
			return ""
		},
	}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("track-refused status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if _, ok := rs.taskLedger.get("task-track-2"); ok {
		t.Error("track-refused task must NOT be registered")
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream tasks/cancel calls = %d, want EXACTLY 1 (anchored compensation)", got)
	}
	comp := aud.fakeEvidenceJournal.lastBinding()
	if got := aud.fakeEvidenceJournal.settledState(comp.OperationID); got != DispatchCompleted {
		t.Errorf("compensation settled state = %q, want completed", got)
	}
}

// countingSettleAuditor wraps fakeEvidenceJournal and fails the Nth Settle
// call (1-based) — the selective settlement-failure knob.
type countingSettleAuditor struct {
	fakeEvidenceJournal
	settleCalls int
	failNth     int
}

func (a *countingSettleAuditor) Settle(ctx context.Context, out GateOutcome) GateSettlement {
	a.settleCalls++
	if a.settleCalls == a.failNth {
		return GateSettlement{FailureClass: sdk.FailureEvidenceFault}
	}
	return a.fakeEvidenceJournal.Settle(ctx, out)
}

// TestTrackSettlementFailureWithholdsHandleButKeepsTracking: the track claim
// anchors and the task registers, but the registration settlement does not
// record — the handle is WITHHELD (503/-31012). The in-memory record
// deliberately stays tracked (the upstream task exists; an untracked live task
// would be invisible to the kill-switch sweep) — the documented stage-4
// posture.
func TestTrackSettlementFailureWithholdsHandleButKeepsTracking(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-track-3")}
	// Settle #1 is the tools/call parent; #2 is the track registration.
	aud := &countingSettleAuditor{failNth: 2}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{"q":"x"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unsettled track status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if code, _, _, _ := rpcErrorEnvelope(t, w.Body.String()); code != -31012 {
		t.Errorf("unsettled track code = %d, want -31012", code)
	}
	if strings.Contains(w.Body.String(), "task-track-3") {
		t.Errorf("handle relayed despite the unsettled registration: %s", w.Body.String())
	}
	if _, ok := rs.taskLedger.get("task-track-3"); !ok {
		t.Error("the record must stay tracked (kill-switch sweep visibility)")
	}
}

// TestTaskCapDenySettlesTrackBlockedAndCompensates: the subject cap refusal
// happens AFTER the track claim — the track operation settles blocked, the
// compensating cancel (class ledger-cap) runs once, and the client gets 429.
func TestTaskCapDenySettlesTrackBlockedAndCompensates(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	next := 0
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			next++
			return json.RawMessage(fmt.Sprintf(`{"resultType":"task","taskId":"task-cap-%d","status":"working"}`, next)), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 1, nil) // cap: 1 active task per subject
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("first task status = %d; body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second task status = %d, want 429; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream cancels = %d, want 1 (the capped handle)", got)
	}
	// Stage-7 B-bis re-verified this word: `blocked` is contractually "the
	// decision stopped the effect before any dispatch", and the cap refusal
	// stops the track REGISTRATION before it ever applies (taskLedger.insert
	// refused atomically — nothing was registered). It stays `blocked`; a
	// `withheld` here would falsely claim the registration effect was produced.
	if got := aud.fakeEvidenceJournal.settledCount(DispatchBlocked); got != 1 {
		t.Errorf("blocked settlements = %d, want 1 (the capped track operation: its effect never applied)", got)
	}
	if got := aud.fakeEvidenceJournal.settledCount(DispatchWithheld); got != 0 {
		t.Errorf("withheld settlements = %d, want 0: no observed effect was withheld on this flow", got)
	}
}

// --- exploit H: kill-switch sweep is evidence-gated per task -------------------

func TestCancelActiveTasksSweepEvidenceGated(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	_ = token

	t.Run("anchored sweep cancels and settles per task", func(t *testing.T) {
		up := &taskUpstream{}
		aud := &taskAuditor{}
		rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-sweep-1"})
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-sweep-2"})
		canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop stop-1")
		if err != nil || canceled != 2 {
			t.Fatalf("CancelActiveTasks = %d, %v; want 2, nil", canceled, err)
		}
		if got := up.count(methodTasksCancel); got != 2 {
			t.Errorf("upstream cancels = %d, want 2", got)
		}
		// One evidence-bound operation PER task, each settled completed.
		if got := aud.fakeEvidenceJournal.opCount(); got != 2 {
			t.Errorf("claimed sweep operations = %d, want 2 (one per task)", got)
		}
		if got := aud.fakeEvidenceJournal.settledCount(DispatchCompleted); got != 2 {
			t.Errorf("settled completed sweep operations = %d, want 2", got)
		}
	})

	t.Run("ledger down: sweep cancels NOTHING upstream and returns the fault", func(t *testing.T) {
		up := &taskUpstream{}
		aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{recordFault: sdk.EvidenceFaultLedgerUnavailable}}
		rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-sweep-3"})
		canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop stop-2")
		if err == nil {
			t.Error("sweep with an unanchorable cancel must return the fault")
		}
		if canceled != 0 {
			t.Errorf("canceled = %d, want 0", canceled)
		}
		if got := up.count(methodTasksCancel); got != 0 {
			t.Errorf("upstream cancels = %d, want 0 (safety-over-liveness: no unanchored emergency cancel)", got)
		}
		if rec, ok := rs.taskLedger.get("task-sweep-3"); !ok || rec.Status != taskStatusWorking {
			t.Errorf("unanchorable task record = %+v ok=%t, want still active (retryable next sweep)", rec, ok)
		}
	})

	t.Run("one unanchorable task is skipped; the others continue", func(t *testing.T) {
		up := &taskUpstream{}
		aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
			recordFaultFn: func(dec ToolDecision, binding sdk.EvidenceBinding) sdk.EvidenceFault {
				if dec.TaskID == "task-sweep-4" {
					return sdk.EvidenceFaultLedgerUnavailable
				}
				return ""
			},
		}}
		rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-sweep-4"})
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-sweep-5"})
		canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop stop-3")
		if err == nil {
			t.Error("sweep must return the first fault of a skipped cancellation")
		}
		if canceled != 1 {
			t.Errorf("canceled = %d, want 1 (the anchorable task)", canceled)
		}
		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("upstream cancels = %d, want 1", got)
		}
		if rec, _ := rs.taskLedger.get("task-sweep-4"); rec.Status != taskStatusWorking {
			t.Errorf("skipped task status = %q, want still working", rec.Status)
		}
		// Round-2 N-02: an acknowledged cooperative cancellation is recorded as the
		// NON-TERMINAL cancel_requested state, never as terminal `canceled`.
		if rec, _ := rs.taskLedger.get("task-sweep-5"); rec.Status != taskCancelRequestedStatus {
			t.Errorf("anchorable task status = %q, want %q", rec.Status, taskCancelRequestedStatus)
		}
	})
}

// --- guard: task-method policy denials never depend on the ledger --------------

// TestTaskMethodPolicyDeniesStandWithoutLedger: policy/authz denials on the
// task surface must keep answering WITHOUT evidence (deny is never blocked by
// the ledger — sdk/pdp.go doctrine). This passed BEFORE stage 4 too; it is the
// regression guard that the inversion did not couple denials to the ledger.
func TestTaskMethodPolicyDeniesStandWithoutLedger(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	t.Run("missing taskId", func(t *testing.T) {
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{}`))
		if w.Code != http.StatusBadRequest {
			t.Errorf("missing-taskId status = %d, want 400", w.Code)
		}
		if got := len(up.calls); got != 0 {
			t.Errorf("upstream calls = %d, want 0", got)
		}
	})

	t.Run("unknown task", func(t *testing.T) {
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"missing"}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("unknown-task status = %d, want 403 (denial must NEVER be blocked by evidence)", w.Code)
		}
	})

	t.Run("foreign subject", func(t *testing.T) {
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-foreign", Subject: "agent:other"})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"task-foreign"}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("foreign-subject status = %d, want 403", w.Code)
		}
	})

	t.Run("update insufficient scope", func(t *testing.T) {
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, nil, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-admin", Tool: "delete_db", RequiredScope: "tools:admin", Destructive: true})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-admin","inputResponses":{}}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("insufficient-scope status = %d, want 403", w.Code)
		}
		if got := len(up.calls); got != 0 {
			t.Errorf("upstream calls = %d, want 0", got)
		}
	})
}

// --- the forwarded task bytes are the canonical governed bytes -----------------

// TestTaskGetForwardsCanonicalBytesAndEvidenceIdentity: the bytes governed are
// the bytes sent (F3 on the task surface): the operation key is STRIPPED,
// keys are recursively sorted, and the upstream request carries the claimed
// evidence identity + fence token.
func TestTaskGetForwardsCanonicalBytesAndEvidenceIdentity(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-cn-1"})
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksGet,
		`{"z":1.0,"taskId":"task-cn-1","_meta":{"traceparent":"00-11111111111111111111111111111111-2222222222222222-01","ai.olivares/operationId":"get-key-1"},"a":{"y":2e3}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("keyed tasks/get status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	fwd, ok := up.last(methodTasksGet)
	if !ok {
		t.Fatal("tasks/get was not forwarded")
	}
	if strings.Contains(string(fwd.Params), "ai.olivares/operationId") {
		t.Errorf("forwarded params leak the operation-key extension: %s", fwd.Params)
	}
	want := `{"_meta":{"traceparent":"00-11111111111111111111111111111111-2222222222222222-01"},"a":{"y":2e3},"taskId":"task-cn-1","z":1.0}`
	if string(fwd.Params) != want {
		t.Errorf("forwarded params are not the canonical governed bytes:\n got %s\nwant %s", fwd.Params, want)
	}
	if fwd.OperationID == "" || fwd.EffectDigest == "" || fwd.FenceToken != "epoch-1" {
		t.Errorf("upstream request lacks the evidence identity/fence: %+v", fwd)
	}
	if got := aud.fakeEvidenceJournal.settledState(fwd.OperationID); got != DispatchCompleted {
		t.Errorf("tasks/get settled state = %q, want completed", got)
	}
}

// --- cross-method rebind: one op-key names ONE operation across methods --------

// TestTaskCrossMethodRebindRefused pins the deliberate design decision that the
// OperationID excludes the method: reusing a tools/call operation key on a task
// method keeps the SAME OperationID with a DIFFERENT (task-domain) EffectDigest
// — a rebind, refused 409/-31011, second effect absent.
func TestTaskCrossMethodRebindRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-xm-1"})
	w1 := httptest.NewRecorder()
	rs.ServeHTTP(w1, customToolsCallReq(token, `{"name":"search","arguments":{},"_meta":{"ai.olivares/operationId":"shared-key-1"}}`))
	if w1.Code != http.StatusOK {
		t.Fatalf("keyed tools/call status = %d; body=%s", w1.Code, w1.Body.String())
	}
	w2 := httptest.NewRecorder()
	rs.ServeHTTP(w2, taskReq(token, methodTasksGet, `{"taskId":"task-xm-1","_meta":{"ai.olivares/operationId":"shared-key-1"}}`))
	if w2.Code != http.StatusConflict {
		t.Errorf("cross-method key reuse status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	if code, _, data, _ := rpcErrorEnvelope(t, w2.Body.String()); code != -31011 || data["failure_class"] != "replay" {
		t.Errorf("cross-method envelope = code %d data %v, want -31011/replay", code, data)
	}
	if got := up.count(methodTasksGet); got != 0 {
		t.Errorf("tasks/get forwards after cross-method rebind = %d, want 0", got)
	}
}

// --- derivation unit pins ------------------------------------------------------

// TestTaskChildBindingDerivations pins the server-initiated child derivations:
// determinism, parent chaining, per-axis digest sensitivity, and the sweep's
// random-per-attempt OperationID with a stable effect identity.
func TestTaskChildBindingDerivations(t *testing.T) {
	parent := sdk.EvidenceBinding{OperationID: "parent-op", EffectDigest: "parent-digest"}
	otherParent := sdk.EvidenceBinding{OperationID: "parent-op-2", EffectDigest: "parent-digest-2"}

	t.Run("track is deterministic and binds the FULL registration record", func(t *testing.T) {
		base := TaskRecord{
			TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Issuer: "https://idp-a",
			Subject: "agent:42", RequiredScope: "tools:read", Status: "working",
		}
		with := func(mut func(*TaskRecord)) TaskRecord {
			r := base
			mut(&r)
			return r
		}
		ttl := int64(60000)
		otherTTL := int64(30000)
		a := deriveTaskTrackBinding("tenant-1", "up-1", parent, base)
		if b := deriveTaskTrackBinding("tenant-1", "up-1", parent, base); a != b {
			t.Error("track binding must be deterministic")
		}
		for name, alt := range map[string]sdk.EvidenceBinding{
			"different parent":   deriveTaskTrackBinding("tenant-1", "up-1", otherParent, base),
			"different task":     deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.TaskID = "task-2" })),
			"different tenant":   deriveTaskTrackBinding("tenant-2", "up-1", parent, base),
			"different tool":     deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.Tool = "other" })),
			"destructive flip":   deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.Destructive = true })),
			"different status":   deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.Status = "input_required" })),
			"different scope":    deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.RequiredScope = "tools:admin" })),
			"different upstream": deriveTaskTrackBinding("tenant-1", "up-2", parent, base),
			// F-11: TTL semantics are effect-changing — one registration expires
			// locally and becomes unsweepable, the other persists.
			"ttl present vs absent": deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.TTLMs = &ttl })),
			// F-06/F-11: the canonical owner is part of the registration identity.
			"different issuer":  deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.Issuer = "https://idp-b" })),
			"different act-as":  deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.ActAs = "user:bob" })),
			"different client":  deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.ClientID = "client-9" })),
			"different subject": deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.Subject = "agent:99" })),
		} {
			if alt.EffectDigest == a.EffectDigest {
				t.Errorf("%s must change the track effect digest", name)
			}
		}
		withTTL := deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.TTLMs = &ttl }))
		withOtherTTL := deriveTaskTrackBinding("tenant-1", "up-1", parent, with(func(r *TaskRecord) { r.TTLMs = &otherTTL }))
		if withTTL.EffectDigest == withOtherTTL.EffectDigest {
			t.Error("a different ttlMs must change the track effect digest")
		}
	})

	t.Run("compensation binds the stable reason class", func(t *testing.T) {
		a := deriveTaskCancelCompensationBinding("tenant-1", parent, "task-1", "gen-1", taskCancelClassAdmissionDenied)
		b := deriveTaskCancelCompensationBinding("tenant-1", parent, "task-1", "gen-1", taskCancelClassAdmissionDenied)
		if a != b {
			t.Error("compensation binding must be deterministic (a duplicate replays, never re-cancels)")
		}
		if c := deriveTaskCancelCompensationBinding("tenant-1", parent, "task-1", "gen-1", taskCancelClassLedgerCap); c.EffectDigest == a.EffectDigest {
			t.Error("the reason class must change the compensation effect digest")
		}
		if c := deriveTaskCancelCompensationBinding("tenant-1", otherParent, "task-1", "gen-1", taskCancelClassAdmissionDenied); c.OperationID == a.OperationID {
			t.Error("a different parent must change the compensation operation id")
		}
		// ROUND-2 N-03: the record GENERATION is part of the compensation identity
		// — a replacement task that reuses the textual id is a DIFFERENT effect and
		// must never inherit the old task's operation.
		gen2 := deriveTaskCancelCompensationBinding("tenant-1", parent, "task-1", "gen-2", taskCancelClassAdmissionDenied)
		if gen2.OperationID == a.OperationID {
			t.Error("a different record generation must change the compensation operation id")
		}
		if gen2.EffectDigest == a.EffectDigest {
			t.Error("a different record generation must change the compensation effect digest")
		}
	})

	// F-01 correction: the round-1 sweep binding minted a RANDOM OperationID per
	// call, so two calls with identical arguments named two different operations
	// — the invariant was evaded, not enforced. The identity is now DETERMINISTIC
	// in the attempt generation the ledger's atomic cancel intent hands out.
	// F-11 correction: the effect identity binds the upstream descriptor and the
	// canonical owner too, so two gateways under one tenant canceling the same
	// textual id against different upstreams/owners are different effects.
	t.Run("sweep identity is deterministic per attempt and fully bound", func(t *testing.T) {
		owner := taskOwner{Tenant: "tenant-1", Issuer: "https://idp-a", Subject: "agent:42"}
		a := deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-1", "gen-1", taskCancelClassKillSwitch, 1)
		again := deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-1", "gen-1", taskCancelClassKillSwitch, 1)
		if a != again {
			t.Error("the same attempt must derive the SAME operation (a re-derivation is a replay, never a second effect)")
		}
		next := deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-1", "gen-1", taskCancelClassKillSwitch, 2)
		if next.OperationID == a.OperationID {
			t.Error("a new attempt generation must mint a new operation id")
		}
		if next.EffectDigest != a.EffectDigest {
			t.Error("the sweep effect identity must be stable across attempts")
		}
		// ROUND-2 N-03: a REPLACEMENT record that reuses the identifier derives a
		// different operation AND a different effect at the SAME attempt number.
		// Without this, after a restart (in-memory ledger gone, durable journal
		// intact) attempt 1 of the replacement replays attempt 1 of the old task
		// and its emergency cancellation is silently refused.
		replacement := deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-1", "gen-2", taskCancelClassKillSwitch, 1)
		if replacement.OperationID == a.OperationID {
			t.Error("a replacement record generation must change the sweep operation id (N-03)")
		}
		if replacement.EffectDigest == a.EffectDigest {
			t.Error("a replacement record generation must change the sweep effect digest (N-03)")
		}
		for name, alt := range map[string]sdk.EvidenceBinding{
			"different upstream": deriveTaskSweepBinding("tenant-1", "up-2", owner, "task-1", "gen-1", taskCancelClassKillSwitch, 1),
			"different owner": deriveTaskSweepBinding("tenant-1", "up-1",
				taskOwner{Tenant: "tenant-1", Issuer: "https://idp-b", Subject: "agent:42"}, "task-1", "gen-1", taskCancelClassKillSwitch, 1),
			"different act-as": deriveTaskSweepBinding("tenant-1", "up-1",
				taskOwner{Tenant: "tenant-1", Issuer: "https://idp-a", Subject: "agent:42", ActAs: "user:bob"},
				"task-1", "gen-1", taskCancelClassKillSwitch, 1),
			"different task":       deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-2", "gen-1", taskCancelClassKillSwitch, 1),
			"different tenant":     deriveTaskSweepBinding("tenant-2", "up-1", owner, "task-1", "gen-1", taskCancelClassKillSwitch, 1),
			"different reason":     deriveTaskSweepBinding("tenant-1", "up-1", owner, "task-1", "gen-1", taskCancelClassToolRevoked, 1),
			"different generation": replacement,
		} {
			if alt.EffectDigest == a.EffectDigest {
				t.Errorf("%s must change the sweep effect digest (F-11: a full binding, not a label)", name)
			}
		}
	})

	// ROUND-2 N-03: the record generation itself is an immutable, unique identity.
	t.Run("record generations are unique per registration instance", func(t *testing.T) {
		rec := TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Issuer: "https://idp-a", Subject: "agent:42"}
		g1 := deriveTaskGeneration(rec, parent, 1)
		if again := deriveTaskGeneration(rec, parent, 1); again != g1 {
			t.Error("the generation derivation must be deterministic in its inputs")
		}
		for name, alt := range map[string]string{
			"a later ledger sequence": deriveTaskGeneration(rec, parent, 2),
			"a different origin":      deriveTaskGeneration(rec, otherParent, 1),
			"a different task id": deriveTaskGeneration(
				TaskRecord{TaskID: "task-2", Tool: "search", Tenant: "tenant-1", Issuer: "https://idp-a", Subject: "agent:42"}, parent, 1),
			"a different owner": deriveTaskGeneration(
				TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Issuer: "https://idp-b", Subject: "agent:42"}, parent, 1),
			"a different tenant": deriveTaskGeneration(
				TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-2", Issuer: "https://idp-a", Subject: "agent:42"}, parent, 1),
		} {
			if alt == g1 {
				t.Errorf("%s must change the record generation", name)
			}
		}
	})

	// F-04: the safety compensation of a tracked task chains from the task's own
	// ANCHORED origin, and the local-origin fallback takes no client input.
	t.Run("task origin prefers the anchored track binding", func(t *testing.T) {
		anchored := sdk.EvidenceBinding{OperationID: "track-op", EffectDigest: "track-digest"}
		rec := TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Subject: "agent:42", Origin: anchored}
		if got := taskOriginBinding("tenant-1", rec); got != anchored {
			t.Errorf("origin = %+v, want the anchored track binding", got)
		}
		bare := TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Subject: "agent:42"}
		local := taskOriginBinding("tenant-1", bare)
		if !local.Valid() {
			t.Fatal("an unanchored record must still derive a valid server-side origin")
		}
		if local == anchored {
			t.Error("the local origin must not collide with an anchored one")
		}
		if again := taskOriginBinding("tenant-1", bare); again != local {
			t.Error("the local origin must be deterministic")
		}
		other := TaskRecord{TaskID: "task-1", Tool: "search", Tenant: "tenant-1", Subject: "agent:99"}
		if taskOriginBinding("tenant-1", other) == local {
			t.Error("a different owner must change the local origin")
		}
	})
}

// taskHandleUpstreamFn answers tools/call with a durable task handle and every
// other method with the NORMATIVE state-free base result (round-4 R4-02:
// `resultType:"complete"` is mandatory on `UpdateTaskResult = Result`; `{}` is
// not a conformant acknowledgement).
func taskHandleUpstreamFn(taskID string) func(UpstreamRequest) (json.RawMessage, error) {
	return func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"` + taskID + `","status":"working"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
}

// --- review round-1 regressions -------------------------------------------

// errUpstreamRPC is the PRODUCTION shape of a strictly valid upstream JSON-RPC
// ERROR: mcpUpstreamForwarder returns {State: completed} together with a non-nil
// error for it (cmd/olivares/mcpgateway.go). Round-1 F-13: the connector fake
// mapped every error to not_sent and therefore could not represent this leg at
// all — the very leg F-02 exploits.
var errUpstreamRPC = &dispatchError{"mcp gateway: upstream rpc -32000 task cannot be canceled"}

// completedErrorCancelUpstream answers tasks/cancel with the production
// completed-with-JSON-RPC-error shape and everything else normally.
func completedErrorCancelUpstream(taskID string) *taskUpstream {
	return &taskUpstream{shaped: func(req UpstreamRequest) (UpstreamResult, error) {
		switch req.Method {
		case methodTasksCancel:
			return UpstreamResult{State: DispatchCompleted}, errUpstreamRPC
		case "tools/call":
			return UpstreamResult{
				Result: json.RawMessage(`{"resultType":"task","taskId":"` + taskID + `","status":"working"}`),
				State:  DispatchCompleted,
			}, nil
		default:
			return UpstreamResult{Result: json.RawMessage(`{}`), State: DispatchCompleted}, nil
		}
	}}
}

// TestSweepCompletedUpstreamErrorIsNotACancellation is F-02: upstream answers
// the sweep's tasks/cancel with a strictly valid HTTP 2xx JSON-RPC error
// ("task cannot be canceled"). The round-1 code checked only
// `state == completed`, so it marked the task locally canceled, counted it as a
// success and REMOVED a live task from every future emergency sweep. "Completed"
// describes an observed round trip, never a successful actuation.
func TestSweepCompletedUpstreamErrorIsNotACancellation(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := completedErrorCancelUpstream("task-ce")
	aud := &taskAuditor{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-ce"})

	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch estate stop ce-1")
	if canceled != 0 {
		t.Errorf("canceled = %d, want 0 (upstream REFUSED the cancellation)", canceled)
	}
	if err == nil {
		t.Error("a refused upstream cancellation must be reported, not swallowed")
	}
	rec, ok := rs.taskLedger.get("task-ce")
	if !ok {
		t.Fatal("the still-live task must remain tracked")
	}
	if taskStatusTerminal(rec.Status) {
		t.Errorf("task status = %q, want a NON-terminal status: upstream said it did not cancel", rec.Status)
	}
	if !taskRecordActive(rec, rsClock()) {
		t.Error("the task must stay visible to reconciliation and to future sweeps")
	}
	// The evidence is still honest: the operation settled `completed` (the round
	// trip happened) — the SUCCESS verdict is what must not be inferred from it.
	if got := aud.fakeEvidenceJournal.settledCount(DispatchCompleted); got != 1 {
		t.Errorf("settled completed operations = %d, want 1 (the round trip IS completed)", got)
	}
}

// TestTaskCancelCompletedUpstreamErrorKeepsTaskAlive is the client-side twin of
// F-02 on tasks/cancel: the client's own cancellation gets a valid upstream
// JSON-RPC error, so the local record must NOT go terminal.
func TestTaskCancelCompletedUpstreamErrorKeepsTaskAlive(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := completedErrorCancelUpstream("task-ce2")
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-ce2"})

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-ce2"}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (the upstream refused the cancellation); body=%s", w.Code, w.Body.String())
	}
	rec, _ := rs.taskLedger.get("task-ce2")
	if taskStatusTerminal(rec.Status) {
		t.Errorf("status = %q, want non-terminal", rec.Status)
	}
}

// TestSweepNeverRepeatsAnAmbiguousCancellation is F-01: the first sweep's
// cancellation settles `unknown` (the request may have landed). The round-1 code
// minted a fresh RANDOM operation on the next tick and dispatched the SAME
// logical cancellation a second time. The frozen law says `unknown` is terminal
// for automatic dispatch — never re-forward.
func TestSweepNeverRepeatsAnAmbiguousCancellation(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{shaped: func(req UpstreamRequest) (UpstreamResult, error) {
		if req.Method == methodTasksCancel {
			return UpstreamResult{State: DispatchUnknown}, errAmbiguousTransport
		}
		return UpstreamResult{Result: json.RawMessage(`{}`), State: DispatchCompleted}, nil
	}}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-amb"})

	if _, err := rs.CancelActiveTasks(context.Background(), nil, "stop-1"); err == nil {
		t.Error("an ambiguous cancellation must be reported")
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Fatalf("first sweep cancels = %d, want 1", got)
	}
	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "stop-2")
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("cancels after the SECOND sweep = %d, want EXACTLY 1 (unknown is terminal for automatic dispatch)", got)
	}
	if canceled != 0 || err == nil {
		t.Errorf("second sweep = %d, %v; want 0 and a reconciliation-required fault", canceled, err)
	}
	// Explicit reconciliation is the ONLY way back — and it names the EXACT
	// generation it reconciles (round-3 R3-07).
	amb, ok := rs.taskLedger.lookup("task-amb")
	if !ok {
		t.Fatal("the ambiguous task must stay visible for reconciliation")
	}
	if rs.taskLedger.clearCancelBar("task-amb", "some-other-generation") {
		t.Error("clearCancelBar must REFUSE a generation the live record does not hold")
	}
	if !rs.taskLedger.clearCancelBar("task-amb", amb.Generation) {
		t.Fatal("clearCancelBar must clear the bar of the generation it names")
	}
	_, _ = rs.CancelActiveTasks(context.Background(), nil, "stop-3")
	if got := up.count(methodTasksCancel); got != 2 {
		t.Errorf("cancels after explicit reconciliation = %d, want 2", got)
	}
}

// TestSweepNeverRepeatsAnUnsettledCancellation is the settlement-loss leg of
// F-01: the cancel was dispatched but its outcome did not record. The claim is
// burned and no automatic re-attempt may follow.
func TestSweepNeverRepeatsAnUnsettledCancellation(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{settleFail: true}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-unsettled"})

	if _, err := rs.CancelActiveTasks(context.Background(), nil, "stop-1"); err == nil {
		t.Error("an unsettled cancellation must be reported")
	}
	aud.fakeEvidenceJournal.mu.Lock()
	aud.fakeEvidenceJournal.settleFail = false
	aud.fakeEvidenceJournal.mu.Unlock()
	if _, err := rs.CancelActiveTasks(context.Background(), nil, "stop-2"); err == nil {
		t.Error("the second sweep must still refuse (the previous outcome never recorded)")
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("cancels across two sweeps = %d, want EXACTLY 1", got)
	}
	if rec, ok := rs.taskLedger.get("task-unsettled"); !ok || !rec.Quarantined {
		t.Errorf("record = %+v ok=%t, want quarantined for reconciliation", rec, ok)
	}
}

// TestConcurrentSweepsCancelOnce is the concurrency leg of F-01: two sweeps run
// at the same time and both snapshot the same active task. Round-1 they minted
// two different random operations, obtained two fresh claims and BOTH
// dispatched. The atomic per-task cancel intent admits exactly one.
// Round-2 F-13: the 20 ms scheduling sleep that used to "give both goroutines
// time" is replaced by a REAL barrier. Sweep A is held INSIDE its claim — it has
// provably reserved the cancellation intent and not yet dispatched — while sweep
// B runs its entire pass to completion on the test goroutine. Only then is A
// released. There is no timing window and no interleaving in which the assertion
// is vacuously true: if B could also reserve and dispatch (the round-1 defect),
// the second dispatch is guaranteed to happen before the assertion runs.
func TestConcurrentSweepsCancelOnce(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	aReserved := make(chan struct{}) // A has reserved the intent and is anchoring
	bDone := make(chan struct{})     // B's entire sweep pass has finished
	var once sync.Once
	base := &taskAuditor{}
	aud := &hookAuditor{taskAuditor: base, onRecord: func(dec ToolDecision) {
		if dec.EffectAction != taskActionSweep {
			return
		}
		// Only the FIRST sweep claim blocks; a second one (the defect) must be
		// free to proceed so the test observes the double dispatch instead of
		// deadlocking.
		first := false
		once.Do(func() { first = true })
		if !first {
			return
		}
		close(aReserved)
		<-bDone
	}}
	up := &taskUpstream{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-race"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = rs.CancelActiveTasks(context.Background(), nil, "concurrent kill-switch stop A")
	}()

	<-aReserved // A is provably in flight
	_, _ = rs.CancelActiveTasks(context.Background(), nil, "concurrent kill-switch stop B")
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("the concurrent sweep dispatched %d cancellations while another attempt was in flight, want 0", got)
	}
	close(bDone)
	wg.Wait()

	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("concurrent sweeps produced %d upstream cancellations, want EXACTLY 1", got)
	}
}

// hookAuditor observes/steers the evidence lifecycle at exact points so the
// round-2 concurrency and TOCTOU exploits need no sleeps: onRecord runs before
// the claim, onBeforeEffect immediately before the leadership fence (and thus
// immediately before the pre-forward revalidation), and onSettle before the
// outcome is recorded.
type hookAuditor struct {
	*taskAuditor
	onRecord       func(ToolDecision)
	onBeforeEffect func(action string)
	onSettle       func(action string)
	// onSettleDone (round-4 R4-01) fires AFTER the outcome has recorded — the last
	// hookable instant of a dispatch helper, still inside the window in which the
	// generation must remain pinned. ADDITIVE: nil in every pre-existing test.
	onSettleDone func(action string)
}

func (a *hookAuditor) Record(ctx context.Context, dec ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	if a.onRecord != nil {
		a.onRecord(dec)
	}
	return a.taskAuditor.Record(ctx, dec, binding)
}

func (a *hookAuditor) BeforeEffect(ctx context.Context, rec GateRecord) sdk.EvidenceReceipt {
	if a.onBeforeEffect != nil {
		a.onBeforeEffect(a.taskAuditor.fakeEvidenceJournal.action(rec.Binding.OperationID))
	}
	return a.taskAuditor.fakeEvidenceJournal.BeforeEffect(ctx, rec)
}

func (a *hookAuditor) Settle(ctx context.Context, out GateOutcome) GateSettlement {
	action := a.taskAuditor.fakeEvidenceJournal.action(out.Record.Binding.OperationID)
	if a.onSettle != nil {
		a.onSettle(action)
	}
	s := a.taskAuditor.fakeEvidenceJournal.Settle(ctx, out)
	if a.onSettleDone != nil {
		a.onSettleDone(action)
	}
	return s
}

// TestDuplicateUpstreamTaskIDQuarantinesAndNeverCancels is F-05: subject A owns
// live task T; a second tools/call returns a colliding handle also named T. The
// round-1 code classified EVERY insert error as a cap denial and issued a
// compensating tasks/cancel(T) — canceling the FIRST, already governed task.
// A collision is not a capacity failure: quarantine and alert, never actuate.
func TestDuplicateUpstreamTaskIDQuarantinesAndNeverCancels(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-dup")}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("first task status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	first, _ := rs.taskLedger.get("task-dup")

	w = httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("colliding handle status = %d, want 502 (an ambiguous upstream identifier); body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("upstream cancels = %d, want 0: canceling the colliding id would cancel the EXISTING task", got)
	}
	kept, ok := rs.taskLedger.get("task-dup")
	if !ok || kept.Quarantined || kept.Origin != first.Origin {
		t.Errorf("the originally governed record was disturbed: %+v (was %+v)", kept, first)
	}
	if kept.Status != first.Status {
		t.Errorf("existing task status = %q, want it untouched (%q)", kept.Status, first.Status)
	}
	if got := len(rs.taskLedger.collisionRecords()); got != 1 {
		t.Errorf("parked collisions = %d, want 1 (quarantined and alerted)", got)
	}
}

// TestTaskOwnershipIsIssuerQualified is F-06: the ownership check compared only
// the bare `sub`, so a token from another trusted issuer, a different delegated
// act-as, or a different OAuth client could drive someone else's task while the
// EffectDigest happily recorded the unauthorized identity as valid evidence.
func TestTaskOwnershipIsIssuerQualified(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name  string
		owner TaskRecord
	}{
		{"another trusted issuer minted the same subject", TaskRecord{TaskID: "task-own", Issuer: "https://other-idp.example"}},
		{"the same agent acting for a different principal", TaskRecord{TaskID: "task-own", ActAs: "user:alice"}},
		{"a different OAuth client", TaskRecord{TaskID: "task-own", ClientID: "client-other"}},
	} {
		for _, method := range []string{methodTasksGet, methodTasksCancel, methodTasksUpdate} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				up := &taskUpstream{}
				rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
				mustInsertTask(t, rs, tc.owner)
				w := httptest.NewRecorder()
				rs.ServeHTTP(w, taskReq(token, method, `{"taskId":"task-own","inputResponses":{}}`))
				if w.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403 (owner tuple mismatch); body=%s", w.Code, w.Body.String())
				}
				if got := up.total(); got != 0 {
					t.Errorf("upstream calls = %d, want 0 (no effect for a non-owner)", got)
				}
			})
		}
	}
}

// TestMRTRInputResponsesCaseAliasRefused is F-07: `InputResponses` and
// `inputResponses` are DISTINCT JSON keys, so strict duplicate detection accepts
// both; canonical ordering puts the upper-case alias FIRST. Round-1 the mediator
// read the pair with a case-insensitive json.Unmarshal and consumed the later
// benign member, while a case-folding first-match upstream would act on the
// earlier malicious one from the exact forwarded bytes. The mediated member is
// now reserved on both governed surfaces.
func TestMRTRInputResponsesCaseAliasRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const smuggled = `{"answer":{"text":"MALICIOUS"}}`

	t.Run("tasks/update", func(t *testing.T) {
		med := &taskMediatorFake{allow: true}
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-alias"})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
			`{"taskId":"task-alias","InputResponses":`+smuggled+`,"inputResponses":{"answer":{"text":"benign"}}}`))
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (ambiguous mediated member); body=%s", w.Code, w.Body.String())
		}
		if got := up.total(); got != 0 {
			t.Errorf("upstream calls = %d, want 0", got)
		}
		if strings.Contains(strings.Join(mediatedContents(med), "|"), "MALICIOUS") {
			t.Error("the smuggled member must never even reach the mediator")
		}
	})

	t.Run("tools/call", func(t *testing.T) {
		med := &taskMediatorFake{allow: true}
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, med)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, customToolsCallReq(token,
			`{"name":"search","arguments":{},"InputResponses":`+smuggled+`,"inputResponses":{"answer":{"text":"benign"}}}`))
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (ambiguous mediated member); body=%s", w.Code, w.Body.String())
		}
		if got := up.total(); got != 0 {
			t.Errorf("upstream calls = %d, want 0", got)
		}
	})

	t.Run("the mediator still inspects the exact-cased member", func(t *testing.T) {
		med := &taskMediatorFake{allow: false, reason: "blocked"}
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-alias-2"})
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
			`{"taskId":"task-alias-2","inputResponses":{"answer":{"text":"secret"}}}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (mediation deny); body=%s", w.Code, w.Body.String())
		}
		if len(med.calls) != 1 || !strings.Contains(strings.Join(mediatedContents(med), "|"), "secret") {
			t.Errorf("mediator did not see the exact-cased payload: %+v", med.calls)
		}
	})
}

func mediatedContents(m *taskMediatorFake) []string {
	out := make([]string, 0, len(m.calls))
	for _, c := range m.calls {
		out = append(out, string(c.Content))
	}
	return out
}

// planRecordingGate records every plan hash it is asked to approve and approves
// the FIRST plan it ever saw — the realistic "a human approved this update"
// adapter: an approval exists for one plan and nothing else.
type planRecordingGate struct {
	approved string
	plans    []string
}

func (g *planRecordingGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	g.plans = append(g.plans, req.PlanHash)
	if g.approved == "" {
		g.approved = req.PlanHash
	}
	if req.PlanHash == g.approved {
		return GateDecision{ApprovalRef: "approval-1", Status: StatusApproved, PlanHash: req.PlanHash}, nil
	}
	return GateDecision{Status: StatusPending, PlanHash: req.PlanHash}, nil
}

// TestDestructiveTaskUpdateApprovalIsPayloadBound is F-08: a human approves ONE
// benign destructive update. Round-1 the plan hash was only
// (taskID, subject, tool), so the caller could then send arbitrary DIFFERENT
// inputResponses under a new operation key: the gate saw the same plan and
// returned the old approval, and the changed update was forwarded even though no
// human ever approved its payload.
func TestDestructiveTaskUpdateApprovalIsPayloadBound(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin tools:read", validExp())
	// Round-3 R3-01 / round-4 R4-02: tasks/update answers with the NORMATIVE
	// SEP-2663 UpdateTaskResult — an acknowledgement carrying no task state but
	// still a `Result`, so `resultType:"complete"` is mandatory on it. The fake
	// previously returned a state-reporting body (round-1) and then a bare `{}`
	// (round-3); neither is a shape the extension permits for this method. The
	// assertions below are unchanged.
	up := &taskUpstream{fn: func(UpstreamRequest) (json.RawMessage, error) {
		return json.RawMessage(normativeCompleteResult), nil
	}}
	gate := &planRecordingGate{}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, gate, nil, nil)
	mustInsertTask(t, rs, TaskRecord{
		TaskID: "task-destr", Tool: "delete_db", RequiredScope: "tools:admin", Destructive: true,
	})

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-destr","inputResponses":{"answer":{"text":"benign"}},"_meta":{"ai.olivares/operationId":"k-1"}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("approved update status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-destr","inputResponses":{"answer":{"text":"DROP EVERYTHING"}},"_meta":{"ai.olivares/operationId":"k-2"}}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("payload-swapped update status = %d, want 403: no human approved THIS payload; body=%s",
			w.Code, w.Body.String())
	}
	if got := up.count(methodTasksUpdate); got != 1 {
		t.Errorf("upstream updates = %d, want EXACTLY 1 (the approved payload)", got)
	}
	if len(gate.plans) != 2 || gate.plans[0] == gate.plans[1] {
		t.Errorf("plan hashes = %v, want two DIFFERENT payload-bound plans", gate.plans)
	}
}

// TestRevokedToolCompensationIgnoresTheClientOperationKey is F-04: the round-1
// revoked-tool branch derived the compensating cancel from the CLIENT's
// unclaimed operation tuple, so it (a) skipped the parent's rebind/replay check
// entirely and (b) made the child identity client-controlled — a fresh key
// produced a fresh child claim and a fresh upstream cancellation of the same
// task. The compensation now chains from the task's own anchored origin, so it
// is deterministic no matter what key the caller supplies.
func TestRevokedToolCompensationIgnoresTheClientOperationKey(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-revoked"})
	denied, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read", Deny: true}})
	if err != nil {
		t.Fatal(err)
	}
	rs.toolset = denied

	for _, key := range []string{"client-key-A", "client-key-B"} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
			`{"taskId":"task-revoked","inputResponses":{},"_meta":{"ai.olivares/operationId":"`+key+`"}}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("key %s: status = %d, want 403 (the POLICY denial stands); body=%s", key, w.Code, w.Body.String())
		}
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream cancels across two client keys = %d, want EXACTLY 1 "+
			"(the compensation identity is the task's anchored origin, never the client's key)", got)
	}
	// Exactly one compensation operation was claimed for this task.
	comps := 0
	for _, id := range aud.fakeEvidenceJournal.claimedOperations() {
		if aud.fakeEvidenceJournal.action(id) == taskActionCompensation {
			comps++
		}
	}
	if comps != 1 {
		t.Errorf("claimed compensation operations = %d, want 1", comps)
	}
}

// TestRevokedToolDenialStandsWithoutLedger is F-12 on the revoked-tool branch:
// the 403 policy denial may not turn into a 503 just because the compensating
// cancel could not be anchored.
func TestRevokedToolDenialStandsWithoutLedger(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	aud := &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{recordFault: sdk.EvidenceFaultLedgerUnavailable}}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-revoked-2"})
	denied, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read", Deny: true}})
	if err != nil {
		t.Fatal(err)
	}
	rs.toolset = denied

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-revoked-2","inputResponses":{}}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (a policy deny NEVER depends on evidence); body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 0 {
		t.Errorf("upstream cancels = %d, want 0 (unanchored compensation is never issued)", got)
	}
	rec, ok := rs.taskLedger.get("task-revoked-2")
	if !ok || !rec.Quarantined {
		t.Errorf("record = %+v ok=%t, want retained and quarantined (the task was NOT canceled)", rec, ok)
	}
	if taskStatusTerminal(rec.Status) {
		t.Errorf("status = %q, want non-terminal: the upstream cancel never ran", rec.Status)
	}
}

// TestStrictTaskHandleValidation is F-10 on the tools/call result: the round-1
// gate accepted a permissively parsed handle, so `taskId: " T "` was bound as
// " T " and stored trimmed as "T" (the registered target was not the target in
// the receipt), and a `status`/`Status` pair passed straight through.
//
// Design adjudication (round 3): the refusal table holds for results the
// EXACT strict-tree discriminator `resultType:"task"` SELECTED into the
// task-handle contract of a request that DECLARED the Tasks capability
// (toolsCallReq does) — validating an already selected closed shape. The former
// `case-alias resultType` row moved to its own subtest below: with NO exact
// discriminator, `ResultType` is an open-Result EXTENSION member
// (schema.ts:208-216,223-235) that never selects the contract, so that result
// relays as extension data and nothing is registered or bound from it.
func TestStrictTaskHandleValidation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name   string
		result string
	}{
		{"whitespace-ambiguous id", `{"resultType":"task","taskId":" task-ws ","status":"working"}`},
		{"case-alias status", `{"resultType":"task","taskId":"task-x","status":"working","Status":"cancelled"}`},
		{"case-alias id", `{"resultType":"task","taskId":"task-x","TaskId":"task-evil","status":"working"}`},
		{"duplicate id member", `{"resultType":"task","taskId":"task-x","taskId":"task-evil","status":"working"}`},
		{"unknown status", `{"resultType":"task","taskId":"task-x","status":"pretend"}`},
		{"malformed ttl", `{"resultType":"task","taskId":"task-x","status":"working","ttlMs":"soon"}`},
		{"negative ttl", `{"resultType":"task","taskId":"task-x","status":"working","ttlMs":-5}`},
		{"empty id", `{"resultType":"task","taskId":"","status":"working"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
				if req.Method == "tools/call" {
					return json.RawMessage(result), nil
				}
				return json.RawMessage(`{}`), nil
			}}
			rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			if w.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502 (an ungovernable handle is never relayed); body=%s", w.Code, w.Body.String())
			}
			if got := len(rs.taskLedger.active(nil)); got != 0 {
				t.Errorf("registered tasks = %d, want 0 (nothing may be bound from an ambiguous handle)", got)
			}
		})
	}

	t.Run("case-alias resultType without the exact discriminator is extension data", func(t *testing.T) {
		// Design adjudication (round-3 finding 2 dissolution): no exact
		// `resultType` member is present, so the task-handle contract is never
		// selected — `ResultType` is extension data on an open Result and the
		// result RELAYS (the old 502 was the adjudicated capability cut). The
		// governance property survives intact: nothing is registered, nothing is
		// bound, and no task method can ever actuate the folded identity.
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(`{"ResultType":"task","taskId":"task-x","status":"working"}`), nil
			}
			return json.RawMessage(`{}`), nil
		}}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (extension data relays per the exact discriminator); body=%s",
				w.Code, w.Body.String())
		}
		if got := len(rs.taskLedger.active(nil)); got != 0 {
			t.Errorf("registered tasks = %d, want 0 (extension data never selects the task contract)", got)
		}
	})

	t.Run("a valid handle still registers with the exact bound identity", func(t *testing.T) {
		up := &taskUpstream{fn: taskHandleUpstreamFn("task-ok")}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if rec, ok := rs.taskLedger.get("task-ok"); !ok || rec.TaskID != "task-ok" {
			t.Errorf("record = %+v ok=%t, want the exact bound id", rec, ok)
		}
	})
}

// TestTaskStatusSyncRefusesAmbiguousResults is the second half of F-10: a
// tasks/get result carrying `status:"working"` and `Status:"canceled"` passed
// the strict JSON-RPC ENVELOPE parser, and the round-1 case-insensitive sync
// consumed the alias — making the local task terminal (and unsweepable) while an
// exact-casing client still saw a live task.
func TestTaskStatusSyncRefusesAmbiguousResults(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name   string
		result string
	}{
		// ROUND-5 R5-01: each body is now CONFORMING except for the one defect under
		// test, so every case still discriminates the rule it was written for (a
		// round-4 body would now be refused for missing Task fields alone, which
		// would make these subtests vacuous rather than stronger).
		{"case-alias status", `{"resultType":"complete","taskId":"task-sync","status":"working","Status":"cancelled",` +
			`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null}`},
		{"foreign task id", `{"resultType":"complete","taskId":"task-other","status":"cancelled",` +
			`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null}`},
		{"unknown status", `{"resultType":"complete","taskId":"task-sync","status":"vanished",` +
			`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
				return json.RawMessage(result), nil
			}}
			rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
			mustInsertTask(t, rs, TaskRecord{TaskID: "task-sync"})
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, methodTasksGet, `{"taskId":"task-sync"}`))
			if w.Code != http.StatusOK {
				t.Fatalf("tasks/get status = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			rec, _ := rs.taskLedger.get("task-sync")
			if rec.Status != taskStatusWorking {
				t.Errorf("local status = %q, want %q untouched (an ambiguous status never mutates the record)",
					rec.Status, taskStatusWorking)
			}
		})
	}
}

// --- review ROUND-2 regressions (N-01 … N-06) -----------------------------

// newTaskEvidenceRSAt is newTaskEvidenceRS with a MOVABLE clock: the round-2
// N-03 exploits need TTL expiry to happen at an exact point of the scenario, not
// after a real-time wait.
func newTaskEvidenceRSAt(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, clock func() time.Time) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "delete_db", RequiredScope: "tools:admin", Destructive: true},
	})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       fakeToolGate{StatusApproved},
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Upstream:                   up,
		Auditor:                    aud,
		Clock:                      clock,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// TestEarlyDuplicateIDPathsNeverCancelTheExistingTask is round-2 N-01 (the
// unclosed half of F-05). Owner A already has live task T. A second tools/call
// returns the SAME upstream identifier T, but the second call fails EARLY — its
// task admission is denied, or its track claim refuses, or its track fence
// refuses. All three branches execute BEFORE the `insert` collision check, so
// round-1 they reached `withholdUngovernedTask` → `compensateCreatedTask`
// unconditionally and sent tasks/cancel(T). A successful acknowledgement then
// called release(T) and DELETED owner A's governance record: wrong-target
// actuation plus erasure of the legitimate record.
//
// The collision/generation check is now ATOMIC and typed, and it runs before any
// compensation on every one of those paths.
func TestEarlyDuplicateIDPathsNeverCancelTheExistingTask(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	for _, tc := range []struct {
		name       string
		wantStatus int
		// arm turns the SECOND tools/call into the early-failure path under test.
		build func(arm *bool) (*taskAuditor, TaskGate)
	}{
		{
			name:       "task-gate admission denial",
			wantStatus: http.StatusPaymentRequired,
			build: func(arm *bool) (*taskAuditor, TaskGate) {
				gate := &taskGateFake{dec: TaskGateDecision{Allow: true}}
				return &taskAuditor{}, &armedTaskGate{gate: gate, arm: arm,
					denied: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}}
			},
		},
		{
			name:       "track claim refusal",
			wantStatus: http.StatusServiceUnavailable,
			build: func(arm *bool) (*taskAuditor, TaskGate) {
				return &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
					recordFaultFn: func(dec ToolDecision, _ sdk.EvidenceBinding) sdk.EvidenceFault {
						if *arm && dec.EffectAction == taskActionTrack {
							return sdk.EvidenceFaultLedgerUnavailable
						}
						return ""
					},
				}}, nil
			},
		},
		{
			name:       "track fence refusal",
			wantStatus: http.StatusServiceUnavailable,
			build: func(arm *bool) (*taskAuditor, TaskGate) {
				return &taskAuditor{fakeEvidenceJournal: fakeEvidenceJournal{
					fenceFaultFn: func(action string, _ GateRecord) sdk.EvidenceFault {
						if *arm && action == taskActionTrack {
							return sdk.EvidenceFaultLedgerUnavailable
						}
						return ""
					},
				}}, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arm := false
			aud, gate := tc.build(&arm)
			up := &taskUpstream{fn: taskHandleUpstreamFn("task-collide")}
			rs := newTaskEvidenceRS(t, jwks, up, aud, nil, gate, nil)

			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			if w.Code != http.StatusOK {
				t.Fatalf("first task status = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			first, ok := rs.taskLedger.get("task-collide")
			if !ok {
				t.Fatal("the first task must be governed")
			}

			arm = true
			w = httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			if w.Code != tc.wantStatus {
				t.Errorf("colliding second call status = %d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
			}

			// THE invariant: no cancellation may ever be aimed at an identifier
			// that already names another governed task.
			if got := up.count(methodTasksCancel); got != 0 {
				t.Errorf("upstream cancels = %d, want 0: canceling the colliding id would cancel owner A's LIVE task", got)
			}
			kept, stillThere := rs.taskLedger.get("task-collide")
			if !stillThere {
				t.Fatal("the existing governance record was ERASED by a compensation aimed at a colliding id")
			}
			if kept.Generation != first.Generation {
				t.Errorf("existing record generation = %q, want it untouched (%q)", kept.Generation, first.Generation)
			}
			if kept.Quarantined || kept.Reconciling || kept.Status != first.Status {
				t.Errorf("existing record was mutated by the collision: %+v (was %+v)", kept, first)
			}
			if got := len(rs.taskLedger.collisionRecords()); got != 1 {
				t.Errorf("parked collisions = %d, want 1 (parked and alerted, never actuated)", got)
			}
		})
	}
}

// armedTaskGate allows until arm flips, then denies — so the SAME gateway can
// govern task T first and hit the admission-denial path on the collision.
type armedTaskGate struct {
	gate   *taskGateFake
	arm    *bool
	denied TaskGateDecision
}

func (g *armedTaskGate) AuthorizeTask(ctx context.Context, intent TaskIntent) (TaskGateDecision, error) {
	if *g.arm {
		return g.denied, nil
	}
	return g.gate.AuthorizeTask(ctx, intent)
}

// TestCompensationAcknowledgementRetainsTheTaskForReconciliation is round-2
// N-02 on the admission/track compensation path: a nil-error acknowledgement of
// tasks/cancel is NOT proof of a terminal cancellation (Client.TaskCancel
// documents cooperative cancellation explicitly), so deleting the quarantined
// record on the acknowledgement lost a possibly-live external task from every
// later sweep.
func TestCompensationAcknowledgementRetainsTheTaskForReconciliation(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-ack")}
	gate := &taskGateFake{dec: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, gate, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("deny status = %d, want 402; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Fatalf("compensating cancels = %d, want 1", got)
	}

	rec, ok := rs.taskLedger.lookup("task-ack")
	if !ok {
		t.Fatal("N-02: an ACKNOWLEDGED cooperative cancellation must not delete the record — the task may still be live")
	}
	if rec.Status != taskCancelRequestedStatus {
		t.Errorf("retained status = %q, want %q", rec.Status, taskCancelRequestedStatus)
	}
	if taskStatusTerminal(rec.Status) {
		t.Errorf("status %q must not be terminal on a bare acknowledgement", rec.Status)
	}
	if got := len(rs.taskLedger.active(nil)); got != 1 {
		t.Errorf("tasks visible to a later sweep = %d, want 1 (the unconfirmed cancellation)", got)
	}
	if got := len(rs.taskLedger.reconciliationRecords()); got != 1 {
		t.Errorf("reconciliation records = %d, want 1", got)
	}
	// It is NOT a tracked task for the client: an admission-denied task never
	// becomes operable, whatever its cancellation state.
	if _, tracked := rs.taskLedger.get("task-ack"); tracked {
		t.Error("a denied, ungoverned task must not be a tracked governance record")
	}
	wq := httptest.NewRecorder()
	rs.ServeHTTP(wq, taskReq(token, methodTasksGet, `{"taskId":"task-ack"}`))
	if wq.Code != http.StatusForbidden {
		t.Errorf("tasks/get on the retained orphan = %d, want 403", wq.Code)
	}

	// A later sweep sees it, must NOT re-emit the delivered cancellation, and
	// does not report it as a fault (the delivered bar is the steady state).
	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch after the ack")
	if canceled != 0 || err != nil {
		t.Errorf("later sweep = %d, %v; want 0, nil (delivered: never re-forwarded, never a fault)", canceled, err)
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream cancels after the later sweep = %d, want EXACTLY 1", got)
	}
}

// TestClientCancelAcknowledgementIsNotTerminalUntilConfirmed is round-2 N-02 on
// handleTaskCancel: the client's own acknowledged tasks/cancel leaves a
// NON-TERMINAL cancel_requested record that stays sweep-visible, bars automatic
// re-dispatch, and only becomes terminal when tasks/get reports a terminal
// status.
func TestClientCancelAcknowledgementIsNotTerminalUntilConfirmed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			// ROUND-5 R5-01: the AUTHORITATIVE confirmation must be a COMPLETE
			// GetTaskResult; the abbreviated round-4 body confirms nothing.
			return json.RawMessage(conformingGetTaskResult("task-coop", taskStatusCanceled)), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-coop"})

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-coop"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tasks/cancel status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	rec, ok := rs.taskLedger.get("task-coop")
	if !ok {
		t.Fatal("an acknowledged cooperative cancellation must keep the task tracked (tasks/get confirms it)")
	}
	if rec.Status != taskCancelRequestedStatus || taskStatusTerminal(rec.Status) {
		t.Errorf("status = %q, want the non-terminal %q", rec.Status, taskCancelRequestedStatus)
	}
	if got := len(rs.taskLedger.active(nil)); got != 1 {
		t.Errorf("sweep-visible tasks = %d, want 1", got)
	}
	// Automatic duplicate dispatch is barred for this generation.
	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch after a client cancel")
	if canceled != 0 || err != nil {
		t.Errorf("sweep after a client cancel = %d, %v; want 0, nil", canceled, err)
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Errorf("upstream cancels = %d, want EXACTLY 1 (the client's own)", got)
	}
	// tasks/get is the authoritative confirmation: only now is it terminal.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"task-coop"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("tasks/get status = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}
	confirmed, _ := rs.taskLedger.get("task-coop")
	if confirmed.Status != taskStatusCanceled || confirmed.TerminalUnconfirmed {
		t.Errorf("confirmed record = %+v, want a CONFIRMED terminal canceled status", confirmed)
	}
	if got := len(rs.taskLedger.active(nil)); got != 0 {
		t.Errorf("sweep-visible tasks after confirmation = %d, want 0", got)
	}
}

// TestStaleCancellationIntentCannotSuppressAReplacementTask is round-2 N-03
// scenario A: task T carries a TTL and is swept once, which bars further
// automatic attempts. After the TTL the record is evicted and the upstream
// re-issues the SAME identifier for a NEW task. Round-1 the cancellation bar was
// keyed by the textual task id and deliberately survived eviction, so the next
// kill-switch stop found the new task, hit the OLD task's bar, and never
// canceled it: emergency cancellation fail-open by identifier reuse.
func TestStaleCancellationIntentCannotSuppressAReplacementTask(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	now := rsClock()
	clock := func() time.Time { return now }
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			return json.RawMessage(`{"resultType":"task","taskId":"task-reused","status":"working","ttlMs":60000}`), nil
		case methodTasksGet:
			// The AUTHORITATIVE confirmation the round-3 R3-02 state model demands
			// before a cancellation-unconfirmed record may ever be retired.
			//
			// ROUND-8 R8-04 (fake-DATA correction): the report REPEATS the handle's own
			// `ttlMs:60000` instead of the shared helper's `null`. The gateway now applies
			// a report's CURRENT TTL, and `null` means "this task never expires" — which
			// would contradict this test's own next step, where the elapsed TTL is what
			// forgets the confirmed record. Every assertion is untouched.
			return json.RawMessage(conformingGetTaskResultTTL("task-reused", taskStatusCanceled, "60000")), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	rs := newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, clock)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("first task status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	first, _ := rs.taskLedger.get("task-reused")

	if canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 1"); canceled != 1 || err != nil {
		t.Fatalf("first sweep = %d, %v; want 1, nil", canceled, err)
	}
	if got := up.count(methodTasksCancel); got != 1 {
		t.Fatalf("cancels after the first sweep = %d, want 1", got)
	}

	// ROUND-3 R3-02 — RE-TARGETED, not deleted. Round-2 this test simply advanced
	// past the TTL and expected the `cancel_requested` record to disappear, which
	// CONTRADICTED the N-02 state model it was written to defend: a task whose
	// cancellation was merely ACKNOWLEDGED may still be running, so letting its TTL
	// forget it loses a live task from every later sweep. Such a record is now
	// immune to TTL eviction, and the generation is retired the only legitimate
	// way — an AUTHORITATIVE tasks/get confirming the terminal status, after which
	// the TTL may forget it. The invariant under test (a stale bar must never
	// suppress a replacement task's emergency cancellation) is unchanged.
	now = now.Add(2 * time.Minute)
	if _, ok := rs.taskLedger.lookup("task-reused"); !ok {
		t.Fatal("R3-02: a cancellation-unconfirmed record must NOT be TTL-evicted before an authoritative confirmation")
	}
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"task-reused"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("confirming tasks/get status = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}
	// The confirmation makes the record terminal-CONFIRMED, and only then may its
	// elapsed TTL forget it.
	if _, ok := rs.taskLedger.lookup("task-reused"); ok {
		t.Fatal("a CONFIRMED terminal record past its TTL must be evicted")
	}
	// ...and the retirement is a real TOMBSTONE: the old generation can never be
	// acted upon again, so its bar cannot be inherited by the replacement below.
	if res := rs.taskLedger.beginCancelAttempt("task-reused", first.Generation); res.ok || res.bar != taskCancelBarStale {
		t.Errorf("retired generation reservation = %+v, want a refusal with the stale-generation bar", res)
	}

	// The upstream re-issues the SAME identifier for a genuinely new task.
	w = httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("replacement task status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	replacement, ok := rs.taskLedger.get("task-reused")
	if !ok {
		t.Fatal("the replacement task must be governed")
	}
	if replacement.Generation == first.Generation {
		t.Fatal("N-03: a replacement registration must never reuse the evicted record's generation")
	}

	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop 2")
	if got := up.count(methodTasksCancel); got != 2 {
		t.Errorf("cancels after the replacement sweep = %d, want 2: the stale bar SUPPRESSED a live task's emergency cancellation", got)
	}
	if canceled != 1 || err != nil {
		t.Errorf("replacement sweep = %d, %v; want 1, nil", canceled, err)
	}
}

// TestPreForwardRevalidationRefusesAStaleOrQuarantinedRecord covers round-2 N-03
// scenario B (cross-owner TOCTOU across expiry) and N-06 (the lookup-to-forward
// quarantine race) with a real barrier: the ledger is mutated at the exact
// instant between the leadership fence and the upstream forward.
func TestPreForwardRevalidationRefusesAStaleOrQuarantinedRecord(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	for _, tc := range []struct {
		name   string
		mutate func(rs *ResourceServer, rec TaskRecord)
	}{
		{
			// N-03 B: the record expires/releases and a DIFFERENT owner registers a
			// replacement under the same identifier. The in-flight request must not
			// apply its update to somebody else's task.
			name: "the record was replaced by another owner",
			mutate: func(rs *ResourceServer, rec TaskRecord) {
				testReleaseUnlessPinned(rs.taskLedger, rec.TaskID, rec.Generation)
				if _, err := rs.taskLedger.insert(TaskRecord{
					TaskID: rec.TaskID, Tool: "search", Tenant: rs.tenant,
					Issuer: "https://other-idp.example", Subject: "agent:other",
					RequiredScope: "tools:read", Status: taskStatusWorking,
				}); err != nil {
					panic(err)
				}
			},
		},
		{
			// N-06: a concurrent path quarantines the record after the one-time
			// operability check has already passed.
			name: "the record was quarantined after the lookup",
			mutate: func(rs *ResourceServer, rec TaskRecord) {
				rs.taskLedger.markQuarantine(rec.TaskID, rec.Generation, "quarantined by a concurrent path")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &taskUpstream{}
			base := &taskAuditor{}
			var rs *ResourceServer
			var target TaskRecord
			var once sync.Once
			aud := &hookAuditor{taskAuditor: base, onBeforeEffect: func(action string) {
				if !strings.HasPrefix(action, taskActionUpdatePrefix) {
					return
				}
				once.Do(func() { tc.mutate(rs, target) })
			}}
			rs = newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
			target = mustInsertTask(t, rs, TaskRecord{TaskID: "task-toctou"})

			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-toctou","inputResponses":{}}`))
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (the authorized record changed before the effect); body=%s", w.Code, w.Body.String())
			}
			if got := up.count(methodTasksUpdate); got != 0 {
				t.Errorf("upstream tasks/update forwards = %d, want 0", got)
			}
			// Nothing was transmitted, so the claim settles blocked — honest and
			// durable, never left dangling. Stage-7 B-bis re-verified this word:
			// the lease refusal runs BEFORE Upstream.Forward, so `blocked` states
			// its narrowed truth ("nothing reached the upstream") and the cancel
			// custodian's retryability inference from it is legitimate here.
			if got := base.fakeEvidenceJournal.settledCount(DispatchBlocked); got != 1 {
				t.Errorf("blocked settlements = %d, want 1 (stopped before the effect)", got)
			}
			if got := base.fakeEvidenceJournal.settledCount(DispatchWithheld); got != 0 {
				t.Errorf("withheld settlements = %d, want 0: nothing was fetched on this flow", got)
			}
		})
	}
}

// TestPendingRegistrationIsNotOperableUntilSettled is round-2 N-06: between the
// ledger insert and the durable settlement of mcp.task.track the registration is
// PENDING. A client that knows or predicts the identifier must not be able to
// actuate it in that window — the window that ends either in an operable record
// or in a quarantine.
func TestPendingRegistrationIsNotOperableUntilSettled(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-pending")}
	base := &taskAuditor{}
	var rs *ResourceServer
	var inWindow int
	var windowCode int
	aud := &hookAuditor{taskAuditor: base, onSettle: func(action string) {
		if action != taskActionTrack || inWindow > 0 {
			return
		}
		inWindow++
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-pending","inputResponses":{}}`))
		windowCode = w.Code
	}}
	rs = newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if inWindow == 0 {
		t.Fatal("the pending-registration window was never exercised")
	}
	if windowCode != http.StatusForbidden {
		t.Errorf("tasks/update inside the pending window = %d, want 403 (the registration is not operable yet)", windowCode)
	}
	if got := up.count(methodTasksUpdate); got != 0 {
		t.Errorf("upstream task updates during the pending window = %d, want 0", got)
	}
	// Once the registration settled durably the record IS operable.
	rec, ok := rs.taskLedger.get("task-pending")
	if !ok || rec.Pending {
		t.Fatalf("record after a durable registration = %+v, want operable", rec)
	}
	wok := httptest.NewRecorder()
	rs.ServeHTTP(wok, taskReq(token, methodTasksUpdate, `{"taskId":"task-pending","inputResponses":{}}`))
	if wok.Code != http.StatusOK {
		t.Errorf("tasks/update after settlement = %d, want 200; body=%s", wok.Code, wok.Body.String())
	}
}

// TestOrderedTaskAliasesCannotBypassStrictRegistration is round-2 N-04, held to
// the design adjudication (round 3): the property that must hold is that a
// folded/ordered alias can NEVER make the gateway BIND, REGISTER or ACTUATE a
// task identity the exact reader did not select. The exact strict-tree
// discriminator here is `resultType:"complete"`, so the task-handle contract is
// never selected; every alias member is open-Result EXTENSION data
// (schema.ts:208-216,223-235) and the result RELAYS as such — nothing is
// registered, and the gateway holds NO task authority for the folded identity
// (tasks/* on it answers 403 before any forward). The pre-adjudication 502 was
// the body-led refusal round 3 dissolved as a capability cut; the declared
// residual is that a NON-CONFORMING case-folding downstream may read the
// extension members differently — it still cannot drive anything through this
// gateway. The unit subtests below keep pinning strictTaskFromResult itself,
// which survives as the non-enforcing diagnostic (the relay path selects via
// selectDeclaredTaskHandle).
func TestOrderedTaskAliasesCannotBypassStrictRegistration(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const ordered = `{"ResultType":"task","TaskId":"task-hidden","resultType":"complete","taskId":"","status":"working"}`
	// ROUND-3 R3-06, the review's exploit VERBATIM: the same ordered-alias smuggle
	// spelled with Unicode simple-fold keys (U+017F long s in the fold orbit of
	// ASCII `s`). It is a valid JSON object with NO duplicate keys, so the outer
	// strict JSON-RPC decoder's duplicate-member protection does not apply.
	const unicodeOrdered = `{"reſultType":"task","taſkId":"task-hidden","resultType":"complete","taskId":"","status":"working"}`

	for name, smuggle := range map[string]string{
		"ascii ordered aliases":       ordered,
		"unicode simple-fold aliases": unicodeOrdered,
	} {
		raw := smuggle
		t.Run("the "+name+" result is never registered nor actuatable", func(t *testing.T) {
			up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
				if req.Method == "tools/call" {
					return json.RawMessage(raw), nil
				}
				return json.RawMessage(`{}`), nil
			}}
			rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (exact resultType is complete; aliases are extension data); body=%s",
					w.Code, w.Body.String())
			}
			if _, ok := rs.taskLedger.lookup("task-hidden"); ok {
				t.Error("nothing may be registered from a result the exact reader did not select as a handle")
			}
			// The folded identity holds NO task authority at this gateway: a
			// case-folding client that extracted it cannot actuate it here.
			wget := httptest.NewRecorder()
			rs.ServeHTTP(wget, taskReq(token, methodTasksGet, `{"taskId":"task-hidden"}`))
			if wget.Code != http.StatusForbidden {
				t.Errorf("tasks/get on the folded identity = %d, want 403 (task not tracked); body=%s",
					wget.Code, wget.Body.String())
			}
			if got := up.count(methodTasksGet); got != 0 {
				t.Errorf("tasks/get forwards for the folded identity = %d, want 0", got)
			}
		})
	}

	t.Run("classification is order-independent and case-folded", func(t *testing.T) {
		for name, raw := range map[string]string{
			"aliases first":              ordered,
			"aliases last":               `{"resultType":"complete","taskId":"","ResultType":"task","TaskId":"task-hidden"}`,
			"upper-cased marker only":    `{"RESULTTYPE":"task","content":[]}`,
			"case-folded task id only":   `{"TASKID":"task-hidden","content":[]}`,
			"mixed-case resultType task": `{"ResultType":"TASK","content":[]}`,
			// ROUND-3 R3-06: Unicode SIMPLE-FOLD aliases. U+017F (long s) is in the
			// fold orbit of ASCII `s`, so `rejectReservedKeyAliases` — which uses
			// strings.EqualFold — recognizes `reſultType`/`taſkId` as aliases. The
			// round-2 marker classifiers lowercased the key and compared it to an
			// ASCII literal, and strings.ToLower("ſ") is still "ſ": no marker, so the
			// alias error was DISCARDED and the result relayed as synchronous.
			// Both spellings are exercised — the JSON \u escape and the raw UTF-8 key
			// — because the two decoders must agree on either.
			"unicode simple-fold resultType (escaped key)": `{"re\u017fultType":"task","content":[]}`,
			"unicode simple-fold taskId (escaped key)":     `{"ta\u017fkId":"task-hidden","content":[]}`,
			"unicode simple-fold resultType (raw key)":     `{"reſultType":"task","content":[]}`,
			"unicode simple-fold taskId (raw key)":         `{"taſkId":"task-hidden","content":[]}`,
			"unicode fold aliases FIRST":                   unicodeOrdered,
			"unicode fold aliases LAST":                    `{"resultType":"complete","taskId":"","status":"working","reſultType":"task","taſkId":"task-hidden"}`,
			"unicode fold marker with folded value":        `{"reſultType":"TASK","content":[]}`,
		} {
			if _, ok, err := strictTaskFromResult(json.RawMessage(raw)); err == nil || ok {
				t.Errorf("%s: strictTaskFromResult = ok %t err %v, want a FATAL ambiguity refusal", name, ok, err)
			}
		}
		// A genuine synchronous tool result is still an ordinary non-task result.
		if _, ok, err := strictTaskFromResult(json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)); ok || err != nil {
			t.Errorf("a plain tool result = ok %t err %v, want (not a task, no error)", ok, err)
		}
		// ... and a strictly valid handle still parses.
		task, ok, err := strictTaskFromResult(json.RawMessage(`{"resultType":"task","taskId":"t-1","status":"working"}`))
		if !ok || err != nil || task.TaskID != "t-1" {
			t.Errorf("valid handle = %+v ok %t err %v", task, ok, err)
		}
		// The widened predicate must not become a wildcard: a member that merely
		// STARTS like a marker is not one.
		for name, raw := range map[string]string{
			"resultTypeX": `{"resultTypeX":"task","content":[]}`,
			"xTaskId":     `{"xTaskId":"task-hidden","content":[]}`,
		} {
			if _, ok, err := strictTaskFromResult(json.RawMessage(raw)); ok || err != nil {
				t.Errorf("%s: ok %t err %v, want (not a task, no error) — the marker predicate must not over-match", name, ok, err)
			}
		}
	})

	t.Run("marker classification and reserved-alias rejection share ONE predicate", func(t *testing.T) {
		// ROUND-3 R3-06 at the predicate level: for every reserved task-result
		// member, anything the ALIAS rejector treats as a case-variant of it must
		// also be a MARKER key — otherwise the alias error is discarded and the
		// smuggle is relayed. `ſ` (U+017F) is the discriminating case.
		for _, tc := range []struct{ key, reserved string }{
			{"reſultType", "resultType"},
			{"taſkId", "taskId"},
			{"ReSuLtTyPe", "resultType"},
			{"TASKID", "taskId"},
		} {
			if !keyFoldsTo(tc.key, tc.reserved) {
				t.Errorf("keyFoldsTo(%q, %q) = false, want true (the alias rejector's own predicate)", tc.key, tc.reserved)
			}
			if got := taskMarkerKeyKind(tc.key); got != tc.reserved {
				t.Errorf("taskMarkerKeyKind(%q) = %q, want %q: a key the alias rejector calls an alias must be a MARKER",
					tc.key, got, tc.reserved)
			}
		}
	})
}

// TestTaskTTLBoundaryRefusesOverflow is round-2 N-05: strictOptionalPositiveInt
// accepted EVERY positive int64, and taskExpired then multiplied it by
// time.Millisecond — a signed 64-bit multiplication that wraps NEGATIVE. A task
// that registered, bound its evidence and relayed its handle was evicted as
// "already expired" on its very next read: a fully governed task turned into a
// permanent, unsweepable orphan by one accepted result field.
func TestTaskTTLBoundaryRefusesOverflow(t *testing.T) {
	handle := func(ttl string) json.RawMessage {
		return json.RawMessage(`{"resultType":"task","taskId":"task-ttl","status":"working","ttlMs":` + ttl + `}`)
	}

	t.Run("the exact boundary is accepted and does not wrap", func(t *testing.T) {
		task, ok, err := strictTaskFromResult(handle(strconv.FormatInt(maxTaskDurationMillis, 10)))
		if !ok || err != nil {
			t.Fatalf("max representable ttlMs = ok %t err %v, want accepted", ok, err)
		}
		if task.TTLMs == nil || *task.TTLMs != maxTaskDurationMillis {
			t.Fatalf("ttl = %v, want %d", task.TTLMs, maxTaskDurationMillis)
		}
		rec := TaskRecord{TaskID: "task-ttl", CreatedAt: rsClock(), TTLMs: task.TTLMs}
		if taskExpired(rec, rsClock().Add(time.Hour)) {
			t.Error("a max-representable TTL must NOT be expired one hour after creation (the multiplication wrapped)")
		}
	})

	t.Run("one millisecond past the boundary is refused", func(t *testing.T) {
		for name, ttl := range map[string]string{
			"boundary+1": strconv.FormatInt(maxTaskDurationMillis+1, 10),
			"int64 max":  strconv.FormatInt(math.MaxInt64, 10),
		} {
			if _, ok, err := strictTaskFromResult(handle(ttl)); err == nil || ok {
				t.Errorf("%s: ok %t err %v, want a refusal", name, ok, err)
			}
		}
	})

	t.Run("an overflowing handle is never registered nor relayed", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return handle(strconv.FormatInt(math.MaxInt64, 10)), nil
			}
			return json.RawMessage(`{}`), nil
		}}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		if w.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502; body=%s", w.Code, w.Body.String())
		}
		if _, ok := rs.taskLedger.lookup("task-ttl"); ok {
			t.Error("a handle whose TTL cannot be represented must never be registered")
		}
	})

	t.Run("a defensively injected overflowing TTL never evicts a live task", func(t *testing.T) {
		overflow := int64(math.MaxInt64)
		rec := TaskRecord{TaskID: "task-ttl", CreatedAt: rsClock(), TTLMs: &overflow}
		if taskExpired(rec, rsClock().Add(time.Hour)) {
			t.Error("an unrepresentable TTL must be treated as unbounded, never as already expired")
		}
	})
}

// --- review ROUND-3 regressions (R3-01 … R3-07) ---------------------------

// TestInFlightGenerationLeaseHoldsAcrossTheExternalEffect is round-3 R3-03, the
// substrate finding. Round-2 revalidated the record generation immediately
// before the forward and then RELEASED the ledger mutex: a check-then-use race.
// Between that check and the transport write another goroutine could cross the
// record's TTL, evict it and register a DIFFERENT owner's replacement under the
// same textual identifier — and the forward carries the upstream's TEXTUAL task
// id, not the gateway's generation, so the already-admitted update/cancel landed
// on the replacement. Later compare-and-swap bookkeeping can refuse to mutate the
// replacement; it cannot undo the wrong-target upstream effect.
//
// The mutation therefore runs INSIDE the fake upstream — after the pre-forward
// check, before the transport write, exactly where the review says the round-2
// test could not reach — and the generation LEASE must make it impossible:
// while an effect is in flight the record can neither be TTL-evicted nor
// released, so the identifier cannot change owner underneath it.
func TestInFlightGenerationLeaseHoldsAcrossTheExternalEffect(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	type window struct {
		ran        bool
		found      bool
		foundGen   string
		releasedOK bool
		insertErr  error
	}

	// mutate is the post-revalidation replacement attempt.
	mutate := func(rs *ResourceServer, target TaskRecord, advance func(), w *window) {
		w.ran = true
		advance() // cross the leased record's TTL
		rec, ok := rs.taskLedger.lookup(target.TaskID)
		w.found, w.foundGen = ok, rec.Generation
		w.releasedOK = testReleaseUnlessPinned(rs.taskLedger, target.TaskID, target.Generation)
		_, w.insertErr = rs.taskLedger.insert(TaskRecord{
			TaskID: target.TaskID, Tool: "search", Tenant: rs.tenant,
			Issuer: "https://other-idp.example", Subject: "agent:other",
			RequiredScope: "tools:read", Status: taskStatusWorking,
		})
	}

	check := func(t *testing.T, w window, target TaskRecord) {
		t.Helper()
		if !w.ran {
			t.Fatal("the post-revalidation window was never exercised")
		}
		if !w.found || w.foundGen != target.Generation {
			t.Errorf("mid-flight the ledger held generation %q (found=%t), want the LEASED %q: a mid-flight TTL eviction frees the identifier for a replacement",
				w.foundGen, w.found, target.Generation)
		}
		if w.releasedOK {
			t.Error("release() succeeded on a generation whose governed effect was still in flight")
		}
		if !errors.Is(w.insertErr, errTaskDuplicateID) {
			t.Errorf("replacement insert mid-flight = %v, want errTaskDuplicateID (the leased identifier stays taken)", w.insertErr)
		}
	}

	t.Run("a client task method pins its generation across the transport write", func(t *testing.T) {
		ttl := int64(60000)
		now := rsClock()
		var rs *ResourceServer
		var target TaskRecord
		var w window
		var once sync.Once
		up := &taskUpstream{}
		up.gate = func(req UpstreamRequest) {
			if req.Method != methodTasksUpdate {
				return
			}
			once.Do(func() { mutate(rs, target, func() { now = now.Add(2 * time.Minute) }, &w) })
		}
		rs = newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, func() time.Time { return now })
		target = mustInsertTask(t, rs, TaskRecord{TaskID: "task-lease-client", TTLMs: &ttl})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-lease-client","inputResponses":{}}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("tasks/update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		check(t, w, target)
		if got := up.count(methodTasksUpdate); got != 1 {
			t.Errorf("upstream tasks/update forwards = %d, want 1", got)
		}
		// The replacement never entered the ledger, so no later operation can be
		// attributed to an owner that never held this identifier.
		for _, rec := range rs.taskLedger.active(nil) {
			if rec.Subject == "agent:other" {
				t.Errorf("a replacement owner took the leased identifier: %+v", rec)
			}
		}
	})

	t.Run("a server-initiated cancellation pins its generation across the transport write", func(t *testing.T) {
		ttl := int64(60000)
		now := rsClock()
		var rs *ResourceServer
		var target TaskRecord
		var w window
		var once sync.Once
		up := &taskUpstream{}
		up.gate = func(req UpstreamRequest) {
			if req.Method != methodTasksCancel {
				return
			}
			once.Do(func() { mutate(rs, target, func() { now = now.Add(2 * time.Minute) }, &w) })
		}
		rs = newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, func() time.Time { return now })
		target = mustInsertTask(t, rs, TaskRecord{TaskID: "task-lease-sweep", TTLMs: &ttl})

		if _, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop"); err != nil {
			t.Fatalf("sweep = %v, want nil", err)
		}
		check(t, w, target)
		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("upstream cancels = %d, want 1", got)
		}
	})
}

// TestTaskUpdateResultIsAckOnlyAndNeverConfirmsStatus is round-3 R3-01. SEP-2663
// defines UpdateTaskResult as an empty, eventually-consistent ACKNOWLEDGEMENT and
// directs clients to observe status through tasks/get or task notifications, so
// an update result is never authoritative about task state. Round-2 nevertheless
// fed it to syncTaskStatusFromResult — which calls confirmStatus — so a broken or
// hostile upstream could answer a tasks/update with `{"status":"canceled"}` and
// CONFIRM a terminal status for a task nobody read and nobody canceled, removing
// a live task from active() and from every later kill-switch sweep.
func TestTaskUpdateResultIsAckOnlyAndNeverConfirmsStatus(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	t.Run("a status-bearing update result can neither mutate nor retire the record", func(t *testing.T) {
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksUpdate {
				return json.RawMessage(`{"resultType":"complete","taskId":"task-ack-only","status":"cancelled"}`), nil
			}
			return json.RawMessage(`{}`), nil
		}}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		before := mustInsertTask(t, rs, TaskRecord{TaskID: "task-ack-only"})

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-ack-only","inputResponses":{}}`))
		if w.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502 (an ack-only violation is not relayed); body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "cancelled") {
			t.Errorf("the unauthoritative status reached the client: %s", w.Body.String())
		}
		after, ok := rs.taskLedger.get("task-ack-only")
		if !ok {
			t.Fatal("an ack-only violation must never RETIRE the local governance record")
		}
		if after.Status != before.Status || after.TerminalUnconfirmed {
			t.Errorf("record after the status-bearing ack = %+v, want the status untouched (%q)", after, before.Status)
		}
		if got := len(rs.taskLedger.active(nil)); got != 1 {
			t.Errorf("tasks visible to a later sweep = %d, want 1 (only tasks/get may retire a task)", got)
		}
		// A later sweep still cancels it: it was never hidden.
		if canceled, err := rs.CancelActiveTasks(context.Background(), nil, "kill-switch"); canceled != 1 || err != nil {
			t.Errorf("later sweep = %d, %v; want 1, nil", canceled, err)
		}
	})

	t.Run("only tasks/get confirms a status", func(t *testing.T) {
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			switch req.Method {
			case methodTasksUpdate:
				return json.RawMessage(normativeCompleteResult), nil
			case methodTasksGet:
				return json.RawMessage(conformingGetTaskResult("task-ack-get", taskStatusCompleted)), nil
			}
			return json.RawMessage(`{}`), nil
		}}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-ack-get"})

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-ack-get","inputResponses":{}}`))
		if w.Code != http.StatusOK {
			t.Fatalf("ack-only tasks/update status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		if rec, _ := rs.taskLedger.get("task-ack-get"); rec.Status != taskStatusWorking {
			t.Errorf("status after an ack = %q, want %q untouched", rec.Status, taskStatusWorking)
		}
		wg := httptest.NewRecorder()
		rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"task-ack-get"}`))
		if wg.Code != http.StatusOK {
			t.Fatalf("tasks/get status = %d, want 200; body=%s", wg.Code, wg.Body.String())
		}
		rec, _ := rs.taskLedger.get("task-ack-get")
		if rec.Status != taskStatusCompleted || rec.TerminalUnconfirmed {
			t.Errorf("record after the authoritative read = %+v, want a CONFIRMED completed status", rec)
		}
	})

	// ROUND-4 R4-02: the round-3 version of this test pinned the WRONG normative
	// shape — it accepted `{}`/absence and REJECTED `resultType`, which is the
	// mandatory discriminator SEP-2663 puts on `UpdateTaskResult = Result`. A
	// conformant upstream was answered 502. The contract is now pinned in both
	// directions: the normative success is accepted, and every state-reporting,
	// aliased, duplicated or discriminator-less body is refused.
	t.Run("strictTaskUpdateAck accepts the NORMATIVE ack and refuses every state report", func(t *testing.T) {
		for name, raw := range map[string]string{
			"normative ack":         `{"resultType":"complete"}`,
			"normative ack + _meta": `{"resultType":"complete","_meta":{"trace":"x"}}`,
			"member order":          `{"_meta":{},"resultType":"complete"}`,
		} {
			if err := strictTaskUpdateAck(json.RawMessage(raw)); err != nil {
				t.Errorf("%s: strictTaskUpdateAck = %v, want accepted (SEP-2663 UpdateTaskResult = Result)", name, err)
			}
		}
		for name, raw := range map[string]string{
			// The discriminator itself.
			"missing discriminator":     `{}`,
			"missing but _meta present": `{"_meta":{"trace":"x"}}`,
			"absent result body":        ``,
			"wrong discriminator":       `{"resultType":"task"}`,
			"input_required":            `{"resultType":"input_required"}`,
			"non-string discriminator":  `{"resultType":1}`,
			"aliased discriminator":     `{"ResultType":"complete"}`,
			"unicode-folded alias":      `{"reſultType":"complete","resultType":"complete"}`,
			"duplicated discriminator":  `{"resultType":"complete","resultType":"complete"}`,
			// Task state may never ride on an acknowledgement.
			"status":              `{"resultType":"complete","status":"working"}`,
			"taskId":              `{"resultType":"complete","taskId":"t"}`,
			"aliased status":      `{"resultType":"complete","Status":"cancelled"}`,
			"inputRequests":       `{"resultType":"complete","inputRequests":{}}`,
			"unknown member":      `{"resultType":"complete","x":1}`,
			"_meta case alias":    `{"resultType":"complete","_Meta":{}}`,
			"duplicate _meta":     `{"resultType":"complete","_meta":{},"_meta":{}}`,
			"non-object array":    `[]`,
			"non-object string":   `"ok"`,
			"non-object null":     `null`,
			"trailing data":       `{"resultType":"complete"} {}`,
			"bare discriminator?": `{"resulttype":"complete"}`,
		} {
			if err := strictTaskUpdateAck(json.RawMessage(raw)); err == nil {
				t.Errorf("%s: strictTaskUpdateAck accepted a non-conformant acknowledgement", name)
			}
		}
	})
}

// TestCancellationUnconfirmedRecordsSurviveTheirTTL is round-3 R3-02. Round-2
// exempted only QUARANTINED/RECONCILING artifacts from TTL eviction, so a NORMAL
// governed record whose cancellation was merely acknowledged (`cancel_requested`),
// provably failed (`cancel_failed`) or inferred terminal
// (`canceled` + TerminalUnconfirmed) still vanished at `CreatedAt + ttlMs` —
// deleted and tombstoned without any authoritative confirmation that the work
// stopped, and therefore absent from every later kill-switch sweep. That directly
// contradicted the N-02 state model: an acknowledgement is not proof of anything.
func TestCancellationUnconfirmedRecordsSurviveTheirTTL(t *testing.T) {
	t.Run("taskExpired exempts every cancellation-unconfirmed shape", func(t *testing.T) {
		ttl := int64(1000)
		later := rsClock().Add(time.Hour)
		base := func(status string, unconfirmed bool) TaskRecord {
			return TaskRecord{
				TaskID: "t", CreatedAt: rsClock(), TTLMs: &ttl,
				Status: status, TerminalUnconfirmed: unconfirmed,
			}
		}
		for name, rec := range map[string]TaskRecord{
			"cancel_requested":           base(taskCancelRequestedStatus, false),
			"cancel_failed":              base(taskCancelFailedStatus, false),
			"inferred terminal canceled": base(taskStatusCanceled, true),
			"inferred terminal failed":   base(taskStatusFailed, true),
		} {
			if taskExpired(rec, later) {
				t.Errorf("%s: TTL-evicted; a cancellation the upstream never confirmed may name a LIVE task", name)
			}
		}
		// The control cases still expire: nothing else changed.
		for name, rec := range map[string]TaskRecord{
			"working":                   base(taskStatusWorking, false),
			"CONFIRMED terminal cancel": base(taskStatusCanceled, false),
			"CONFIRMED completed":       base(taskStatusCompleted, false),
		} {
			if !taskExpired(rec, later) {
				t.Errorf("%s: must still expire at its TTL", name)
			}
		}
	})

	t.Run("a client-canceled task is still swept long after its TTL elapsed", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		ttl := int64(60000)
		now := rsClock()
		up := &taskUpstream{}
		rs := newTaskEvidenceRSAt(t, jwks, up, &taskAuditor{}, func() time.Time { return now })
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-ttl-coop", TTLMs: &ttl})

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-ttl-coop"}`))
		if w.Code != http.StatusOK {
			t.Fatalf("tasks/cancel status = %d, want 200; body=%s", w.Code, w.Body.String())
		}

		now = now.Add(24 * time.Hour)
		rec, ok := rs.taskLedger.lookup("task-ttl-coop")
		if !ok {
			t.Fatal("R3-02: a cancellation-unconfirmed record must NEVER be forgotten by its TTL")
		}
		if rec.Status != taskCancelRequestedStatus {
			t.Errorf("retained status = %q, want %q", rec.Status, taskCancelRequestedStatus)
		}
		if got := len(rs.taskLedger.active(nil)); got != 1 {
			t.Errorf("sweep-visible tasks after the TTL = %d, want 1", got)
		}
		// The delivered bar is the steady state: visible, never re-emitted, not a fault.
		if canceled, err := rs.CancelActiveTasks(context.Background(), nil, "post-TTL kill-switch"); canceled != 0 || err != nil {
			t.Errorf("post-TTL sweep = %d, %v; want 0, nil", canceled, err)
		}
		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("upstream cancels = %d, want EXACTLY 1 (the client's own)", got)
		}
	})
}

// TestPendingRegistrationIsMonotonicAcrossASweep is round-3 R3-04. `active()`
// includes PENDING registrations (a live external task must never leave the
// sweep's field of view), so a kill-switch sweep can cancel a task whose
// mcp.task.track settlement is still in flight. Round-2 `markCancelRequested`
// then cleared `Pending` unconditionally — making the record OPERABLE before its
// own settlement owner had finished — and `handleToolTaskResult` ignored the
// compare-and-swap result of settleRegistration and relayed the handle anyway.
func TestPendingRegistrationIsMonotonicAcrossASweep(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: taskHandleUpstreamFn("task-pending-sweep")}
	base := &taskAuditor{}
	var rs *ResourceServer
	var once sync.Once
	var swept int
	var sweepErr error
	var windowPending bool
	var windowCode int
	aud := &hookAuditor{taskAuditor: base, onSettle: func(action string) {
		if action != taskActionTrack {
			return
		}
		once.Do(func() {
			// A kill-switch stop lands while the registration is still pending.
			swept, sweepErr = rs.CancelActiveTasks(context.Background(), nil, "kill-switch during registration")
			rec, _ := rs.taskLedger.lookup("task-pending-sweep")
			windowPending = rec.Pending
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, taskReq(token, methodTasksUpdate, `{"taskId":"task-pending-sweep","inputResponses":{}}`))
			windowCode = w.Code
		})
	}}
	rs = newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))

	if swept != 1 || sweepErr != nil {
		t.Fatalf("sweep during the pending window = %d, %v; want 1, nil (a pending task stays sweep-visible)", swept, sweepErr)
	}
	if !windowPending {
		t.Error("R3-04: a sweep acknowledgement CLEARED Pending — only the track-settlement owner may finalize a registration")
	}
	if windowCode != http.StatusForbidden {
		t.Errorf("tasks/update inside the pending window = %d, want 403 (the registration is not operable)", windowCode)
	}
	if got := up.count(methodTasksUpdate); got != 0 {
		t.Errorf("upstream task updates during the pending window = %d, want 0", got)
	}
	// The finalization must REFUSE (the record is no longer the clean pending
	// registration it inserted) and the handle must be withheld.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("tools/call status = %d, want 503 (the handle is withheld); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "task-pending-sweep") {
		t.Errorf("the task handle was relayed despite a refused finalization: %s", w.Body.String())
	}
	rec, ok := rs.taskLedger.lookup("task-pending-sweep")
	if !ok {
		t.Fatal("the created upstream task must stay visible for reconciliation")
	}
	if rec.operable() {
		t.Errorf("record after a refused finalization = %+v, want NON-operable", rec)
	}
	if !rec.Quarantined || rec.Status != taskCancelRequestedStatus {
		t.Errorf("record = %+v, want quarantined and cancel_requested", rec)
	}
	wafter := httptest.NewRecorder()
	rs.ServeHTTP(wafter, taskReq(token, methodTasksUpdate, `{"taskId":"task-pending-sweep","inputResponses":{}}`))
	if wafter.Code != http.StatusForbidden {
		t.Errorf("tasks/update after a refused finalization = %d, want 403", wafter.Code)
	}
}

// TestClientCancelAndSweepShareOneGenerationIntent is round-3 R3-05. Round-2 the
// client's tasks/cancel was OUTSIDE the per-generation cancellation intent: it
// only recorded a bar AFTERWARDS. A kill-switch sweep could therefore reserve and
// dispatch the same logical cancellation of the same generation while the
// client's was in flight — two governed operations, two cooperative
// cancellations upstream — and, because barCancelIntent silently declined to
// record a bar while an attempt was in flight, a retryable sweep verdict could
// leave the generation re-armed for a third dispatch.
func TestClientCancelAndSweepShareOneGenerationIntent(t *testing.T) {
	t.Run("a client cancel cannot race a sweep into a duplicate cancellation", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		sweepInFlight := make(chan struct{})
		releaseSweep := make(chan struct{})
		var once sync.Once
		up := &taskUpstream{}
		up.gate = func(req UpstreamRequest) {
			if req.Method != methodTasksCancel {
				return
			}
			// Only the FIRST cancellation blocks; a second one (the defect) must be
			// free to proceed so the test observes the duplicate instead of hanging.
			first := false
			once.Do(func() { first = true })
			if !first {
				return
			}
			close(sweepInFlight)
			<-releaseSweep
		}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-cancel-race"})

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rs.CancelActiveTasks(context.Background(), nil, "kill-switch stop")
		}()

		<-sweepInFlight // the sweep has reserved the intent and is on the wire
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksCancel, `{"taskId":"task-cancel-race"}`))
		if w.Code != http.StatusConflict {
			t.Errorf("client tasks/cancel during an in-flight server cancellation = %d, want 409; body=%s",
				w.Code, w.Body.String())
		}
		close(releaseSweep)
		wg.Wait()

		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("upstream cancellations = %d, want EXACTLY 1 (one generation, one cooperative cancellation)", got)
		}
	})

	t.Run("a delivered bar survives an in-flight attempt that ends retryable", func(t *testing.T) {
		l := newTaskLedger(0, rsClock)
		stored, err := l.insert(TaskRecord{
			TaskID: "task-bar", Subject: "agent:claude", Tenant: "t", Tool: "search",
			Status: taskStatusWorking, CreatedAt: rsClock(),
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if res := l.beginCancelAttempt("task-bar", stored.Generation); !res.ok {
			t.Fatalf("first reservation = %+v, want ok", res)
		}
		// The caller that OBSERVED the acknowledgement records the delivered bar
		// while its own attempt is still in flight — round-2 dropped it here.
		l.barCancelIntent(stored.Generation, taskCancelBarDelivered, "client ack observed in flight")
		// ...and a RETRYABLE verdict must not re-arm the generation.
		l.endCancelAttempt(stored.Generation, true, taskCancelBarAmbiguous, "retryable outcome")
		res := l.beginCancelAttempt("task-bar", stored.Generation)
		if res.ok || res.bar != taskCancelBarDelivered {
			t.Errorf("reservation after a delivered bar = %+v, want a refusal preserving the DELIVERED bar", res)
		}
	})

	t.Run("a delivered client cancellation bars a second client cancel", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{}
		rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-twice"})

		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, methodTasksCancel,
			`{"taskId":"task-twice","_meta":{"ai.olivares/operationId":"cancel-1"}}`))
		if w.Code != http.StatusOK {
			t.Fatalf("first tasks/cancel = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		// A DIFFERENT operation key: the evidence replay check cannot stop this one;
		// only the per-generation cancellation intent can.
		w2 := httptest.NewRecorder()
		rs.ServeHTTP(w2, taskReq(token, methodTasksCancel,
			`{"taskId":"task-twice","_meta":{"ai.olivares/operationId":"cancel-2"}}`))
		if w2.Code != http.StatusConflict {
			t.Errorf("second tasks/cancel under a fresh key = %d, want 409; body=%s", w2.Code, w2.Body.String())
		}
		if got := up.count(methodTasksCancel); got != 1 {
			t.Errorf("upstream cancels = %d, want EXACTLY 1", got)
		}
	})
}

// TestClearCancelBarRefusesAReplacementGeneration is round-3 R3-07: the explicit
// reconciliation seam was the ONE mutation site outside the compare-and-swap
// discipline. It resolved the record by bare task id, so a reconciliation action
// scheduled against retired T/G1 cleared the cancellation bar of an unrelated
// replacement T/G2 — re-arming automatic cancellation of a task nobody
// reconciled, which a later sweep would then re-emit.
func TestClearCancelBarRefusesAReplacementGeneration(t *testing.T) {
	l := newTaskLedger(0, rsClock)
	mk := func(subject string) TaskRecord {
		rec, err := l.insert(TaskRecord{
			TaskID: "task-recon", Subject: subject, Tenant: "t", Tool: "search",
			Status: taskStatusWorking, CreatedAt: rsClock(),
		})
		if err != nil {
			t.Fatalf("insert %s: %v", subject, err)
		}
		return rec
	}

	g1 := mk("agent:a")
	l.barCancelIntent(g1.Generation, taskCancelBarAmbiguous, "G1 ended ambiguously")
	if !testReleaseUnlessPinned(l, "task-recon", g1.Generation) {
		t.Fatal("G1 must be releasable (no effect in flight)")
	}

	g2 := mk("agent:b")
	if g2.Generation == g1.Generation {
		t.Fatal("a replacement registration must never reuse the retired generation")
	}
	l.barCancelIntent(g2.Generation, taskCancelBarDelivered, "G2 cancellation delivered")

	// The DELAYED reconciliation of G1 arrives.
	if l.clearCancelBar("task-recon", g1.Generation) {
		t.Error("R3-07: a reconciliation aimed at a RETIRED generation must be refused")
	}
	if res := l.beginCancelAttempt("task-recon", g2.Generation); res.ok || res.bar != taskCancelBarDelivered {
		t.Errorf("G2 reservation = %+v, want the delivered bar INTACT (a stale reconciliation cleared it)", res)
	}
	// A reconciliation that names the live generation still works.
	if !l.clearCancelBar("task-recon", g2.Generation) {
		t.Error("clearCancelBar must clear the bar of the generation it names")
	}
	if res := l.beginCancelAttempt("task-recon", g2.Generation); !res.ok {
		t.Errorf("G2 reservation after its own reconciliation = %+v, want ok", res)
	}
}

// TestSettledStatePermitsCancelRetry pins the cancel custodian's reading of the
// stage-7 B-bis vocabulary split. This is deliberately a table test on the ONE
// custodial predicate (both the client tasks/cancel leg and the sweep leg call
// it) rather than a ServeHTTP flow: since round 2 a PARENT dispatch operation
// never settles `withheld` at all — it is the release child's terminal state —
// so no cancel dispatch can present it through the wire. The branch exists for
// readers of child rows or future journal states, and it must already refuse
// re-actuation the moment such a reading appears.
//
// MUTATION VERIFIED (stage-7 method, re-run in round 2): making
// settledStatePermitsCancelRetry return true for DispatchWithheld — the exact
// regression the old ambiguous `blocked` invited — turns the withheld row red
// with the property message.
func TestSettledStatePermitsCancelRetry(t *testing.T) {
	for _, tc := range []struct {
		state DispatchState
		want  bool
		why   string
	}{
		{DispatchNotSent, true, "proven pre-transport failure: nothing reached the upstream"},
		{DispatchBlocked, true, "blocked proves nothing reached the upstream (pre-dispatch by contract since B-bis), so the custodian's retry inference is now legitimate"},
		{DispatchWithheld, false, "a fetched-then-denied response must settle as withheld, not blocked: blocked promises nothing reached the upstream — the dispatch ran, and an automatic retry would re-execute a produced effect"},
		{DispatchUnknown, false, "ambiguous outcomes never license an automatic re-attempt"},
		{DispatchCompleted, false, "a delivered effect is never automatically re-emitted"},
	} {
		if got := settledStatePermitsCancelRetry(tc.state); got != tc.want {
			t.Errorf("settledStatePermitsCancelRetry(%s) = %t, want %t: %s", tc.state, got, tc.want, tc.why)
		}
	}
}
