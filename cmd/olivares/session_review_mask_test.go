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
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestSessionClaudeReviewRefusesMaskedShellOperations(t *testing.T) {
	h := newHarness(t)
	commands := []string{
		"olvs_abcdefgh",
		"printf visible | olvs_abcdefgh",
		"eval olvs_abcdefgh",
		"exec olvs_abcdefgh",
		"password=olvs_abcdefgh; $password",
		">/dev/null olvs_abcdefgh",
		`e""val olvs_abcdefgh`,
		"source olvs_abcdefgh",
		". olvs_abcdefgh",
		"time -p olvs_abcdefgh",
		"coproc olvs_abcdefgh",
		"function olvs_abcdefgh() { printf visible; }",
		"trap 'olvs_abcdefgh' EXIT",
		"alias next=olvs_abcdefgh",
		`bind -x '"x":olvs_abcdefgh'`,
		"enable -f olvs_abcdefgh builtin_name",
		"fc -s olvs_abcdefgh",
		"history -s olvs_abcdefgh",
		"mapfile -C olvs_abcdefgh lines",
		"readarray -C olvs_abcdefgh lines",
		"complete -F olvs_abcdefgh tool",
		"compgen -C olvs_abcdefgh",
		"jobs -x olvs_abcdefgh",
		"hash -p olvs_abcdefgh tool",
		"let x=olvs_abcdefgh",
		"declare -i x=olvs_abcdefgh",
		"typeset -i x=olvs_abcdefgh",
		"local -i x=olvs_abcdefgh",
		"(( x = olvs_abcdefgh ))",
		"echo password=$(printf${IFS}hidden_action)",
		"echo password=`id`",
		"echo password=${VARIABLE}",
		"echo password=$((1+2))",
		"echo password=<(id)",
		"echo password=literal>target",
		"echo password=literal|id",
		"echo password=literal*.txt",
		"echo password=literal{a,b}",
		`echo password=literal\value`,
		"echo password=literal[ab]",
		"echo password=literal[REDACTED]",
	}
	for _, command := range commands {
		for _, rewrite := range []bool{false, true} {
			label := "original/"
			if rewrite {
				label = "rewrite/"
			}
			t.Run(label+command, func(t *testing.T) { checkSessionShellReview(t, h, command, rewrite, "") })
		}
	}
}

func TestSessionClaudeReviewKeepsLiteralSecretsMasked(t *testing.T) {
	h := newHarness(t)
	for _, scenario := range []struct{ command, shown string }{
		{"PASSWORD=synthetic-literal printf visible", "PASSWORD=[REDACTED] printf visible"},
		{`PASSWORD="synthetic-literal" printf visible`, `PASSWORD="[REDACTED]" printf visible`},
		{"PASSWORD=synthetic-literal printf visible; printf visible", "PASSWORD=[REDACTED] printf visible; printf visible"},
		{"PASSWORD=synthetic-literal printf visible && printf visible", "PASSWORD=[REDACTED] printf visible && printf visible"},
		{"PASSWORD=synthetic-literal printf visible | cat", "PASSWORD=[REDACTED] printf visible | cat"},
	} {
		for _, rewrite := range []bool{false, true} {
			label := "original/"
			if rewrite {
				label = "rewrite/"
			}
			t.Run(label+scenario.command, func(t *testing.T) { checkSessionShellReview(t, h, scenario.command, rewrite, scenario.shown) })
		}
	}
}

func checkSessionShellReview(t *testing.T, h *harness, command string, rewrite bool, shown string) {
	t.Helper()
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "shell-review")
	intent.PermissionMode, intent.TemplateBuiltin, intent.AllowedTools = "default", true, nil
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	rule := hookPolicyRule{Tool: "Bash", Decision: "ask"}
	original := command
	if rewrite {
		original = "echo visible"
		rule.Rewrite = map[string]any{"command": command}
	}
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{rule}}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": original}})
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/hooks", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
	defer func() { cancel(); <-done }()
	for {
		items, _, err := service.List(ctx, tenant, hookActionCapability, "pending", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			if shown == "" {
				t.Fatal("masked shell operations reached the approval queue")
			}
			if len(items) != 1 || items[0].Reason != "Claude Code requests Bash\ncommand: "+shown {
				t.Fatal("literal credential or visible shell syntax changed in reviewer facts")
			}
			if _, err := service.Decide(ctx, tenant, human, items[0].ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("review did not finish")
			}
			var reply struct {
				HookSpecificOutput struct {
					Permission string         `json:"permissionDecision"`
					Input      map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.HookSpecificOutput.Permission != "allow" {
				t.Fatal("reviewable literal command was refused")
			}
			if rewrite && reply.HookSpecificOutput.Input["command"] != command {
				t.Fatal("display masking changed the command to execute")
			}
			return
		}
		select {
		case <-done:
			if shown != "" || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) || (!strings.Contains(rec.Body.String(), "redaction would hide shell syntax") && !strings.Contains(rec.Body.String(), "command not reviewable")) {
				t.Fatal("unreviewable command did not explicitly deny before queueing")
			}
			return
		case <-ctx.Done():
			t.Fatal("review did not settle")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSessionClaudeReviewRefusesMaskedDelegatedExecutables(t *testing.T) {
	h := newHarness(t)
	for _, command := range []string{"xargs olvs_abcdefgh", "nice olvs_abcdefgh", "nohup olvs_abcdefgh", "timeout 1 olvs_abcdefgh", "setsid olvs_abcdefgh", "sudo olvs_abcdefgh", "doas olvs_abcdefgh", "stdbuf -oL olvs_abcdefgh", "chroot /tmp olvs_abcdefgh", "watch olvs_abcdefgh", "flock /tmp/lock olvs_abcdefgh", "ionice olvs_abcdefgh", "nsenter olvs_abcdefgh", "unshare olvs_abcdefgh", "strace olvs_abcdefgh", "ltrace olvs_abcdefgh", "unknown-wrapper olvs_abcdefgh", "printf 'olvs_abcdefgh'", "echo password=synthetic-literal", "TOKEN=synthetic-literal printf olvs_abcdefgh"} {
		for _, rewrite := range []bool{false, true} {
			label := "original/"
			if rewrite {
				label = "rewrite/"
			}
			t.Run(label+command, func(t *testing.T) { checkSessionShellReview(t, h, command, rewrite, "") })
		}
	}
}
