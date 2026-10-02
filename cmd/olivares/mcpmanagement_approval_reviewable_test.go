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
)

func TestManagedSessionMCPApprovalRefusesHiddenArgumentBehavior(t *testing.T) {
	for name, value := range map[string]map[string]any{
		"shell substitution":    {"command": "echo password=$(printf${IFS}hidden_operation)"},
		"credential field code": {"token": "sk-abcdefghijklmnopqrstuvwx$(touch hidden_operation)"},
		"mixed data":            {"note": "before sk-abcdefghijklmnopqrstuvwx after"},
		"structured credential": {"token": map[string]any{"script": "echo hidden_operation"}},
		"array credential":      {"token": []any{"sk-abcdefghijklmnopqrstuvwx", "echo hidden_operation"}},
		"masked program":        {"command": "sk-abcdefghijklmnopqrstuvwx"},
		"bearer command line":   {"token": "Bearer\n/tmp/hidden_operation"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			value["text"] = "exact input"
			payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": f.alias, "arguments": value}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			req := httptest.NewRequest("POST", "/session/mcp", bytes.NewReader(payload)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.management.ServeSessionHTTP(w, req)
			var response struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != http.StatusForbidden || response.Error.Message != "argument not reviewable" || f.calls.Load() != 0 {
				t.Fatalf("unreviewable call was not refused before review/effect: status=%d body=%s effects=%d", w.Code, w.Body.String(), f.calls.Load())
			}
			if strings.Contains(w.Body.String(), "hidden_operation") {
				t.Fatal("refusal disclosed the argument")
			}
			var pending struct {
				Items []json.RawMessage `json:"items"`
			}
			if code := f.h.reqInto("GET", "/v1/m/governance/approvals?action=mcp.tool.call", f.h.adminToken, f.h.tenantA, nil, &pending); code != http.StatusOK || len(pending.Items) != 0 {
				t.Fatal("unreviewable argument reached the human queue")
			}
		})
	}
}
