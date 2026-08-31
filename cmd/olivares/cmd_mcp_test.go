// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func prepareMCPCLITest(t *testing.T) {
	t.Helper()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
}

func TestMCPPinsListTextAndRawJSON(t *testing.T) {
	prepareMCPCLITest(t)
	const (
		fingerprint = "abcdefghijklmnop-rest-of-fingerprint"
		drift       = "qrstuvwxyzABCDEF-rest-of-drift"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != mcpToolPinsPath {
			t.Errorf("request = %s %s, want GET %s", r.Method, r.URL.Path, mcpToolPinsPath)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
			t.Errorf("X-Olivares-Tenant = %q, want tenant-a", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"tool": "github.search", "fingerprint": fingerprint,
					"pinned_at": "2026-07-20T09:00:00Z", "updated_at": "2026-07-20T10:00:00Z",
					"pin_count": 2, "drift_fingerprint": drift, "drift_at": "2026-07-20T11:00:00Z",
				},
				{
					"tool": "slack.post", "fingerprint": "short-fp",
					"pinned_at": "2026-07-19T08:00:00Z", "updated_at": "2026-07-19T08:00:00Z",
					"pin_count": 1,
				},
			},
			"request_id": "req-preserved",
		})
	}))
	defer srv.Close()

	base := []string{"mcp", "pins", "ls", "--server", srv.URL, "--token", "secret-token", "--tenant", "tenant-a"}
	out, stderr, err := execRoot(t, base...)
	if err != nil {
		t.Fatalf("mcp pins ls: %v (stderr %q)", err, stderr)
	}
	for _, want := range []string{
		"TOOL", "FINGERPRINT", "PINNED", "COUNT", "DRIFT",
		"github.search", "abcdefghijklmnop…", "2026-07-20T09:00:00Z", "2", "qrstuvwxyzABCDEF…",
		"slack.post", "short-fp", "2026-07-19T08:00:00Z", "1", "-",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, fingerprint) || strings.Contains(out, drift) {
		t.Fatalf("text output contains an untruncated fingerprint:\n%s", out)
	}

	jsonArgs := append(append([]string(nil), base...), "-o", "json")
	jsonOut, stderr, err := execRoot(t, jsonArgs...)
	if err != nil {
		t.Fatalf("mcp pins ls -o json: %v (stderr %q)", err, stderr)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &raw); err != nil {
		t.Fatalf("JSON output is invalid: %v\n%s", err, jsonOut)
	}
	if got := raw["request_id"]; got != "req-preserved" {
		t.Fatalf("raw API field request_id = %#v, want preserved", got)
	}
	items, ok := raw["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("raw API items = %#v, want two entries", raw["items"])
	}
	first, _ := items[0].(map[string]any)
	if got := first["fingerprint"]; got != fingerprint {
		t.Fatalf("JSON fingerprint = %#v, want full raw value", got)
	}
}

