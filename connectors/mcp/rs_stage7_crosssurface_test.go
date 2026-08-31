// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rs_stage7_crosssurface_test.go — the stage-7 B-bis round-2 CROSS-SURFACE
// CONTRACT (Codex r2 F-1): the durable settlement vocabulary has ONE semantic
// unit everywhere. The PARENT operation records the dispatch fact — an observed
// round trip settles `completed`, the moment it is observed — and a governance
// decision that then withholds the observed response records that disposition
// on the RESPONSE-RELEASE CHILD operation, settled `withheld`. The same
// delivered-then-denied business fact must produce the SAME parent state and
// the SAME child state on every surface that owns a parent claim: tools/call,
// tasks/get, tasks/cancel, elicitation/create and resources/read of ui://.
//
// This is the regression that keeps the F-1 ambiguity from returning through a
// new leg: before round 2, generic MRTR parents settled `completed` while the
// UI/elicitation parents settled `withheld` for the identical situation — two
// units of meaning in one durable vocabulary.
//
// Every subtest drives ResourceServer.ServeHTTP.
//
// MUTATIONS VERIFIED (round-2 method):
//
//   - the SHARED child writer (settleWithheldRelease) flipped to
//     DispatchBlocked turns ALL FIVE subtests red on both the withheld-child
//     and the blocked==0 assertions;
//   - REMOVING each surface's child-settlement call — the tools/call hijack
//     leg's settleWithheldRelease, taskGetWithheld (tasks/get hijack),
//     taskCancelWithheld (tasks/cancel hijack), elicitationWithheld
//     (response-deny leg) and uiMRTRWithheld (ui:// hijack) — turns EXACTLY
//     its own subtest red on the withheld-child assertion (the silent
//     no-durable-disposition shape this contract exists to forbid);
//   - the elicitation PARENT settling DispatchWithheld again (the round-1
//     shape) turns its subtest red on the parent-completed assertion.
func TestStage7MRTRDenyCrossSurfaceContract(t *testing.T) {
	const secret = "xs-stage7-secret"

	// assertUnit is THE contract: completed parents only (wantCompleted counts
	// the surface's non-release operations — 1 everywhere except the durable
	// task handle, whose flow also settles the mcp.task.track registration),
	// one withheld release child, nothing released, no blocked settlement
	// anywhere.
	assertUnit := func(t *testing.T, j *fakeEvidenceJournal, rec *httptest.ResponseRecorder, wantCompleted int) {
		t.Helper()
		if rec.Code == http.StatusOK {
			t.Fatalf("a denied release answered 200: %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("the denied payload leaked into the refusal: %s", rec.Body.String())
		}
		if got := j.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
			t.Errorf("withheld RELEASE-CHILD settlements = %d, want exactly 1: an observed-then-denied response must record its disposition on the release child, on THIS surface exactly as on every other", got)
		}
		if got := j.settledActionCount(s504ReleaseActionPrefix, DispatchCompleted); got != 0 {
			t.Errorf("completed release children = %d, want 0: nothing was released", got)
		}
		if got := j.settledCount(DispatchCompleted); got != wantCompleted {
			t.Errorf("completed settlements = %d, want exactly %d: the PARENT records the observed dispatch — completed — regardless of the release verdict that followed", got, wantCompleted)
		}
		if got := j.settledCount(DispatchBlocked); got != 0 {
			t.Errorf("blocked settlements = %d, want 0: the round trip finished, so `blocked` anywhere would falsely claim nothing reached the upstream", got)
		}
	}

	t.Run("tools/call", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			return json.RawMessage(`{"resultType":"input_required",` +
				`"inputRequests":{"q1":{"message":"` + secret + `"}},"requestState":"s1"}`), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: false, reason: "cross-surface deny"}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		if len(med.calls) == 0 {
			t.Fatal("the mediator was never consulted; the subtest would pass for the wrong reason")
		}
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 1)
	})

	t.Run("tasks/get", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksGet {
				return json.RawMessage(`{"taskId":"task-xs-get","status":"input_required",` +
					`"inputRequests":{"q1":{"message":"` + secret + `"}}}`), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: false, reason: "cross-surface deny"}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-xs-get"})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"task-xs-get"}`))
		if len(med.calls) == 0 {
			t.Fatal("the mediator was never consulted; the subtest would pass for the wrong reason")
		}
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 1)
	})

	t.Run("tasks/cancel", func(t *testing.T) {
		// RE-TRIGGERED 2026-07-30 (stage-7 P0-3). This subtest used to drive a
		// mediator DENY over the exact core discriminator, which the cancel profile
		// accepted "for compatibility". The Tasks extension defines CancelTaskResult
		// as the ack-only `resultType:"complete"` shape, so that variant is now
		// REFUSED as an upstream protocol fault and never reaches the mediator — the
		// cancel leg has no mediatable MRTR discriminator left. The contract this
		// file exists for is unchanged and is still exercised here: the SAME
		// delivered-then-refused business fact must produce the same parent state and
		// the same withheld child on this surface as on every other.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksCancel {
				return json.RawMessage(`{"resultType":"input_required",` +
					`"inputRequests":{"q1":{"message":"` + secret + `"}}}`), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: false, reason: "cross-surface deny"}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-xs-cancel"})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksCancel, `{"taskId":"task-xs-cancel"}`))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (the unsanctioned core discriminator on a cancellation ack); "+
				"the subtest would otherwise pass for the wrong reason; body = %s", rec.Code, rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("the mediator was consulted for a result no conforming server may send: %+v", med.calls)
		}
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 1)
	})

	t.Run("elicitation response", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"action":"accept","content":{"password":"` + secret + `"}}`)}
		aud := &recordingAuditor{}
		med := &channelAwareMediator{requestAllow: true, responseDeny: true, responseReason: "cross-surface deny"}
		rs := newS504RS(t, jwks, up, aud, nil, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, elicitationReq(token, "Enter password", ""))
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 1)
	})

	t.Run("tools/call task handle", func(t *testing.T) {
		// The round-3 surface (Codex r2, F-1 residual): a DURABLE TASK HANDLE is
		// itself an MRTR carrier, and its denied relay must record the identical
		// disposition. wantCompleted is 2: the tools/call parent AND the
		// mcp.task.track registration both settled completed — the registration
		// stands (the upstream task stays governed and sweepable), only the
		// relay was withheld.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(`{"resultType":"task","taskId":"task-xs-handle","status":"input_required",` +
					`"inputRequests":{"q1":{"message":"` + secret + `"}}}`), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: false, reason: "cross-surface deny"}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		if len(med.calls) == 0 {
			t.Fatal("the mediator was never consulted; the subtest would pass for the wrong reason")
		}
		if strings.Contains(rec.Body.String(), "task-xs-handle") {
			t.Fatalf("the denied handle leaked its task id: %s", rec.Body.String())
		}
		// The registration must stand: the upstream task stays governed.
		if _, ok := rs.taskLedger.get("task-xs-handle"); !ok {
			t.Fatal("the denied handle's registration must be retained (sweepable, reconcilable)")
		}
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 2)
	})

	t.Run("ui:// resources/read", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"resultType":"input_required",` +
			`"inputRequests":{"q1":{"method":"elicitation/create","params":{"message":"` + secret + `"}}}}`)}
		aud := &recordingAuditor{}
		med := &fakeElicitationMediator{allow: false, reason: "cross-surface deny"}
		rs := newS504RS(t, jwks, up, aud, nil, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))
		assertUnit(t, &aud.fakeEvidenceJournal, rec, 1)
	})
}

