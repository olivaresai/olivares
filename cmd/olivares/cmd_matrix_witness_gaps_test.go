// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// These witnesses deliberately spell each command as real root argv. The value-matrix
// probe can therefore point to the invocation, while each test also proves the behavior
// behind the token and its no-fire direction.

func TestAgentSessionInputArgvSendsOnlyWithCredentials(t *testing.T) {
	prepareBootstrapCLITest(t)
	var calls atomic.Int64
	var method, path, body, authorization, tenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		method = r.Method
		path = r.URL.EscapedPath()
		authorization = r.Header.Get("Authorization")
		tenant = r.Header.Get("X-Olivares-Tenant")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	// No-fire: resolution refuses a missing credential before opening a connection.
	if _, _, err := execRoot(t, "agent", "session", "input", "run-123", "--line", `{"type":"user","message":"continue"}`,
		"--server", srv.URL, "--tenant", "tenant-a"); err == nil {
		t.Fatal("input without a credential must fail")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("input without a credential made %d request(s), want 0", got)
	}

	if _, _, err := execRoot(t, "agent", "session", "input", "run-123", "--line", `{"type":"user","message":"continue"}`,
		"--server", srv.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("input with a credential: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("credentialed input made %d request(s), want 1", got)
	}
	if method != http.MethodPost || path != "/v1/m/sessions/runs/run-123/input" {
		t.Fatalf("input request = %s %s, want POST /v1/m/sessions/runs/run-123/input", method, path)
	}
	if authorization != "Bearer test-token" || tenant != "tenant-a" {
		t.Fatalf("input credentials = Authorization %q, tenant %q", authorization, tenant)
	}
	if !strings.Contains(body, `"line":"{\"type\":\"user\",\"message\":\"continue\"}"`) {
		t.Fatalf("input request lost the NDJSON line: %s", body)
	}
}

func TestHooksConformArgvReachesEditionBoundaryOnlyForValidArgs(t *testing.T) {
	_, _, err := execRoot(t, "hooks", "conform")
	if err == nil || !strings.Contains(err.Error(), "hooks-hardening add-on not available") {
		t.Fatalf("hooks conform error = %v, want the honest edition boundary", err)
	}

	// No-fire: Cobra must reject an extra positional argument before resolving the
	// enterprise seam. Otherwise malformed argv is reported as an edition problem.
	_, _, badErr := execRoot(t, "hooks", "conform", "unexpected")
	if badErr == nil {
		t.Fatal("hooks conform with an extra argument must fail")
	}
	if strings.Contains(badErr.Error(), "hooks-hardening add-on not available") {
		t.Fatalf("invalid conform argv reached the enterprise seam: %v", badErr)
	}
}

func TestAuditKeyTransitionArgvReachesSignerGuardOnlyForValidArgs(t *testing.T) {
	t.Setenv("OLIVARES_LEDGER_SIGNER", "")
	dir := initialisedDataDir(t)
	_, _, err := execRoot(t, "audit", "key-transition", "--data-dir", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "requires an off-box checkpoint signer") {
		t.Fatalf("audit key-transition error = %v, want the off-box signer guard", err)
	}

	// No-fire: an argument error must be rejected before auditBoot can initialize
	// or mutate the operator-selected data directory.
	untouched := filepath.Join(t.TempDir(), "must-stay-absent")
	_, _, badErr := execRoot(t, "audit", "key-transition", "unexpected", "--data-dir", untouched, "--yes")
	if badErr == nil {
		t.Fatal("audit key-transition with an extra argument must fail")
	}
	if strings.Contains(badErr.Error(), "requires an off-box checkpoint signer") {
		t.Fatalf("invalid key-transition argv reached the signer guard: %v", badErr)
	}
	if _, statErr := os.Stat(untouched); !os.IsNotExist(statErr) {
		t.Fatalf("invalid key-transition argv touched %s: %v", untouched, statErr)
	}
}

func TestUsersSuperadminsArgvReadsRosterOnlyWithCredentials(t *testing.T) {
	prepareBootstrapCLITest(t)
	var calls atomic.Int64
	var method, path, authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		method = r.Method
		path = r.URL.EscapedPath()
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"u-1","email":"ops@example.test","status":"active","is_superadmin":true,"created_at":"2026-08-22T00:00:00Z"}]}`)
	}))
	t.Cleanup(srv.Close)

	// No-fire: the client refuses an unauthenticated roster read locally.
	if _, _, err := execRoot(t, "users", "superadmins", "--server", srv.URL); err == nil {
		t.Fatal("users superadmins without a credential must fail")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("users superadmins without a credential made %d request(s), want 0", got)
	}

	out, _, err := execRoot(t, "users", "superadmins", "--server", srv.URL, "--token", "test-token")
	if err != nil {
		t.Fatalf("users superadmins with a credential: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("credentialed users superadmins made %d request(s), want 1", got)
	}
	if method != http.MethodGet || path != "/v1/users/superadmins" {
		t.Fatalf("users superadmins request = %s %s, want GET /v1/users/superadmins", method, path)
	}
	if authorization != "Bearer test-token" {
		t.Fatalf("users superadmins Authorization = %q, want bearer token", authorization)
	}
	if !strings.Contains(out, "ops@example.test") || !strings.Contains(out, "active") {
		t.Fatalf("users superadmins output lost roster facts: %q", out)
	}
}
