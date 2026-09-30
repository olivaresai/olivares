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

// The TOTP CLI verbs drive the engine routes with the caller's credential and
// tenant, surface the non-secret factor view, and never print key material
// (the wire shape has none to print).

func TestUsersTOTPShowsFactorStatus(t *testing.T) {
	const token = "olvk_totp-status"
	var gotPath, gotMethod, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotTenant = r.URL.Path, r.Method, r.Header.Get("X-Olivares-Tenant")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enrolled": true, "algorithm": "SHA1", "digits": 6, "period": 30,
			"seed_hint": "ab12cd34ef56", "activated_at": "2026-09-30T00:00:00Z",
			"recovery_codes_remaining": 7,
		})
	}))
	t.Cleanup(srv.Close)

	out, stderr, err := execRoot(t, "users", "totp", "018f2c2e-0000-7000-8000-000000000002",
		"--server", srv.URL, "--token", token, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("users totp: %v\n%s", err, stderr)
	}
	if gotPath != "/v1/users/018f2c2e-0000-7000-8000-000000000002/totp" || gotMethod != http.MethodGet || gotTenant != "tenant-a" {
		t.Fatalf("request = %s %s tenant=%s", gotMethod, gotPath, gotTenant)
	}
	for _, want := range []string{"enrolled ab12cd34ef56", "recovery codes remaining: 7", "algorithm=SHA1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

func TestUsersTOTPResetRequiresYesAndPosts(t *testing.T) {
	const token = "olvk_totp-reset"
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(srv.Close)

	id := "018f2c2e-0000-7000-8000-000000000002"
	_, stderr, err := execRoot(t, "users", "totp-reset", id, "--server", srv.URL, "--token", token)
	if err == nil || !strings.Contains(err.Error(), "reset the TOTP factor") {
		t.Fatalf("unconfirmed reset err = %v, stderr = %q", err, stderr)
	}
	if gotPath != "" {
		t.Fatalf("unconfirmed reset reached the engine at %s", gotPath)
	}
	out, stderr, err := execRoot(t, "users", "totp-reset", id, "--yes",
		"--server", srv.URL, "--token", token, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("reset: %v\n%s", err, stderr)
	}
	if gotPath != "/v1/users/"+id+"/totp/reset" || gotMethod != http.MethodPost {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(out, "enroles again at next login") {
		t.Fatalf("output = %q", out)
	}
}

func TestAuthTOTPPolicyGetAndSet(t *testing.T) {
	const token = "olvk_totp-policy"
	var gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"require_for_admins": true})
	}))
	t.Cleanup(srv.Close)

	out, stderr, err := execRoot(t, "auth", "totp-policy", "get", "--server", srv.URL, "--token", token)
	if err != nil {
		t.Fatalf("policy get: %v\n%s", err, stderr)
	}
	if gotMethod != http.MethodGet || !strings.Contains(out, "require TOTP for administrators: on") {
		t.Fatalf("get = %s %q", gotMethod, out)
	}
	out, stderr, err = execRoot(t, "auth", "totp-policy", "set", "--require-for-admins",
		"--server", srv.URL, "--token", token)
	if err != nil {
		t.Fatalf("policy set: %v\n%s", err, stderr)
	}
	if gotMethod != http.MethodPut || gotBody["require_for_admins"] != true {
		t.Fatalf("set = %s body=%v", gotMethod, gotBody)
	}
	if !strings.Contains(out, ": on") {
		t.Fatalf("set output = %q", out)
	}
}
