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

// rs_s504_round4_test.go — Stage-5 review round 4 (Codex,
// an internal design note (not shipped), VERDICT:
// MUST-FIX-BEFORE-LAND): three gaps IN the context-led redesign (b176e795), each
// pinned by the reviewer's OWN concrete failing transaction.
//
// RED-FIRST: findings 1 and 2 were written and RUN against b176e795 BEFORE the
// fix and observed to FAIL (the governed payload was released). Finding 3 is the
// INVERTED direction — protocol-valid traffic currently REFUSED must go
// red-then-green. The literal failing output lives in
// an internal design note (not shipped)
//
// PRIMARY SOURCE for every protocol shape asserted here is the vendored MCP
// 2026-07-28 RC schema (an internal design note (not shipped), upstream 76346843):
//   - ResultType = "complete" | "input_required" | string — OPEN enum (schema.ts:208-216)
//   - Result requires exact resultType in this revision; extensible     (schema.ts:223-235)
//   - InputRequests keys are SERVER-ASSIGNED identifiers                 (schema.ts:544-555)
//   - InputRequiredResult: inputRequests OR requestState                (schema.ts:571-595)
//   - clientCapabilities are PER-REQUEST; never inferred                (schema.ts:63-98)

// s504R4ContentDenyMediator denies iff the inspected Content contains its
// trigger substring — a content-driven mediator, unlike taskMediatorFake whose
// verdict is fixed. It proves WHAT the mediator saw, not just that it was called.
type s504R4ContentDenyMediator struct {
	trigger string
	calls   []ElicitationInspectionInput
}

func (m *s504R4ContentDenyMediator) Mediate(_ context.Context, in ElicitationInspectionInput) ElicitationInspectionDecision {
	m.calls = append(m.calls, in)
	if strings.Contains(string(in.Content), m.trigger) {
		return ElicitationInspectionDecision{Allow: false, Reason: "trigger seen in inspected content"}
	}
	return ElicitationInspectionDecision{Allow: true}
}

// s504R4CapNoTasksReq is a tools/call request that carries the required
// per-request clientCapabilities OBJECT but does NOT declare the Tasks
// extension, so canonicalToolCallParams.DeclaresTasks is false and the
// synchronous core-result contract applies (schema.ts:63-98).
func s504R4CapNoTasksReq(token, args string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":` + args +
		`,"_meta":{"io.modelcontextprotocol/clientCapabilities":{"extensions":{}}}}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// --- Round-4 finding 1 (P0) ---------------------------------------------------

// TestS504R4StrictDecodeErrorOnCoreMRTRWithheld: a strict-decode error on an
// EXACT core input_required result must have ONE fail-safe meaning — UNREADABLE
// (refuse) — never "not MRTR" (plain release). extractMRTRInputRequests returned
// (nil,false) on a decode failure, which is NEITHER unreadable NOR mediate, so
// the settled result reached writeResult with the mediator never called.
//
// Reviewer's concrete failing transaction (round-4 finding 1), verbatim: a
// genuine exact input_required result with governed inputRequests, plus an
// unrelated duplicate extension key that makes decodeStrictJSON fail.
func TestS504R4StrictDecodeErrorOnCoreMRTRWithheld(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const result = `{"resultType":"input_required","inputRequests":{"need":{"method":"roots/list",` +
		`"params":{"secret":"s504-r4-release-secret"}}},"x":0,"x":0}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(result), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	// allow=true would still forward if the classifier reached the mediator; the
	// bug is that it never does — the plain leg fires first.
	med := &taskMediatorFake{allow: true}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, s504R4CapNoTasksReq(token, `{}`))

	if strings.Contains(rec.Body.String(), "s504-r4-release-secret") {
		t.Fatalf("a strict-decode failure on an exact core MRTR result released the governed payload "+
			"on the plain leg: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (an undecodable governed result is unreadable — refused, not relayed); body: %s",
			rec.Code, rec.Body.String())
	}
}

