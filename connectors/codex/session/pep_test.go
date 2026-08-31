// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// realPreToolUsePayload is a VERBATIM capture from a live `codex exec` run against
// codex-cli 0.145.0 — not a hand-written fixture. Every test that asserts something about
// the inbound shape asserts it against what Codex actually sent.
const realPreToolUsePayload = `{"session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","turn_id":"019fc4c3-4157-7380-867c-474a842a75e5","transcript_path":"/workspace/.s528-probe/sessions/2026/08/03/rollout-2026-08-03T01-15-58-019fc4c3-40c5-7371-9c92-7b269d23897b.jsonl","cwd":"/workspace/.s528-probe","hook_event_name":"PreToolUse","model":"gpt-5.6-terra","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"echo HELLO_S528"},"tool_use_id":"exec-5e34277c-9063-4eb0-95dd-79e6fe3e8960"}`

// realSessionStartPayload is likewise a verbatim capture.
const realSessionStartPayload = `{"session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","transcript_path":"/workspace/.s528-probe/sessions/2026/08/03/rollout.jsonl","cwd":"/workspace/.s528-probe","hook_event_name":"SessionStart","model":"gpt-5.6-terra","permission_mode":"bypassPermissions","source":"startup"}`

type fakeDecider struct {
	calls    int
	seen     []Request
	bearer   string
	decision Decision
	err      error
}

func (f *fakeDecider) Decide(_ context.Context, req Request, bearer string) (Decision, error) {
	f.calls++
	f.seen = append(f.seen, req)
	f.bearer = bearer
	return f.decision, f.err
}

func post(t *testing.T, p *PEP, body string, hdr map[string]string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (the verdict travels in the body), got %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return m
}

func denied(m map[string]any) bool {
	if hs, ok := m["hookSpecificOutput"].(map[string]any); ok {
		if hs["permissionDecision"] == "deny" {
			return true
		}
		if d, ok := hs["decision"].(map[string]any); ok && d["behavior"] == "deny" {
			return true
		}
	}
	return m["decision"] == "block" || m["continue"] == false
}

// TestRealPayloadIsUnderstood pins the parse against the captured payload. If Codex changes
// a field name, this is what notices — not a fixture we wrote to match our own parser.
func TestRealPayloadIsUnderstood(t *testing.T) {
	p, ok := ParseHookPayload([]byte(realPreToolUsePayload))
	if !ok {
		t.Fatal("the real captured payload must parse")
	}
	if p.SessionID != "019fc4c3-40c5-7371-9c92-7b269d23897b" {
		t.Errorf("session_id = %q", p.SessionID)
	}
	if p.ToolUseID != "exec-5e34277c-9063-4eb0-95dd-79e6fe3e8960" {
		t.Errorf("tool_use_id = %q — this is the precise correlation join; losing it means falling back to FIFO", p.ToolUseID)
	}
	if p.ToolName != "Bash" || p.HookEventName != EventPreToolUse {
		t.Errorf("tool/event = %q/%q", p.ToolName, p.HookEventName)
	}
}

// TestDenyClosedWithoutADecider pins the posture of an unwired endpoint: it denies, loudly
// and in the right shape, rather than becoming a silent open door.
func TestDenyClosedWithoutADecider(t *testing.T) {
	p := NewPEP(nil, nil, testClock)
	if !denied(post(t, p, realPreToolUsePayload, nil)) {
		t.Error("a PEP with no decider must deny every call")
	}
}

func TestDenyClosedOnDeciderError(t *testing.T) {
	p := NewPEP(&fakeDecider{err: errors.New("pdp unreachable")}, nil, testClock)
	if !denied(post(t, p, realPreToolUsePayload, nil)) {
		t.Error("a failed governed decision must deny")
	}
}

func TestDenyClosedOnUnreadablePayload(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_x"}}
	p := NewPEP(d, nil, testClock)
	if !denied(post(t, p, `{"hook_event_name":"PreToolUse"`, nil)) {
		t.Error("an unparsable payload must deny")
	}
	if d.calls != 0 {
		t.Error("an unparsable payload must never reach the decider")
	}
}

// TestUnknownEventNeverReachesTheDecider is the deny-closed rule for a Codex release that
// adds an event: it must not acquire an ungoverned path by being unrecognized.
func TestUnknownEventNeverReachesTheDecider(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_x"}}
	p := NewPEP(d, nil, testClock)
	m := post(t, p, `{"hook_event_name":"SomeFutureEvent","session_id":"s1"}`, nil)
	if !denied(m) {
		t.Errorf("an unknown event must be deny-closed, got %+v", m)
	}
	if d.calls != 0 {
		t.Error("an unknown event must be refused before the decision, not by it")
	}
}

