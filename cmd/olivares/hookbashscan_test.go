// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
)

func TestHookBashExtractPaths(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		want          []string
		wantAmbiguous bool
	}{
		{name: "absolute path argument", command: `cat /etc/secrets/db.pem`, want: []string{"/etc/secrets/db.pem"}},
		{name: "env assignment skipped", command: `A=/etc/secrets/db.pem cat /tmp/x`, want: []string{"/tmp/x"}},
		{name: "program token skipped", command: `/etc/secrets/program /tmp/x`, want: []string{"/tmp/x"}},
		{name: "single quoted path", command: `cat '/etc/secrets/db.pem'`, want: []string{"/etc/secrets/db.pem"}},
		{name: "double quoted path", command: `cat "/etc/secrets/db.pem"`, want: []string{"/etc/secrets/db.pem"}},
		{name: "public sibling", command: `cat /etc/public/x`, want: []string{"/etc/public/x"}},
		{name: "pipe", command: `cat /etc/secrets/db.pem | wc`, wantAmbiguous: true},
		{name: "semicolon", command: `cat /etc/secrets/db.pem; rm x`, wantAmbiguous: true},
		{name: "and list", command: `ls /etc/secrets && rm x`, wantAmbiguous: true},
		{name: "subshell", command: `$(printf /etc/sec)rets`, wantAmbiguous: true},
		{name: "redirect", command: `echo x > /etc/secrets/y`, wantAmbiguous: true},
		{name: "variable expansion", command: `cat $SECRET`, wantAmbiguous: true},
		{name: "glob meta", command: `cat /etc/secrets/*.pem`, wantAmbiguous: true},
		{name: "partial quote", command: `cat "/etc/sec"rets/x`, wantAmbiguous: true},
		{name: "unbalanced quote", command: `cat "/etc/secrets/db.pem`, wantAmbiguous: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ambiguous := extractBashPaths(tc.command)
			if ambiguous != tc.wantAmbiguous || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractBashPaths(%q) = %v, ambiguous=%v; want %v, ambiguous=%v", tc.command, got, ambiguous, tc.want, tc.wantAmbiguous)
			}
		})
	}
}

// TestCampaignHookBashBackslashEscape pins the finding: an unquoted backslash must
// escape the next byte (bash: \x -> x) so a path-scoped deny fires on the RESOLVED path.
// Before the fix, `cat /etc/secrets/.en\v` tokenized as the literal ".en\v" and silently
// bypassed a deny rule for ".env".
func TestCampaignHookBashBackslashEscape(t *testing.T) {
	tests := []struct {
		command       string
		want          []string
		wantAmbiguous bool
	}{
		{command: `cat /etc/secrets/.en\v`, want: []string{"/etc/secrets/.env"}},
		{command: `cat /etc/pass\wd`, want: []string{"/etc/passwd"}},
		{command: `cat "/etc/secrets/a\$b"`, want: []string{"/etc/secrets/a$b"}}, // double-quote escape set
		{command: `cat /etc/secrets/db.pem\`, wantAmbiguous: true},               // trailing backslash: incomplete
	}
	for _, tc := range tests {
		got, ambiguous := extractBashPaths(tc.command)
		if ambiguous != tc.wantAmbiguous || (!tc.wantAmbiguous && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("extractBashPaths(%q) = %v, ambiguous=%v; want %v, ambiguous=%v", tc.command, got, ambiguous, tc.want, tc.wantAmbiguous)
		}
	}
}

func TestHookBashScanPathPolicyMatrix(t *testing.T) {
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Paths:    []string{"/etc/secrets/**"},
			Decision: "deny",
			Reason:   "secret subtree",
		}},
	}
	base := hookDisposition{decision: claude.DecisionAllow}
	tests := []struct {
		name           string
		command        string
		want           string
		secretFragment string
	}{
		{name: "denies absolute secret path", command: `cat /etc/secrets/db.pem`, want: claude.DecisionDeny, secretFragment: "/etc/secrets/db.pem"},
		{name: "denies after env assignment", command: `A=1 cat /etc/secrets/db.pem`, want: claude.DecisionDeny, secretFragment: "/etc/secrets/db.pem"},
		{name: "public sibling stays base", command: `cat /etc/public/x`, want: claude.DecisionAllow},
		{name: "relative traversal asks", command: `cat ../../etc/secrets/db.pem`, want: claude.DecisionAsk, secretFragment: "../../etc/secrets/db.pem"},
		{name: "relative non traversal stays base", command: `cat ./notes.txt`, want: claude.DecisionAllow},
		{name: "partial quote asks", command: `cat "/etc/sec"rets/x`, want: claude.DecisionAsk, secretFragment: `/etc/sec"rets/x`},
		{name: "redirect asks", command: `echo x > /etc/secrets/y`, want: claude.DecisionAsk, secretFragment: "/etc/secrets/y"},
		{name: "and list asks", command: `ls /etc/secrets && rm x`, want: claude.DecisionAsk, secretFragment: "/etc/secrets"},
		{name: "subshell asks", command: `$(printf /etc/sec)rets`, want: claude.DecisionAsk},
		{name: "variable asks", command: `cat $SECRET`, want: claude.DecisionAsk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := runHookBashScanPEP(t, pol, base, tc.command)
			if got := decisionOf(out); got != tc.want {
				t.Fatalf("decision = %q, want %q (%v)", got, tc.want, out)
			}
			reason, _ := out["permissionDecisionReason"].(string)
			if tc.secretFragment != "" && strings.Contains(reason, tc.secretFragment) {
				t.Fatalf("reason must not echo raw path/command fragment %q: %q", tc.secretFragment, reason)
			}
		})
	}
}

