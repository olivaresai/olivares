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

// rs_s504_round1_test.go — Stage-5 review round 1 (Codex,
// an internal design note (not shipped)): the six blocking
// findings S5-01…S5-06, each pinned by the reviewer's OWN concrete failing input.
//
// RED-FIRST: every test here was written and RUN against commit fb310421 BEFORE
// the fixes existed; the literal failing output lives in
// an internal design note (not shipped)
//
// PRIMARY SOURCE for every protocol shape asserted here is the vendored MCP
// 2026-07-28 RC schema (an internal design note (not shipped), upstream 76346843):
//   - ElicitResult.action = "accept" | "decline" | "cancel"  (schema.ts:3121-3136)
//   - ElicitRequestURLParams = TOP-LEVEL mode:"url" + message + url (schema.ts:2808-2825)
//   - SamplingMessage.content = block | block[]              (schema.ts:2232-2236)
//   - ReadResourceResult.contents                            (schema.ts:1229-1231)

// planHashMediator is a mediator that requests HITL and names the plan its
// approval must be bound to (S5-05).
type planHashMediator struct {
	plan  string
	calls []ElicitationInspectionInput
}

func (m *planHashMediator) Mediate(_ context.Context, in ElicitationInspectionInput) ElicitationInspectionDecision {
	m.calls = append(m.calls, in)
	return ElicitationInspectionDecision{Allow: true, HITL: true, ApprovalPlanHash: m.plan}
}

// planHashGate answers APPROVED but bound to a DIFFERENT plan than the one it
// was asked about — the anti-TOCTOU mismatch gate.go:60-70 requires the caller
// to re-check (S5-05).
type planHashGate struct {
	plan     string
	requests []ToolApprovalRequest
}

func (g *planHashGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	g.requests = append(g.requests, req)
	return GateDecision{ApprovalRef: "appr-s504-r1", Status: StatusApproved, PlanHash: g.plan}, nil
}

// newS504R1RS is newS504RS plus an explicit ApprovalGate (the S5-05 exploit
// needs a gate that answers approved-for-another-plan).
func newS504R1RS(t *testing.T, jwks []byte, up Upstream, aud GateAuditor, ri RenderInspector, em ElicitationMediator, gate ApprovalGate) *ResourceServer {
	t.Helper()
	ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource:                   rsResource,
		AuthorizationServers:       []string{rsIssuer},
		Issuer:                     rsIssuer,
		IssuerJWKS:                 jwks,
		Toolset:                    ts,
		Upstream:                   up,
		Auditor:                    aud,
		Gate:                       gate,
		DurableTaskStore:           newMemoryDurableTaskStore(),
		Clock:                      rsClock,
		RenderInspector:            ri,
		ElicitationMediator:        em,
		UITemplates:                []UITemplatePolicy{{URI: "ui://srv/dashboard"}},
		DisableNextRevisionHeaders: true,
	})
	if err != nil {
		t.Fatalf("new rs: %v", err)
	}
	return rs
}

// --- S5-01 (P0) ---------------------------------------------------------------