// TestS504R4StrictDecodeErrorOnTaskGetWithheld proves the finding-1 fail-safe is
// implemented CONSISTENTLY at every site, not only the synchronous core leg
// (round-4 brief: "one fail-safe meaning ... at every site"). A ledger-bound
// tasks/get whose authoritative response is an input-required payload plus an
// unrelated duplicate extension key must be WITHHELD, never relayed on the plain
// leg. RED-first: before the fix, task-get relayed the undecodable governed
// result (rs.go plain writeResult tail).
func TestS504R4StrictDecodeErrorOnTaskGetWithheld(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const result = `{"resultType":"input_required","status":"input_required",` +
		`"inputRequests":{"need":{"method":"roots/list","params":{"secret":"s504-r4-taskget-secret"}}},"x":0,"x":0}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(result), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	med := &taskMediatorFake{allow: true}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-s504-r4-get"})

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r4-get"}`))

	if strings.Contains(rec.Body.String(), "s504-r4-taskget-secret") {
		t.Fatalf("a strict-decode failure on a governed tasks/get result released the payload: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (an undecodable governed tasks/get result is unreadable — refused); body: %s",
			rec.Code, rec.Body.String())
	}
}

// --- Round-4 finding 2 (P0) ---------------------------------------------------

// TestS504R4MRTRMapIdentifierInspected: the InputRequests map KEY is a
// server-assigned, arbitrary, GOVERNED identifier (schema.ts:544-555). The
// projection passed only the entry VALUE as Content, so a policy token placed
// solely in the identifier was released without ever being inspected.
//
// Reviewer's concrete failing input (round-4 finding 2), verbatim: a mediator
// whose deny trigger is the map KEY sees no trigger, allows, and the anchored
// raw result releases the identifier.
func TestS504R4MRTRMapIdentifierInspected(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const result = `{"resultType":"input_required","inputRequests":{"s504-r4-secret-map-key":{"method":"roots/list"}}}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(result), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	med := &s504R4ContentDenyMediator{trigger: "s504-r4-secret-map-key"}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

	if strings.Contains(rec.Body.String(), "s504-r4-secret-map-key") {
		t.Fatalf("a policy token in the map identifier was released unmediated: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (the identifier must be inspected and denied); body: %s",
			rec.Code, rec.Body.String())
	}
	if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r4-secret-map-key") {
		t.Fatalf("the map identifier must be part of the inspected content: %+v", med.calls)
	}
}

// --- Round-4 finding 3 (P1, INVERTED red) -------------------------------------

// TestS504R4DeclaredTasksRelaysCompleteResult: declaring Tasks must not newly
// refuse a protocol-valid complete open result. selectDeclaredTaskHandle
// strict-decoded the WHOLE open result before checking whether exact resultType
// is "task", so a duplicate extension key made it refuse (502) a complete result
// that at 276d6d4e relayed. The declared-Tasks path must check the exact
// discriminator FIRST and relay a valid complete open result.
//
// Reviewer's concrete failing transaction (round-4 finding 3), verbatim.
func TestS504R4DeclaredTasksRelaysCompleteResult(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const result = `{"resultType":"complete","content":[],"x":1,"x":1}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(result), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	med := &taskMediatorFake{allow: true}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

	rec := httptest.NewRecorder()
	// toolsCallReq DECLARES the Tasks extension per-request.
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a valid complete open result must relay, not be refused before "+
			"the exact discriminator is checked); body: %s", rec.Code, rec.Body.String())
	}
	// The raw complete result relays verbatim (the duplicate extension key rides
	// along — RFC 8259 makes unique names a SHOULD, and the pin admits extensions).
	if !strings.Contains(rec.Body.String(), `"x":1`) {
		t.Fatalf("the complete open result must relay intact: %s", rec.Body.String())
	}
	if len(med.calls) != 0 {
		t.Fatalf("a complete result carries no governed MRTR content to mediate: %+v", med.calls)
	}
}

// TestS504R4DeclaredTaskHandleStillRefusesUndecodable is the CONTROL that the
// finding-3 fix is NARROW: a result that DID select the task variant (exact
// resultType:"task") with a duplicate key is an ungovernable task identity and is
// STILL refused (502) — green before and after the fix. Only NON-task results
// escape the closed task validator.
func TestS504R4DeclaredTaskHandleStillRefusesUndecodable(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const handle = `{"resultType":"task","taskId":"task-s504-r4","status":"working","x":1,"x":1}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(handle), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	aud := &taskAuditor{}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, nil)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (an undecodable task handle is an ungovernable identity — refused); body: %s",
			rec.Code, rec.Body.String())
	}
	// The task must not have registered (its identity could not be bound).
	rec2 := httptest.NewRecorder()
	rs.ServeHTTP(rec2, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r4"}`))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("tasks/get status = %d, want 403 (an ungovernable handle is never registered); body: %s",
			rec2.Code, rec2.Body.String())
	}
}
