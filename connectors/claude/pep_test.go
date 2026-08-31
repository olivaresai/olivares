// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pep_test.go proves the governed PEP protocol shell: it renders the verified
// Claude Code wire format for every verdict, applies the governed rewrite, and is
// DENY-CLOSED on every failure edge (nil decider, decider error, malformed payload). The
// governed brain itself (PDP/firm-identity/HITL) is tested against the real engine in
// cmd/olivares (claudehookpep_test.go); here the decider is faked so the protocol is
// isolated.

type fakeDecider struct {
	res       HookDecisionResult
	err       error
	gotBearer string
	gotInput  HookDecisionInput
	calls     int
}

func (f *fakeDecider) Decide(_ context.Context, in HookDecisionInput, bearer string) (HookDecisionResult, error) {
	f.calls++
	f.gotBearer = bearer
	f.gotInput = in
	if f.err != nil {
		return HookDecisionResult{}, f.err
	}
	return f.res, nil
}

type fakeAuditor struct {
	recs []fakeAuditRec
}

type fakeAuditRec struct {
	in         HookDecisionInput
	res        HookDecisionResult
	denyClosed bool
}

func (a *fakeAuditor) Record(_ context.Context, in HookDecisionInput, res HookDecisionResult, denyClosed bool) {
	a.recs = append(a.recs, fakeAuditRec{in: in, res: res, denyClosed: denyClosed})
}

// pepRequest builds a hook POST with a payload and optional headers.
func pepRequest(event, tool string, input map[string]any, headers map[string]string) *http.Request {
	payload := map[string]any{
		"session_id":      "sess-1",
		"hook_event_name": event,
		"tool_name":       tool,
		"tool_use_id":     "tu-1",
		"tool_input":      input,
	}
	b, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// serve drives the PEP and returns the parsed response object.
func serve(t *testing.T, pep *HookPEP, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	pep.ServeHTTP(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("response is not JSON: %q (%v)", rec.Body.String(), err)
		}
	}
	return rec.Code, m
}

// hso extracts hookSpecificOutput (PreToolUse/PermissionRequest schema).
func hso(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	out, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("response has no hookSpecificOutput: %v", m)
	}
	return out
}

func TestHookPEPRendersAllowDenyAsk(t *testing.T) {
	for _, perm := range []string{DecisionAllow, DecisionDeny, DecisionAsk} {
		dec := &fakeDecider{res: HookDecisionResult{Permission: perm, Reason: "by policy"}}
		pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
		code, m := serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "ls"}, nil))
		if code != http.StatusOK {
			t.Fatalf("%s: status = %d", perm, code)
		}
		out := hso(t, m)
		if out["permissionDecision"] != perm {
			t.Fatalf("permissionDecision = %v, want %s", out["permissionDecision"], perm)
		}
		if out["hookEventName"] != hookPreToolUse {
			t.Fatalf("hookEventName = %v", out["hookEventName"])
		}
	}
}