// crashOnBodyWriteRecorder accepts the status and headers but PANICS on the
// first body write — the ServeHTTP-level simulation of a process crash at the
// exact moment the refusal bytes leave the process.
type crashOnBodyWriteRecorder struct {
	*httptest.ResponseRecorder
}

func (c *crashOnBodyWriteRecorder) Write([]byte) (int, error) {
	panic("simulated crash while writing the deny response")
}

// TestStage7WithheldChildDurableBeforeDenyWrite pins the round-3 ORDERING half
// of the contract (Codex r2, F-1 residual): on a post-observation MRTR deny the
// withheld release child settles BEFORE the deny response is written. A crash
// while the refusal is leaving the process must find the disposition already
// durable — the same principle that settles the parent the moment the dispatch
// is observed, applied to the child.
//
// MUTATION VERIFIED (round-3 method): inverting the order inside
// mediateMRTRResultEntries — writeMediationDeny before settleChild — turns this
// red with the property message below.
func TestStage7WithheldChildDurableBeforeDenyWrite(t *testing.T) {
	const secret = "xs-stage7-crash-secret"
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"resultType":"input_required",` +
			`"inputRequests":{"q1":{"message":"` + secret + `"}},"requestState":"s1"}`), nil
	}}
	aud := &taskAuditor{}
	med := &taskMediatorFake{allow: false, reason: "crash-ordering deny"}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

	w := &crashOnBodyWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	crashed := false
	func() {
		defer func() {
			if recover() != nil {
				crashed = true
			}
		}()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
	}()
	if !crashed {
		t.Fatal("the crash writer never fired; the ordering was not exercised")
	}
	if len(med.calls) == 0 {
		t.Fatal("the mediator was never consulted; the test would pass for the wrong reason")
	}
	if got := aud.fakeEvidenceJournal.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Fatalf("withheld RELEASE-CHILD settlements after the crash = %d, want exactly 1: the withheld child must be durable before the refusal leaves the process", got)
	}
	if got := aud.fakeEvidenceJournal.settledCount(DispatchCompleted); got != 1 {
		t.Fatalf("completed settlements after the crash = %d, want exactly 1 (the parent's dispatch fact was durable before any verdict)", got)
	}
}
