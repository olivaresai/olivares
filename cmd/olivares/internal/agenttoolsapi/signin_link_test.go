// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool that fails before its prompt must not offer the endpoint in its error
// as the sign-in page (#828). Exercise both streams through the real reader.
func TestSignInErrorURLIsNotTheSignInLink(t *testing.T) {
	const reason = "Error logging in with device code: error sending request for url (https://auth.openai.com/api/accounts/deviceauth/usercode)"
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			call, _ := newSignInServer(t, func(_ *Module, bin string) {
				stub := "#!/bin/sh\nprintf '%s' '" + reason + "'" + redirect + "\nexit 1\n"
				if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			code, start := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
			if code != 202 {
				t.Fatalf("start = %d %v, want 202", code, start)
			}
			failed := pollSignIn(t, call, start["id"].(string), "failed")
			for _, view := range []map[string]any{start, failed} {
				if link, ok := view["url"]; ok {
					t.Errorf("sign-in carries the error URL as its link: %v", link)
				}
			}
			want := "Codex stopped (exit status 1): Error logging in with device code: error sending request for url (https://auth.openai.com). Fix that, then start the sign-in again."
			if failed["message"] != want {
				t.Errorf("message = %q, want %q", failed["message"], want)
			}
		})
	}
}

func TestSignInPromptAfterAnErrorKeepsItsOwnLinkAndCode(t *testing.T) {
	for _, tc := range []struct {
		driver, stub, link, code, state string
	}{
		{"codex", stubCodex, "https://auth.openai.com/codex/device", "ABCD-12345", "waiting"},
		{"grok", stubGrok, "https://accounts.x.ai/device", "K7M2QX9P", "waiting"},
		{"claude", stubClaude, "https://claude.com/cai/oauth/authorize?code=true&state=stub", "", "needs_code"},
		{"opencode", stubOpenCode, "https://auth.openai.com/codex/device", "ABCD-12345", "waiting"},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			call, _ := newSignInServer(t, func(_ *Module, bin string) {
				stub := strings.Replace(tc.stub, "#!/bin/sh\n", "#!/bin/sh\nprintf '■ Error: request to https://vendor.test/api/login failed; retrying\\n' >&2\n", 1)
				if err := os.WriteFile(filepath.Join(bin, tc.driver), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			status, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": tc.driver})
			if status != 202 || flow["state"] != tc.state || flow["url"] != tc.link {
				t.Fatalf("start = %d %v, want %s with %s", status, flow, tc.state, tc.link)
			}
			if tc.code != "" && flow["user_code"] != tc.code {
				t.Errorf("code = %v, want %s", flow["user_code"], tc.code)
			}
		})
	}
}

// The login now gets the host's proxy (#547), whose URL can carry its
// credential. A tool that names that proxy before its link must not make it the
// sign-in link the console shows: the link is the first one without userinfo.
func TestTheSignInLinkIsNeverAProxyWithItsCredential(t *testing.T) {
	stub := "#!/bin/sh\n" +
		"[ \"$1 $2\" = 'login status' ] && { [ -f \"$CODEX_HOME/auth.json\" ] && echo 'Logged in using ChatGPT' && exit 0; exit 1; }\n" +
		"echo 'proxy https://user:hunter2@proxy.corp:443 answered 407, retrying'\n" +
		"printf '" + codexPrompt + "'\n" +
		"sleep 1; mkdir -p \"$CODEX_HOME\"; echo '{}' > \"$CODEX_HOME/auth.json\"\n"
	call, _ := newSignInServer(t, func(_ *Module, bin string) {
		if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "codex"})
	if code != 202 || s["url"] != "https://auth.openai.com/codex/device" || s["user_code"] != "ABCD-12345" {
		t.Fatalf("start = %d %v, want the device link and code", code, s)
	}
	done := pollSignIn(t, call, s["id"].(string), "signed_in")
	for _, state := range []map[string]any{s, done} {
		if raw, _ := json.Marshal(state); strings.Contains(string(raw), "hunter2") {
			t.Fatalf("the sign-in shows the proxy's credential: %s", raw)
		}
	}
}