// TestHookPEPGatingEventWireShapes proves every gating event renders its deny in the wire
// shape Claude Code HONORS — emitting the wrong shape means the deny is silently
// ignored (no enforcement). The decider is faked; this isolates the render contract.
func TestHookPEPGatingEventWireShapes(t *testing.T) {
	// Top-level decision:"block" events.
	for _, ev := range []string{hookUserPromptSubmit, hookUserPromptExpansion, hookPreCompact, hookConfigChange, hookPostToolBatch} {
		dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "blocked by policy"}}
		pep := NewHookPEP(dec, &fakeAuditor{}, nil)
		code, m := serve(t, pep, pepRequest(ev, "", nil, nil))
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", ev, code)
		}
		if m["decision"] != "block" || m["reason"] == "" {
			t.Errorf("%s deny must render top-level decision:block + reason; got %v", ev, m)
		}
		if _, has := m["hookSpecificOutput"]; has {
			t.Errorf("%s must NOT use the permissionDecision schema; got %v", ev, m)
		}
	}

	// continue:false + stopReason events.
	for _, ev := range []string{hookTaskCreated, hookTaskCompleted, hookTeammateIdle} {
		dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "blocked"}}
		pep := NewHookPEP(dec, &fakeAuditor{}, nil)
		_, m := serve(t, pep, pepRequest(ev, "", nil, nil))
		if m["continue"] != false || m["stopReason"] == "" {
			t.Errorf("%s deny must render continue:false + stopReason; got %v", ev, m)
		}
	}

	// PermissionRequest deny → decision.behavior (never permissionDecision).
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "x"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := serve(t, pep, pepRequest(hookPermissionRequest, "Bash", map[string]any{"command": "rm"}, nil))
	out := hso(t, m)
	if _, has := out["permissionDecision"]; has {
		t.Error("PermissionRequest must not emit permissionDecision")
	}
	if d, _ := out["decision"].(map[string]any); d == nil || d["behavior"] != "deny" {
		t.Errorf("PermissionRequest deny = %v, want decision.behavior=deny", m)
	}

	// INVERTED Stop/SubagentStop: even a DENY verdict renders NEUTRAL — a decision:block here
	// would KEEP the agent running (the opposite of a safety stop).
	for _, ev := range []string{hookStop, hookSubagentStop} {
		d2 := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "would-block"}}
		p2 := NewHookPEP(d2, &fakeAuditor{}, nil)
		rec := httptest.NewRecorder()
		p2.ServeHTTP(rec, pepRequest(ev, "", nil, nil))
		if body := strings.TrimSpace(rec.Body.String()); body != "{}" {
			t.Errorf("%s deny must render neutral {} (inverted block keeps the agent alive); got %q", ev, body)
		}
	}

	// Stop allow + feedback → additionalContext (the only meaningful Stop output).
	d3 := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow, AdditionalContext: "uncommitted changes"}}
	p3 := NewHookPEP(d3, &fakeAuditor{}, nil)
	_, m3 := serve(t, p3, pepRequest(hookStop, "", nil, nil))
	if hso(t, m3)["additionalContext"] != "uncommitted changes" {
		t.Errorf("Stop additionalContext not rendered; got %v", m3)
	}

	// An allow on a top-level-decision event, and any verdict on an OBSERVE event → neutral {}.
	for _, tc := range []struct {
		event string
		res   HookDecisionResult
	}{
		{hookUserPromptSubmit, HookDecisionResult{Permission: DecisionAllow}},
		{hookNotification, HookDecisionResult{Permission: DecisionDeny, Reason: "x"}},
		{hookSessionEnd, HookDecisionResult{Permission: DecisionDeny}},
	} {
		d := &fakeDecider{res: tc.res}
		pp := NewHookPEP(d, &fakeAuditor{}, nil)
		rec := httptest.NewRecorder()
		pp.ServeHTTP(rec, pepRequest(tc.event, "", nil, nil))
		if body := strings.TrimSpace(rec.Body.String()); body != "{}" {
			t.Errorf("%s must render neutral {}; got %q", tc.event, body)
		}
	}
}

// TestHookPEPPostToolUseDenyBlocks: a governed POLICY deny on PostToolUse must render
// decision:block (a HARD block, no continue) — not the neutral {} that would fail OPEN.
func TestHookPEPPostToolUseDenyBlocks(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "output flagged"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := serve(t, pep, pepRequest(hookPostToolUse, "Bash", map[string]any{"command": "cat /etc/shadow"}, nil))
	if m["decision"] != "block" || m["reason"] == "" {
		t.Fatalf("PostToolUse policy deny must render decision:block + reason; got %v", m)
	}
	if _, hasContinue := m["continue"]; hasContinue {
		t.Error("a policy deny is a HARD block — it must NOT carry continue softening")
	}
}

// TestDenyClosedDecisionMatchesEventMechanism: the managed client's FAIL-CLOSED deny renderer
// uses the SAME wire shape per event as the PEP's render (single source of truth) — else a
// fail-closed deny on a top-level/continue-false event would be silently ignored.
func TestDenyClosedDecisionMatchesEventMechanism(t *testing.T) {
	parse := func(event string) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(denyClosedDecision(event, "pep unreachable"), &m)
		return m
	}
	block := func(m map[string]any) bool { return m["decision"] == "block" }
	contFalse := func(m map[string]any) bool { v, ok := m["continue"]; return ok && v == false }
	neutral := func(m map[string]any) bool { return len(m) == 0 }
	behavior := func(m map[string]any) bool {
		hso, _ := m["hookSpecificOutput"].(map[string]any)
		d, _ := hso["decision"].(map[string]any)
		return d != nil && d["behavior"] == "deny"
	}
	permDecision := func(m map[string]any) bool {
		hso, _ := m["hookSpecificOutput"].(map[string]any)
		return hso != nil && hso["permissionDecision"] == "deny"
	}
	for _, tc := range []struct {
		event string
		ok    func(map[string]any) bool
		desc  string
	}{
		{hookUserPromptSubmit, block, "top-level decision:block"},
		{hookConfigChange, block, "top-level decision:block"},
		{hookPreCompact, block, "top-level decision:block"},
		{hookPostToolBatch, block, "top-level decision:block"},
		{hookPostToolUse, block, "PostToolUse block"},
		{hookTaskCreated, contFalse, "continue:false"},
		{hookTaskCompleted, contFalse, "continue:false"},
		{hookStop, neutral, "inverted Stop → neutral"},
		{hookSubagentStop, neutral, "inverted SubagentStop → neutral"},
		{hookNotification, neutral, "observe → neutral"},
		{hookPermissionRequest, behavior, "decision.behavior=deny"},
		{hookPreToolUse, permDecision, "permissionDecision=deny"},
	} {
		if m := parse(tc.event); !tc.ok(m) {
			t.Errorf("denyClosedDecision(%s) wrong shape (%s): %v", tc.event, tc.desc, m)
		}
	}
}

