// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// rs_s504_round3_test.go — Stage-5 round 3 + the design adjudication
// (an internal design note (not shipped),
// PREMISE: UNSOUND). The adjudication moves the "must this be mediated?"
// decision from the OPEN result body to the AUTHORITATIVE LOCAL CONTEXT the
// gateway already holds (the method called, the task registration and ledger
// record, the per-request task-capability declaration); the body supplies only
// WHAT is mediated, never whether.
//
// RED-FIRST: every test in this file marked RED below was written and RUN
// against commit 276d6d4e BEFORE the redesign existed; the literal failing
// output lives in an internal design note (not shipped)
// Subtests marked CONTROL pin behavior that was already correct and must
// SURVIVE the redesign (they are green before and after — declared, not
// hidden).
//
// PRIMARY SOURCE for every protocol shape asserted here is the vendored MCP
// 2026-07-28 RC schema (an internal design note (not shipped), upstream 76346843):
//   - ResultType = "complete" | "input_required" | string — OPEN enum (schema.ts:208-216)
//   - Result requires exact resultType in this revision; extensible     (schema.ts:223-235)
//   - InputRequests is a map with NO minimum cardinality               (schema.ts:553-555)
//   - InputRequiredResult: inputRequests OR requestState                (schema.ts:571-595)
//   - clientCapabilities are PER-REQUEST; never inferred                (schema.ts:63-98)
//   - ClientCapabilities.extensions declares e.g. the Tasks extension   (schema.ts:776-786)
//   - ToolUseContent.input is an arbitrary object                       (schema.ts:2393-2411)
//   - ToolResultContent.content + structuredContent are arbitrary JSON  (schema.ts:2432-2456)

// s504R3TasksDeclaredMeta is the exact per-request Tasks-extension declaration
// (schema.ts:92-98 requires clientCapabilities on every request; schema.ts:776-786
// places extension declarations inside it). A string literal on purpose: the RED
// tests compiled against the pre-redesign tree.
const s504R3TasksDeclaredMeta = `"_meta":{"io.modelcontextprotocol/clientCapabilities":` +
	`{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`

// --- Round-3 finding 1 (SURVIVES) — structured sampling keys ------------------

