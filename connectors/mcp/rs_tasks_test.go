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

	"github.com/olivaresai/olivares/sdk"
)

type taskAuditor struct {
	fakeEvidenceJournal
	mu        sync.Mutex // decisions capture is concurrency-safe (duplicate-claim race test)
	decisions []ToolDecision
}

func (a *taskAuditor) Record(ctx context.Context, d ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	a.mu.Lock()
	a.decisions = append(a.decisions, d)
	a.mu.Unlock()
	return a.fakeEvidenceJournal.Record(ctx, d, binding)
}

type taskUpstream struct {
	// mu makes the fake safe under the round-2 concurrency exploits
	// (concurrent sweeps / barrier-synchronized duplicate claims). ADDITIVE: the
	// unlocked append was a pre-existing weakness of the fake, never an assert.
	mu    sync.Mutex
	calls []UpstreamRequest
	fn    func(UpstreamRequest) (json.RawMessage, error)
	// shaped, when set, takes precedence over fn and returns the PRODUCTION
	// dispatch shapes the forwarder can actually produce (round-1 F-13:
	// mapping every error to not_sent could not represent the
	// {completed, error} and {unknown} legs that trigger F-01/F-02).
	shaped func(UpstreamRequest) (UpstreamResult, error)
	// gate, when set, is invoked (unlocked) before the call is recorded, so a
	// test can hold a dispatch in flight and force a genuine claim race.
	gate func(UpstreamRequest)
}

func (u *taskUpstream) Forward(_ context.Context, req UpstreamRequest) (UpstreamResult, error) {
	if u.gate != nil {
		u.gate(req)
	}
	u.mu.Lock()
	u.calls = append(u.calls, req)
	shaped, fn := u.shaped, u.fn
	u.mu.Unlock()
	if shaped != nil {
		return shaped(req)
	}
	if fn != nil {
		raw, err := fn(req)
		if err != nil {
			return UpstreamResult{State: DispatchNotSent}, err
		}
		return UpstreamResult{Result: raw, State: DispatchCompleted}, nil
	}
	return UpstreamResult{Result: json.RawMessage(normativeCompleteResult), State: DispatchCompleted}, nil
}

// normativeCompleteResult is the NORMATIVE SEP-2663 base result an upstream
// returns for a method that carries no method-specific payload — notably
// `UpdateTaskResult`, which the extension defines as `Result` and which MUST
// therefore carry `resultType:"complete"` (review round-4 R4-02). The
// round-3 fakes answered `{}`, which is NOT conformant: it made the green suite
// entrench an interoperability failure in which the gateway rejected the real
// success shape with 502. This is a FAKE-IMPLEMENTATION correction to the shape
// the extension actually mandates; no assertion is weakened by it.
const normativeCompleteResult = `{"resultType":"complete"}`

func (u *taskUpstream) count(method string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	count := 0
	for _, c := range u.calls {
		if c.Method == method {
			count++
		}
	}
	return count
}

func (u *taskUpstream) total() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *taskUpstream) last(method string) (UpstreamRequest, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := len(u.calls) - 1; i >= 0; i-- {
		if u.calls[i].Method == method {
			return u.calls[i], true
		}
	}
	return UpstreamRequest{}, false
}

type taskGateFake struct {
	dec     TaskGateDecision
	err     error
	intents []TaskIntent
}

func (g *taskGateFake) AuthorizeTask(_ context.Context, intent TaskIntent) (TaskGateDecision, error) {
	g.intents = append(g.intents, intent)
	if g.err != nil {
		return TaskGateDecision{}, g.err
	}
	return g.dec, nil
}

type taskMediatorFake struct {
	allow  bool
	reason string
	calls  []ElicitationInspectionInput
}

func (m *taskMediatorFake) Mediate(_ context.Context, in ElicitationInspectionInput) ElicitationInspectionDecision {
	m.calls = append(m.calls, in)
	return ElicitationInspectionDecision{Allow: m.allow, Reason: m.reason}
}

