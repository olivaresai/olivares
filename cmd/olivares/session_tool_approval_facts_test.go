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
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func TestSessionClaudeApprovalCarriesRedactedToolFacts(t *testing.T) {
	maxCommand := "printf '" + strings.Repeat("x", 16370) + "'; pwd"
	for _, tc := range []struct {
		name, tool string
		input      map[string]any
		want       string
		deny       bool
	}{
		{"command", "Bash", map[string]any{"command": "printf 'hello world' > /tmp/approved.txt"}, "printf 'hello world' > /tmp/approved.txt", false},
		{"full 16 KiB command", "Bash", map[string]any{"command": maxCommand}, maxCommand, false},
		{"redacted command", "Bash", map[string]any{"command": "TOKEN=abc123secret curl https://user:examplepass@host/path --token olvs_1234567890abcdef"}, "https://[REDACTED]@host/path", false},
		{"write path only", "Write", map[string]any{"file_path": "/tmp/approval-write.go", "content": "do-not-retain-file-contents"}, "/tmp/approval-write.go", false},
		{"edit path only", "Edit", map[string]any{"file_path": "/tmp/approval-edit.go", "old_string": "do-not-retain-file-contents", "new_string": "do-not-retain-file-contents"}, "/tmp/approval-edit.go", false},
		{"oversized command", "Bash", map[string]any{"command": "printf " + strings.Repeat("x", 16384)}, "command too long to review (16391 bytes)", true},
		{"missing command", "Bash", map[string]any{}, "command is unavailable for human review", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "approval-facts-"+tc.name)
			token, err := c.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			createSessionReviewPolicy(t, h, hookpep.ActionCapability, "claude.tool")
			d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Approvals: service, Store: h.st, Clock: time.Now, Log: discardLog()})
			payload, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "untrusted-vendor-session", "tool_name": tc.tool, "tool_input": tc.input})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			done := make(chan struct{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/hooks", bytes.NewReader(payload)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
			defer func() { cancel(); <-done }()
			var pending governance.Approval
			for pending.ID == "" {
				items, _, err := service.List(context.Background(), tenant, hookpep.ActionCapability, "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != 0 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					if !tc.deny || !bytes.Contains(rec.Body.Bytes(), []byte(`"permissionDecision":"deny"`)) {
						t.Fatal("hook finished without the expected review or explicit refusal")
					}
					if !strings.Contains(rec.Body.String(), tc.want) {
						t.Fatal("hook refusal did not explain the unavailable review facts")
					}
					return
				case <-ctx.Done():
					t.Fatal("hook facts decision deadline")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if tc.deny {
				t.Fatal("incomplete or oversized command reached human review")
			}
			if !strings.Contains(pending.Reason, tc.tool) || !strings.Contains(pending.Reason, tc.want) || len(pending.Reason) > 65536 {
				t.Fatal("reviewer lacks the bounded full tool/command/path facts")
			}
			for _, secret := range []string{"examplepass", "olvs_1234567890abcdef", "abc123secret", "do-not-retain-file-contents"} {
				if strings.Contains(pending.Reason, secret) {
					t.Fatal("approval retained a secret or file contents")
				}
			}
			code, raw := h.req(http.MethodGet, "/v1/m/governance/approvals/"+pending.ID, h.adminToken, h.tenantA, nil)
			var view governance.Approval
			if code != http.StatusOK || json.Unmarshal(raw, &view) != nil || view.Reason != pending.Reason {
				t.Fatal("REST approval view did not carry the reviewer facts unchanged")
			}
			if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			<-done
			if !bytes.Contains(rec.Body.Bytes(), []byte(`"permissionDecision":"allow"`)) {
				t.Fatal("approved hook did not receive allow")
			}
		})
	}
}