// TestGovernedDenyReachesTheAgentInTheRightShape is the end-to-end shape assertion for the
// one event whose deny was confirmed against a live codex exec.
func TestGovernedDenyReachesTheAgentInTheRightShape(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictDeny, Reason: "shell writes are denied here", SessionSID: "osn_1", Enforced: true}}
	m := post(t, NewPEP(d, nil, testClock), realPreToolUsePayload, nil)
	hs, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok || hs["permissionDecision"] != "deny" {
		t.Fatalf("a PreToolUse deny must use permissionDecision, got %+v", m)
	}
	if hs["permissionDecisionReason"] != "shell writes are denied here" {
		t.Errorf("the reason must reach the agent, got %v", hs["permissionDecisionReason"])
	}
}

// TestRedactionOfTheRequest pins the minimal-data rule: the decider sees a derived,
// bounded resource reference — never the raw command line.
func TestRedactionOfTheRequest(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_1"}}
	post(t, NewPEP(d, nil, testClock), realPreToolUsePayload, nil)
	if len(d.seen) != 1 {
		t.Fatalf("expected one decision, got %d", len(d.seen))
	}
	req := d.seen[0]
	if req.ResourceKind != kindShell {
		t.Errorf("a Bash call must be classified as shell, got %q", req.ResourceKind)
	}
	if req.ResourceRef != "echo HELLO_S528" && req.ResourceRef != "echo" {
		t.Errorf("resource ref should be the command head, got %q", req.ResourceRef)
	}
	if req.ToolUseID == "" {
		t.Error("the tool_use_id must reach the decider: it is half the idempotency key")
	}
	// Shape without values: the decider can reason about which keys exist, never about
	// what they contain.
	if keys := req.RawInputKeys(); len(keys) != 1 || keys[0] != "command" {
		t.Errorf("RawInputKeys should expose shape only, got %v", keys)
	}
}

// TestIdentityHintsAndBearer pins that the client's hints arrive and that the bearer is
// passed through to the decider (which is the only thing allowed to resolve it).
func TestIdentityHintsAndBearer(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_1"}}
	post(t, NewPEP(d, nil, testClock), realPreToolUsePayload, map[string]string{
		hdrTenant:       "t1",
		hdrAgent:        "a1",
		"Authorization": "Bearer secret-token",
	})
	if d.seen[0].Identity.Tenant != "t1" || d.seen[0].Identity.Agent != "a1" {
		t.Errorf("identity hints must reach the decider, got %+v", d.seen[0].Identity)
	}
	if d.bearer != "secret-token" {
		t.Errorf("the bearer must be handed to the decider, got %q", d.bearer)
	}
}

// TestNoObservationWithoutACanonicalSession is the honesty guard on the emit path. A
// decision that resolved no canonical identity is not a session fact: emitting it would
// write a row the live view discards while looking, in our own logs, like a delivered fact.
func TestNoObservationWithoutACanonicalSession(t *testing.T) {
	var observed int
	obs := func(Request, Decision) { observed++ }

	withSID := NewPEP(&fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_1"}}, obs, testClock)
	post(t, withSID, realPreToolUsePayload, nil)
	if observed != 1 {
		t.Fatalf("a resolved session must emit exactly once, got %d", observed)
	}

	observed = 0
	noSID := NewPEP(&fakeDecider{decision: Decision{Verdict: VerdictAllow}}, obs, testClock)
	m := post(t, noSID, realPreToolUsePayload, nil)
	if observed != 0 {
		t.Errorf("an unresolved session must emit nothing, got %d emissions", observed)
	}
	if denied(m) {
		t.Error("failing to resolve identity must not change the verdict the agent is told")
	}
}

// TestOneDeliveryIsOneDecision is the shape of R-01 at this layer: the PEP calls the
// decider exactly once per delivery and emits at most one observation set. The ledger-side
// idempotency proof lives with the decider; this is the guard that the connector never
// becomes a second writer by fanning out.
func TestOneDeliveryIsOneDecision(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictDeny, Reason: "no", SessionSID: "osn_1", Enforced: true}}
	var emitted int
	p := NewPEP(d, func(Request, Decision) { emitted++ }, testClock)
	post(t, p, realPreToolUsePayload, nil)
	if d.calls != 1 || emitted != 1 {
		t.Errorf("one delivery must be one decision and one emission, got decisions=%d emissions=%d", d.calls, emitted)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	NewPEP(nil, nil, testClock).ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET must be rejected, got %d", w.Code)
	}
}

