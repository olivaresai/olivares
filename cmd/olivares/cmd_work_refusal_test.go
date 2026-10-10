// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestWorkCLIRefusalPreservesEngineFieldHint(t *testing.T) {
	for _, tc := range []struct {
		name, code, field, hint string
		status, exit            int
	}{
		{"missing workspace", "invalid_command", "workspace_id", "workspace_id", 400, exitcode.Usage},
		{"incomplete acceptance", "acceptance_incomplete", "required (at least one criterion)", "required (at least one criterion)", 422, exitcode.Conflict},
		{"terminal and credential safety", "invalid_command", "workspace_id\x1b[31m secret-token", "workspace_id", 400, exitcode.Usage},
		{"ordinary validation refusal", "invalid_command", "", "", 400, exitcode.Usage},
		{"authority refusal", "forbidden", "", "", 403, exitcode.Auth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var doc map[string]any
				if r.Method != http.MethodPost || r.URL.Path != "/v1/m/sessions/work-items" || r.URL.Query().Get("mode") != "apply" ||
					r.Header.Get("Authorization") != "Bearer secret-token" || json.NewDecoder(r.Body).Decode(&doc) != nil ||
					len(doc) != 3 || doc["command"] != "item.create" || doc["title"] != "Review README.md" || doc["brief_md"] != "Review the file before handing it over." {
					t.Error("the incomplete request was changed or not sent to the engine")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"verdict": "ROTO", "evidence_ref": tc.field,
					"error": map[string]string{"code": tc.code, "message": tc.code}})
			}))
			defer server.Close()
			out, _, err := execRoot(t, withConnect(server.URL, "work", "apply", "item.create", "--title", "Review README.md", "--brief", "Review the file before handing it over.", "-o", "json")...)
			if err == nil || exitcode.From(err) != tc.exit || out != "" || calls != 1 || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("refusal contract changed: err=%v out=%q calls=%d", err, out, calls)
			}
			if tc.hint != "" && !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("the engine's field hint was lost: %v", err)
			}
			if strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("unsafe engine evidence reached the terminal: %v", err)
			}
			if tc.field == "" && err.Error() != "the engine rejected this request: invalid_command (HTTP 400)" && tc.status == 400 {
				t.Fatalf("a refusal without a field changed: %v", err)
			}
		})
	}
}
