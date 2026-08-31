// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	testProtocolSpecID    = "01989d7d-4a66-7b8d-95de-56369fea4c42"
	testProtocolBindingID = "01989d7d-4d49-76cb-a219-8b05868cbabb"
	testProtocolWorkspace = "01989d7d-5035-7f3f-902f-da7b5bd8daef"
	testProtocolKey       = "01989d7d-54e1-76ad-8ee4-f2fd4d3d3d84"
)

func TestProtocolBindingCLIPlansAndAppliesDraftSpec(t *testing.T) {
	planHash := strings.Repeat("a", 64)
	specPath := filepath.Join(t.TempDir(), "binding.yaml")
	if err := os.WriteFile(specPath, []byte(
		"workspace_id: "+testProtocolWorkspace+"\n"+
			"binding_key: support-task\n"+
			"generation: 1\n"+
			"protocol: a2a\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != workAPIBase+"/protocol-binding-specs" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["workspace_id"] != testProtocolWorkspace || body["binding_key"] != "support-task" {
			t.Errorf("body = %#v", body)
		}
		switch call {
		case 1:
			if r.URL.Query().Get("mode") != "plan" || r.Header.Get("Idempotency-Key") != "" ||
				r.Header.Get("If-Plan-Hash") != "" {
				t.Errorf("plan envelope = mode=%q key=%q hash=%q", r.URL.Query().Get("mode"),
					r.Header.Get("Idempotency-Key"), r.Header.Get("If-Plan-Hash"))
			}
			_, _ = w.Write([]byte(`{"verdict":"CLEAN","code":"draft_planned","plan_hash":"` + planHash + `"}`))
		case 2:
			if r.URL.Query().Get("mode") != "apply" || r.Header.Get("Idempotency-Key") != testProtocolKey ||
				r.Header.Get("If-Plan-Hash") != planHash {
				t.Errorf("apply envelope = mode=%q key=%q hash=%q", r.URL.Query().Get("mode"),
					r.Header.Get("Idempotency-Key"), r.Header.Get("If-Plan-Hash"))
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"verdict":"CLEAN","code":"draft_created","spec":{"id":"` + testProtocolSpecID + `"}}`))
		default:
			t.Errorf("unexpected call %d", call)
		}
	}))
	defer srv.Close()

	for _, args := range [][]string{
		{"protocol-binding", "spec", "create", "--mode", "plan", "-f", specPath},
		{"protocol-binding", "spec", "create", "--mode", "apply", "-f", specPath,
			"--plan-hash", planHash, "--idempotency-key", testProtocolKey},
	} {
		cmd := newWorkCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(args, "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a", "--json"))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestProtocolBindingCLIUsesStateAndReconcilePreconditions(t *testing.T) {
	planHash := strings.Repeat("b", 64)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-Match") != `"v7"` || r.Header.Get("If-Plan-Hash") != planHash ||
			r.Header.Get("Idempotency-Key") != testProtocolKey || r.URL.Query().Get("mode") != "apply" {
			t.Errorf("headers = %#v mode=%q", r.Header, r.URL.Query().Get("mode"))
		}
		wantPath := workAPIBase + "/protocol-binding-specs/" + testProtocolSpecID + "/activate"
		if calls.Load() == 2 {
			wantPath = workAPIBase + "/protocol-bindings/" + testProtocolBindingID + "/reconcile"
		}
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
		}
		_, _ = w.Write([]byte(`{"verdict":"CLEAN","code":"ok"}`))
	}))
	defer srv.Close()

	base := []string{"--mode", "apply", "--version", "7", "--plan-hash", planHash,
		"--idempotency-key", testProtocolKey, "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a"}
	for _, prefix := range [][]string{
		{"protocol-binding", "spec", "activate", testProtocolSpecID},
		{"protocol-binding", "binding", "reconcile", testProtocolBindingID},
	} {
		cmd := newWorkCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(prefix, base...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", prefix, err)
		}
	}
}

func TestProtocolBindingCLIListsTypedFilters(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		_, _ = w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	defer srv.Close()

	commands := [][]string{
		{"protocol-binding", "spec", "list", "--workspace-id", testProtocolWorkspace,
			"--binding-key", "support-task", "--generation", "2", "--protocol", "a2a",
			"--direction", "outbound", "--state", "active", "--limit", "25", "--cursor", "cur"},
		{"protocol-binding", "binding", "list", "--workspace-id", testProtocolWorkspace,
			"--binding-spec-id", testProtocolSpecID, "--work-item-id", testWorkItemID,
			"--protocol", "mcp", "--owner-kind", "agent", "--owner-ref", "agent:ops",
			"--external-kind", "task", "--external-id", "task-7", "--verdict", "CLEAN",
			"--terminal", "false"},
	}
	for _, args := range commands {
		cmd := newWorkCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(args, "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a"))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "binding_key=support-task") ||
		!strings.Contains(paths[0], "generation=2") || !strings.Contains(paths[1], "binding_spec_id="+testProtocolSpecID) ||
		!strings.Contains(paths[1], "terminal=false") {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestProtocolBindingCLIRejectsIncompleteApplyBeforeHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, args := range [][]string{
		{"protocol-binding", "spec", "activate", testProtocolSpecID, "--mode", "apply", "--version", "3"},
		{"protocol-binding", "binding", "reconcile", testProtocolBindingID, "--mode", "apply",
			"--plan-hash", strings.Repeat("c", 64)},
		{"protocol-binding", "spec", "create", "--mode", "apply", "-f", "missing.yaml",
			"--plan-hash", "not-a-hash"},
	} {
		cmd := newWorkCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(args, "--server", srv.URL, "--token", "tok", "--tenant", "tenant-a"))
		err := cmd.Execute()
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v error = %v code=%d", args, err, exitcode.From(err))
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid commands fired %d HTTP requests", hits.Load())
	}
}