func TestHookPEPAppliesGovernedRewrite(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{
		Permission:   DecisionAllow,
		Reason:       "allowed with rewrite",
		UpdatedInput: map[string]any{"command": "ls --dry-run"},
	}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "ls"}, nil))
	out := hso(t, m)
	ui, ok := out["updatedInput"].(map[string]any)
	if !ok {
		t.Fatalf("expected updatedInput, got %v", out)
	}
	if ui["command"] != "ls --dry-run" {
		t.Fatalf("updatedInput.command = %v, want the rewrite", ui["command"])
	}
}

func TestHookPEPRewriteNeverRidesADeny(t *testing.T) {
	// A deny runs nothing; a rewrite on a deny would be misleading, so it is dropped.
	dec := &fakeDecider{res: HookDecisionResult{
		Permission:   DecisionDeny,
		UpdatedInput: map[string]any{"command": "rm -rf /"},
	}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "x"}, nil))
	if _, ok := hso(t, m)["updatedInput"]; ok {
		t.Fatal("a deny must not carry updatedInput")
	}
}

func TestHookPEPDenyClosedOnNilDecider(t *testing.T) {
	aud := &fakeAuditor{}
	pep := NewHookPEP(nil, aud, nil)
	_, m := serve(t, pep, pepRequest(hookPreToolUse, "Write", map[string]any{"file_path": "/etc/x"}, nil))
	if hso(t, m)["permissionDecision"] != DecisionDeny {
		t.Fatalf("nil decider must deny-closed: %v", m)
	}
	if len(aud.recs) != 1 || !aud.recs[0].denyClosed {
		t.Fatalf("deny-closed decision must be audited with denyClosed=true: %+v", aud.recs)
	}
}

func TestHookPEPDenyClosedOnDeciderError(t *testing.T) {
	aud := &fakeAuditor{}
	pep := NewHookPEP(&fakeDecider{err: context.DeadlineExceeded}, aud, nil)
	_, m := serve(t, pep, pepRequest(hookPreToolUse, "Bash", map[string]any{"command": "x"}, nil))
	if hso(t, m)["permissionDecision"] != DecisionDeny {
		t.Fatalf("decider error must deny-closed: %v", m)
	}
	if len(aud.recs) != 1 || !aud.recs[0].denyClosed {
		t.Fatalf("error decision must be audited denyClosed=true: %+v", aud.recs)
	}
}

func TestHookPEPDenyClosedOnMalformedBody(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	// A body with neither session nor tool cannot be attributed → deny, decider never run.
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	_, m := serve(t, pep, req)
	if hso(t, m)["permissionDecision"] != DecisionDeny {
		t.Fatalf("malformed payload must deny-closed: %v", m)
	}
	if dec.calls != 0 {
		t.Fatal("decider must not run on an unattributable payload")
	}
}

func TestHookPEPRejectsNonPost(t *testing.T) {
	pep := NewHookPEP(&fakeDecider{}, &fakeAuditor{}, nil)
	rec := httptest.NewRecorder()
	pep.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}

func TestHookPEPPostToolUseBlock(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Block: true, Reason: "sensitive output"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := serve(t, pep, pepRequest(hookPostToolUse, "Read", map[string]any{"file_path": "/secret"}, nil))
	if m["decision"] != "block" {
		t.Fatalf("PostToolUse block must emit decision:block, got %v", m)
	}
	if m["reason"] != "sensitive output" {
		t.Fatalf("block reason = %v", m["reason"])
	}
}

