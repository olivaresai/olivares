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

// rs_s504_round2_test.go — Stage-5 review round 2 (Codex,
// an internal design note (not shipped)): the four
// blocking findings, each pinned by the reviewer's OWN concrete failing input.
//
// PROVENANCE, corrected by round-3 finding 5: the B1/B2/B3 tests and the first
// TWO B4 subtests (mediated sampling, destructive tools/call) were written and
// RUN against commit 515ff1e6 BEFORE the fixes existed — their literal failing
// output lives in an internal design note (not shipped)
// The THIRD B4 subtest (destructive tasks/update) was added AFTER that RED run
// and is MUTATION-PROVED, not RED-first (MUT-4c in the same artifact; corrected
// reproducible record in its CORRECTION section). Blocker 3's RED is the
// INVERSE direction — the guard's FALSE POSITIVE on spec-conforming extension
// traffic. The exact-input_required/alias mirror direction was ADJUDICATED in
// round 3: it is mediated from its exact members, not refused — see
// TestS504R1MRTRAliasedResultMediatedNotRefused and
// TestS504R3AliasPairsFollowExactDiscriminator.
//
// PRIMARY SOURCE for every protocol shape asserted here is the vendored MCP
// 2026-07-28 RC schema (an internal design note (not shipped), upstream 76346843):
//   - SamplingMessageContentBlock = Text|Image|Audio|ToolUse|ToolResult (schema.ts:2245-2250)
//   - ToolUseContent.input is arbitrary structured input               (schema.ts:2393-2411)
//   - ToolResultContent.content: ContentBlock[] + structuredContent    (schema.ts:2432-2456)
//   - ResultType = "complete" | "input_required" | string — OPEN enum  (schema.ts:208-216)
//   - Result is extensible: [key: string]: unknown                     (schema.ts:223-235)
//   - InputRequests is a map object                                    (schema.ts:553-555,584-595)

// --- Round-2 blocker 1 (P0, S5-03) -------------------------------------------

// TestS504R2SamplingToolResultContentMediated: the pinned sampling content union
// includes ToolResultContent with NESTED `content` blocks and arbitrary
// `structuredContent`, and ToolUseContent with arbitrary `input`
// (schema.ts:2232-2250,2393-2456) — but extractSamplingText read only a DIRECT
// `text` member of each block (elicitationpep.go:204-223), so a schema-valid
// request produced EMPTY inspection content, skipped the mediator at
// rs.go:3655-3679, and was forwarded to the model with zero inspection.
//
// Reviewer's concrete failing input (round-2 blocking finding 1).
func TestS504R2SamplingToolResultContentMediated(t *testing.T) {
	send := func(t *testing.T, med ElicitationMediator, params string) (*httptest.ResponseRecorder, *fakeUpstream) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
		rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)
		body := `{"jsonrpc":"2.0","id":40,"method":"sampling/createMessage","params":` + params + `}`
		req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, req)
		return rec, up
	}

	t.Run("nested tool_result text is inspected", func(t *testing.T) {
		med := &fakeElicitationMediator{allow: false, reason: "prompt injection"}
		// The reviewer's literal failing input, verbatim (maxTokens included).
		rec, up := send(t, med,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","toolUseId":"u1",`+
				`"content":[{"type":"text","text":"s504-r2-secret"}]}]}],"maxTokens":32}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (nested tool_result content must reach the mediator); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r2-secret") {
			t.Fatalf("the nested tool_result text must be inspected: %+v", med.calls)
		}
	})

	t.Run("tool_result structuredContent is inspected", func(t *testing.T) {
		med := &fakeElicitationMediator{allow: false, reason: "prompt injection"}
		rec, up := send(t, med,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","toolUseId":"u2",`+
				`"content":[],"structuredContent":{"cmd":"s504-r2-structured"}}]}],"maxTokens":32}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (structuredContent must reach the mediator); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r2-structured") {
			t.Fatalf("the tool_result structuredContent must be inspected: %+v", med.calls)
		}
	})

	t.Run("tool_use input is inspected", func(t *testing.T) {
		med := &fakeElicitationMediator{allow: false, reason: "prompt injection"}
		rec, up := send(t, med,
			`{"messages":[{"role":"user","content":[{"type":"tool_use","id":"u3","name":"exec",`+
				`"input":{"arg":"s504-r2-input"}}]}],"maxTokens":32}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (tool_use input must reach the mediator); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r2-input") {
			t.Fatalf("the tool_use input must be inspected: %+v", med.calls)
		}
	})
}

