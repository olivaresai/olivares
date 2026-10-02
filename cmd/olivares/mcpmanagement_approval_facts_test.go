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
)

// Review facts and execution are observed at their public HTTP boundaries. The
// redacted preview must not change the canonical arguments sent to the tool.
func TestManagedSessionMCPApprovalCarriesBoundedRedactedArguments(t *testing.T) {
	for _, kind := range []string{"exact redacted arguments", "structured secrets", "embedded string secrets", "escaped string secrets", "mixed string secrets", "unrecognized secret value", "secret-shaped key", "short secret key", "full 16 KiB", "over 16 KiB"} {
		t.Run(kind, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			args := map[string]any{"text": "exact input", "path": "./scripts/notebook.go", "mode": "write", "token": "sk-abcdefghijklmnopqrstuvwx"}
			if kind == "unrecognized secret value" {
				args["token"] = "fixture-secret-only"
			}
			if kind == "structured secrets" {
				args["password"] = "fixture secret tail"
				args["token"] = []any{"fixture-array-secret", map[string]any{"nested": "fixture-object-secret"}}
				args["secret"] = map[string]any{"value": "fixture nested secret"}
				args["credentials"] = map[string]any{"password": "fixture \"quoted\" tail"}
				args["key_block"] = "-----BEGIN PRIVATE " + "KEY-----\nfixture-private-key-body\n-----END PRIVATE " + "KEY-----"
			}
			if kind == "embedded string secrets" {
				args["command"] = "password='first second' && echo ./scripts/notebook.go"
				args["encoded"] = `{"password":"nested string suffix"}`
			}
			if kind == "escaped string secrets" {
				args["command"] = "password=first\\ second && echo ./scripts/notebook.go"
				args["continued"] = "password='first\\\nsecond' && echo ./scripts/notebook.go"
				args["unterminated"] = "password='first second\\"
			}
			if kind == "mixed string secrets" {
				args["command"] = "password=first' second' && echo ./scripts/notebook.go"
				args["adjacent"] = "password='first'\"second\" && echo ./scripts/notebook.go"
			}
			if kind == "short secret key" {
				args["password='ab'"] = "benign value"
			}
			if kind == "secret-shaped key" {
				args["sk-abcdefghijklmnopqrstuvwx"] = "benign value"
			}
			if kind == "full 16 KiB" || kind == "over 16 KiB" {
				args = map[string]any{"text": "exact input", "padding": ""}
				empty, _ := json.Marshal(args)
				size := 16384
				if kind == "over 16 KiB" {
					size++
				}
				args["padding"] = strings.Repeat("x", size-len(empty))
			}
			payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": f.alias, "arguments": args}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			req := httptest.NewRequest("POST", "/session/mcp", bytes.NewReader(payload)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
			defer func() { cancel(); <-done }()
			if kind == "over 16 KiB" || kind == "secret-shaped key" || kind == "short secret key" || kind == "structured secrets" || kind == "embedded string secrets" || kind == "escaped string secrets" || kind == "mixed string secrets" || kind == "unrecognized secret value" {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("unreviewable arguments reached human review instead of immediate refusal")
				}
				var response struct {
					Error json.RawMessage `json:"error"`
				}
				if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Error) == 0 || f.calls.Load() != 0 {
					t.Fatal("unreviewable arguments were not refused before execution")
				}
				var pending struct {
					Items []json.RawMessage `json:"items"`
				}
				if code := f.h.reqInto("GET", "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &pending); code != http.StatusOK || len(pending.Items) != 0 {
					t.Fatal("unreviewable arguments left a pending approval")
				}
				return
			}
			ref := f.pendingApproval(ctx, done, w)
			var approval struct {
				Reason string `json:"reason"`
			}
			if code := f.h.reqInto("GET", "/v1/m/governance/approvals/"+ref, f.h.adminToken, f.h.tenantA, nil, &approval); code != http.StatusOK {
				t.Fatalf("review = %d", code)
			}
			if !strings.Contains(approval.Reason, "MCP tool: write_echo") || !strings.Contains(approval.Reason, "server: Approval HTTPS") || len(approval.Reason) > 65536 {
				t.Fatal("reviewer lacks the bounded tool and server facts")
			}
			if kind == "exact redacted arguments" {
				for _, fact := range []string{`"text":"exact input"`, `"path":"./scripts/notebook.go"`, `"mode":"write"`, "[REDACTED]"} {
					if !strings.Contains(approval.Reason, fact) {
						t.Fatalf("reviewer lacks argument fact %q", fact)
					}
				}
				for _, secret := range []string{"sk-abcdefghijklmnopqrstuvwx", "fixture-secret-only", "secret tail", "fixture-array-secret", "fixture-object-secret", "fixture nested secret", "quoted", "fixture-private-key-body", "first", "second", "nested string suffix", f.token} {
					if strings.Contains(approval.Reason, secret) {
						t.Fatal("approval retained a secret value")
					}
				}
			} else if !strings.Contains(approval.Reason, args["padding"].(string)) {
				t.Fatal("16 KiB review facts were truncated")
			}
			if code, _ := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusCreated && code != http.StatusOK {
				t.Fatalf("human decision = %d", code)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("approved call did not finish")
			}
			var response struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Error) != 0 || f.calls.Load() != 1 {
				t.Fatal("approved call did not execute once")
			}
			var effect struct {
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(<-f.executed, &effect); err != nil {
				t.Fatal(err)
			}
			if len(effect.Arguments) != len(args) {
				t.Fatal("execution argument shape changed")
			}
			for key, value := range args {
				if !reflect.DeepEqual(effect.Arguments[key], value) {
					t.Fatalf("execution argument %q changed after review", key)
				}
			}
		})
	}
}