// TestS504R1TaskHandleInputRequiredIsMediated: an accepted durable task handle
// whose status is `input_required` carries MRTR payloads in `inputRequests`, and
// the stage-5 tools/call branches to handleToolTaskResult BEFORE MRTR
// classification (rs.go:665-704) — the raw handle, payloads included, was
// written at rs.go:991 with NO mediation and NO response-release evidence.
//
// Reviewer's concrete failing input (round-1 finding 1).
func TestS504R1TaskHandleInputRequiredIsMediated(t *testing.T) {
	const handle = `{"resultType":"task","taskId":"task-s504-r1","status":"input_required",` +
		`"inputRequests":{"need":{"message":"s504-r1-handle-secret"}},"requestState":"s1"}`

	t.Run("mediator deny withholds the handle", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(handle), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		med := &taskMediatorFake{allow: false, reason: "secret in the task input request"}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (MRTR deny on a durable task handle); body: %s",
				rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "s504-r1-handle-secret") {
			t.Fatalf("the MRTR payload of a task handle was RELEASED unmediated: %s", rec.Body.String())
		}
		if len(med.calls) == 0 {
			t.Fatal("the MRTR payloads of a task handle must reach the mediator")
		}
	})

	t.Run("mediator allow anchors a response-release child", func(t *testing.T) {
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

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (mediation allowed); body: %s", rec.Code, rec.Body.String())
		}
		rel := s504ReleaseOps(&aud.fakeEvidenceJournal)
		if len(rel) != 1 {
			t.Fatalf("release operations = %d, want exactly 1 (the mediated handle relay is a release)", len(rel))
		}
		if st := aud.settledState(rel[0]); st != DispatchCompleted {
			t.Fatalf("the task-handle release settled %q, want completed", st)
		}
	})

	t.Run("release anchor failure withholds the handle", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(handle), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		aud.recordFaultFn = s504RefuseReleaseClaims
		med := &taskMediatorFake{allow: true}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (release anchor refused ⇒ handle withheld); body: %s",
				rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "s504-r1-handle-secret") {
			t.Fatalf("the task handle was RELEASED after a refused release anchor: %s", rec.Body.String())
		}
	})
}