func newTaskRS(t *testing.T, jwks []byte, up Upstream, aud *taskAuditor, gate ApprovalGate, taskGate TaskGate, maxActive int, med ElicitationMediator) *ResourceServer {
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
	if aud == nil {
		aud = &taskAuditor{}
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Gate:                       gate,
		TaskGate:                   taskGate,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Upstream:                   up,
		Auditor:                    aud,
		Clock:                      rsClock,
		ElicitationMediator:        med,
		MaxActiveTasksPerSubject:   maxActive,
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

func taskReq(token, method, params string) *http.Request {
	body := `{"jsonrpc":"2.0","id":9,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func customToolsCallReq(token, params string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func mustInsertTask(t *testing.T, rs *ResourceServer, rec TaskRecord) TaskRecord {
	t.Helper()
	if rec.TaskID == "" {
		t.Fatal("test task record needs task id")
	}
	if rec.Tenant == "" {
		rec.Tenant = rs.tenant
	}
	if rec.Subject == "" {
		rec.Subject = "agent:claude"
	}
	// Round-1 F-06: ownership is the CANONICAL OWNER TUPLE, so an injected
	// record must carry the issuer the test tokens are minted under (the test
	// tokens carry no client_id/azp, so ClientID stays empty on both sides).
	// ADDITIVE default: a test that sets Issuer explicitly (the cross-issuer
	// exploit) keeps its value.
	if rec.Issuer == "" {
		rec.Issuer = rsIssuer
	}
	if rec.Tool == "" {
		rec.Tool = "search"
	}
	if rec.RequiredScope == "" {
		rec.RequiredScope = "tools:read"
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rs.clock()
	}
	if rec.Status == "" {
		rec.Status = taskStatusWorking
	}
	// ROUND-8 R8-01 (fake-DATA correction, no assertion touched): this helper models
	// a task that was REGISTERED AND HANDED TO ITS OWNER — every test built on it
	// then drives client task methods with the owner's token, which is only possible
	// because the owner knows the identifier. The retirement rule now reads that
	// IMMUTABLE handle-relay provenance instead of inferring "the owner has a
	// delivery to lose" from mutable current state, so the fixture has to state the
	// fact the scenario already assumed. A test that needs the opposite (a handle
	// that was never delivered) drives the real relay path, which is where the
	// provenance is actually written.
	rec.HandleRelayed = true
	// Round-2 N-03: the ledger assigns the record's IMMUTABLE generation and
	// returns the stored record; a test that needs it (revalidation/CAS exploits)
	// takes it from here. ADDITIVE: existing callers ignore the return value.
	if rs.durableTasks == nil {
		t.Fatal("test task ResourceServer needs a durable store")
	}
	rec, err := rs.registerDurableTask(context.Background(), rec)
	if err != nil {
		t.Fatalf("register durable test task: %v", err)
	}
	stored, err := rs.taskLedger.insertDurable(rec)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return stored
}

// testReleaseUnlessPinned is the round-7 UNGATED compare-and-delete, preserved for
// the FOUR pre-existing call sites that use it as a SETUP action (free the
// identifier so a replacement registration can be attempted) or as a PROBE of the
// generation pin — neither of which is a claim about the retirement rule. They are
// `task_evidence_test.go:2098,2407,2891` and `task_round4_test.go:654`.
//
// ROUND-9 R9-05: round 8 wrote "three" here and in the session. No call site was
// hidden by it, but a miscount inside an assertion-integrity note is exactly the
// kind of claim that has to be exact, so it is corrected rather than explained away.
//
// ROUND-8 R8-01 folded the production `release` into the ONE compare-and-delete
// predicate the operator retirement uses, so it now refuses a record that is not
// confirmed-terminal or whose owner has an uncollected result. That is the point of
// the finding. This helper keeps the OTHER half — the generation compare-and-swap
// and the lease refusal — bit for bit, so those tests keep discriminating exactly
// what they were written to discriminate.
func testReleaseUnlessPinned(l *taskLedger, taskID, generation string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.byID[taskID]
	if !ok || rec.Generation != generation {
		return false
	}
	if l.leasedLocked(rec.Generation) {
		return false
	}
	delete(l.byID, taskID)
	l.retireLocked(rec.Generation, "record released by a test fixture")
	return true
}

func TestRSTaskCreationRegistersAndNilGateAllows(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-1","status":"working","ttlMs":60000}`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, nil)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{"q":"x"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, ok := rs.taskLedger.get("task-1")
	if !ok || got.Tool != "search" || got.Subject != "agent:claude" || got.RequiredScope != "tools:read" {
		t.Fatalf("task not registered correctly: ok=%v rec=%+v", ok, got)
	}
	found := false
	for _, d := range aud.decisions {
		if d.Allowed && d.TaskID == "task-1" && strings.Contains(d.Reason, "registered") {
			found = true
		}
	}
	if !found {
		t.Fatalf("registration decision with task id was not audited: %+v", aud.decisions)
	}
	if !strings.Contains(rec.Body.String(), `"taskId":"task-1"`) {
		t.Fatalf("allowed task handle must be relayed verbatim, body=%s", rec.Body.String())
	}
}

func TestRSTaskGateDenyAndErrorCancelUpstream(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	for _, tc := range []struct {
		name       string
		gate       *taskGateFake
		wantStatus int
	}{
		{
			name:       "deny maps status",
			gate:       &taskGateFake{dec: TaskGateDecision{Allow: false, Reason: "budget cap", DeniedStatus: http.StatusPaymentRequired}},
			wantStatus: http.StatusPaymentRequired,
		},
		{
			name:       "error fail closed",
			gate:       &taskGateFake{err: errors.New("budget store down")},
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
				switch req.Method {
				case "tools/call":
					return json.RawMessage(`{"resultType":"task","taskId":"task-denied","status":"working"}`), nil
				case methodTasksCancel:
					return json.RawMessage(`{}`), nil
				default:
					return json.RawMessage(`{}`), nil
				}
			}}
			rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, tc.gate, 0, nil)
			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "task-denied") {
				t.Fatalf("denied handle must not be revealed to client: %s", rec.Body.String())
			}
			if up.count(methodTasksCancel) != 1 {
				t.Fatalf("denied task must be canceled upstream, calls=%+v", up.calls)
			}
			if _, ok := rs.taskLedger.get("task-denied"); ok {
				t.Fatal("denied task must not remain registered")
			}
		})
	}
}

