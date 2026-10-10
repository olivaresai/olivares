//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommunityCLIHasNoCedarAuthoring(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")
	for _, args := range [][]string{{"publish", "--engine", "cedar", "--source", "permit(principal, action, resource);"}, {"rollback", "--engine", "cedar", "--revision", "1"}} {
		err, _ := runHookPEPCLI(t, args...)
		if err == nil {
			t.Errorf("Community accepted Cedar authoring: %v", args)
		}
	}
	if calls != 0 {
		t.Fatalf("Community Cedar commands entered HTTP: %d", calls)
	}
}

func TestCommunityCLIContinuesNormalizedOPAAuthoring(t *testing.T) {
	for _, action := range []string{"publish", "rollback"} {
		for _, engine := range []string{"opa", " OPA "} {
			t.Run(action+engine, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body struct {
						Engine string `json:"engine"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Engine != "opa" {
						t.Errorf("engine=%q", body.Engine)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"engine":"opa","revision":1,"active":true,"from_revision":0,"to_revision":1}`))
				}))
				defer server.Close()
				t.Setenv("OLIVARES_HOOK_PEP_URL", server.URL)
				t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "policy-token")
				args := []string{action, "--engine", engine, "--format", "json"}
				if action == "publish" {
					args = append(args, "--source", "package fixture\ndefault allow := false")
				} else {
					args = append(args, "--revision", "1")
				}
				err, _ := runHookPEPCLI(t, args...)
				if err != nil || calls != 1 {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
			})
		}
	}
}
