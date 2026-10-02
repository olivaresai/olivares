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
)

func TestReportingSigningCLIUsesTheAuthenticatedTransport(t *testing.T) {
	for _, tc := range []struct {
		verb, method string
		enabled      bool
	}{
		{"status", http.MethodGet, false}, {"enable", http.MethodPut, true}, {"disable", http.MethodPut, false},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			prepareModelstackCLITest(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != "/v1/m/reporting/signing" || r.Header.Get("Authorization") != "Bearer fixture-signing-token" {
					t.Errorf("wrong edge: %s %s", r.Method, r.URL.Path)
				}
				if r.Method == http.MethodPut {
					var body struct {
						Enabled *bool `json:"enabled"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil || *body.Enabled != tc.enabled {
						t.Errorf("wrong signing input: %+v, %v", body, err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"enabled":true,"ready":true,"reason":"","key_id":"reporting-test","public_key":"public-test-key","source":"product"}`))
			}))
			defer srv.Close()
			out, stderr, err := execRoot(t, "reporting", "signing", tc.verb, "--server", srv.URL, "--token", "fixture-signing-token", "-o", "json")
			if err != nil {
				t.Fatalf("signing %s: %v (%s)", tc.verb, err, stderr)
			}
			if calls != 1 {
				t.Fatalf("requests=%d, want 1", calls)
			}
			var status struct {
				Ready bool   `json:"ready"`
				KeyID string `json:"key_id"`
			}
			if err := json.Unmarshal([]byte(out), &status); err != nil || !status.Ready || status.KeyID != "reporting-test" {
				t.Fatalf("wrong status: %s (%v)", out, err)
			}
			if strings.Contains(out, "private_key") || strings.Contains(out, "secret_ref") {
				t.Fatal("secret metadata in management response")
			}
		})
	}
}