func TestRSTaskSubjectCapDeniesThirtyThirdAndCancels(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	next := 0
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			next++
			return json.RawMessage(fmt.Sprintf(`{"resultType":"task","taskId":"task-%02d","status":"working"}`, next)), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, nil)
	for i := 0; i < defaultMaxActiveTasksPerSubject; i++ {
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("task %d status = %d, want 200; body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("33rd task status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if up.count(methodTasksCancel) != 1 {
		t.Fatalf("33rd task must be canceled upstream, calls=%+v", up.calls)
	}
	if cancelReq, ok := up.last(methodTasksCancel); !ok || !strings.Contains(string(cancelReq.Params), "task-33") {
		t.Fatalf("cancel must target the refused handle, got %+v", cancelReq)
	}
}

func TestRSTaskMethodsGateSubjectPolicyApprovalAndStatus(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case methodTasksGet:
			// Review ROUND-5 R5-01: `GetTaskResult = Result & DetailedTask`, so the
			// mandatory Task fields (createdAt/lastUpdatedAt/ttlMs) belong here too — an
			// abbreviated body is exactly what the gateway was wrongly accepting as
			// authoritative proof of termination. FAKE-IMPLEMENTATION correction to the
			// shape the extension mandates; the "get syncs status" assertion below is
			// untouched and still exercised.
			return json.RawMessage(conformingGetTaskResult("task-get", taskStatusCompleted)), nil
		case methodTasksUpdate:
			// Review ROUND-3 R3-01 / ROUND-4 R4-02: SEP-2663 defines
			// UpdateTaskResult as an acknowledgement that carries NO TASK STATE — but
			// it is still `Result`, so `resultType:"complete"` is MANDATORY on it and
			// `{}` is not conformant. The gateway refuses to relay a state-reporting
			// body on this method AND refuses a body missing the discriminator. This
			// is a FAKE-IMPLEMENTATION correction to the shape the extension actually
			// mandates; every assertion of this test is untouched and still exercised.
			return json.RawMessage(normativeCompleteResult), nil
		case methodTasksCancel:
			return json.RawMessage(`{}`), nil
		default:
			return json.RawMessage(`{}`), nil
		}
	}}

	t.Run("unknown and foreign subject deny", func(t *testing.T) {
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, nil)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"missing"}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unknown task status = %d, want 403", rec.Code)
		}
		mustInsertTask(t, rs, TaskRecord{TaskID: "foreign", Subject: "agent:other"})
		rec = httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"foreign"}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("foreign task status = %d, want 403", rec.Code)
		}
	})

	t.Run("get syncs status", func(t *testing.T) {
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-get"})
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"task-get"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("tasks/get status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		got, _ := rs.taskLedger.get("task-get")
		if got.Status != taskStatusCompleted {
			t.Fatalf("ledger status = %q, want completed", got.Status)
		}
	})

	t.Run("kill-switched tool cancels", func(t *testing.T) {
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, nil)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-kill"})
		denied, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read", Deny: true}})
		if err != nil {
			t.Fatal(err)
		}
		rs.toolset = denied
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-kill","inputResponses":{}}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("kill-switched update status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
		got, _ := rs.taskLedger.get("task-kill")
		if got.Status != taskStatusCanceled {
			t.Fatalf("kill-switched task ledger status = %q, want canceled", got.Status)
		}
		if up.count(methodTasksCancel) == 0 {
			t.Fatal("kill-switched task update must cancel upstream")
		}
	})

	t.Run("destructive update requires approval", func(t *testing.T) {
		rsDenied := newTaskRS(t, jwks, up, &taskAuditor{}, fakeToolGate{StatusPending}, nil, 0, nil)
		mustInsertTask(t, rsDenied, TaskRecord{
			TaskID: "task-update-denied", Tool: "delete_db", RequiredScope: "tools:admin",
			Destructive: true,
		})
		rec := httptest.NewRecorder()
		rsDenied.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-update-denied","inputResponses":{}}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("destructive pending status = %d, want 403", rec.Code)
		}

		rsAllowed := newTaskRS(t, jwks, up, &taskAuditor{}, fakeToolGate{StatusApproved}, nil, 0, nil)
		mustInsertTask(t, rsAllowed, TaskRecord{
			TaskID: "task-update", Tool: "delete_db", RequiredScope: "tools:admin",
			Destructive: true,
		})
		rec = httptest.NewRecorder()
		rsAllowed.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-update","inputResponses":{}}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("destructive approved status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestRSCancelActiveTasksDoD(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(`{"resultType":"task","taskId":"task-dod","status":"working"}`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, nil)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("create task status = %d, want 200", rec.Code)
	}
	canceled, err := rs.CancelActiveTasks(context.Background(), func(TaskRecord) bool { return true }, "kill-switch")
	if err != nil || canceled != 1 {
		t.Fatalf("CancelActiveTasks = %d, %v; want 1,nil", canceled, err)
	}
	if cancelReq, ok := up.last(methodTasksCancel); !ok || !strings.Contains(string(cancelReq.Params), "task-dod") {
		t.Fatalf("cancel did not target task-dod: %+v", cancelReq)
	}
	// Review ROUND-2 N-02 — the ONE sanctioned change to a pre-existing
	// assertion, made because the independent review judged the previous
	// expectation (`want canceled`) to PIN A FAIL-OPEN INFERENCE rather than a
	// security invariant: tasks/cancel is COOPERATIVE by contract (see
	// Client.TaskCancel in tasks.go — "empty ack; the final status may still be
	// non-canceled"), so an acknowledgement is not proof of a terminal
	// cancellation. Storing terminal `canceled` removed a possibly still-live
	// external task from `active()` and therefore from every later kill-switch
	// sweep. The assertion now pins the corrected semantics — and is STRONGER
	// than the one it replaces: the status must be the non-terminal
	// `cancel_requested`, the record must survive, and it must stay visible to
	// reconciliation and to a future sweep.
	got, ok := rs.taskLedger.get("task-dod")
	if !ok {
		t.Fatal("an acknowledged cooperative cancellation must NOT delete the task record")
	}
	if got.Status != taskCancelRequestedStatus {
		t.Fatalf("ledger status = %q, want %q (an ack is not a terminal cancellation)", got.Status, taskCancelRequestedStatus)
	}
	if taskStatusTerminal(got.Status) {
		t.Fatalf("status %q must NOT be terminal: the upstream never confirmed the cancellation", got.Status)
	}
	if len(rs.taskLedger.active(nil)) != 1 {
		t.Fatal("a cancel-requested task must stay visible to reconciliation and to future sweeps")
	}
	found := false
	for _, d := range aud.decisions {
		if d.TaskID == "task-dod" && strings.Contains(d.Reason, "kill-switch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("kill-switch cancellation reason not audited: %+v", aud.decisions)
	}
}

func TestRSMRTRMediationForTasks(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())

	t.Run("input_required result deny and allow", func(t *testing.T) {
		result := json.RawMessage(`{"resultType":"input_required","inputRequests":{"need":{"message":"enter secret"}},"requestState":"s1"}`)
		for _, tc := range []struct {
			name       string
			med        *taskMediatorFake
			wantStatus int
		}{
			{name: "deny", med: &taskMediatorFake{allow: false, reason: "blocked"}, wantStatus: http.StatusForbidden},
			{name: "allow", med: &taskMediatorFake{allow: true}, wantStatus: http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
					if req.Method == "tools/call" {
						return result, nil
					}
					return json.RawMessage(`{}`), nil
				}}
				rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, tc.med)
				rec := httptest.NewRecorder()
				rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if len(tc.med.calls) != 1 || tc.med.calls[0].Channel != ChannelMRTRInputRequest {
					t.Fatalf("MRTR request was not mediated: %+v", tc.med.calls)
				}
			})
		}
	})

	t.Run("inputResponses on tools/call and tasks/update", func(t *testing.T) {
		med := &taskMediatorFake{allow: false, reason: "response blocked"}
		up := &taskUpstream{}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, customToolsCallReq(token, `{"name":"search","arguments":{},"inputResponses":{"answer":{"text":"secret"}}}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("tools/call inputResponses status = %d, want 403", rec.Code)
		}
		if up.count("tools/call") != 0 {
			t.Fatal("denied inputResponses must not be forwarded")
		}
		if len(med.calls) != 1 || med.calls[0].Channel != ChannelMRTRInputResponse {
			t.Fatalf("tools/call inputResponses not mediated: %+v", med.calls)
		}

		med2 := &taskMediatorFake{allow: false, reason: "response blocked"}
		rs2 := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med2)
		mustInsertTask(t, rs2, TaskRecord{TaskID: "task-mrtr"})
		rec = httptest.NewRecorder()
		rs2.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-mrtr","inputResponses":{"answer":{"text":"secret"}}}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("tasks/update inputResponses status = %d, want 403", rec.Code)
		}
		if len(med2.calls) != 1 || med2.calls[0].Channel != ChannelMRTRInputResponse {
			t.Fatalf("tasks/update inputResponses not mediated: %+v", med2.calls)
		}
	})

	t.Run("nil mediator passes", func(t *testing.T) {
		up := &taskUpstream{}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, nil)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, customToolsCallReq(token, `{"name":"search","arguments":{},"inputResponses":{"answer":{"text":"ok"}}}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("nil mediator status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if up.count("tools/call") != 1 {
			t.Fatal("nil mediator must forward the request")
		}
	})
}
