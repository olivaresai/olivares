// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rs_stage7_tasks_guard_test.go — the stage-7 P0-3 guard: the CORE
// `resultType:"input_required"` discriminator is sanctioned on exactly three
// client requests, and the Tasks extension is NOT one of them.
//
// NORMATIVE SOURCES, both consulted 2026-07-29:
//
//   - MRTR (mrtr.mdx:182-192): "Servers MAY send InputRequiredResult responses"
//     on prompts/get, resources/read and tools/call; "Servers MUST NOT send
//     InputRequiredResult responses on any other client requests."
//   - The Tasks extension: `GetTaskResult` and `CancelTaskResult` are
//     `resultType:"complete"`, and an outstanding input requirement is reported
//     by the TASK STATUS (`status:"input_required"` + `inputRequests`), never by
//     the core result discriminator. `UpdateTaskResult = Result` is an ack.
//
// The finding this closes (an internal design note (not shipped),
// §"P0-3 pendiente"): the common readers accepted the core discriminator under
// ANY authority profile, so `tasks/get` and `tasks/cancel` mediated and RELEASED
// a result the extension forbids — and the release child was claimed for bytes
// no conforming server may send.
//
// The contract implemented here (mediation-design §7):
//
//	(a) `status:"input_required"` under the Tasks contract is permitted and GOVERNED;
//	(b) the core `resultType:"input_required"` is REFUSED in tasks/get, tasks/update
//	    and tasks/cancel — 502 / -31002, zero mediator calls, zero release;
//	(c) an incidental `status` is NEVER read as core MRTR.
//
// Every refusal of an OBSERVED result follows the stage-7 B-bis unit: the parent
// settles the dispatch fact (`completed`) and the RESPONSE-RELEASE CHILD settles
// `withheld` before the refusal is written.

const stage7GuardSecret = "stage7-guard-secret"

// stage7CoreInputRequired is the CORE InputRequiredResult variant — the exact
// `resultType` discriminator (schema.ts:571-595).
const stage7CoreInputRequired = `{"resultType":"input_required","inputRequests":` +
	`{"q1":{"method":"elicitation/create","params":{"message":"` + stage7GuardSecret + `"}}}}`

const stage7GuardTaskID = "task-stage7-guard"

// stage7TaskInputRequiredReport is the CONFORMING SEP-2663 `GetTaskResult` for a
// task that is waiting for input: `resultType:"complete"` (the mandatory Result
// discriminator) plus the task `status`. This is what a conforming server sends,
// and it must keep working — the guard refuses the CORE discriminator, never the
// extension profile.
const stage7TaskInputRequiredReport = `{"resultType":"complete","taskId":"` + stage7GuardTaskID + `",` +
	`"status":"input_required","createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:00Z",` +
	`"ttlMs":60000,"inputRequests":{"q1":{"method":"elicitation/create","params":{"message":"` +
	stage7GuardSecret + `"}}}}`

func stage7GuardToken(t *testing.T) (string, []byte) {
	t.Helper()
	return mintAccessToken(t, "k1", rsResource,
		"tools:read resources:read prompts:read completion:read", validExp())
}

