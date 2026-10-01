// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkSessionCLITokenPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, session, operator, context, want string
		flags                                  []string
		wantError                              bool
	}{
		{name: "session only", session: "scoped-work", want: "scoped-work"},
		{name: "session before operator environment", session: "scoped-work", operator: "operator-env", want: "scoped-work"},
		{name: "session before saved context", session: "scoped-work", context: "operator-context", want: "scoped-work"},
		{name: "explicit operator token", session: "scoped-work", flags: []string{"--token", "explicit-token"}, want: "explicit-token"},
		{name: "explicit empty stays empty", session: "scoped-work", operator: "operator-env", flags: []string{"--token="}, wantError: true},
		{name: "operator outside session", operator: "operator-env", want: "operator-env"},
		{name: "context outside session", context: "operator-context", want: "operator-context"},
		{name: "invalid session cannot use operator", session: "invalid\nwork", operator: "operator-env", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_WORK_TOKEN", tc.session)
			t.Setenv("OLIVARES_TOKEN", tc.operator)
			t.Setenv("OLIVARES_COMMUNICATION_TOKEN", "separate-communication")
			configPath := filepath.Join(t.TempDir(), "client.yaml")
			t.Setenv(cliConfigOverrideEnv, configPath)
			if tc.context != "" {
				if err := writeCLIConfig(configPath, cliConfig{
					CurrentContext: "operator",
					Contexts:       []cliContext{{Name: "operator", Token: tc.context}},
				}); err != nil {
					t.Fatal(err)
				}
			}
			var requests int
			var authorization string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				authorization = r.Header.Get("Authorization")
				if r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
					t.Error("request lost the selected tenant")
				}
				_, _ = w.Write([]byte(`{"verdict":"LIMPIO","version":4}`))
			}))
			defer srv.Close()
			cmd := newWorkCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			args := []string{
				"apply", "item.submit", "--work-item-id", testWorkItemID, "--version", "3",
				"--server", srv.URL, "--tenant", "tenant-a", "--json",
			}
			cmd.SetArgs(append(args, tc.flags...))
			err := cmd.Execute()
			if tc.wantError {
				if err == nil || requests != 0 {
					t.Fatalf("invalid token reached HTTP: error=%v requests=%d", err, requests)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if requests != 1 || authorization != "Bearer "+tc.want {
					t.Fatal("work request did not use the expected credential")
				}
			}
			for _, token := range []string{tc.session, tc.operator, tc.context, "explicit-token", "separate-communication"} {
				if token != "" && (strings.Contains(output.String(), token) || err != nil && strings.Contains(err.Error(), token)) {
					t.Fatal("CLI output exposed a credential")
				}
			}
		})
	}
}

func TestWorkSessionCLIDenialDoesNotRetryWithOperatorToken(t *testing.T) {
	t.Setenv("OLIVARES_WORK_TOKEN", "scoped-work")
	t.Setenv("OLIVARES_TOKEN", "operator-env")
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "absent.yaml"))
	var authorizations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"verdict":"ROTO","code":"forbidden"}`))
	}))
	defer srv.Close()
	cmd := newWorkCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"get", "item", testWorkItemID, "--server", srv.URL, "--tenant", "tenant-a"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("server denial was hidden")
	}
	if len(authorizations) != 1 || authorizations[0] != "Bearer scoped-work" {
		t.Fatal("work read denial used or retried with operator authority")
	}
}

func TestWorkSessionCLITokenDoesNotAuthorizeAgentClient(t *testing.T) {
	t.Setenv("OLIVARES_WORK_TOKEN", "scoped-work")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "absent.yaml"))
	cfg := agentClientConfig{server: "http://127.0.0.1:1", tenant: "tenant-a"}
	if err := cfg.resolve(); err == nil {
		t.Fatal("agent client accepted the work-only token")
	}
}