// TestS504R3SamplingStructuredKeysMediated: `ToolUseContent.input` is an
// arbitrary object and `ToolResultContent.structuredContent` is arbitrary JSON
// (schema.ts:2393-2411,2432-2456), but the round-2 collector walked object
// VALUES only, so a pin-valid request whose only sensitive text lives in an
// object KEY produced empty inspection content and bypassed the mediator
// entirely. The redesign projects the CANONICAL JSON BYTES of each arbitrary
// structured subtree — keys, scalars and structure.
func TestS504R3SamplingStructuredKeysMediated(t *testing.T) {
	send := func(t *testing.T, med ElicitationMediator, params string) (*httptest.ResponseRecorder, *fakeUpstream) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
		rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)
		body := `{"jsonrpc":"2.0","id":41,"method":"sampling/createMessage","params":` + params + `}`
		req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, req)
		return rec, up
	}
	// The reviewer's literal failing input (round-3 blocking finding 1), verbatim.
	const keyOnlyInput = `{"messages":[{"role":"user","content":{"type":"tool_use","id":"u1","name":"exec",` +
		`"input":{"s504-r3-secret":true}}}],"maxTokens":32}`

	t.Run("tool_use input object keys reach the mediator", func(t *testing.T) { // RED
		med := &fakeElicitationMediator{allow: false, reason: "secret in a structured key"}
		rec, up := send(t, med, keyOnlyInput)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (object keys of tool_use.input must be mediated); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r3-secret") {
			t.Fatalf("the structured subtree's keys must be inspected: %+v", med.calls)
		}
	})

	t.Run("structuredContent object keys reach the mediator", func(t *testing.T) { // RED
		med := &fakeElicitationMediator{allow: false, reason: "secret in a structured key"}
		rec, up := send(t, med,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","toolUseId":"u2",`+
				`"content":[],"structuredContent":{"s504-r3-sc-key":1}}]}],"maxTokens":32}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (structuredContent keys must be mediated); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r3-sc-key") {
			t.Fatalf("the structuredContent keys must be inspected: %+v", med.calls)
		}
	})

	// The adjudication's capability check (§4) makes this the mandatory POSITIVE
	// CONTROL: with an ALLOW decision the same pin-valid request must still
	// forward — an implementation that rejects requests merely for containing
	// object keys violates the design.
	t.Run("allow still forwards the same request", func(t *testing.T) { // RED (mediator sees zero calls today)
		med := &fakeElicitationMediator{allow: true}
		rec, up := send(t, med, keyOnlyInput)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (allowed key-only content must forward); body: %s",
				rec.Code, rec.Body.String())
		}
		if !up.called {
			t.Fatal("an allowed sampling request must forward")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r3-secret") {
			t.Fatalf("the allow path must still have inspected the keys: %+v", med.calls)
		}
	})
}

// --- Round-3 finding 4 (SURVIVES) — empty InputRequests map -------------------

// TestS504R3EmptyInputRequestsMapRelays: `InputRequests` is a map with NO
// minimum cardinality (schema.ts:553-555) and a request-state-only input
// requirement is expressly permitted (schema.ts:571-595). Round 2 conflated
// `{}` with a wrong-typed member and refused a pin-valid result. The redesign
// yields ZERO entries for an absent member or `{}`: no mediator call, no
// structural refusal, plain relay.
func TestS504R3EmptyInputRequestsMapRelays(t *testing.T) {
	// The reviewer's literal failing input (round-3 blocking finding 4), verbatim.
	const result = `{"resultType":"input_required","inputRequests":{},"requestState":"opaque-state"}`

	send := func(t *testing.T, med ElicitationMediator) (*httptest.ResponseRecorder, *taskAuditor, *taskMediatorFake) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(result), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		mf, _ := med.(*taskMediatorFake)
		return rec, aud, mf
	}

	t.Run("mediator wired", func(t *testing.T) { // RED (502 today)
		rec, aud, med := send(t, &taskMediatorFake{allow: true})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a pin-valid empty map is zero entries, not a defect); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "opaque-state") {
			t.Fatalf("the request-state-only result must relay intact: %s", rec.Body.String())
		}
		if med != nil && len(med.calls) != 0 {
			t.Fatalf("zero entries mean zero mediator calls: %+v", med.calls)
		}
		// INVERTED 2026-07-29 (Codex r2 review, P0-2). This asserted "zero governed
		// entries plan no release child", encoding the reading that the
		// release child follows the MEDIATION. Measured against a release journal
		// that refuses every `mcp.release.*` action, that reading is an
		// evidence-or-refuse bypass: an upstream picks a variant the classifier
		// itself calls MRTR — requestState-only, or this valid empty map — and
		// obtains a release no hostile or unavailable journal can stop, carrying
		// the server-controlled opaque state out with it.
		//
		// Zero entries still means zero mediator calls (asserted above). It does
		// not mean zero evidence.
		if rel := s504ReleaseOps(&aud.fakeEvidenceJournal); len(rel) != 1 {
			t.Fatalf("a classified input_required result must claim its release child even with zero governed entries; got %d", len(rel))
		}
	})

	t.Run("mediator nil", func(t *testing.T) { // CONTROL (nil-mediator clean pass, adjudication §2)
		rec, _, _ := send(t, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "opaque-state") {
			t.Fatalf("the result must relay intact: %s", rec.Body.String())
		}
	})
}

// --- Round-3 finding 3 (DISSOLVES) — authority comes from context -------------

// TestS504R3StatusAuthorityByContext is the adjudication's central acceptance
// matrix row: ONE open result body has DIFFERENT MRTR meaning under different
// method/task context, so no body-only classifier can be right in both. `status`
// has NO authority in a synchronous core tools/call result (it is extension data
// on an open Result, schema.ts:223-235); it is authoritative ONLY under the
// caller-selected task contracts (the ledger-bound tasks/get; never the
// tasks/cancel acknowledgement).
func TestS504R3StatusAuthorityByContext(t *testing.T) {
	// The reviewer's literal failing input (round-3 blocking finding 3), verbatim.
	const openBytes = `{"resultType":"complete","content":[],"status":"input_required",` +
		`"inputRequests":"ordinary extension data"}`

	t.Run("tools/call: exact status on a complete result relays", func(t *testing.T) { // RED (502 today)
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(openBytes), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (status has no authority on a core result); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "ordinary extension data") {
			t.Fatalf("the extension members must relay intact: %s", rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("extension data is not governed MRTR content: %+v", med.calls)
		}
	})

	t.Run("tasks/get: the same bytes are governed and refuse", func(t *testing.T) { // CONTROL
		// Under the ledger-selected task contract, exact status IS authoritative and
		// the selected `inputRequests` is present with the WRONG type — the retained
		// projection-integrity refusal (502, no raw body) applies.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksGet {
				return json.RawMessage(openBytes), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-s504-r3-ctx"})
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r3-ctx"}`))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (the selected governed member is unreadable); body: %s",
				rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "ordinary extension data") {
			t.Fatalf("an unreadable governed payload must never be released: %s", rec.Body.String())
		}
		// Stage-7 B-bis round 2: the unreadable refusal of an OBSERVED governed
		// tasks/get result records its disposition on the release child. MUTATION
		// VERIFIED: removing the taskGetWithheld call on the unreadable leg turns
		// this red.
		if got := aud.fakeEvidenceJournal.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
			t.Errorf("withheld RELEASE-CHILD settlements = %d, want exactly 1 (the unreadable refusal is a durable release disposition)", got)
		}
	})

	t.Run("tasks/cancel: status is never task state", func(t *testing.T) { // RED (mediated today)
		// The cancellation acknowledgement is not an authoritative task-status read
		// (adjudication §2): exact status/inputRequests on a complete ack are
		// preserved extension data, never a mediation trigger.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksCancel {
				return json.RawMessage(`{"resultType":"complete","status":"input_required",` +
					`"inputRequests":{"need":{"message":"s504-r3-cancel-ext"}}}`), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-s504-r3-cancel"})
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksCancel, `{"taskId":"task-s504-r3-cancel"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "s504-r3-cancel-ext") {
			t.Fatalf("the acknowledgement's extension members must relay intact: %s", rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("a task-cancel body never selects its own mediation: %+v", med.calls)
		}
		if rel := s504ReleaseOps(&aud.fakeEvidenceJournal); len(rel) != 0 {
			t.Fatalf("an unmediated ack plans no release child; got %d", len(rel))
		}
	})
}

// --- Round-3 finding 2 (DISSOLVES) — exact discriminator, no alias folding ----

// TestS504R3AliasPairsFollowExactDiscriminator: on an open Result, `ResultType`
// and other case variants of reserved members are EXTENSION members
// (schema.ts:208-216,223-235). The exact lowercase discriminator alone decides:
// exact `complete` relays whatever aliases ride along; exact `input_required`
// selects mediation FROM ITS EXACT MEMBERS whatever aliases ride along. There
// is no ordering problem once no case-folding classifier participates.
func TestS504R3AliasPairsFollowExactDiscriminator(t *testing.T) {
	send := func(t *testing.T, result string, med *taskMediatorFake) (*httptest.ResponseRecorder, *taskAuditor) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(result), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		return rec, aud
	}

	t.Run("reverse pair relays on exact complete", func(t *testing.T) { // CONTROL
		// The reviewer's literal round-3 finding-2 input, verbatim: the exact reader
		// sees `complete`; the earlier `ResultType` alias is valid extension data.
		med := &taskMediatorFake{allow: true}
		rec, _ := send(t, `{"ResultType":"input_required","resultType":"complete","content":[],`+
			`"inputRequests":{"need":{"method":"roots/list"}}}`, med)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (exact complete is authoritative); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "roots/list") {
			t.Fatalf("the extension result must relay intact: %s", rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("extension data on a complete result is never mediated: %+v", med.calls)
		}
	})

	t.Run("mirror: exact input_required is mediated, not refused", func(t *testing.T) { // RED (502 today)
		// The round-1 exact/alias mirror: exact lowercase `input_required` selects
		// core MRTR REGARDLESS of the `ResultType` alias — the payload is mediated
		// from the exact members, and a deny withholds it. Refusing it outright was
		// the capability cut the adjudication removes.
		med := &taskMediatorFake{allow: false, reason: "secret in the input request"}
		rec, _ := send(t, `{"resultType":"input_required","ResultType":"complete",`+
			`"inputRequests":{"need":{"message":"s504-r3-mirror-secret"}},"requestState":"s1"}`, med)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (mediation deny on the exact governed payload); body: %s",
				rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "s504-r3-mirror-secret") {
			t.Fatalf("a denied MRTR payload leaked: %s", rec.Body.String())
		}
		if len(med.calls) == 0 || !strings.Contains(string(med.calls[0].Content), "s504-r3-mirror-secret") {
			t.Fatalf("the exact governed payload must reach the mediator: %+v", med.calls)
		}
	})

	t.Run("mirror with allow releases through the anchored child", func(t *testing.T) { // RED (502 today)
		med := &taskMediatorFake{allow: true}
		rec, aud := send(t, `{"resultType":"input_required","ResultType":"complete",`+
			`"inputRequests":{"need":{"message":"s504-r3-mirror-ok"}},"requestState":"s1"}`, med)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (mediated and released); body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "s504-r3-mirror-ok") {
			t.Fatalf("the released result must be the exact upstream result: %s", rec.Body.String())
		}
		rel := s504ReleaseOps(&aud.fakeEvidenceJournal)
		if len(rel) != 1 {
			t.Fatalf("release operations = %d, want exactly 1 (a mediated release is anchored)", len(rel))
		}
		if st := aud.settledState(rel[0]); st != DispatchCompleted {
			t.Fatalf("the release settled %q, want completed", st)
		}
	})
}

// --- Context selection: the per-request task-capability fact ------------------

// TestS504R3TaskContractRequiresDeclaredCapability: the pin makes client
// capabilities PER-REQUEST and forbids inferring them (schema.ts:63-98,92-98);
// a server MUST NOT honor an extension the request did not declare (-32021,
// schema.ts:503-505). Exact `resultType:"task"` is therefore only a custom core
// ResultType string until the request's OWN Tasks-extension declaration
// (schema.ts:776-786) gives it the CreateTaskResult meaning. A permissive
// task-marker probe must not select the contract.
func TestS504R3TaskContractRequiresDeclaredCapability(t *testing.T) {
	const handle = `{"resultType":"task","taskId":"task-s504-r3-cap","status":"working"}`
	build := func(t *testing.T) (*ResourceServer, *taskAuditor, string) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(handle), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, nil)
		return rs, aud, token
	}
	trackOps := func(aud *taskAuditor) int {
		n := 0
		for _, op := range aud.fakeEvidenceJournal.claimedOperations() {
			if aud.fakeEvidenceJournal.action(op) == "mcp.task.track" {
				n++
			}
		}
		return n
	}

	t.Run("undeclared: a task-shaped result is open extension data", func(t *testing.T) { // RED (registered today)
		rs, aud, token := build(t)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, customToolsCallReq(token, `{"name":"search","arguments":{}}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a custom resultType relays on an open result); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "task-s504-r3-cap") {
			t.Fatalf("the extension result must relay intact: %s", rec.Body.String())
		}
		if got := trackOps(aud); got != 0 {
			t.Fatalf("track operations = %d, want 0 (no declared capability, no task contract)", got)
		}
		// The gateway holds NO task authority for this id: the ledger never saw it.
		rec2 := httptest.NewRecorder()
		rs.ServeHTTP(rec2, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r3-cap"}`))
		if rec2.Code != http.StatusForbidden {
			t.Fatalf("tasks/get status = %d, want 403 (task never registered); body: %s",
				rec2.Code, rec2.Body.String())
		}
	})

	t.Run("declared: the handle contract selects and registers", func(t *testing.T) { // CONTROL
		rs, aud, token := build(t)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, customToolsCallReq(token,
			`{"name":"search","arguments":{},`+s504R3TasksDeclaredMeta+`}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		if got := trackOps(aud); got != 1 {
			t.Fatalf("track operations = %d, want 1 (declared capability selects the task contract)", got)
		}
		rec2 := httptest.NewRecorder()
		rs.ServeHTTP(rec2, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r3-cap"}`))
		if rec2.Code != http.StatusOK {
			t.Fatalf("tasks/get status = %d, want 200 (registered under the declared contract); body: %s",
				rec2.Code, rec2.Body.String())
		}
	})
}