// stage7GenericReq builds a JSON-RPC request for a method that has no dedicated
// helper (the generic dispatch family).
func stage7GenericReq(token, method, params string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// stage7MRTRCalls counts the mediator calls on the MRTR channel ONLY. The
// elicitation and sampling surfaces mediate their own REQUEST content before the
// forward, so "the mediator was called" is not the question — "was the upstream
// result treated as a governed MRTR payload" is.
func stage7MRTRCalls(med *fakeElicitationMediator) int {
	n := 0
	for _, c := range med.calls {
		if c.Channel == ChannelMRTRInputRequest {
			n++
		}
	}
	return n
}

// stage7GuardRS builds the RS every subtest drives: one toolset entry, one
// declared ui:// template, a mediator, and a task record for the task methods.
func stage7GuardRS(t *testing.T, result string, med ElicitationMediator) (*ResourceServer, *taskAuditor, string) {
	t.Helper()
	token, jwks := stage7GuardToken(t)
	up := &taskUpstream{fn: func(UpstreamRequest) (json.RawMessage, error) {
		return json.RawMessage(result), nil
	}}
	aud := &taskAuditor{}
	rs := newS504RS(t, jwks, up, aud, nil, med)
	mustInsertTask(t, rs, TaskRecord{TaskID: stage7GuardTaskID})
	return rs, aud, token
}

// --- T14: the closed method matrix -------------------------------------------

// TestStage7CoreMRTRMethodMatrix is T14 of the mediation design's §9 matrix: the
// core input_required discriminator is admitted on exactly the three sanctioned
// requests and refused — 502 / -31002 — on EVERY other known method, including
// the ones served by dedicated handlers that never reach the generic dispatch
// table (tasks/*, elicitation/create, sampling/createMessage).
//
// MUTATION (recorded in the session report): adding a fourth entry to
// mrtrSanctionedCoreMethods turns that method's row red; removing the guard from
// a dedicated route turns that route's row red.
func TestStage7CoreMRTRMethodMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sanctioned bool
		req        func(token string) *http.Request
	}{
		// The three the specification sanctions (a ui:// read IS a resources/read).
		{"tools/call", true, func(tk string) *http.Request { return toolsCallReq(tk, "search", `{}`) }},
		{"prompts/get", true, func(tk string) *http.Request {
			return stage7GenericReq(tk, "prompts/get", `{"name":"p"}`)
		}},
		{"resources/read", true, func(tk string) *http.Request {
			return stage7GenericReq(tk, "resources/read", `{"uri":"file:///x"}`)
		}},
		{"resources/read ui://", true, func(tk string) *http.Request {
			return resourcesReadReq(tk, "ui://srv/dashboard")
		}},

		// The Tasks extension — the P0-3 surface.
		{methodTasksGet, false, func(tk string) *http.Request {
			return taskReq(tk, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`)
		}},
		{methodTasksCancel, false, func(tk string) *http.Request {
			return taskReq(tk, methodTasksCancel, `{"taskId":"`+stage7GuardTaskID+`"}`)
		}},
		{methodTasksUpdate, false, func(tk string) *http.Request {
			return taskReq(tk, methodTasksUpdate, `{"taskId":"`+stage7GuardTaskID+`","inputResponses":{}}`)
		}},

		// The other dedicated handlers.
		{"elicitation/create", false, func(tk string) *http.Request { return elicitationReq(tk, "prompt", "") }},
		{"sampling/createMessage", false, func(tk string) *http.Request {
			return samplingReq(tk, `[{"role":"user","content":{"type":"text","text":"hi"}}]`)
		}},

		// The generic dispatch family.
		{"completion/complete", false, func(tk string) *http.Request {
			return stage7GenericReq(tk, "completion/complete", `{}`)
		}},
		{"resources/list", false, func(tk string) *http.Request { return stage7GenericReq(tk, "resources/list", `{}`) }},
		{"resources/templates/list", false, func(tk string) *http.Request {
			return stage7GenericReq(tk, "resources/templates/list", `{}`)
		}},
		{"resources/subscribe", false, func(tk string) *http.Request {
			return stage7GenericReq(tk, "resources/subscribe", `{"uri":"file:///x"}`)
		}},
		{"resources/unsubscribe", false, func(tk string) *http.Request {
			return stage7GenericReq(tk, "resources/unsubscribe", `{"uri":"file:///x"}`)
		}},
		{"prompts/list", false, func(tk string) *http.Request { return stage7GenericReq(tk, "prompts/list", `{}`) }},
		{"tools/list", false, func(tk string) *http.Request { return stage7GenericReq(tk, "tools/list", `{}`) }},
		{"initialize", false, func(tk string) *http.Request { return stage7GenericReq(tk, "initialize", `{}`) }},
		{"ping", false, func(tk string) *http.Request { return stage7GenericReq(tk, "ping", `{}`) }},
		{"logging/setLevel", false, func(tk string) *http.Request {
			return stage7GenericReq(tk, "logging/setLevel", `{"level":"info"}`)
		}},
		{methodServerDiscover, false, func(tk string) *http.Request {
			return stage7GenericReq(tk, methodServerDiscover, `{}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			med := &fakeElicitationMediator{allow: true}
			rs, _, token := stage7GuardRS(t, stage7CoreInputRequired, med)

			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, tc.req(token))

			if tc.sanctioned {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: the specification SANCTIONS an InputRequiredResult on %s; body = %s",
						rec.Code, tc.name, rec.Body.String())
				}
				if got := stage7MRTRCalls(med); got != 1 {
					t.Fatalf("MRTR mediator calls = %d, want 1: a sanctioned input-required result must be GOVERNED, not merely relayed", got)
				}
				return
			}
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502: the specification forbids an InputRequiredResult on %s, so it must not be relayed; body = %s",
					rec.Code, tc.name, rec.Body.String())
			}
			if code := rpcErrorCode(t, rec.Body.String()); code != rpcUpstreamError {
				t.Errorf("code = %d, want %d: a MUST NOT of the upstream is a bad gateway, not a policy deny", code, rpcUpstreamError)
			}
			if strings.Contains(rec.Body.String(), stage7GuardSecret) {
				t.Errorf("the forbidden payload leaked into the refusal: %s", rec.Body.String())
			}
			if got := stage7MRTRCalls(med); got != 0 {
				t.Errorf("MRTR mediator calls = %d, want 0: a result no conforming server may send is a protocol refusal, never a content question", got)
			}
		})
	}
}

// --- T15: the Tasks discriminator ---------------------------------------------

