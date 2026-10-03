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

// HU2-24: `olivares provider add` required --kind and --name, which the console no longer
// asks for, and did not test the key it had just added, which the console does. The kind
// comes from the key (or openai_compatible with --base-url), the name from the kind, and
// the key is tested at once; explicit flags are kept and --no-test skips the test.
func TestProviderAddTestsTheKeyItAdded(t *testing.T) {
	for _, tc := range []struct {
		name, key, probe     string
		args                 []string
		kind, display, shown string
		calls                int
	}{
		{"an Anthropic key, nothing else", "sk-ant-api03-fixture-0001", "ok", nil, "anthropic", "Anthropic", "accepted this credential", 2},
		{"an OpenAI key the provider refuses", "sk-proj-fixture-0002", "refused", nil, "openai", "OpenAI", "REFUSED this credential", 2},
		{"an xAI key", "xai-fixture-0003", "ok", nil, "xai", "xAI", "accepted this credential", 2},
		{"an endpoint makes it compatible", "fixture-0004", "ok", []string{"--base-url", "https://llm.example.test/v1"}, "openai_compatible", "OpenAI-compatible", "accepted this credential", 2},
		{"explicit flags are kept", "sk-ant-api03-fixture-0005", "ok", []string{"--kind", "openai_compatible", "--name", "Mine", "--base-url", "https://llm.example.test/v1"}, "openai_compatible", "Mine", "accepted this credential", 2},
		{"--no-test", "sk-ant-api03-fixture-0006", "ok", []string{"--no-test"}, "anthropic", "Anthropic", "not tested yet", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var added map[string]any
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				w.Header().Set("Content-Type", "application/json")
				rec := map[string]any{"provider_ref": "prv_fixture", "kind": tc.kind, "display_name": tc.display, "state": "active", "key_hint": "…0001"}
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/m/sessions/providers":
					raw, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(raw, &added)
					w.WriteHeader(http.StatusCreated)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/m/sessions/providers/prv_fixture/test":
					rec["probe_state"] = tc.probe
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
				_ = json.NewEncoder(w).Encode(rec)
			}))
			defer srv.Close()
			args := append(append([]string{"provider", "add"}, tc.args...), "--server", srv.URL, "--token", "t", "--tenant", "tenant-a")
			out, errb, err := execRootStdin(t, tc.key+"\n", args...)
			if err != nil {
				t.Fatalf("add: %v %s", err, errb)
			}
			mu.Lock()
			defer mu.Unlock()
			if added["kind"] != tc.kind || added["display_name"] != tc.display || added["api_key"] != tc.key {
				t.Fatalf("added %v, want kind %s named %s with the key", added, tc.kind, tc.display)
			}
			if calls != tc.calls || !strings.Contains(out, tc.shown) || strings.Contains(out, tc.key) {
				t.Fatalf("calls=%d output:\n%s\nwant %d calls and %q, never the key", calls, out, tc.calls, tc.shown)
			}
		})
	}
	t.Run("a key that says nothing needs --kind", func(t *testing.T) {
		_, _, err := execRootStdin(t, "fixture-unknown\n", "provider", "add", "--server", "https://127.0.0.1:1", "--token", "t", "--tenant", "tenant-a")
		if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "pass --kind") {
			t.Fatalf("an unknown key = %v, want a usage error naming --kind", err)
		}
	})
}
