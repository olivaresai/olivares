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

func TestDestructiveBootstrapCommandsHonorJSONOutput(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		path, text, action string
	}{
		{"token", []string{"tokens", "revoke", "item-1"}, "/v1/tokens/item-1", "revoked API token item-1\n", "revoked"},
		{"invitation", []string{"members", "invites", "revoke", "item-1"}, "/v1/invites/item-1", "revoked invitation item-1\n", "revoked"},
		{"tenant", []string{"tenants", "rm", "item-1"}, "/v1/system/orgs/item-1", "deleted tenant item-1\n", "deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			status := http.StatusNoContent
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer secret-token" {
					t.Error("mutation request changed")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(status)
				if status != http.StatusNoContent {
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "item not found"}})
				}
			}))
			defer server.Close()
			for _, format := range []string{"", "text", "json"} {
				args := append(append([]string{}, tc.args...), "--yes")
				if format != "" {
					args = append(args, "-o", format)
				}
				out, _, err := execRoot(t, withConnect(server.URL, args...)...)
				if err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if format != "json" {
					if out != tc.text {
						t.Fatalf("plain output changed: got %q, want %q", out, tc.text)
					}
					continue
				}
				var result map[string]any
				if json.Unmarshal([]byte(out), &result) != nil || len(result) != 2 || result["id"] != "item-1" || result[tc.action] != true {
					t.Fatalf("JSON consumer could not read successful %s result: %q", tc.name, out)
				}
			}
			if requests != 3 {
				t.Fatalf("requests = %d, want 3", requests)
			}
			status = http.StatusNotFound
			args := append(append([]string{}, tc.args...), "--yes", "-o", "json")
			out, _, err := execRoot(t, withConnect(server.URL, args...)...)
			if exitcode.From(err) != exitcode.NotFound || strings.TrimSpace(out) != "" || requests != 4 {
				t.Fatalf("refusal changed or reported success: %v, out=%q requests=%d", err, out, requests)
			}
		})
	}
}