// TestStage7TasksExtensionProfileIsGoverned pins half (a) of the §7 contract: a
// CONFORMING Tasks input requirement — `resultType:"complete"` plus
// `status:"input_required"` — is still mediated and still released through its
// anchored child. The guard removes a variant no conforming server sends; it
// removes no capability.
func TestStage7TasksExtensionProfileIsGoverned(t *testing.T) {
	t.Run("deny withholds and settles the child", func(t *testing.T) {
		med := &fakeElicitationMediator{allow: false, reason: "credential harvesting"}
		rs, aud, token := stage7GuardRS(t, stage7TaskInputRequiredReport, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`))

		if got := stage7MRTRCalls(med); got != 1 {
			t.Fatalf("MRTR mediator calls = %d, want 1: the extension's input requirement must reach the mediator", got)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (a mediation deny); body = %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), stage7GuardSecret) {
			t.Fatalf("the denied payload leaked: %s", rec.Body.String())
		}
		if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
			t.Errorf("withheld release children = %d, want 1", got)
		}
	})

	t.Run("allow releases through the anchored child", func(t *testing.T) {
		med := &fakeElicitationMediator{allow: true}
		rs, aud, token := stage7GuardRS(t, stage7TaskInputRequiredReport, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: a conforming Tasks input requirement must still be served; body = %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), stage7GuardSecret) {
			t.Fatalf("the allowed report must relay intact: %s", rec.Body.String())
		}
		rel := s504ReleaseOps(&aud.fakeEvidenceJournal)
		if len(rel) != 1 {
			t.Fatalf("release operations = %d, want exactly 1", len(rel))
		}
		if st := aud.settledState(rel[0]); st != DispatchCompleted {
			t.Fatalf("the release settled %q, want completed", st)
		}
	})
}

// TestStage7TasksRefuseCoreDiscriminator pins half (b): the core discriminator in
// tasks/get and tasks/cancel is an upstream protocol violation — refused with the
// stage-7 B-bis evidence unit (parent completed, release child withheld, nothing
// released, nothing mediated).
//
// MUTATION (recorded in the session report): letting the task profiles honor the
// core discriminator again — the single body heuristic that accepts BOTH
// discriminators, which is exactly the T15 mutation the design names — turns
// both subtests red on the wire code and on the mediator count.
func TestStage7TasksRefuseCoreDiscriminator(t *testing.T) {
	for _, method := range []string{methodTasksGet, methodTasksCancel} {
		t.Run(method, func(t *testing.T) {
			med := &fakeElicitationMediator{allow: true}
			rs, aud, token := stage7GuardRS(t, stage7CoreInputRequired, med)

			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, taskReq(token, method, `{"taskId":"`+stage7GuardTaskID+`"}`))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502: the Tasks extension forbids the core input_required discriminator on %s; body = %s",
					rec.Code, method, rec.Body.String())
			}
			if code := rpcErrorCode(t, rec.Body.String()); code != rpcUpstreamError {
				t.Errorf("code = %d, want %d", code, rpcUpstreamError)
			}
			if strings.Contains(rec.Body.String(), stage7GuardSecret) {
				t.Errorf("the unsanctioned payload leaked into the refusal: %s", rec.Body.String())
			}
			if got := stage7MRTRCalls(med); got != 0 {
				t.Errorf("MRTR mediator calls = %d, want 0: an unsanctioned result is never a content question", got)
			}
			// The stage-7 B-bis unit: the round trip HAPPENED (parent completed) and
			// the retention of the observed bytes is the release child's record.
			if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
				t.Errorf("withheld release children = %d, want exactly 1: a refused OBSERVED result records its disposition on the child", got)
			}
			if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchCompleted); got != 0 {
				t.Errorf("completed release children = %d, want 0: nothing was released", got)
			}
			if got := aud.settledCount(DispatchCompleted); got != 1 {
				t.Errorf("completed settlements = %d, want exactly 1 (the parent's dispatch fact)", got)
			}
			if got := aud.settledCount(DispatchBlocked); got != 0 {
				t.Errorf("blocked settlements = %d, want 0: the round trip finished", got)
			}
		})
	}

	// tasks/update needs no new leg: strictTaskUpdateAck already allow-lists the
	// ack members and refuses any state-reporting body, `inputRequests` and a
	// non-"complete" discriminator included. It is pinned here as a REGRESSION so
	// the third method of the §7 contract cannot silently regain the variant.
	t.Run(methodTasksUpdate, func(t *testing.T) {
		med := &fakeElicitationMediator{allow: true}
		rs, _, token := stage7GuardRS(t, stage7CoreInputRequired, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"`+stage7GuardTaskID+`","inputResponses":{}}`))

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502; body = %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), stage7GuardSecret) {
			t.Errorf("the unsanctioned payload leaked: %s", rec.Body.String())
		}
		if got := stage7MRTRCalls(med); got != 0 {
			t.Errorf("MRTR mediator calls = %d, want 0", got)
		}
	})
}

// stage7CancelIntent reads the per-generation cancellation-intent bar the ledger
// holds after a settled attempt (in-package white-box read; the ServeHTTP calls
// of the test have returned, so no lock-free concurrency is involved — the mutex
// is taken anyway).
func stage7CancelIntent(rs *ResourceServer, generation string) (bar taskCancelBar, inFlight, ok bool) {
	rs.taskLedger.mu.Lock()
	defer rs.taskLedger.mu.Unlock()
	in := rs.taskLedger.cancels[generation]
	if in == nil {
		return taskCancelBarNone, false, false
	}
	return in.bar, in.inFlight, true
}

