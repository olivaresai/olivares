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
)

func TestAuthOSAccountCLIProofUsesFileAndOwnCredential(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "native-password")
	if err := os.WriteFile(secretPath, []byte("native-proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/auth/os-account-bindings/complete" || r.Header.Get("Authorization") != "Bearer olvs_subject-fixture" {
			t.Error("wrong credential/route")
		}
		var body struct {
			Ceremony string `json:"ceremony_id"`
			Password []byte `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		defer clear(body.Password)
		if body.Ceremony != "019c0000-0000-7000-8000-000000000001" || string(body.Password) != "native-proof" {
			t.Error("wrong proof")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant": "tenant-a", "user_id": "019c0000-0000-7000-8000-000000000002", "uid": 1234, "account": "alice", "digest": "public-digest"})
	}))
	defer server.Close()
	args := []string{"auth", "os-account", "complete", "019c0000-0000-7000-8000-000000000001", "--server", server.URL, "--token", "olvs_subject-fixture", "--insecure", "--password-file", secretPath}
	out, stderr, err := execRoot(t, args...)
	if err != nil {
		t.Fatalf("complete: %v %s", err, stderr)
	}
	if calls != 1 || !strings.Contains(out, "alice") || strings.Contains(out+stderr, "native-proof") {
		t.Fatal("proof failed or secret printed")
	}
	args[len(args)-2] = "--password"
	_, _, err = execRoot(t, args...)
	if err == nil || calls != 1 {
		t.Fatal("password argv accepted")
	}
}
