// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares voice`.

func TestVoiceVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"voice", "sessions", "ls"}, "GET", "/v1/m/voice/sessions"},
		{[]string{"voice", "sessions", "get", "vs-1"}, "GET", "/v1/m/voice/sessions/vs-1"},
		{[]string{"voice", "sessions", "decisions", "vs-1"}, "GET", "/v1/m/voice/sessions/vs-1/decisions"},
		{[]string{"voice", "sessions", "open", "--session-ref", "vs-1", "--agent-ref", "a",
			"--model-ref", "m", "--provider-ref", "p"}, "POST", "/v1/m/voice/sessions/open"},
		{[]string{"voice", "policies", "ls"}, "GET", "/v1/m/voice/policies"},
		{[]string{"voice", "policies", "set", "--agent-ref", "a",
			"--allowed-model-ref", "m", "--allowed-provider-ref", "p"}, "PUT", "/v1/m/voice/policies"},
		{[]string{"voice", "decisions"}, "GET", "/v1/m/voice/decisions"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"id":"vs-1","op":"open","op_status":"opened"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestVoiceOpenCarriesTheWholeRequestAndReportsThePolicyVerdict. A session that
// "opened" is a live audio path to a customer, so what travels and what comes
// back both matter.
func TestVoiceOpenCarriesTheWholeRequestAndReportsThePolicyVerdict(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"op":"open","op_status":"opened","policy_verdict":"allow",
		"gate_status":"approved","dispatch_ref":"d-1"}`))
	out, _, err := execRoot(t, lot3Args(srv.URL, "voice", "sessions", "open",
		"--session-ref", "vs-1", "--agent-ref", "agent-a",
		"--model-ref", "m1", "--provider-ref", "p1")...)
	if err != nil {
		t.Fatalf("opening must succeed, got %v", err)
	}
	body := srv.lastBody()
	for _, want := range []string{`"session_ref":"vs-1"`, `"agent_ref":"agent-a"`,
		`"model_ref":"m1"`, `"provider_ref":"p1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the open request is missing %s: %s", want, body)
		}
	}
	if !strings.Contains(out, "policy_verdict") || !strings.Contains(out, "allow") {
		t.Errorf("the policy verdict must be reported, got:\n%s", out)
	}
}

// TestVoiceOpenPolicyDenialIsExplainedNotJustDenied. "You are missing a role" and
// "your policy forbids that model" have different remedies.
func TestVoiceOpenPolicyDenialIsExplainedNotJustDenied(t *testing.T) {
	srv := newLot3Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"op":"open","op_status":"denied","policy_verdict":"model_not_allowed",
			"detail":"agent-a may only speak through m1"}`)
	})
	_, _, err := execRoot(t, lot3Args(srv.URL, "voice", "sessions", "open",
		"--session-ref", "vs-1", "--agent-ref", "agent-a",
		"--model-ref", "m9", "--provider-ref", "p1")...)
	if err == nil || exitcode.From(err) != exitcode.Auth {
		t.Fatalf("a policy denial must exit %d, got %v", exitcode.Auth, err)
	}
	if !strings.Contains(err.Error(), "model_not_allowed") {
		t.Errorf("the policy verdict must be named, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not by your role") {
		t.Errorf("the wrong diagnosis must be ruled out, got: %v", err)
	}
}

// TestVoicePolicySetIsAWholePolicyAndRejectsNegativeLimits.
func TestVoicePolicySetIsAWholePolicyAndRejectsNegativeLimits(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"vp-1","agent_ref":"agent-a"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "voice", "policies", "set",
		"--agent-ref", "a", "--allowed-model-ref", "m", "--allowed-provider-ref", "p",
		"--max-session-minutes", "-5")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("a negative limit must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with a negative limit", n)
	}

	// THE CONTROL: a whole policy travels, including the optional call policy.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "voice", "policies", "set",
		"--agent-ref", "agent-a", "--allowed-model-ref", "m1", "--allowed-provider-ref", "p1",
		"--max-session-minutes", "30", "--max-latency-ms", "800",
		"--calls-file", lot3WriteTempJSON(t, `{"outbound":false}`))...); err != nil {
		t.Fatalf("a complete policy must be accepted, got %v", err)
	}
	body := srv.lastBody()
	for _, want := range []string{`"agent_ref":"agent-a"`, `"allowed_model_ref":"m1"`,
		`"max_session_minutes":30`, `"max_latency_ms":800`, `"outbound":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("the policy body is missing %s: %s", want, body)
		}
	}
}

// TestVoiceMissingRequiredFlagsAreAUsageErrorBeforeAnyRequest: a policy or an
// open with half its identifiers is a request the engine would have to guess at.
func TestVoiceMissingRequiredFlagsAreAUsageErrorBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{}`))
	for _, incomplete := range [][]string{
		{"voice", "sessions", "open", "--session-ref", "vs-1"},
		{"voice", "policies", "set", "--agent-ref", "a"},
	} {
		_, _, err := execRoot(t, lot3Args(srv.URL, incomplete...)...)
		if err == nil {
			t.Fatalf("%v must not succeed", incomplete)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d incomplete request(s) reached the server", n)
	}
}
