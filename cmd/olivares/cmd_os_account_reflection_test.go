// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthOSAccountCLIProtectsReflectionsWithoutFalseRefusal(t *testing.T) {
	for _, test := range []struct {
		name, password, account string
		status                  int
	}{
		{"metadata key", "uid", "alice", 200},
		{"public value", "alice", "alice", 200},
		{"reflected value", "native-proof", "echo-native-proof", 200},
		{"reflected failure", "native-proof", "", 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "proof")
			if err := os.WriteFile(file, []byte(test.password), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				if test.status != 200 {
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "unavailable", "message": test.password}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"tenant": "tenant-a", "user_id": "019c0000-0000-7000-8000-000000000002", "uid": 1234, "account": test.account, "digest": "public-digest"})
			}))
			defer server.Close()
			out, stderr, err := execRoot(t, "auth", "os-account", "complete", "019c0000-0000-7000-8000-000000000001", "--server", server.URL, "--insecure", "--token", "olvs_subject-fixture", "--password-file", file)
			if test.status == 200 && err != nil {
				t.Fatalf("accepted completion falsely refused: %v", err)
			}
			if test.status != 200 && err == nil {
				t.Fatal("failed completion succeeded")
			}
			if strings.Contains(out+stderr, test.password) {
				t.Fatal("proof reflected in rendered output")
			}
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				if strings.Contains(cause.Error(), test.password) {
					t.Fatal("retained unredacted proof cause")
				}
			}
		})
	}
}