// --- Round-2 blocker 2 (P0, S5-01/S5-02) -------------------------------------

// TestS504R2MalformedMRTRValueRefused: a result whose exact-cased members SAY
// input_required but whose `inputRequests` value is not the schema's map
// (InputRequests is a map object — schema.ts:553-555) was classified "not MRTR"
// by both readers (the permissive scan required a non-empty MAP,
// elicitationpep.go:330-337; the strict reader turned a wrong TYPE into
// not-MRTR, elicitationpep.go:343-390) and took the PLAIN leg —
// strictTaskFromResult accepts the handle without validating the value
// (tasks.go:227-272), so the raw handle was written at rs.go:1096-1100 with the
// governed member's data inside it.
//
// Reviewer's concrete failing input (round-2 blocking finding 2).
func TestS504R2MalformedMRTRValueRefused(t *testing.T) {
	t.Run("durable task handle", func(t *testing.T) {
		// The reviewer's literal failing input, verbatim.
		const handle = `{"resultType":"task","taskId":"t1","status":"input_required",` +
			`"inputRequests":"s504-r2-secret"}`
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(handle), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

		if strings.Contains(rec.Body.String(), "s504-r2-secret") {
			t.Fatalf("a malformed MRTR member took the plain task-handle leg: %s", rec.Body.String())
		}
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (the governed inputRequests member is unreadable — refused); body: %s",
				rec.Code, rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("an unreadable payload must be refused, never partially mediated: %+v", med.calls)
		}
		// Round 3 (Codex r2, F-1 residual): the unreadable refusal of an OBSERVED
		// task handle is a release disposition — durable on the withheld child,
		// exactly like the synchronous leg. MUTATION VERIFIED: removing the
		// handleWithheld call on the unreadable leg of handleToolTaskResult turns
		// this red.
		if got := aud.fakeEvidenceJournal.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
			t.Errorf("withheld RELEASE-CHILD settlements = %d, want exactly 1 (a retained task-handle payload is never a fact without its durable disposition)", got)
		}
	})

	t.Run("synchronous result", func(t *testing.T) {
		const result = `{"resultType":"input_required","inputRequests":"s504-r2-secret","requestState":"s1"}`
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(result), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

		if strings.Contains(rec.Body.String(), "s504-r2-secret") {
			t.Fatalf("a malformed MRTR member took the plain write leg: %s", rec.Body.String())
		}
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (the governed inputRequests member is unreadable — refused); body: %s",
				rec.Code, rec.Body.String())
		}
		// Stage-7 B-bis round 2: the unreadable refusal of an OBSERVED governed
		// result is a release disposition — the parent settles completed (the
		// dispatch fact) and the release child settles withheld. MUTATION
		// VERIFIED: removing the settleWithheldRelease call on the tools/call
		// unreadable leg turns this red.
		if got := aud.fakeEvidenceJournal.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
			t.Errorf("withheld RELEASE-CHILD settlements = %d, want exactly 1 (the unreadable refusal is a durable release disposition)", got)
		}
		if got := aud.fakeEvidenceJournal.settledCount(DispatchCompleted); got != 1 {
			t.Errorf("completed settlements = %d, want exactly 1 (the PARENT records the dispatch fact)", got)
		}
	})
}

// --- Round-2 blocker 3 (P1, S5-02 — the guard's FALSE POSITIVE) --------------