// TestS504R1TaskCancelResultIsNeverReleased: the SAME class as S5-01 on a surface
// the review did not examine — `tasks/cancel` relays the upstream result verbatim
// (rs.go, the write after the cancellation settlement), so an upstream that
// answers a client cancellation with MRTR input requests handed the caller those
// payloads with no mediation and no release evidence. `tasks/update` is NOT
// affected: strictTaskUpdateAck allow-lists the ack members and refuses any
// state-reporting body, `inputRequests` included.
//
// Found while fixing S5-01; RED-verified before the fix like the other six.
//
// INVERTED 2026-07-30 (stage-7 P0-3, r2 review §"P0-3 pendiente"). The round-1
// fix MEDIATED this payload, on the reading that the cancel leg supported the
// core discriminator "for compatibility". The Tasks extension does not: SEP-2663
// defines `CancelTaskResult` as the ack-only `resultType:"complete"` shape, so
// this body is one no conforming server may send. The security property of the
// original finding is unchanged and stronger — the payload still never reaches
// the caller — but the disposition is now a PROTOCOL refusal (502/-31002, zero
// mediator calls) rather than a content verdict, and the retention of the
// observed bytes is recorded on the withheld release child.
func TestS504R1TaskCancelResultIsNeverReleased(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksCancel {
			return json.RawMessage(`{"resultType":"input_required",` +
				`"inputRequests":{"need":{"message":"s504-r1-cancel-secret"}},"requestState":"s1"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	aud := &taskAuditor{}
	med := &taskMediatorFake{allow: false, reason: "secret in the cancellation result"}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-s504-r1-cancel"})

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, taskReq(token, methodTasksCancel, `{"taskId":"task-s504-r1-cancel"}`))

	if strings.Contains(rec.Body.String(), "s504-r1-cancel-secret") {
		t.Fatalf("the tasks/cancel result released MRTR payloads: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (a variant the Tasks extension does not sanction); body: %s",
			rec.Code, rec.Body.String())
	}
	if len(med.calls) != 0 {
		t.Fatalf("a result no conforming server may send is never a content question: %+v", med.calls)
	}
	if got := aud.fakeEvidenceJournal.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Errorf("withheld RELEASE-CHILD settlements = %d, want exactly 1 (the refusal of an OBSERVED result is a durable release disposition)", got)
	}
}

// --- S5-02 (P0) ---------------------------------------------------------------

// TestS504R1MRTRAliasedResultMediatedNotRefused — REWRITTEN by the design
// adjudication (round 3). The original round-1 finding was real: a case-folding
// classifier let a later `ResultType` alias flip the exact `input_required`
// result onto the PLAIN write leg, releasing the MRTR payload unmediated. The
// round-1 FIX, however, refused the whole result (502) — and the adjudication
// establishes that refusal as part of the round-1→3 oscillation: on an open
// Result, `ResultType` is an EXTENSION member (schema.ts:208-216,223-235), and
// the exact lowercase `resultType:"input_required"` alone selects core MRTR.
// The payload is therefore MEDIATED FROM ITS EXACT MEMBERS — never the plain
// leg (the surviving security property, unchanged since round 1), and never a
// wholesale refusal (the dissolved capability cut).
//
// Reviewer's concrete round-1 input, adjudicated disposition. The relay/release
// direction of the same input is pinned by
// TestS504R3AliasPairsFollowExactDiscriminator.
func TestS504R1MRTRAliasedResultMediatedNotRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const aliased = `{"resultType":"input_required","ResultType":"complete",` +
		`"inputRequests":{"need":{"message":"s504-r1-alias-secret"}},"requestState":"s1"}`
	up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == "tools/call" {
			return json.RawMessage(aliased), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}}
	aud := &taskAuditor{}
	med := &taskMediatorFake{allow: false, reason: "secret in the input request"}
	rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, med)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))

	if strings.Contains(rec.Body.String(), "s504-r1-alias-secret") {
		t.Fatalf("an MRTR payload took the PLAIN write leg: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (mediation deny on the exact governed payload); body: %s",
			rec.Code, rec.Body.String())
	}
	if len(med.calls) == 0 || !strings.Contains(string(med.calls[0].Content), "s504-r1-alias-secret") {
		t.Fatalf("the exact governed payload must reach the mediator: %+v", med.calls)
	}
}

// --- S5-03 (P0) ---------------------------------------------------------------

// TestS504R1ElicitationAcceptActionMediated: the MCP 2026-07-28 RC ElicitResult
// action for a submitted form is `accept` (schema.ts:3121-3136); stage 5
// recognized only the non-schema `submit` (elicitationpep.go:146-160), so a
// CONFORMING accept carrying user data was released with no response mediation.
//
// Reviewer's concrete failing input (round-1 finding 3).
func TestS504R1ElicitationAcceptActionMediated(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"action":"accept","content":{"ssn":"123-45-6789"}}`)}
	aud := &recordingAuditor{}
	med := &channelAwareMediator{requestAllow: true, responseDeny: true, responseReason: "sensitive data"}
	rs := newS504RS(t, jwks, up, aud, nil, med)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, elicitationReq(token, "Enter SSN", ""))

	if strings.Contains(rec.Body.String(), "123-45-6789") {
		t.Fatalf("a schema-conforming accept response was released unmediated: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (response mediator deny); body: %s", rec.Code, rec.Body.String())
	}
	// Stage-7 B-bis round 2: the accept response was OBSERVED and then denied —
	// the parent settles `completed` (dispatch fact) and the RELEASE CHILD
	// settles `withheld`; never `blocked`, which promises nothing reached the
	// upstream. MUTATION VERIFIED: removing the elicitationWithheld call on the
	// response-deny leg of handleElicitation turns this red.
	if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Fatalf("withheld RELEASE-CHILD settlements = %d, want exactly 1: a fetched-then-denied response must settle its release child as withheld — blocked promises nothing reached the upstream", got)
	}
	if got := aud.settledCount(DispatchCompleted); got != 1 {
		t.Fatalf("completed settlements = %d, want exactly 1 (the PARENT records the dispatch fact)", got)
	}
	responseSeen := false
	for _, c := range med.calls {
		if c.Channel == ChannelElicitationResponse {
			responseSeen = true
		}
	}
	if !responseSeen {
		t.Fatal("the accept content must reach the RESPONSE channel of the mediator")
	}
}