func TestHookPEPPassesBearerToDeciderButNotToAuditor(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	aud := &fakeAuditor{}
	pep := NewHookPEP(dec, aud, nil)
	req := pepRequest(hookPreToolUse, "Read", map[string]any{"file_path": "/x"}, map[string]string{
		"Authorization": "Bearer super-secret-token",
		hdrHookTenant:   "tnt_123",
		hdrHookAgent:    "agent-7",
	})
	serve(t, pep, req)
	if dec.gotBearer != "super-secret-token" {
		t.Fatalf("decider must receive the bearer, got %q", dec.gotBearer)
	}
	// The auditor's input must carry the identity hints but NEVER the bearer or raw args.
	if len(aud.recs) != 1 {
		t.Fatalf("want 1 audit record, got %d", len(aud.recs))
	}
	in := aud.recs[0].in
	if in.Identity.Tenant != "tnt_123" || in.Identity.Agent != "agent-7" {
		t.Fatalf("audited identity hints missing: %+v", in.Identity)
	}
	// The audited input exposes no raw arguments (rawInput is unexported and never
	// surfaced); the resource is the redacted reference.
	if in.ResourceKind != resFile {
		t.Fatalf("resource kind = %q, want %q", in.ResourceKind, resFile)
	}
}

func TestHookPEPDerivesPlanHashStably(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	serve(t, pep, pepRequest(hookPreToolUse, "Write", map[string]any{"file_path": "/a"}, nil))
	first := dec.gotInput.PlanHash
	serve(t, pep, pepRequest(hookPreToolUse, "Write", map[string]any{"file_path": "/a"}, nil))
	if first == "" || first != dec.gotInput.PlanHash {
		t.Fatalf("plan hash must be non-empty and stable: %q vs %q", first, dec.gotInput.PlanHash)
	}
	// A different target hashes differently (anti-TOCTOU binding).
	serve(t, pep, pepRequest(hookPreToolUse, "Write", map[string]any{"file_path": "/b"}, nil))
	if dec.gotInput.PlanHash == first {
		t.Fatal("a different tool-call must hash differently")
	}
}

// --- 2.1.17x hook-field parity (VERIFIED 2026-06-10) --------------------

// TestHookPEPMessageDisplayIsNeutral proves the display-only event never gets a
// permission verdict from the PEP: MessageDisplay has NO decision control
// (changelog 2.1.152), so even a deny-everything decider renders neutral "{}".
func TestHookPEPMessageDisplayIsNeutral(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "deny all"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
	code, m := serve(t, pep, pepRequest(hookMessageDisplay, "", map[string]any{"delta": "hello"}, nil))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(m) != 0 {
		t.Errorf("MessageDisplay must render neutral {}, got %v", m)
	}
}

// TestHookPEPUnknownEventStillDenies proves the deny-closed contract did NOT
// widen with the event classes: an event the PEP does not recognize keeps
// the permission-deny schema (only explicitly-recognized non-gating events earn
// a neutral answer).
func TestHookPEPUnknownEventStillDenies(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{}} // zero verdict → deny-closed
	pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
	_, m := serve(t, pep, pepRequest("SomeFutureEvent", "Bash", map[string]any{"command": "ls"}, nil))
	out := hso(t, m)
	if out["permissionDecision"] != permDeny {
		t.Errorf("unknown event must deny-close, got %v", out)
	}
}

// TestHookPEPStopAdditionalContext proves Stop/SubagentStop render the 2.1.163
// context-feedback schema: additionalContext when the decider supplies feedback,
// neutral otherwise — never a permission decision, never a synthetic block.
func TestHookPEPStopAdditionalContext(t *testing.T) {
	for _, event := range []string{hookStop, hookSubagentStop} {
		dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow, AdditionalContext: "two review findings remain open"}}
		pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
		_, m := serve(t, pep, pepRequest(event, "", nil, nil))
		out := hso(t, m)
		if out["additionalContext"] != "two review findings remain open" || out["hookEventName"] != event {
			t.Errorf("%s: feedback output = %v", event, m)
		}
		if _, has := out["permissionDecision"]; has {
			t.Errorf("%s must never carry a permission decision: %v", event, out)
		}

		// No feedback → neutral, even on a deny verdict (Stop is not a gate; a
		// synthetic block would keep the agent RUNNING — the opposite of safe).
		dec = &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny}}
		pep = NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
		_, m = serve(t, pep, pepRequest(event, "", nil, nil))
		if len(m) != 0 {
			t.Errorf("%s without feedback must render neutral {}, got %v", event, m)
		}
	}
}

