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
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `provider account edit <name>` finds the account the way `tool login --account`
// does (findToolAccount: exact name, once) and renames it by its reference.
func TestProviderAccountEditByNameRenamesTheMatchingAccount(t *testing.T) {
	var mu sync.Mutex
	var patched []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerAccountsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"account_ref": "ppf_a", "name": "claude", "driver": "claude"},
				{"account_ref": "ppf_b", "name": "claude-b", "driver": "claude"},
			}})
		case r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			patched = append(patched, r.URL.Path+" "+string(raw))
			mu.Unlock()
			_, _ = io.WriteString(w, strings.ReplaceAll(providerAccountJSON, "ppf_fixture", "ppf_b"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	_, errb, err := execSessionCLI(t, nil, append([]string{"provider", "account", "edit", "claude-b", "--name", "work"}, sessionCreds(server.URL)...)...)
	if err != nil {
		t.Fatalf("edit by name: %v %s", err, errb)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(patched) != 1 || patched[0] != providerAccountsPath+`/ppf_b {"name":"work"}` {
		t.Fatalf("patched = %q, want one rename of ppf_b", patched)
	}

	_, _, err = execSessionCLI(t, nil, append([]string{"provider", "account", "edit", "nobody", "--name", "work"}, sessionCreds(server.URL)...)...)
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("unknown account name = %v, want not found", err)
	}
}