// TestS504R1UIResultAliasNotBypassed: extractHTMLFromResult used a case-folding
// unmarshal (renderinspect.go:98-115), so a result carrying exact `contents`
// followed by `"Contents":[]` made the inspector see NO html while an
// exact-cased consumer still renders the first member — released uninspected.
//
// Reviewer's concrete failing input (round-1 finding 3, UI leg).
func TestS504R1UIResultAliasNotBypassed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(
		`{"contents":[{"uri":"ui://srv/dashboard","text":"<script>s504-r1-ui-evil</script>"}],"Contents":[]}`)}
	aud := &recordingAuditor{}
	inspector := &fakeRenderInspector{allow: false, reason: "prompt injection detected"}
	rs := newS504RS(t, jwks, up, aud, inspector, nil)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, resourcesReadReq(token, "ui://srv/dashboard"))

	if strings.Contains(rec.Body.String(), "s504-r1-ui-evil") {
		t.Fatalf("an alias-ambiguous render result was released uninspected: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (the two readers disagree ⇒ ungovernable render, refused); body: %s",
			rec.Code, rec.Body.String())
	}
	// Stage-7 B-bis round 2: the ambiguous render was FETCHED before the gate
	// refused to read it — the parent settles `completed` (dispatch fact) and
	// the RELEASE CHILD settles `withheld`. MUTATION VERIFIED: removing the
	// uiWithheld call on the `ambiguous` leg of handleUIRead turns this red.
	if got := aud.settledActionCount(s504ReleaseActionPrefix, DispatchWithheld); got != 1 {
		t.Fatalf("withheld RELEASE-CHILD settlements = %d, want exactly 1: a fetched-then-refused render must settle its release child as withheld — blocked promises nothing reached the upstream", got)
	}
	if got := aud.settledCount(DispatchCompleted); got != 1 {
		t.Fatalf("completed settlements = %d, want exactly 1 (the PARENT records the dispatch fact)", got)
	}
}

// TestS504R1SamplingNestedContentMediated: extractSamplingText re-parsed the
// NESTED members with the case-folding encoding/json decoder
// (elicitationpep.go:117-143). Two consequences, both in this test:
//
//   - a nested `Text` alias emptied the inspected text while an exact-cased peer
//     still consumes `text` (the reviewer's finding-3 nested leg); and
//   - the schema's ARRAY form of SamplingMessage.content (schema.ts:2232-2236)
//     failed the whole struct unmarshal, so those messages were never inspected
//     at all — a capability the gate must mediate, not skip.
func TestS504R1SamplingNestedContentMediated(t *testing.T) {
	t.Run("nested case alias is refused", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
		med := &fakeElicitationMediator{allow: false, reason: "prompt injection"}
		rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)

		// Why this must refuse (review round 2, adjudication b — the round-1
		// comment here was WRONG about the mechanism): canonical encoding sorts
		// members, so the forwarded bytes are `{"Text":"...","text":"","type":...}`.
		// Go's encoding/json resolves a key by exact-then-folded name and assigns as
		// it visits ($GOROOT/src/encoding/json/decode.go:699-702,766-773), so the
		// later exact `text` wins: a last-assignment case-folding consumer AND an
		// exact-cased peer BOTH read "" — neither leaks "exfil". The differential is
		// real only for a FIRST-MATCH case-folding consumer, which would read
		// "Text"="exfil". The gateway does not depend on any consumer's fold order:
		// the reserved-alias guard refuses the case-variant of a mediated member
		// outright, so the ambiguity is closed regardless of who reads the bytes.
		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, samplingReq(token,
			`[{"role":"user","content":{"type":"text","text":"","Text":"s504-r1-exfil"}}]`))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (nested reserved-member alias ⇒ ambiguous params); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("ambiguous sampling params must never reach the upstream")
		}
	})

	t.Run("array content blocks are inspected", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
		med := &fakeElicitationMediator{allow: false, reason: "prompt injection"}
		rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, samplingReq(token,
			`[{"role":"user","content":[{"type":"text","text":"s504-r1-array-injection"}]}]`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (array content blocks are mediated); body: %s",
				rec.Code, rec.Body.String())
		}
		if up.called {
			t.Fatal("a denied sampling request must never reach the upstream")
		}
		if len(med.calls) != 1 || !strings.Contains(string(med.calls[0].Content), "s504-r1-array-injection") {
			t.Fatalf("the array-form content block must be inspected: %+v", med.calls)
		}
	})
}