// TestStage7RejectedCancelCustodyIsNotAnAck is the H-1 custody contract (r1
// contrast 2026-07-30; the P0-3 disposition table, r2-review §P0-3): when the
// tasks/cancel round trip returns the CORE input_required discriminator — a
// result the extension forbids, refused 502 — the transport fact must NOT be
// recorded as a delivered acknowledgement. "The transport returned something"
// and "what returned is a conforming ack" are different facts, and only the
// second may write `cancel_requested` or arm the `delivered` bar (the ledger
// reserves `ambiguous` precisely for what needs reconciliation).
//
// The five P0-3 dimensions, each observed through its ledger consequence:
//
//	dispatched=true        → CancelUnconfirmed=true (the record is TTL-immune);
//	acked=false            → no `cancel_requested`: the status stays untouched;
//	bar=ambiguous          → never `delivered` (a false durable delivery claim);
//	retryable=false        → a second cancel is suppressed, zero re-forwards;
//	CancelUnconfirmed=true → asserted directly.
//
// MUTATION (measured, r2 contrast MUT-R2-1): restoring the collapsed
// transport-equals-ack reading turns this red on the STATUS and BAR assertions.
// The withheld child stays green under that mutation — its settlement is the
// refusal branch's, independent of the custody collapse — so it is not part of
// this mutation's claim.
func TestStage7RejectedCancelCustodyIsNotAnAck(t *testing.T) {
	token, jwks := stage7GuardToken(t)
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksCancel {
			return json.RawMessage(stage7CoreInputRequired), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	aud := &taskAuditor{}
	med := &taskMediatorFake{allow: true}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
	stored := mustInsertTask(t, rs, TaskRecord{TaskID: stage7GuardTaskID})

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, taskReq(token, methodTasksCancel, `{"taskId":"`+stage7GuardTaskID+`"}`))

	// The wire + evidence unit (the guard's own contract, re-asserted so this
	// test stands alone): 502/-31002, nothing mediated, parent completed, child
	// withheld.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", rec.Code, rec.Body.String())
	}
	if code := rpcErrorCode(t, rec.Body.String()); code != rpcUpstreamError {
		t.Errorf("code = %d, want %d", code, rpcUpstreamError)
	}
	if len(med.calls) != 0 {
		t.Errorf("mediator calls = %d, want 0", len(med.calls))
	}
	if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Errorf("withheld release children = %d, want 1", got)
	}
	if got := aud.settledCount(DispatchCompleted); got != 1 {
		t.Errorf("completed settlements = %d, want 1 (the parent's dispatch fact)", got)
	}

	// Dimension: acked=false ⇒ the record NEVER advances to cancel_requested.
	got, ok := rs.taskLedger.get(stage7GuardTaskID)
	if !ok {
		t.Fatal("the record must stay retained")
	}
	if got.Status != taskStatusWorking {
		t.Errorf("record status = %q, want %q: a refused result is not an acknowledgement, so no cancel_requested may be written", got.Status, taskStatusWorking)
	}
	// Dimension: dispatched=true ⇒ CancelUnconfirmed (TTL-immune, reconciliation).
	if !got.CancelUnconfirmed {
		t.Error("CancelUnconfirmed = false, want true: the forward happened, so the cancellation's fate is unproven")
	}
	// Dimension: bar=ambiguous, never delivered. `delivered` is the steady state
	// the ledger expects after a PROVEN acknowledgement; recording it here is a
	// false durable delivery claim on the evidence surface.
	bar, inFlight, iok := stage7CancelIntent(rs, stored.Generation)
	if !iok {
		t.Fatal("the cancellation intent must exist after a settled attempt")
	}
	if bar != taskCancelBarAmbiguous {
		t.Errorf("cancel bar = %q, want %q: an unproven delivery demands reconciliation, not the delivered steady state", bar, taskCancelBarAmbiguous)
	}
	if inFlight {
		t.Error("the attempt reservation must be closed")
	}

	// Dimension: retryable=false ⇒ a further attempt is suppressed and nothing is
	// re-forwarded (at-most-once holds even though the first answer was refused).
	rec2 := httptest.NewRecorder()
	rs.ServeHTTP(rec2, taskReq(token, methodTasksCancel, `{"taskId":"`+stage7GuardTaskID+`"}`))
	if rec2.Code != http.StatusConflict {
		t.Errorf("second cancel status = %d, want 409 (suppressed by the per-generation intent); body = %s", rec2.Code, rec2.Body.String())
	}
	if n := up.count(methodTasksCancel); n != 1 {
		t.Errorf("upstream tasks/cancel forwards = %d, want 1: the ambiguous bar must not license a re-dispatch", n)
	}
}