// TestHookPEPContinueOnBlock proves the PostToolUse continue-softening (2.1.139):
// the decider's ContinueOnBlock renders "continue": true alongside the block; the
// default omits the field (omission == hard block, the strictest behavior).
func TestHookPEPContinueOnBlock(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Block: true, Reason: "output flagged", ContinueOnBlock: true}}
	pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
	_, m := serve(t, pep, pepRequest(hookPostToolUse, "Bash", map[string]any{"command": "ls"}, nil))
	if m["decision"] != "block" || m["continue"] != true {
		t.Errorf("softened block = %v, want decision=block + continue=true", m)
	}

	dec = &fakeDecider{res: HookDecisionResult{Block: true, Reason: "output flagged"}}
	pep = NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
	_, m = serve(t, pep, pepRequest(hookPostToolUse, "Bash", map[string]any{"command": "ls"}, nil))
	if m["decision"] != "block" {
		t.Fatalf("block = %v", m)
	}
	if _, has := m["continue"]; has {
		t.Error("a hard block must NOT carry continue (the zero value is the strictest behavior)")
	}
}

// --- permissionPromptToolName route ----------------------------------------

// promptRequest builds a permission-prompt POST (the SDK passes tool_name + tool_input).
func promptRequest(tool string, input map[string]any, headers map[string]string) *http.Request {
	payload := map[string]any{"session_id": "sess-p", "tool_name": tool, "tool_use_id": "tu-p", "tool_input": input}
	b, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "/permission-prompt", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// servePrompt drives the permission-prompt route and returns the PermissionResult body.
func servePrompt(t *testing.T, pep *HookPEP, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	pep.ServePermissionPrompt(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("response is not JSON: %q (%v)", rec.Body.String(), err)
		}
	}
	return rec.Code, m
}

func TestPermissionPromptAllowDeny(t *testing.T) {
	// Allow → {"behavior":"allow"}.
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow, Reason: "ok"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, func() time.Time { return time.Unix(0, 0) })
	code, m := servePrompt(t, pep, promptRequest("Read", map[string]any{"file_path": "/x"}, nil))
	if code != http.StatusOK || m["behavior"] != "allow" {
		t.Fatalf("allow must render behavior=allow, got code=%d %v", code, m)
	}
	// The decision context carries the permissionPromptToolName origin (audit honesty).
	if dec.gotInput.Event != eventPermissionPrompt {
		t.Errorf("event = %q, want %q", dec.gotInput.Event, eventPermissionPrompt)
	}

	// Deny → {"behavior":"deny","message":...}.
	dec = &fakeDecider{res: HookDecisionResult{Permission: DecisionDeny, Reason: "blocked by policy"}}
	pep = NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m = servePrompt(t, pep, promptRequest("Bash", map[string]any{"command": "rm -rf /"}, nil))
	if m["behavior"] != "deny" || m["message"] != "blocked by policy" {
		t.Fatalf("deny payload = %v", m)
	}
	if m["interrupt"] != false {
		t.Errorf("deny must not interrupt the session by default: %v", m)
	}
}

func TestPermissionPromptAllowCarriesRewrite(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow, UpdatedInput: map[string]any{"command": "ls --dry-run"}}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := servePrompt(t, pep, promptRequest("Bash", map[string]any{"command": "ls"}, nil))
	ui, ok := m["updatedInput"].(map[string]any)
	if !ok || ui["command"] != "ls --dry-run" {
		t.Fatalf("allow must carry the governed updatedInput, got %v", m)
	}
}

func TestPermissionPromptAskMapsToDenyClosed(t *testing.T) {
	// The permission-prompt tool is binary; a pending HITL (ask) is surfaced as a deny.
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAsk, Reason: "pending approval ref-9"}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	_, m := servePrompt(t, pep, promptRequest("Bash", map[string]any{"command": "deploy"}, nil))
	if m["behavior"] != "deny" {
		t.Fatalf("ask must map to deny-closed, got %v", m)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "human approval required") {
		t.Errorf("ask deny must explain it is pending HITL, got %q", msg)
	}
}