// --- S5-04 (P1) ---------------------------------------------------------------

// TestS504R1ElicitationURLModeTopLevel: SEP-1036 URL mode is TOP-LEVEL in the
// pinned RC schema — ElicitRequestURLParams {mode:"url", message, url}
// (schema.ts:2808-2825; schema.json required [message, mode, url]). Stage 5 read
// it from `_meta.elicitation.{mode,url}` (evidencerelease.go:88-96,144-168), so a
// conforming request reached the policy with URLTarget:"" (rs.go:3215-3223).
//
// Reviewer's concrete failing input (round-1 finding 4).
func TestS504R1ElicitationURLModeTopLevel(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"action":"cancel"}`)}
	med := &fakeElicitationMediator{allow: true}
	rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)

	body := `{"jsonrpc":"2.0","id":21,"method":"elicitation/create","params":` +
		`{"mode":"url","message":"Authorize","elicitationId":"e1","url":"https://evil.example/phish"}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, req)

	if len(med.calls) < 1 {
		t.Fatalf("mediator must be called; status %d body %s", rec.Code, rec.Body.String())
	}
	if med.calls[0].URLTarget != "https://evil.example/phish" {
		t.Fatalf("SEP-1036 top-level url not carried to policy: %q", med.calls[0].URLTarget)
	}
}

// TestS504R1ElicitationURLModeAliasRefused: with `mode`/`url` reserved, a
// case-variant alias of either is the ordinary ambiguous-params refusal.
func TestS504R1ElicitationURLModeAliasRefused(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"action":"cancel"}`)}
	med := &fakeElicitationMediator{allow: true}
	rs := newS504RS(t, jwks, up, &recordingAuditor{}, nil, med)

	body := `{"jsonrpc":"2.0","id":22,"method":"elicitation/create","params":` +
		`{"mode":"url","message":"Authorize","url":"https://good.example/ok","URL":"https://evil.example/phish"}}`
	req := httptest.NewRequest(http.MethodPost, rsResource, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (case-variant alias of a reserved member); body: %s",
			rec.Code, rec.Body.String())
	}
	if up.called {
		t.Fatal("ambiguous elicitation params must never reach the upstream")
	}
}

// --- S5-05 (P1) ---------------------------------------------------------------

// TestS504R1HITLPlanMismatchDenied: evaluateElicitationVerdict (rs.go:3475-3489)
// checked only status and error, never gdec.PlanHash — although GateDecision
// documents caller-side re-checking (gate.go:60-70) and tools/call does exactly
// that (rs.go:570). An approval issued for ANOTHER plan authorized the mediated
// request and its release.
//
// Reviewer's concrete failing input (round-1 finding 5).
func TestS504R1HITLPlanMismatchDenied(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
	med := &planHashMediator{plan: "plan-A"}
	gate := &planHashGate{plan: "plan-B"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, nil, med, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, samplingReq(token, `[{"role":"user","content":{"type":"text","text":"export data"}}]`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (approval bound to another plan is NOT an approval); body: %s",
			rec.Code, rec.Body.String())
	}
	if up.called {
		t.Fatal("a plan-mismatched approval must never forward")
	}
	if len(gate.requests) != 1 || gate.requests[0].PlanHash != "plan-A" {
		t.Fatalf("the gate must be asked about the mediator's plan: %+v", gate.requests)
	}
}

// TestS504R1HITLPlanMatchAllowed: the matching approval still authorizes — the
// fix is a mismatch check, not a HITL capability cut.
func TestS504R1HITLPlanMatchAllowed(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &fakeUpstream{result: json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"}}`)}
	med := &planHashMediator{plan: "plan-A"}
	gate := &planHashGate{plan: "plan-A"}
	rs := newS504R1RS(t, jwks, up, &recordingAuditor{}, nil, med, gate)

	rec := httptest.NewRecorder()
	rs.ServeHTTP(rec, samplingReq(token, `[{"role":"user","content":{"type":"text","text":"export data"}}]`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (approval bound to the SAME plan); body: %s", rec.Code, rec.Body.String())
	}
	if !up.called {
		t.Fatal("an approved, plan-matched HITL request must forward")
	}
}