func TestMCPPinsApproveExplicitAndFromDrift(t *testing.T) {
	prepareMCPCLITest(t)
	seen := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != mcpToolPinsPath+"/approve" {
			t.Errorf("request = %s %s, want POST approve", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tool, _ := body["tool"].(string)
		seen[tool] = body
		fingerprint, _ := body["fingerprint"].(string)
		if body["from_drift"] == true {
			fingerprint = "drift-fingerprint"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tool": tool, "fingerprint": fingerprint})
	}))
	defer srv.Close()

	connection := []string{"--server", srv.URL, "--token", "token", "--tenant", "tenant-a"}
	explicitArgs := append([]string{"mcp", "pins", "approve", "explicit.tool", "--fingerprint", "explicit-fingerprint"}, connection...)
	out, stderr, err := execRoot(t, explicitArgs...)
	if err != nil {
		t.Fatalf("explicit approve: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(out, "approved explicit.tool at explicit-fingerp…") {
		t.Fatalf("explicit approve output = %q", out)
	}
	if got := seen["explicit.tool"]["fingerprint"]; got != "explicit-fingerprint" {
		t.Fatalf("explicit request fingerprint = %#v", got)
	}
	if _, present := seen["explicit.tool"]["from_drift"]; present {
		t.Fatalf("explicit request unexpectedly contains from_drift: %#v", seen["explicit.tool"])
	}

	driftArgs := append([]string{"mcp", "pins", "approve", "drift.tool", "--from-drift"}, connection...)
	out, stderr, err = execRoot(t, driftArgs...)
	if err != nil {
		t.Fatalf("drift approve: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(out, "approved drift.tool at drift-fingerprin…") {
		t.Fatalf("drift approve output = %q", out)
	}
	if got := seen["drift.tool"]["from_drift"]; got != true {
		t.Fatalf("drift request from_drift = %#v, want true", got)
	}
	if _, present := seen["drift.tool"]["fingerprint"]; present {
		t.Fatalf("drift request unexpectedly contains fingerprint: %#v", seen["drift.tool"])
	}
}

func TestMCPPinsRemove(t *testing.T) {
	prepareMCPCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != mcpToolPinsPath+"/unpin" {
			t.Errorf("request = %s %s, want POST unpin", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body mcpToolPinActionInput
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Tool != "github.search" {
			t.Errorf("tool = %q, want github.search", body.Tool)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tool": body.Tool})
	}))
	defer srv.Close()

	out, stderr, err := execRoot(t, "mcp", "pins", "rm", "github.search",
		"--server", srv.URL, "--token", "token", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("mcp pins rm: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(out, "unpinned github.search") {
		t.Fatalf("remove output = %q", out)
	}
}

func TestMCPPinsHTTPExitCodes(t *testing.T) {
	prepareMCPCLITest(t)
	tests := []struct {
		name       string
		status     int
		args       []string
		wantCode   int
		wantDetail string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, args: []string{"mcp", "pins", "ls"}, wantCode: exitcode.Auth},
		{name: "forbidden", status: http.StatusForbidden, args: []string{"mcp", "pins", "approve", "search", "--fingerprint", "fp"}, wantCode: exitcode.Auth},
		{name: "not found", status: http.StatusNotFound, args: []string{"mcp", "pins", "rm", "missing"}, wantCode: exitcode.NotFound},
		{name: "drift conflict", status: http.StatusConflict, args: []string{"mcp", "pins", "approve", "stable", "--from-drift"}, wantCode: exitcode.Conflict, wantDetail: "no current drift"},
		{name: "server", status: http.StatusInternalServerError, args: []string{"mcp", "pins", "ls"}, wantCode: exitcode.Server},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"test failure"}`))
			}))
			defer srv.Close()
			args := append(append([]string(nil), tc.args...), "--server", srv.URL, "--token", "token", "--tenant", "tenant-a")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("command succeeded, want HTTP error")
			}
			if got := exitcode.From(err); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d: %v", got, tc.wantCode, err)
			}
			if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("error = %q, want detail %q", err, tc.wantDetail)
			}
		})
	}
}

func TestMCPPinsEnterprisePendingIsClearGenericError(t *testing.T) {
	prepareMCPCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"no verifier wired"}`))
	}))
	defer srv.Close()

	_, _, err := execRoot(t, "mcp", "pins", "ls", "--server", srv.URL, "--token", "token", "--tenant", "tenant-a")
	if err == nil {
		t.Fatal("community 501 must fail")
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Fatalf("exit code = %d, want generic %d: %v", got, exitcode.Err, err)
	}
	if !strings.Contains(err.Error(), "enterprise add-on") {
		t.Fatalf("501 error is not actionable: %q", err)
	}
}

// The approve flags are mutually exclusive and mandatory-one: both invalid
// combinations must classify as usage errors (review finding — the
// double-negation guard had no test).
func TestMCPPinsApproveFlagValidation(t *testing.T) {
	for name, args := range map[string][]string{
		"neither": {"mcp", "pins", "approve", "tool-a", "--server", "https://x", "--token", "t", "--tenant", "tn"},
		"both":    {"mcp", "pins", "approve", "tool-a", "--fingerprint", "fp", "--from-drift", "--server", "https://x", "--token", "t", "--tenant", "tn"},
	} {
		_, _, err := execRoot(t, args...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%s: want usage exit, got %v", name, err)
		}
	}
}

// httpErr is SHARED: the reclassification (401/403→3, 404→4, 409→5, 5xx→6)
// now applies to every command family that calls it. Pin one representative
// family beyond mcp (agent session get) so a regression there cannot land
// silently (review finding).
func TestAgentSessionGetInheritsTypedExitCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"no such run"}}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, _, err := execRoot(t, "agent", "session", "get", "run-x",
		"--server", srv.URL, "--token", "tok", "--tenant", "tn")
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("agent 404: want not-found exit, got %v", err)
	}
}