func TestPermissionPromptDenyClosedEdges(t *testing.T) {
	// Nil decider, decider error and a malformed payload all deny-closed.
	aud := &fakeAuditor{}
	pep := NewHookPEP(nil, aud, nil)
	if _, m := servePrompt(t, pep, promptRequest("Read", map[string]any{"file_path": "/x"}, nil)); m["behavior"] != "deny" {
		t.Fatalf("nil decider must deny-closed: %v", m)
	}
	if len(aud.recs) != 1 || !aud.recs[0].denyClosed {
		t.Fatalf("nil-decider deny must be audited denyClosed: %+v", aud.recs)
	}

	pep = NewHookPEP(&fakeDecider{err: context.DeadlineExceeded}, &fakeAuditor{}, nil)
	if _, m := servePrompt(t, pep, promptRequest("Bash", map[string]any{"command": "x"}, nil)); m["behavior"] != "deny" {
		t.Fatalf("decider error must deny-closed: %v", m)
	}

	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	pep = NewHookPEP(dec, &fakeAuditor{}, nil)
	// No tool name → cannot attribute → deny, decider never run.
	req := httptest.NewRequest(http.MethodPost, "/permission-prompt", bytes.NewReader([]byte(`{"session_id":"s"}`)))
	if _, m := servePrompt(t, pep, req); m["behavior"] != "deny" {
		t.Fatalf("malformed payload must deny-closed: %v", m)
	}
	if dec.calls != 0 {
		t.Fatal("decider must not run on an unattributable payload")
	}
}

func TestPermissionPromptRejectsNonPost(t *testing.T) {
	pep := NewHookPEP(&fakeDecider{}, &fakeAuditor{}, nil)
	rec := httptest.NewRecorder()
	pep.ServePermissionPrompt(rec, httptest.NewRequest(http.MethodGet, "/permission-prompt", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}

func TestPermissionPromptPassesBearerAndIdentity(t *testing.T) {
	dec := &fakeDecider{res: HookDecisionResult{Permission: DecisionAllow}}
	pep := NewHookPEP(dec, &fakeAuditor{}, nil)
	req := promptRequest("Read", map[string]any{"file_path": "/x"}, map[string]string{
		"Authorization": "Bearer prompt-secret",
		hdrHookTenant:   "tnt_9",
		hdrHookAgent:    "agent-sdk-1",
	})
	servePrompt(t, pep, req)
	if dec.gotBearer != "prompt-secret" {
		t.Fatalf("decider must receive the bearer, got %q", dec.gotBearer)
	}
	if dec.gotInput.Identity.Tenant != "tnt_9" || dec.gotInput.Identity.Agent != "agent-sdk-1" {
		t.Fatalf("identity hints not forwarded: %+v", dec.gotInput.Identity)
	}
	// The resource is the redacted reference; the plan hash binds the exact call.
	if dec.gotInput.ResourceKind != resFile || dec.gotInput.PlanHash == "" {
		t.Fatalf("resource/plan hash missing: %+v", dec.gotInput)
	}
}

// TestHookClientDenyClosedRespectsEventClass proves the managed hook command's
// deny-closed verdicts honor the event classes: gating events deny,
// PostToolUse blocks WITHOUT continue, display/feedback events answer neutral.
func TestHookClientDenyClosedRespectsEventClass(t *testing.T) {
	cases := map[string]struct {
		event   string
		wantKey string // top-level key expected in the verdict; "" = neutral {}
	}{
		"PreToolUse denies":      {hookPreToolUse, "hookSpecificOutput"},
		"PostToolUse blocks":     {hookPostToolUse, "decision"},
		"MessageDisplay neutral": {hookMessageDisplay, ""},
		"Stop neutral":           {hookStop, ""},
		"SubagentStop neutral":   {hookSubagentStop, ""},
	}
	for name, tc := range cases {
		var m map[string]any
		if err := json.Unmarshal(denyClosedDecision(tc.event, "endpoint down"), &m); err != nil {
			t.Fatalf("%s: verdict not JSON: %v", name, err)
		}
		if tc.wantKey == "" {
			if len(m) != 0 {
				t.Errorf("%s: want neutral {}, got %v", name, m)
			}
			continue
		}
		if _, has := m[tc.wantKey]; !has {
			t.Errorf("%s: want %q in verdict, got %v", name, tc.wantKey, m)
		}
		if _, has := m["continue"]; has {
			t.Errorf("%s: a deny-closed verdict must never carry continue: %v", name, m)
		}
	}
}