// --- S5-06 (P1) ---------------------------------------------------------------

// TestS504R1MRTRParentNamesReleaseChild: the two in-scope MRTR parents settled
// with a ZERO GateOutcome.ReleaseBinding (rs.go:640-644, :1596-1599) while their
// release child was derived later (:721, :1687) — the parent record could not
// name the child it authorized. This is NOT the declared production-adapter
// residual: the connector never put the binding in the outcome at all.
//
// Reviewer's concrete failing input (round-1 finding 6).
func TestS504R1MRTRParentNamesReleaseChild(t *testing.T) {
	const mrtr = `{"resultType":"input_required","inputRequests":{"need":{"message":"enter secret"}},"requestState":"s1"}`

	namedChild := func(t *testing.T, aud *taskAuditor) {
		t.Helper()
		rel := s504ReleaseOps(&aud.fakeEvidenceJournal)
		if len(rel) != 1 {
			t.Fatalf("release operations = %d, want exactly 1", len(rel))
		}
		named := false
		for _, op := range aud.claimedOperations() {
			if b := aud.settledRelease(op); b.Valid() {
				if b.OperationID != rel[0] {
					t.Fatalf("the named release binding %q is not the claimed release operation %q",
						b.OperationID, rel[0])
				}
				named = true
			}
		}
		if !named {
			t.Fatal("the MRTR parent settlement must NAME its response-release child (GateOutcome.ReleaseBinding)")
		}
	}

	t.Run("tools/call", func(t *testing.T) {
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == "tools/call" {
				return json.RawMessage(mrtr), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, &taskMediatorFake{allow: true})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, toolsCallReq(token, "search", `{}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		namedChild(t, aud)
	})

	t.Run("tasks/get", func(t *testing.T) {
		// RE-FIXTURED 2026-07-30 (stage-7 P0-3): this subtest drove the CORE
		// discriminator, which the Tasks extension does not sanction on tasks/get and
		// which is now refused. The S5-06 property under test — the parent settlement
		// NAMES the release child it authorizes — is unchanged and is exercised with
		// the body a conforming server actually sends for an outstanding input
		// requirement: `resultType:"complete"` plus the task `status`.
		const taskProfileMRTR = `{"resultType":"complete","taskId":"task-s504-r1-get","status":"input_required",` +
			`"inputRequests":{"need":{"message":"enter secret"}}}`
		token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
		up := &taskUpstream{fn: func(req UpstreamRequest) (json.RawMessage, error) {
			if req.Method == methodTasksGet {
				return json.RawMessage(taskProfileMRTR), nil
			}
			return json.RawMessage(normativeCompleteResult), nil
		}}
		aud := &taskAuditor{}
		rs := newTaskRS(t, jwks, up, aud, nil, nil, 0, &taskMediatorFake{allow: true})
		mustInsertTask(t, rs, TaskRecord{TaskID: "task-s504-r1-get"})

		rec := httptest.NewRecorder()
		rs.ServeHTTP(rec, taskReq(token, methodTasksGet, `{"taskId":"task-s504-r1-get"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		namedChild(t, aud)
	})
}