// TestS504R2ExtensionResultTrafficRelayed: the pin deliberately makes ResultType
// an OPEN enum and Result extensible (schema.ts:208-235; CallToolResult does not
// close the shape, schema.ts:1796-1808). The round-1 permissive scan case-folded
// extension member NAMES (`Status`) and open-enum VALUES ("INPUT_REQUIRED")
// into protocol markers (elicitationpep.go:322-333) and then refused on the
// resulting disagreement (elicitationpep.go:388-397; rs.go:705-721) — a
// capability cut: spec-conforming traffic answered 502.
//
// This is the INVERSE red: these valid results must RELAY. (The round-2 fix
// kept refusing the exact-input_required/alias pair; round 3 adjudicated that
// refusal away too — that direction is now mediation from the exact members,
// pinned by TestS504R1MRTRAliasedResultMediatedNotRefused.)
func TestS504R2ExtensionResultTrafficRelayed(t *testing.T) {
	send := func(t *testing.T, result string) (*httptest.ResponseRecorder, *taskMediatorFake) {
		t.Helper()
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(result), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 0, med)
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		return rec, med
	}

	t.Run("Status extension member on a complete result", func(t *testing.T) {
		// The reviewer's literal failing input, verbatim: `Status` is an allowed
		// extension member and its value is ordinary extension data.
		rec, med := send(t, `{"resultType":"complete","content":[{"type":"text","text":"ok"}],`+
			`"Status":"input_required","inputRequests":{"domainStatus":{"method":"roots/list"}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (valid open-result extension traffic must not be refused); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "roots/list") {
			t.Fatalf("the extension result must relay intact: %s", rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("extension data is not protocol-MRTR content and must not be mediated as such: %+v", med.calls)
		}
	})

	t.Run("open-enum custom resultType", func(t *testing.T) {
		// "INPUT_REQUIRED" is a valid CUSTOM ResultType (the enum is open); it is
		// NOT the reserved lowercase literal, and case-folding it into one turned
		// a conforming result into a 502.
		rec, med := send(t, `{"resultType":"INPUT_REQUIRED",`+
			`"inputRequests":{"need":{"message":"extension data"}},"requestState":"s1"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a custom ResultType is valid under the open enum); body: %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "extension data") {
			t.Fatalf("the custom-typed result must relay intact: %s", rec.Body.String())
		}
		if len(med.calls) != 0 {
			t.Fatalf("a custom-typed result carries no protocol-MRTR content to mediate: %+v", med.calls)
		}
	})
}

// --- Round-2 blocker 4 (P1, S5-05) -------------------------------------------

// TestS504R2HITLEmptyPlanHashDenied: rs.go:3779-3786 rejected only a NON-EMPTY
// unequal PlanHash, so a gate answering {Status:approved, PlanHash:""} — an
// approval bound to NO plan — authorized the mediated request and its release.
// gate.go:60-70 makes the caller's equality re-check the only plan binding; an
// approval that cannot prove which plan it was granted for is not an approval
// for THIS plan.
//
// Reviewer's concrete failing input (round-2 blocking finding 4). The matching
// positive control is the untouched TestS504R1HITLPlanMatchAllowed.
func TestS504R2HITLEmptyPlanHashDenied(t *testing.T) {
	t.Run("mediated sampling", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
		med := &planHashMediator{plan: "plan-A"}
		gate := &planHashGate{plan: ""}
		rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, nil, med, gate)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, samplingReq(token, `[{"role":"user","content":{"type":"text","text":"export data"}}]`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (an approval bound to NO plan is not an approval for this plan); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("an unbound approval must never forward")
		}
		if len(gate.requests) != 1 || gate.requests[0].PlanHash != "plan-A" {
			t.Fatalf("the gate must be asked about the mediator's plan: %+v", gate.requests)
		}
	})

	t.Run("destructive tools/call", func(t *testing.T) {
		// The SAME class at rs.go:570 (found in this round while fixing the cited
		// site — not a reviewer finding): the destructive-tool HITL check used the
		// identical `PlanHash != "" &&` guard, so an approved decision with an
		// empty PlanHash authorized a destructive tools/call.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin", validExp())
		up := &taskUpstream{}
		gate := &planHashGate{plan: ""}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, gate, nil, 0, nil)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "delete_db", `{}`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (an approval bound to NO plan must not authorize a destructive tool); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.total() != 0 {
			t.Fatal("an unbound approval must never forward a destructive tools/call")
		}
	})

	t.Run("destructive tasks/update", func(t *testing.T) {
		// The THIRD site of the same class (rs.go:2107): the destructive tasks/update
		// HITL check had the identical `PlanHash != "" &&` guard.
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:admin", validExp())
		up := &taskUpstream{}
		gate := &planHashGate{plan: ""}
		rs := newTaskRS(t, jwks, up, &taskAuditor{}, gate, nil, 0, nil)
		mustInsertTask(t, rs, TaskRecord{
			TaskID: "task-s504-r2-update", Tool: "delete_db", RequiredScope: "tools:admin",
			Destructive: true,
		})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksUpdate, `{"taskId":"task-s504-r2-update","inputResponses":{}}`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (an approval bound to NO plan must not authorize a destructive update); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.count(methodTasksUpdate) != 0 {
			t.Fatal("an unbound approval must never forward a destructive tasks/update")
		}
	})
}