// TestStage7ServerInitiatedCancelRejectsCoreResultAsNonAck is H-1R2 (r2
// contrast, 2026-07-30): the package's SECOND tasks/cancel emitter. The
// kill-switch sweep, the compensation of a denied/ungoverned task and the
// revoked-tool compensation all emit through dispatchAnchoredCancel, which used
// to keep only the TRANSPORT verdict (`succeeded: relay`) — so a core
// input_required answer to a sweep cancellation was recorded as a delivered
// acknowledgement: CancelActiveTasks returned success, the record advanced to
// `cancel_requested` and the bar armed `delivered`. Round 1 fixed the class in
// the HTTP handler only; the acknowledgement-conformity decision now lives in
// ONE shared predicate (conformingCancelAck) both emitters consume.
//
// Same five P0-3 dimensions as the handler test, on the server-initiated route:
//
//	dispatched=true        → CancelUnconfirmed=true;
//	acked=false            → no `cancel_requested` (status untouched);
//	bar=ambiguous          → never `delivered`;
//	retryable=false        → a second sweep re-forwards nothing;
//	success FALSE          → CancelActiveTasks counts 0 and reports the fault.
//
// MUTATION (recorded in the session report): restoring `succeeded: relay` in
// dispatchAnchoredCancel turns this red on the count/error, status and bar
// assertions.
func TestStage7ServerInitiatedCancelRejectsCoreResultAsNonAck(t *testing.T) {
	_, jwks := stage7GuardToken(t)
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksCancel {
			return json.RawMessage(stage7CoreInputRequired), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, &taskMediatorFake{allow: true})
	stored := mustInsertTask(t, rs, TaskRecord{TaskID: stage7GuardTaskID})

	canceled, err := rs.CancelActiveTasks(context.Background(), nil, "stage7 kill-switch")

	// Dimension: success FALSE. A cancellation whose acknowledgement does not
	// belong to the contract was never proven requested; counting it as one
	// tells the kill-switch operator the fleet is safer than it is.
	if canceled != 0 {
		t.Errorf("successful cancellations = %d, want 0: a core input_required answer is not a conforming CancelTaskResult acknowledgement", canceled)
	}
	if err == nil {
		t.Error("CancelActiveTasks error = nil, want the non-conforming acknowledgement reported")
	}

	got, ok := rs.taskLedger.get(stage7GuardTaskID)
	if !ok {
		t.Fatal("the record must stay retained")
	}
	// Dimension: acked=false ⇒ status untouched — neither cancel_requested nor
	// any other advance may be inferred from a result outside the contract.
	if got.Status != taskStatusWorking {
		t.Errorf("record status = %q, want %q: a non-conforming result cannot write cancel_requested", got.Status, taskStatusWorking)
	}
	// Dimension: dispatched=true ⇒ CancelUnconfirmed (TTL-immune, reconciliation).
	if !got.CancelUnconfirmed {
		t.Error("CancelUnconfirmed = false, want true: the forward happened, so the cancellation's fate is unproven")
	}
	// Dimension: bar=ambiguous, never the delivered steady state.
	bar, inFlight, iok := stage7CancelIntent(rs, stored.Generation)
	if !iok {
		t.Fatal("the cancellation intent must exist after a settled attempt")
	}
	if bar != taskCancelBarAmbiguous {
		t.Errorf("cancel bar = %q, want %q: an unproven delivery demands reconciliation, not the delivered steady state", bar, taskCancelBarAmbiguous)
	}
	if inFlight {
		t.Error("the attempt reservation must be closed")
	}

	// Dimension: retryable=false ⇒ a second sweep re-forwards NOTHING for this
	// generation (at-most-once holds even though the first answer was refused).
	_, _ = rs.CancelActiveTasks(context.Background(), nil, "stage7 kill-switch second pass")
	if n := up.count(methodTasksCancel); n != 1 {
		t.Errorf("upstream tasks/cancel forwards = %d, want 1: the ambiguous bar must not license a re-dispatch", n)
	}
}

// TestStage7ClientTaskCancelRejectsNonConformingAck is H-1R3 (r3 contrast,
// 2026-07-30): the package's THIRD tasks/cancel emitter — the EXPORTED
// Client.TaskCancel. It discarded the raw acknowledgement and turned taskCall's
// error alone into the public success contract, so a duplicated discriminator
// that conformingCancelAck classifies as a non-acknowledgement was confirmed to
// an external consumer as `nil` (success). checkResultEnvelope cannot cover it:
// its last-wins read sees `"complete"` (or nothing) on exactly the duplicates
// the predicate refuses. An emitter with no in-repo production callers is the
// one nobody's integration test catches — external consumers rely on the
// documented contract alone — so it takes the SAME predicate as the other two
// emitters, not a weaker private reading.
//
// MUTATION (recorded in the session report): dropping the conformity check —
// restoring `_, err := c.taskCall(...); return err` — turns the two
// non-conforming rows red.
func TestStage7ClientTaskCancelRejectsNonConformingAck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  string
		wantErr bool
	}{
		// Codex's r3 probe body, verbatim: a first-wins reader sees the core
		// input_required variant; the client's last-wins envelope read sees
		// "complete" and used to confirm success.
		{"duplicated discriminator, input_required first", `{"resultType":"input_required","resultType":"complete"}`, true},
		{"duplicated discriminator, mixed types", `{"resultType":"complete","resultType":null}`, true},
		// Narrowness controls: the conforming ack and the pre-RC absent
		// discriminator keep the exported contract exactly as documented.
		{"conforming ack", normativeCompleteResult, false},
		{"conforming ack with _meta", `{"resultType":"complete","_meta":{"trace":"x"}}`, false},
		{"pre-RC absent discriminator", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt := newMockTransport()
			mt.reply(methodTasksCancel, tc.result)
			c := newStatelessClient(mt)

			err := c.TaskCancel(context.Background(), "t1")
			if tc.wantErr && err == nil {
				t.Fatalf("TaskCancel = nil, want an error: the exported API confirmed as success an acknowledgement the shared predicate rejects (%s)", tc.result)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("TaskCancel = %v, want nil for a conforming acknowledgement", err)
			}
			if tc.wantErr && strings.Contains(err.Error(), "input_required") &&
				!strings.Contains(tc.result, `"resultType":"input_required"`) {
				t.Errorf("the error affirms a literal the body does not carry: %v", err)
			}
		})
	}

	// The single exact core literal keeps its existing deny-closed refusal at the
	// envelope (errInputRequired) — asserted so the guard cannot silently replace
	// a stricter pre-existing refusal.
	t.Run("single exact core literal still refused", func(t *testing.T) {
		mt := newMockTransport()
		mt.reply(methodTasksCancel, stage7CoreInputRequired)
		c := newStatelessClient(mt)
		if err := c.TaskCancel(context.Background(), "t1"); err == nil {
			t.Fatal("TaskCancel = nil, want the deny-closed input_required refusal")
		}
	})
}

