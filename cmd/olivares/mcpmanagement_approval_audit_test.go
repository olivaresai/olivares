// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The waiting HTTP call remains connected while the operator interrupts or
// stops its real session. The refusal still belongs in the tenant's ledger.
func TestManagedSessionMCPApprovalCancellationAnchorsOneRefusal(t *testing.T) {
	for _, control := range []string{"interrupt", "stop"} {
		t.Run(control, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			var logs bytes.Buffer
			f.management.eng.log = slog.New(slog.NewTextHandler(&logs, nil))
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			req := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"text":"exact input"}}}`, f.alias))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
			defer func() { cancel(); <-done }()
			ref := f.pendingApproval(ctx, done, w)
			f.waitForRunProjection(ctx, ref)
			if code, body := f.h.req("POST", "/v1/m/sessions/runs/"+f.run+"/"+control, f.h.adminToken, f.h.tenantA, nil); code != http.StatusOK {
				t.Fatalf("%s = %d: %s", control, code, body)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("ended call did not return")
			}
			var response struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Error) == 0 {
				t.Fatalf("ended call did not return a refusal: %s", w.Body.String())
			}
			if code, body := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusConflict {
				t.Errorf("late approve = %d: %s, want 409", code, body)
			}
			if f.calls.Load() != 0 {
				t.Errorf("abandoned upstream effect ran %d times", f.calls.Load())
			}
			var refusals []canonicalLedgerEvent
			for _, row := range canonicalLedgerEventsFrom(t, f.h.st, f.tenant, 1) {
				if row.event.Action == "mcp.tool.deny" {
					refusals = append(refusals, row)
				}
			}
			if len(refusals) != 1 {
				t.Errorf("MCP refusal ledger entries = %d, want exactly 1", len(refusals))
			} else {
				row := refusals[0]
				if row.event.TargetKind != "mcp.tool" || row.event.TargetID.String() != "write_echo" || row.meta["approval_ref"] != ref || row.meta["allowed"] != false || row.meta["reason"] != "destructive tool not approved (rejected)" {
					t.Errorf("ledger refusal lost its tool, approval or cause: %+v, %+v", row.event, row.meta)
				}
				if row.meta["policy_decision"] != "ask" || row.meta["policy_id"] != "tool:write_echo" || row.meta["server_name"] == nil || row.meta["client_id"] == nil {
					t.Errorf("ledger refusal lost its decision-table row: %+v", row.meta)
				}
			}
			if strings.Contains(logs.String(), "evidence gap") {
				t.Errorf("cancellation lost audit evidence: %s", logs.String())
			}
		})
	}
}
