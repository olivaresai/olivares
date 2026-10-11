// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComplianceRiskCLI(t *testing.T) {
	for _, verb := range []string{"ls", "classify"} {
		t.Run(verb, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
					t.Error("native saved authority omitted")
				}
				body := `{"id":"risk-1","subject_kind":"agent","subject_ref":"agent-ref","tier":"limited","suggested_tier":"limited","state":"suggested","signals":{"total_edges":0,"declared_autonomy":{"state":"declared","scheduled":true,"autonomous":true}}}`
				if verb == "classify" {
					if r.Method != "POST" || r.URL.Path != "/v1/m/compliance/risk/classify" {
						t.Error("wrong classify route")
					}
					var req map[string]any
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Fatal(err)
					}
					if req["subject_ref"] != "agent-ref" || req["subject_kind"] != "agent" {
						t.Error("native identity changed")
					}
					w.WriteHeader(http.StatusCreated)
					io.WriteString(w, body)
				} else {
					if r.Method != "GET" || r.URL.Path != "/v1/m/compliance/risk" {
						t.Error("wrong risk read route")
					}
					io.WriteString(w, `{"items":[`+body+`],"has_more":false}`)
				}
			}))
			t.Cleanup(srv.Close)
			args := []string{"compliance", "risk", verb}
			if verb == "classify" {
				args = append(args, "agent-ref")
			}
			args = append(args, "-o", "json")
			out, _, err := execRoot(t, complianceTestArgs(srv.URL, args...)...)
			if err != nil || !called || !strings.Contains(out, `"declared_autonomy"`) || !strings.Contains(out, `"total_edges"`) {
				t.Fatalf("native risk CLI missing intent/activity read-back: err=%v out=%s", err, out)
			}
		})
	}
}