// TestStage7OrdinaryTaskCancelKeepsItsAck is the narrowness control of the same
// contract: a NORMAL cooperative cancellation acknowledgement is untouched — 200,
// the cooperative `cancel_requested` custody, no mediation and no release child.
// Without it, a guard that refused every tasks/cancel result would satisfy the
// test above.
func TestStage7OrdinaryTaskCancelKeepsItsAck(t *testing.T) {
	med := &fakeElicitationMediator{allow: true}
	rs, aud, token := stage7GuardRS(t, normativeCompleteResult, med)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, taskReq(token, methodTasksCancel, `{"taskId":"`+stage7GuardTaskID+`"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a conforming ack-only CancelTaskResult); body = %s", rec.Code, rec.Body.String())
	}
	if got := stage7MRTRCalls(med); got != 0 {
		t.Errorf("MRTR mediator calls = %d, want 0 for an ordinary acknowledgement", got)
	}
	if rel := s504ReleaseOps(&aud.fakeEvidenceJournal); len(rel) != 0 {
		t.Errorf("release operations = %d, want 0: an unmediated ack plans no release child", len(rel))
	}
	got, ok := rs.taskLedger.get(stage7GuardTaskID)
	if !ok {
		t.Fatal("an acknowledged cooperative cancellation must not delete the record")
	}
	if got.Status != taskCancelRequestedStatus {
		t.Errorf("ledger status = %q, want %q: the ack itself is unchanged by the guard", got.Status, taskCancelRequestedStatus)
	}
}

// --- Adversarial strictness of the DISCRIMINATOR DETECTION ---------------------

// TestStage7GuardStrictness pins the strictness of the guard's exact-core
// DISCRIMINATOR DETECTION, precisely scoped:
//
//   - the strict tree (decodeStrictJSON) refuses duplicate keys at every depth
//     and lossy strings, so a governed entry two readers would read differently
//     is unreadable, never a plain relay;
//   - the resilient discriminator read is deny-closed on DUPLICATION: more than
//     one occurrence of the exact reserved key — WHATEVER the value types — is
//     an ambiguity no conforming server produces, and it refuses (M-1 of the r1
//     contrast closed the string/non-string gap the value-typed scan left).
//
// DECISION, explicit rather than implied: a case alias alone (`ResultType`) is
// EXTENSION data, never the discriminator. JSON — and MCP property naming — is
// case-sensitive, and the pin makes Result an open, extensible shape with an
// open ResultType enum (schema.ts:208-216,223-235): `ResultType` is a DIFFERENT
// member a conforming extension may legitimately carry, so refusing it would
// refuse conforming open-result traffic (the round-1→3 oscillation the
// adjudication dissolved). Where the gateway INTERPRETS a member, its
// case-variants are separately refused by the reserved-alias machinery of the
// strict tree — that protection is unchanged.
//
// What this suite deliberately does NOT claim: T16 of the mediation design's §9
// — the common structural validator of the inputRequests UNION (the three
// official request variants), their params and the per-request capability
// declarations (design §4.3) — is the declared stage residual
// (an internal design note (not shipped), item 3), not closed here.
//
// MUTATIONS (recorded in the session report): making the duplicate read
// permissive for string occurrences turns the string/string rows red; counting
// only string occurrences again (the M-1 gap) turns the mixed-type rows red.
func TestStage7GuardStrictness(t *testing.T) {
	t.Run("unit: the forbidden-core predicate", func(t *testing.T) {
		// M-1R2: the guard distinguishes the two refusals it can observe — a
		// single exact `input_required` literal (unsanctioned origin) and a
		// duplicated exact key of any value types (malformed/ambiguous) — because
		// each produces a DIFFERENT controlled record and neither may borrow the
		// other's claim.
		for _, tc := range []struct {
			name   string
			method string
			result string
			want   coreMRTRRefusal
		}{
			{"exact core discriminator on tasks/get", methodTasksGet, stage7CoreInputRequired, coreMRTRUnsanctioned},
			{"exact core discriminator on a sanctioned method", "tools/call", stage7CoreInputRequired, coreMRTRAdmitted},
			// A duplicated discriminator is refused as the AMBIGUITY it is — even
			// when one of its values happens to be the literal, the record may not
			// claim the body "carries" it: a first-wins reader sees the other value.
			{"duplicate exact resultType cannot be excluded", methodTasksGet,
				`{"resultType":"complete","resultType":"input_required"}`, coreMRTRDuplicated},
			{"duplicate exact resultType, neither input_required", methodTasksGet,
				`{"resultType":"complete","resultType":"complete"}`, coreMRTRDuplicated},
			// M-1 (r1 contrast): duplication is deny-closed WHATEVER the value
			// types. A string count let `"resultType":null` hide the second
			// occurrence and the ambiguous object relayed.
			{"mixed-type duplicate, string then null", methodTasksGet,
				`{"resultType":"complete","resultType":null}`, coreMRTRDuplicated},
			{"mixed-type duplicate, null then string", methodTasksGet,
				`{"resultType":null,"resultType":"complete"}`, coreMRTRDuplicated},
			{"mixed-type duplicate, composite value", methodTasksGet,
				`{"resultType":{"x":1},"resultType":"complete"}`, coreMRTRDuplicated},
			{"duplicate, no string occurrence at all", methodTasksGet,
				`{"resultType":null,"resultType":null}`, coreMRTRDuplicated},
			// A SINGLE non-string occurrence stays non-selecting: there is no
			// duplication ambiguity and the value is not the reserved literal.
			{"single null resultType selects nothing", methodTasksGet,
				`{"resultType":null}`, coreMRTRAdmitted},
			{"case alias is extension data, never the discriminator", methodTasksGet,
				`{"resultType":"complete","ResultType":"input_required","status":"working"}`, coreMRTRAdmitted},
			{"case alias alone selects nothing", methodTasksGet,
				`{"ResultType":"input_required"}`, coreMRTRAdmitted},
			{"open-enum custom value is not the reserved literal", methodTasksGet,
				`{"resultType":"INPUT_REQUIRED"}`, coreMRTRAdmitted},
			{"the extension profile is not the core discriminator", methodTasksGet,
				stage7TaskInputRequiredReport, coreMRTRAdmitted},
			{"an incidental status is not the core discriminator", methodTasksCancel,
				`{"resultType":"complete","status":"input_required","inputRequests":{"a":{"m":1}}}`, coreMRTRAdmitted},
			{"a conforming complete result relays anywhere", "ping",
				`{"resultType":"complete","content":[]}`, coreMRTRAdmitted},
			{"no discriminator at all", "ping", `{"messages":[]}`, coreMRTRAdmitted},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := coreMRTRRefusalOn(tc.method, json.RawMessage(tc.result)); got != tc.want {
					t.Errorf("coreMRTRRefusalOn(%q) = %v, want %v", tc.method, got, tc.want)
				}
			})
		}
	})

	t.Run("unit: the classifier refuses the core variant under every task profile", func(t *testing.T) {
		for _, authority := range []mrtrAuthority{
			mrtrAuthorityTaskGet, mrtrAuthorityTaskCancel, mrtrAuthorityTaskHandle,
		} {
			cls := classifyMRTRResult(json.RawMessage(stage7CoreInputRequired), authority)
			if !cls.unsanctionedCore {
				t.Errorf("authority %d: the core discriminator must be UNSANCTIONED under a Tasks profile: %+v", authority, cls)
			}
			if len(cls.entries) != 0 {
				t.Errorf("authority %d: an unsanctioned result projects NO governed entries: %+v", authority, cls.entries)
			}
		}
		// The core-result contract is the one that sanctions it.
		cls := classifyMRTRResult(json.RawMessage(stage7CoreInputRequired), mrtrAuthorityCoreResult)
		if cls.unsanctionedCore || !cls.inputRequired || len(cls.entries) != 1 {
			t.Errorf("the core-result contract must still classify and project its governed entries: %+v", cls)
		}
		// M-1R2: a duplicated discriminator under a Tasks profile is its OWN
		// classification — never unsanctionedCore, never the literal's claim.
		for _, authority := range []mrtrAuthority{
			mrtrAuthorityTaskGet, mrtrAuthorityTaskCancel, mrtrAuthorityTaskHandle,
		} {
			dup := classifyMRTRResult(json.RawMessage(`{"resultType":"complete","resultType":null}`), authority)
			if !dup.ambiguousDiscriminator || dup.unsanctionedCore || dup.inputRequired || len(dup.entries) != 0 {
				t.Errorf("authority %d: a duplicated discriminator must classify as ambiguous only: %+v", authority, dup)
			}
		}
	})

	// The cancel profile has NO authoritative MRTR discriminator left, which is
	// what makes the mediation/release legs of handleTaskCancel unreachable. It is
	// pinned here so the edit that revives them cannot happen silently.
	t.Run("unit: the cancel profile has no authoritative discriminator", func(t *testing.T) {
		if mrtrAuthorityTaskCancel.coreResultTypeAuthoritative() || mrtrAuthorityTaskCancel.taskStatusAuthoritative() {
			t.Fatal("the tasks/cancel acknowledgement contract must interpret neither discriminator")
		}
		for _, body := range []string{
			`{"resultType":"complete","status":"input_required","inputRequests":{"a":{"m":1}}}`,
			`{"resultType":"complete","taskId":"t","status":"working"}`,
			normativeCompleteResult,
		} {
			if cls := classifyMRTRResult(json.RawMessage(body), mrtrAuthorityTaskCancel); cls.inputRequired {
				t.Errorf("a cancellation acknowledgement never classifies as MRTR: %s → %+v", body, cls)
			}
		}
	})

	t.Run("a mixed-type duplicate discriminator is refused end to end", func(t *testing.T) {
		// The r1 contrast's literal probe: `{"resultType":"complete","resultType":
		// null}` relayed 200 on every route because only string occurrences were
		// counted. Deny-closed now on BOTH kinds of route: the unsanctioned method
		// refuses through the guard, and the sanctioned method refuses through the
		// classifier's unreadable leg — the same 502 the string/string duplicate
		// already produced there.
		const mixedDup = `{"resultType":"complete","resultType":null}`
		for _, tc := range []struct {
			name string
			req  func(token string) *http.Request
		}{
			{"unsanctioned tasks/get", func(tk string) *http.Request {
				return taskReq(tk, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`)
			}},
			{"sanctioned tools/call", func(tk string) *http.Request {
				return toolsCallReq(tk, "search", `{}`)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				med := &fakeElicitationMediator{allow: true}
				rs, _, token := stage7GuardRS(t, mixedDup, med)

				rec := httptest.NewRecorder()
				rs.ServeHTTP(rec, tc.req(token))

				if rec.Code != http.StatusBadGateway {
					t.Fatalf("status = %d, want 502: a duplicated exact discriminator is ambiguous JSON no "+
						"conforming server produces, whatever the value types; body = %s", rec.Code, rec.Body.String())
				}
				if got := stage7MRTRCalls(med); got != 0 {
					t.Errorf("MRTR mediator calls = %d, want 0", got)
				}
			})
		}
	})

	t.Run("a duplicate refusal never invents the input_required literal", func(t *testing.T) {
		// M-1R2 (r2 contrast): the deny of a duplicated discriminator is correct,
		// but its REPRESENTATION lied — a `"complete"/null` duplicate was refused
		// with the wire and audit claiming the body carried `input_required`, a
		// literal neither of its two values holds. On an evidence control plane
		// that attributes to the upstream a different violation than the one
		// observed. The refusal keeps its 502; the recorded fact is now the
		// OBSERVED one — a duplicated/ambiguous discriminator, reason class
		// mrtr.malformed_result — and only a single exact string `input_required`
		// may produce mrtr.unsanctioned_origin.
		const mixedDup = `{"resultType":"complete","resultType":null}`
		for _, tc := range []struct {
			name string
			req  func(token string) *http.Request
		}{
			{"classifier route tasks/get", func(tk string) *http.Request {
				return taskReq(tk, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`)
			}},
			{"method-matrix route ping", func(tk string) *http.Request {
				return stage7GenericReq(tk, "ping", `{}`)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				token, jwks := stage7GuardToken(t)
				up := &taskUpstream{fn: func(UpstreamRequest) (json.RawMessage, error) {
					return json.RawMessage(mixedDup), nil
				}}
				aud := &taskAuditor{}
				rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, &taskMediatorFake{allow: true})
				mustInsertTask(t, rs, TaskRecord{TaskID: stage7GuardTaskID})

				rec := httptest.NewRecorder()
				rs.ServeHTTP(rec, tc.req(token))

				if rec.Code != http.StatusBadGateway {
					t.Fatalf("status = %d, want 502 (the deny stands); body = %s", rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "input") {
					t.Errorf("the wire claims a literal the body does not carry: %s", rec.Body.String())
				}
				sawClass := false
				for _, d := range aud.decisions {
					if strings.Contains(d.Reason, "input_required") {
						t.Errorf("an audit record affirms a literal the body does not carry: %q", d.Reason)
					}
					if strings.Contains(d.Reason, mrtrReasonMalformedResult) {
						sawClass = true
					}
				}
				if !sawClass {
					t.Errorf("no audit record carries the controlled malformed/ambiguous reason class %q", mrtrReasonMalformedResult)
				}
			})
		}

		// The shared cancel-ack predicate treats the ambiguous duplicate exactly
		// like the forbidden core variant: not an acknowledgement.
		if conformingCancelAck(true, json.RawMessage(mixedDup)) {
			t.Error("a duplicated-discriminator cancel result must not be a conforming acknowledgement")
		}

		// M-1R3 (r3 contrast): the SANCTIONED generic methods (prompts/get and the
		// ordinary resources/read) refuse the duplicate through the unreadable leg,
		// and their wire still said "ambiguous input-required result" — a literal
		// the body may not carry. The wire must state the observed duplication;
		// the audit disjunction was already honest. (The unreadable leg keeps its
		// projection category — no mrtr.* class is asserted here; that taxonomy is
		// part of the declared structural-validation residual.)
		t.Run("sanctioned generic wire", func(t *testing.T) {
			token, jwks := stage7GuardToken(t)
			up := &taskUpstream{fn: func(UpstreamRequest) (json.RawMessage, error) {
				return json.RawMessage(mixedDup), nil
			}}
			aud := &taskAuditor{}
			rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, &taskMediatorFake{allow: true})

			rec := httptest.NewRecorder()
			rs.ServeHTTP(rec, stage7GenericReq(token, "prompts/get", `{"name":"p"}`))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (the deny stands); body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "input") {
				t.Errorf("the wire claims a literal the body does not carry: %s", rec.Body.String())
			}
		})
	})

	t.Run("a duplicate key inside a governed entry is unreadable, never a plain relay", func(t *testing.T) {
		const result = `{"resultType":"input_required","inputRequests":` +
			`{"q1":{"method":"elicitation/create","method":"roots/list","params":{"message":"` + stage7GuardSecret + `"}}}}`
		med := &fakeElicitationMediator{allow: true}
		rs, _, token := stage7GuardRS(t, result, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: a governed entry two readers would read differently is unreadable; body = %s",
				rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), stage7GuardSecret) {
			t.Errorf("an unreadable governed payload was relayed: %s", rec.Body.String())
		}
		if got := stage7MRTRCalls(med); got != 0 {
			t.Errorf("MRTR mediator calls = %d, want 0: the gateway never mediates what it admits it cannot project", got)
		}
	})

	t.Run("a case-alias of the governed member is extension data, released under evidence", func(t *testing.T) {
		// `InputRequests` is not `inputRequests`: the governed member is ABSENT, so
		// there is nothing to inspect — and, per the r2 review's P0-2, zero
		// inspections is still not zero evidence: the classified variant claims its
		// release child.
		const result = `{"resultType":"complete","taskId":"` + stage7GuardTaskID + `","status":"input_required",` +
			`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:00Z","ttlMs":60000,` +
			`"InputRequests":{"q1":{"message":"` + stage7GuardSecret + `"}}}`
		med := &fakeElicitationMediator{allow: true}
		rs, aud, token := stage7GuardRS(t, result, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"`+stage7GuardTaskID+`"}`))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if got := stage7MRTRCalls(med); got != 0 {
			t.Errorf("MRTR mediator calls = %d, want 0: a case variant is extension data, not the governed member", got)
		}
		if rel := s504ReleaseOps(&aud.fakeEvidenceJournal); len(rel) != 1 {
			t.Errorf("release operations = %d, want 1: the classified variant is released under evidence", len(rel))
		}
	})
}
