// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestAuthSessionsListAndEnd(t *testing.T) {
	const (
		currentID = "018f2c2e-0000-7000-8000-000000000001"
		otherID   = "018f2c2e-0000-7000-8000-000000000002"
	)
	var authorization, endedPath, endedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"id": currentID, "created_at": "2026-10-24T10:00:00Z", "last_used_at": "2026-10-24T11:00:00Z", "kind": "browser", "current": true, "expires_at": "2026-10-25T10:00:00Z"},
				{"id": otherID, "created_at": "2026-10-23T10:00:00Z", "last_used_at": nil, "kind": "token", "current": false, "expires_at": "2026-10-24T10:00:00Z"},
			}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/auth/sessions/"+otherID:
			endedPath, endedMethod = r.URL.Path, r.Method
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	out, stderr, err := execRoot(t, "auth", "sessions", "ls", "--server", srv.URL, "--token", "session-secret")
	if err != nil {
		t.Fatalf("auth sessions ls: %v\n%s", err, stderr)
	}
	if authorization != "Bearer session-secret" {
		t.Fatalf("authorization = %q", authorization)
	}
	for _, want := range []string{currentID, otherID, "browser", "token", "2026-10-24T11:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+stderr, "session-secret") {
		t.Fatal("session credential leaked in list output")
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, currentID) && !strings.Contains(line, "true"):
			t.Errorf("the current sign-in is not marked: %q", line)
		case strings.Contains(line, otherID) && strings.Contains(line, "true"):
			t.Errorf("another sign-in is marked current: %q", line)
		}
	}

	out, stderr, err = execRoot(t, "auth", "sessions", "end", otherID, "--yes", "--server", srv.URL, "--token", "session-secret", "-o", "json")
	if err != nil {
		t.Fatalf("auth sessions end: %v\n%s", err, stderr)
	}
	if endedPath != "/v1/auth/sessions/"+otherID || endedMethod != http.MethodDelete {
		t.Fatalf("end request = %s %s", endedMethod, endedPath)
	}
	var result struct {
		ID    string `json:"id"`
		Ended bool   `json:"ended"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.ID != otherID || !result.Ended {
		t.Fatalf("end JSON = %s, err=%v", out, err)
	}
}

func TestAuthSessionsEndRequiresConfirmationBeforeRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	_, _, err := execRoot(t, "auth", "sessions", "end", "018f2c2e-0000-7000-8000-000000000002", "--server", srv.URL, "--token", "session-secret")
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("unconfirmed end error = %v, want usage refusal", err)
	}
	if called {
		t.Fatal("unconfirmed end reached the engine")
	}
}

func TestAuthSessionsEndReportsTheEnginesRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"Use sign out to end the current session."}}`))
	}))
	t.Cleanup(srv.Close)
	out, stderr, err := execRoot(t, "auth", "sessions", "end", "018f2c2e-0000-7000-8000-000000000001", "--yes", "--server", srv.URL, "--token", "session-secret")
	if err == nil || !strings.Contains(err.Error()+stderr, "Use sign out to end the current session.") {
		t.Fatalf("refused end = %v\n%s\n%s, want the engine's words and a failure", err, out, stderr)
	}
	if strings.Contains(out, "Ended sign-in") {
		t.Fatalf("a refused end printed success:\n%s", out)
	}
}

func TestAuthSessionsPairCreatesPrivateFragmentLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authSessionsPath+"/pairing" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != "My phone" {
			t.Errorf("pair body = %v, %v", body, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "one-time-secret", "expires_at": "2026-10-28T00:00:00Z"})
	}))
	defer srv.Close()
	out, stderr, err := execRoot(t, "auth", "sessions", "pair", "My phone", "--server", srv.URL, "--token", "current-session", "-o", "json")
	if err != nil {
		t.Fatalf("pair: %v %s", err, stderr)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["link"] != srv.URL+"/pair#pair=one-time-secret" {
		t.Fatalf("link = %s, %v", out, err)
	}
	if strings.Contains(stderr, "one-time-secret") {
		t.Fatal("pairing secret leaked to stderr")
	}
}

func TestAuthSessionsAcceptRefusesForeignLinkWithoutRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, stderr, err := execRootStdin(t, "https://other.example/pair#pair=private-token", "auth", "sessions", "accept", "--server", srv.URL)
	if err == nil || called {
		t.Fatalf("foreign link refusal = %v, called=%v", err, called)
	}
	if strings.Contains(stderr, "private-token") {
		t.Fatal("pairing secret leaked in refusal")
	}
}

func TestAuthSessionsAcceptSavesPerDeviceContextWithoutPrintingCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	t.Setenv("OLIVARES_TOKEN", "stale-ambient-secret")
	t.Setenv("OLIVARES_TENANT", "stale-tenant")
	var accepted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case authSessionsPath + "/pairing/accept":
			if r.Header.Get("Authorization") != "" {
				t.Error("pair acceptance sent ambient credential")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["token"] != "one-time-secret" {
				t.Errorf("accept body = %v, %v", body, err)
			}
			accepted = true
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "device-session-secret", "session_id": "device-session"})
		case "/v1/auth/whoami":
			if !accepted || r.Header.Get("Authorization") != "Bearer device-session-secret" || r.Header.Get("X-Olivares-Tenant") != "" {
				t.Error("new identity was not independently validated")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "user", "grants": []map[string]string{{"tenant": "new-tenant", "role": "viewer"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	out, stderr, err := execRootStdin(t, srv.URL+"/pair#pair=one-time-secret\n", "auth", "sessions", "accept", "--server", srv.URL)
	if err != nil {
		t.Fatalf("accept: %v %s", err, stderr)
	}
	for _, secret := range []string{"one-time-secret", "device-session-secret", "stale-ambient-secret"} {
		if strings.Contains(out+stderr, secret) {
			t.Fatal("credential leaked in CLI output")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "device-session-secret") || !strings.Contains(string(data), "new-tenant") || strings.Contains(string(data), "stale-tenant") {
		t.Fatalf("saved context invalid: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("context permissions are not private: %v", err)
	}
}