func TestHookBashScanSubtreeAsk(t *testing.T) {
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Subtree:  "/srv/acme/Finance",
			Decision: "ask",
		}},
	}
	out := runHookBashScanPEP(t, pol, hookDisposition{decision: claude.DecisionAllow}, `cat /srv/acme/Finance/q3.xlsx`)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("subtree ask rule must ask, got %q (%v)", got, out)
	}
}

func TestHookBashScanNoPathScopedPolicyNoop(t *testing.T) {
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Decision: "allow",
		}},
	}
	out := runHookBashScanPEP(t, pol, hookDisposition{decision: claude.DecisionAllow}, `cat $SECRET`)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("non path-scoped Bash policy must stay base allow, got %q (%v)", got, out)
	}
}

func TestHookBashScanKeepsExistingAsk(t *testing.T) {
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Paths:    []string{"/etc/secrets/**"},
			Decision: "deny",
		}},
	}
	base := hookDisposition{decision: claude.DecisionAsk, reason: "existing review"}
	out := runHookBashScanPEP(t, pol, base, `cat $SECRET`)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("existing ask must remain ask, got %q (%v)", got, out)
	}
	if reason, _ := out["permissionDecisionReason"].(string); reason != base.reason {
		t.Fatalf("existing ask reason must be preserved, got %q", reason)
	}
}

func TestHookBashDecideStep4bRestrictsDefaultAllow(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-bash-path@e2e.test")
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Paths:    []string{"/etc/secrets/**"},
			Decision: "deny",
			Reason:   "secret subtree",
		}},
	}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	out := f.call(t, "Bash", map[string]any{"command": "cat /etc/secrets/db.pem"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("Decide step 4b must deny Bash path hit under default allow, got %q (%v)", got, out)
	}
	reason, _ := out["permissionDecisionReason"].(string)
	if strings.Contains(reason, "/etc/secrets/db.pem") {
		t.Fatalf("deny reason must not echo the raw path, got %q", reason)
	}
}

type hookBashScanTestDecider struct {
	pol  hookPolicyDoc
	base hookDisposition
}

func (d hookBashScanTestDecider) Decide(_ context.Context, in claude.HookDecisionInput, _ string) (claude.HookDecisionResult, error) {
	disp := bashPathScan(d.pol, in, "", d.base)
	return claude.HookDecisionResult{
		Permission: firstNonEmptyStr(disp.decision, claude.DecisionAllow),
		Reason:     disp.reason,
	}, nil
}

func runHookBashScanPEP(t *testing.T, pol hookPolicyDoc, base hookDisposition, command string) map[string]any {
	t.Helper()
	pep := claude.NewHookPEP(hookBashScanTestDecider{pol: pol, base: base}, nil, time.Now)
	payload := map[string]any{
		"session_id":      "sess-bash",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-bash",
		"tool_input":      map[string]any{"command": command},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	pep.ServeHTTP(rec, req)

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", rec.Body.String())
	}
	hso, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("response has no hookSpecificOutput: %v", m)
	}
	return hso
}

func FuzzBashPathScanNeverWidens(f *testing.F) {
	f.Add([]byte(`cat /etc/secrets/db.pem`))
	f.Add([]byte(`cat ../../etc/secrets/db.pem`))
	f.Add([]byte(`cat "$(printf /etc/secrets/db.pem)"`))
	f.Add([]byte{0xff, 0x00, '|', '$', '('})

	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{{
			Tool:     "Bash",
			Paths:    []string{"/etc/secrets/**"},
			Decision: "deny",
		}},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		command := string(data)
		_, _ = extractBashPaths(command)

		for _, base := range []string{claude.DecisionAsk, claude.DecisionDeny} {
			out := runHookBashScanPEP(t, pol, hookDisposition{decision: base}, command)
			if got := decisionOf(out); got == claude.DecisionAllow {
				t.Fatalf("Bash path scan widened base %q to allow for command bytes %x", base, data)
			}
		}
	})
}
