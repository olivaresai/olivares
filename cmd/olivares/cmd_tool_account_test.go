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

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestToolAccountSelectionReachesLoginAndStatus(t *testing.T) {
	for _, verb := range []string{"login", "status"} {
		t.Run(verb, func(t *testing.T) {
			var starts, statusReads, pages int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reply := func(code int, v any) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					_ = json.NewEncoder(w).Encode(v)
				}
				switch r.URL.Path {
				case providerAccountsPath:
					pages++
					if r.URL.Query().Get("cursor") == "" {
						reply(200, map[string]any{"items": []any{}, "has_more": true, "cursor": "next"})
						return
					}
					reply(200, map[string]any{"items": []map[string]any{{"account_ref": "ppf_second", "name": "claude-b", "driver": "claude", "state": "active"}}, "has_more": false})
				case agentToolsPath + "/sign-in":
					if r.Method == "GET" {
						statusReads++
						if r.URL.Query().Get("account_ref") != "ppf_second" || r.URL.Query().Get("tenant_id") != "tenant-a" {
							t.Errorf("status selected another home: %s", r.URL.RawQuery)
						}
						reply(200, map[string]any{"driver": "claude", "installed": true, "signed_in": false})
						return
					}
					starts++
					var in map[string]any
					_ = json.NewDecoder(r.Body).Decode(&in)
					if in["account_ref"] != "ppf_second" || in["tenant_id"] != "tenant-a" || in["driver"] != "claude" {
						t.Errorf("login selected another home: %v", in)
					}
					reply(202, map[string]any{"id": "login-b", "state": "signed_in", "url": "https://claude.com/fixture"})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					reply(404, map[string]any{})
				}
			}))
			defer server.Close()
			args := []string{"tool", verb}
			if verb == "login" {
				args = append(args, "claude")
			}
			args = append(args, "--account", "claude-b")
			args = append(args, sessionCreds(server.URL)...)
			out, errout, err := execSessionCLI(t, strings.NewReader(""), args...)
			if err != nil {
				t.Fatalf("%s: %v %s", verb, err, errout)
			}
			if statusReads != 1 || pages != 2 || (verb == "login" && starts != 1) || (verb == "status" && starts != 0) {
				t.Fatalf("wrong requests: pages=%d status=%d starts=%d", pages, statusReads, starts)
			}
			if verb == "status" && (!strings.Contains(out, "claude-b") || !strings.Contains(out, "not signed in")) {
				t.Fatalf("status = %q", out)
			}
		})
	}
}

func TestToolAccountSelectionRefusesAmbiguousNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerAccountsPath {
			t.Errorf("ambiguous name reached %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"account_ref": "ppf_a", "name": "claude-b", "driver": "claude"}, {"account_ref": "ppf_b", "name": "claude-b", "driver": "claude"}}})
	}))
	defer server.Close()
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "login", "claude", "--account", "claude-b"}, sessionCreds(server.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("ambiguous account = %v, want conflict", err)
	}
}

func TestToolAccountSelectionNeverFallsBackToDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile map[string]any
	}{
		{"missing reference", map[string]any{"driver": "claude"}},
		{"another reference", map[string]any{"profile_ref": "ppf_another", "driver": "claude"}},
		{"missing driver", map[string]any{"profile_ref": "ppf_selected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/m/sessions/provider-profiles/ppf_selected" {
					t.Errorf("invalid account reached login: %s", r.URL.String())
				}
				_ = json.NewEncoder(w).Encode(tc.profile)
			}))
			defer server.Close()
			_, _, err := execSessionCLI(t, nil, append([]string{"tool", "status", "--account", "ppf_selected"}, sessionCreds(server.URL)...)...)
			if exitcode.From(err) != exitcode.Server {
				t.Fatalf("invalid account = %v, want server refusal", err)
			}
		})
	}
}

func TestToolAccountStatusOffersAValidInstallCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == providerAccountsPath {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"account_ref": "ppf_a", "name": "claude-b", "driver": "claude"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"driver": "claude", "installed": false, "signed_in": false})
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "status", "--account", "claude-b"}, sessionCreds(server.URL)...)...)
	if err != nil || !strings.Contains(out, "olivares tool install claude") || strings.Contains(out, "install claude --account") {
		t.Fatalf("invalid install guidance: %q (%v)", out, err)
	}
}

// The tools `tool login` accepts are the ones whose facts declare a sign-in. Its
// usage line and its refusal are built from them, so a tool that gains a sign-in
// needs no edit here; the sentences themselves still name the four of today.
func TestToolLoginNamesTheToolsThatCanSignIn(t *testing.T) {
	if want := "login <claude|codex|grok|opencode|gemini>"; newToolLoginCmd().Use != want {
		t.Errorf("Use = %q, want %q", newToolLoginCmd().Use, want)
	}
	_, _, err := execSessionCLI(t, nil, "tool", "login", "ollama", "--server", "http://127.0.0.1:1", "--token", "t", "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "Use claude, codex, grok, opencode or gemini.") {
		t.Errorf("login of a tool without sign-in = %v, want a usage refusal that lists the tools that can", err)
	}
}

// `tool login --method <id>` sends the method id with the start. An id the
// engine does not know is refused with its list and exits with the usage code.
func TestToolLoginMethodReachesTheEngine(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == agentToolsPath+"/sign-in" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"driver": "opencode", "installed": true, "signed_in": false})
		case r.URL.Path == agentToolsPath+"/sign-in" && r.Method == "POST":
			sent = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&sent)
			if sent["method"] != "chatgpt-headless" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"OpenCode has no sign-in method \"browser\". Use one of: chatgpt-headless."}}`))
				return
			}
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "login-m", "state": "signed_in"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	_, errb, err := execSessionCLI(t, nil, append([]string{"tool", "login", "opencode", "--method", "chatgpt-headless"}, sessionCreds(server.URL)...)...)
	if err != nil || sent["method"] != "chatgpt-headless" || sent["driver"] != "opencode" {
		t.Fatalf("login with a method: %v %s sent=%v", err, errb, sent)
	}

	sent = nil
	_, _, err = execSessionCLI(t, nil, append([]string{"tool", "login", "opencode", "--method", "browser"}, sessionCreds(server.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "chatgpt-headless") {
		t.Fatalf("unknown method = %v (exit %d), want a usage refusal that lists the valid ids", err, exitcode.From(err))
	}

	// Without --method the body carries none: the call is what it always was.
	sent = nil
	_, _, _ = execSessionCLI(t, nil, append([]string{"tool", "login", "opencode"}, sessionCreds(server.URL)...)...)
	if _, has := sent["method"]; has {
		t.Fatalf("a login without --method sent a method: %v", sent)
	}
}
