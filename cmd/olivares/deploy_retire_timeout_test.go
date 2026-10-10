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
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/deploy"
)

func TestDeployRetireOutlivesOrdinaryDeadline(t *testing.T) {
	restore := defaultCLIRequestTimeout
	t.Cleanup(func() { defaultCLIRequestTimeout = restore })
	defaultCLIRequestTimeout = 100 * time.Millisecond

	for _, tc := range []struct {
		name     string
		args     []string
		wantExit int
	}{
		{"approved retirement", []string{"retire", "dep-1", "--yes", "--approval-ref", "approved"}, 0},
		{"explicit short deadline", []string{"retire", "dep-1", "--yes", "--approval-ref", "approved", "--timeout", "50ms"}, exitcode.Server},
		{"explicit long deadline", []string{"retire", "dep-1", "--yes", "--approval-ref", "approved", "--timeout", "2s"}, 0},
		{"explicit zero keeps transport default", []string{"retire", "dep-1", "--yes", "--approval-ref", "approved", "--timeout", "0"}, exitcode.Server},
		{"inherited explicit deadline", []string{"--timeout", "50ms", "retire", "dep-1", "--yes", "--approval-ref", "approved"}, exitcode.Server},
		{"approval request keeps ordinary deadline", []string{"retire", "dep-1", "--yes"}, exitcode.Server},
		{"apply keeps ordinary deadline", []string{"apply", "dep-1", "--approval-ref", "approved"}, exitcode.Server},
		{"read keeps ordinary deadline", []string{"definitions", "ls"}, exitcode.Server},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var removed atomic.Bool
			var stopped atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "target", "Names": []string{"/deadline-agent"}, "Image": "fixture:1", "State": "running"}})
				case r.Method == http.MethodPost && r.URL.Path == "/containers/target/stop":
					if r.URL.RawQuery != "" {
						t.Errorf("stop grace was overridden: %s", r.URL.RawQuery)
					}
					select {
					case <-r.Context().Done():
						return
					case <-time.After(300 * time.Millisecond):
					}
					stopped.Store(true)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodDelete && r.URL.Path == "/containers/target":
					if !stopped.Load() {
						t.Error("remove reached Docker before stop completed")
					}
					removed.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker call: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer target.Close()
			backend := executor.NewDockerBackend(executor.DockerConfig{RemoteBaseURL: target.URL, RemoteInsecure: true})
			adapter := &deployExecutor{e: executor.New(executor.WithBackend(backend, "docker"), executor.WithCredentialSource(alwaysMint()))}
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					http.Error(w, "bad body", http.StatusBadRequest)
					return
				}
				// All rows use the same slow backend, to prove only the approved retire
				// invocation changes its default HTTP deadline.
				result, err := adapter.Retire(r.Context(), deploy.ExecRequest{Tenant: model.TenantID("tenant-a"), Environment: "test", Target: "docker.host/test", Runtime: "docker", SubjectKind: "agent", SubjectRef: "deadline-agent"})
				if err != nil {
					http.Error(w, "retire failed", http.StatusBadGateway)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"op": "retire", "status": "retired", "detail": result.Detail})
			}))
			defer engine.Close()
			args := append([]string{"deploy"}, tc.args...)
			args = append(args, "-o", "json")
			out, _, err := execRoot(t, lot3Args(engine.URL, args...)...)
			got := 0
			if err != nil {
				got = exitcode.From(err)
			}
			if got != tc.wantExit {
				t.Fatalf("exit=%d, want %d: %v", got, tc.wantExit, err)
			}
			if tc.wantExit == 0 {
				var result map[string]any
				if err := json.Unmarshal([]byte(out), &result); err != nil || result["status"] != "retired" || !removed.Load() {
					t.Fatalf("retirement did not complete: removed=%v output=%s err=%v", removed.Load(), out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
				t.Fatalf("expected the CLI transport deadline, got %v", err)
			} else if removed.Load() || out != "" {
				t.Fatalf("deadline cancellation reported success or removed the target: removed=%v output=%s", removed.Load(), out)
			}
		})
	}
}
