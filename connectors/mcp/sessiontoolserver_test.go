// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionToolServerUsesExistingScopeApprovalAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, tool, scope     string
		destructive, evidence bool
		status                int
		forward               bool
	}{
		{"allowed", "search", "tools:call", false, true, 200, true},
		{"unknown", "unlisted", "tools:call", false, true, 403, false},
		{"scope", "search", "other", false, true, 403, false},
		{"approval", "search", "tools:call", true, true, 403, false},
		{"evidence", "search", "tools:call", false, false, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUpstream{}
			var aud GateAuditor = nopGateAuditor{}
			if tc.evidence {
				aud = &fakeEvidenceJournal{}
			}
			ts, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:call", Destructive: tc.destructive}})
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSessionToolServer(SessionToolServerConfig{Tenant: "tenant", ServerID: "server", Descriptor: "backend:v1", Toolset: ts, Upstream: up, Auditor: aud})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/session/mcp?server=server", nil)
			params, _ := json.Marshal(map[string]any{"name": tc.tool, "arguments": map[string]any{}})
			s.CallTool(w, r, SessionToolIdentity{Subject: "session", ClientID: "launch", Scopes: []string{tc.scope}}, json.RawMessage(`1`), params)
			if w.Code != tc.status || up.called != tc.forward {
				t.Fatalf("status=%d forwarded=%v body=%s", w.Code, up.called, w.Body)
			}
		})
	}
}
