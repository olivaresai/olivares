// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyAuthoringUsesTheExistingPolicyAPI(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
	}{
		{[]string{"ls"}, "GET", "/v1/m/governance/policies"},
		{[]string{"get", "policy-1"}, "GET", "/v1/m/governance/policies/policy-1"},
		{[]string{"create", "--data", `{"name":"linked","kind":"linked-kind","enabled":true,"spec":{"mode":"bounded"}}`}, "POST", "/v1/m/governance/policies"},
		{[]string{"update", "policy-1", "--data", `{"name":"linked","kind":"linked-kind","enabled":false,"spec":{"mode":"bounded"}}`}, "PUT", "/v1/m/governance/policies/policy-1"},
		{[]string{"rm", "policy-1", "--yes"}, "DELETE", "/v1/m/governance/policies/policy-1"},
	} {
		t.Run(strings.Join(tc.args[:1], ""), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("X-Olivares-Tenant") != "tenant-a" || r.Header.Get("Authorization") != "Bearer secret-token" {
					t.Error("request lost tenant or authentication")
				}
				if r.Method == "POST" || r.Method == "PUT" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["kind"] != "linked-kind" {
						t.Errorf("kind=%v", body["kind"])
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				if tc.args[0] == "ls" {
					_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"policy-1","name":"linked","kind":"linked-kind"}`))
			}))
			defer srv.Close()
			args := append([]string{"policy"}, tc.args...)
			_, _, err := execRoot(t, withConnect(srv.URL, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("API calls=%d", calls)
			}
		})
	}
}