// TestSessionStartIsGoverned pins that the lifecycle events go through the same path: the
// claim moment is not a special case that skips governance.
func TestSessionStartIsGoverned(t *testing.T) {
	d := &fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_1"}}
	post(t, NewPEP(d, nil, testClock), realSessionStartPayload, nil)
	if d.calls != 1 {
		t.Fatal("SessionStart must reach the decider")
	}
	if d.seen[0].Event != EventSessionStart {
		t.Errorf("event = %q", d.seen[0].Event)
	}
	if d.seen[0].ExternalSessionID == "" {
		t.Error("SessionStart must carry the external session id: it is what gets resolved")
	}
}

func testClock() time.Time { return time.Date(2026, 8, 3, 1, 15, 58, 0, time.UTC) }

// TestEmitPanicDoesNotChangeTheVerdict pins the separation between telemetry and safety.
// Without the recover, a panic in the emit path aborts the handler before the verdict is
// written, the client reads an empty body as a fault, and a legitimate ALLOW becomes a
// DENY — a governance decision changed by a bookkeeping bug.
func TestEmitPanicDoesNotChangeTheVerdict(t *testing.T) {
	var reported string
	p := NewPEP(
		&fakeDecider{decision: Decision{Verdict: VerdictAllow, SessionSID: "osn_1"}},
		func(Request, Decision) { panic("a malformed label") },
		testClock,
	)
	p.OnEmitPanic(func(event string, _ any) { reported = event })

	m := post(t, p, realPreToolUsePayload, nil)
	if denied(m) {
		t.Errorf("a panic while EMITTING must not turn an allow into a deny, got %+v", m)
	}
	if reported != EventPreToolUse {
		t.Errorf("the lost observation must be reported, got %q", reported)
	}
}

// TestUnknownEventKeepsExitTwoThroughTheClient is the second-channel guard the E5 contrast
// found missing. The SERVER denies an unknown event, but the client used to relay that body
// and exit 0 — so the exit-code channel only ever applied to the client's own deny-closed.
// For an event with no verified output shape, the stdout shape is a guess, which is exactly
// when the exit code is worth having.
func TestUnknownEventKeepsExitTwoThroughTheClient(t *testing.T) {
	// A server that answers 200 with a governed deny, as the real PEP does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(DenyClosed("SomeFutureEvent", "unknown hook event (deny-closed)"))
	}))
	defer srv.Close()

	res := RunClient(context.Background(),
		strings.NewReader(`{"hook_event_name":"SomeFutureEvent","session_id":"s1"}`),
		ClientConfig{Endpoint: srv.URL})

	if res.ExitCode != 2 {
		t.Errorf("an unknown event must deny on BOTH channels even when the server answered, got exit %d", res.ExitCode)
	}
	if res.Stderr == "" {
		t.Error("Codex complains when a hook exits 2 with no reason on stderr; the reason must be there")
	}
	if len(res.Stdout) == 0 {
		t.Error("stdout must still carry the governed answer: an empty stdout reads as no objection")
	}
}

// TestKnownEventRelaysWithExitZero is the other half: a verified event's verdict travels in
// stdout and the exit code stays 0, which is how Codex consumes a hook decision.
func TestKnownEventRelaysWithExitZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(Render(EventPreToolUse, Decision{Verdict: VerdictDeny, Reason: "no"}))
	}))
	defer srv.Close()

	res := RunClient(context.Background(), strings.NewReader(realPreToolUsePayload), ClientConfig{Endpoint: srv.URL})
	if res.ExitCode != 0 {
		t.Errorf("a verified event's deny travels in stdout; exit must stay 0, got %d", res.ExitCode)
	}
	if !denied(decode(t, res.Stdout)) {
		t.Errorf("the governed deny must be relayed verbatim, got %s", res.Stdout)
	}
}

// TestClientDeniesClosedWhenTheEndpointIsDown pins the whole point of the client: a control
// plane that is down must BLOCK the agent, not become invisible.
func TestClientDeniesClosedWhenTheEndpointIsDown(t *testing.T) {
	res := RunClient(context.Background(), strings.NewReader(realPreToolUsePayload),
		ClientConfig{Endpoint: "http://127.0.0.1:1/"})
	if !denied(decode(t, res.Stdout)) {
		t.Errorf("an unreachable endpoint must deny, got %s", res.Stdout)
	}
}
