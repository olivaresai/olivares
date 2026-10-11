// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentSessionAttachReadsTheOneAttachStream: attach prints output lines until
// the end event and its notices on stderr, and a refused attach never repeats the
// bearer token the engine echoed back.
func TestAgentSessionAttachReadsTheOneAttachStream(t *testing.T) {
	const token = "attach-secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case sessionRunsPath + "/r1/attach":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: output\ndata: {\"line\":\"hello\"}\n\nevent: notice\ndata: draining\n\n" +
				"event: end\ndata: {}\n\nevent: output\ndata: {\"line\":\"after the end\"}\n\n"))
		default:
			http.Error(w, `{"error":"token `+token+` is not allowed here"}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "client.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", srv.URL)
	t.Setenv("OLIVARES_TOKEN", token)
	t.Setenv("OLIVARES_TENANT", "t1")

	cmd := newAgentSessionAttachCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"r1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("attach = %v", err)
	}
	if out.String() != "hello\n" || !strings.Contains(errOut.String(), "[attach] notice: draining") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}

	cfg := agentClientConfig{}
	if err := cfg.resolve(); err != nil {
		t.Fatal(err)
	}
	err := cfg.readAttach(context.Background(), "r2", 0, func(string, string) bool { return true })
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("refused attach = %v", err)
	}
}