// --- Post-redesign unit pins (written AFTER the fix; MUTATION-PROVED) ---------
//
// The tests below were written after the redesign went green, so they are NOT
// RED-first evidence. Their non-vacuity is proved by the reproducible mutation
// records MUT-R3-1 / MUT-R3-2 in
// an internal design note (not shipped) (exact sed command,
// full before/mutated/restored sha256, literal failing and passing output).

// TestS504R3ExtractMRTRByAuthority pins the exact discriminator procedure of
// the adjudicated classifier (adjudication §1), profile by profile.
func TestS504R3ExtractMRTRByAuthority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      string
		authority   mrtrAuthority
		wantEntries int
		wantUnread  bool
	}{
		// Core: only exact resultType:"input_required" selects MRTR.
		{"core exact input_required", `{"resultType":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityCoreResult, 1, false},
		{"core status has no authority", `{"resultType":"complete","content":[],"status":"input_required","inputRequests":"x"}`,
			mrtrAuthorityCoreResult, 0, false},
		{"core custom open-enum value", `{"resultType":"INPUT_REQUIRED","inputRequests":{"a":{}}}`,
			mrtrAuthorityCoreResult, 0, false},
		{"core empty map is zero entries", `{"resultType":"input_required","inputRequests":{},"requestState":"s"}`,
			mrtrAuthorityCoreResult, 0, false},
		{"core absent member is zero entries", `{"resultType":"input_required","requestState":"s"}`,
			mrtrAuthorityCoreResult, 0, false},
		{"core wrong type is unreadable", `{"resultType":"input_required","inputRequests":"secret"}`,
			mrtrAuthorityCoreResult, 0, true},
		{"core alias never decides", `{"ResultType":"input_required","resultType":"complete","inputRequests":{"a":{}}}`,
			mrtrAuthorityCoreResult, 0, false},
		// Task contracts: exact task status is authoritative (handle + get)...
		{"task-get status selects", `{"resultType":"complete","status":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskGet, 1, false},
		{"task-get wrong type is unreadable", `{"resultType":"complete","status":"input_required","inputRequests":"x"}`,
			mrtrAuthorityTaskGet, 0, true},
		{"task-handle status selects", `{"resultType":"task","taskId":"t","status":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskHandle, 1, false},
		// ...but never the cancel acknowledgement.
		{"task-cancel status never selects", `{"resultType":"complete","status":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskCancel, 0, false},
		// INVERTED 2026-07-30 (stage-7 P0-3): the CORE discriminator no longer
		// selects under ANY Tasks profile — the extension defines all three task
		// results as `resultType:"complete"`. This extractor projects nothing for
		// them; whether that body is an upstream MUST NOT is the CLASSIFIER's
		// question, pinned by TestStage7GuardStrictness (unsanctionedCore) and end to
		// end by TestStage7TasksRefuseCoreDiscriminator.
		{"task-cancel core discriminator selects nothing here", `{"resultType":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskCancel, 0, false},
		{"task-get core discriminator selects nothing here", `{"resultType":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskGet, 0, false},
		{"task-handle core discriminator selects nothing here", `{"resultType":"input_required","inputRequests":{"a":{"m":1}}}`,
			mrtrAuthorityTaskHandle, 0, false},
		// A result the strict reader cannot read is not classifiable here.
		{"undecodable is not classifiable", `not json`, mrtrAuthorityCoreResult, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, unreadable := extractMRTRInputRequests(json.RawMessage(tc.result), tc.authority)
			if len(entries) != tc.wantEntries || unreadable != tc.wantUnread {
				t.Errorf("entries=%d unreadable=%v, want %d/%v", len(entries), unreadable, tc.wantEntries, tc.wantUnread)
			}
		})
	}
}

// TestS504R3PlanWithoutAuthorityFailsSafe pins the §5 internal-defect posture:
// there is NO default authority profile and NO body-only fallback — the zero
// value plans nothing (no entries, no mediation, no release child) and reports
// noAuthority so the call site withholds the raw result.
func TestS504R3PlanWithoutAuthorityFailsSafe(t *testing.T) {
	rs := &ResourceServer{tenant: "t1", elicitationMediator: &fakeElicitationMediator{allow: true}}
	parent := sdk.EvidenceBinding{OperationID: "op-parent", EffectDigest: "ed-parent"}
	plan := rs.planMRTRRelease(
		json.RawMessage(`{"resultType":"input_required","inputRequests":{"a":{"m":1}}}`),
		parent, releaseClassMRTRToolResult, mrtrAuthorityNone)
	if !plan.noAuthority {
		t.Fatal("a zero authority profile must report noAuthority (fail-safe, adjudication §5)")
	}
	if plan.mediate || plan.unreadable || len(plan.entries) != 0 || plan.release.Valid() {
		t.Fatalf("a noAuthority plan must plan NOTHING: %+v", plan)
	}
}
