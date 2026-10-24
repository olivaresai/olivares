// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// The sign-in start takes an optional method id. A listed id starts that method;
// an unknown one is refused with the valid ids and starts nothing; a start
// without one is the call it always was.
func TestSignInStartTakesAnOptionalMethod(t *testing.T) {
	call, _ := newSignInServer(t)

	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "chatgpt-headless"})
	if code != 202 || flow["user_code"] != "ABCD-12345" {
		t.Fatalf("listed method = %d %v, want the device flow", code, flow)
	}
	pollSignIn(t, call, flow["id"].(string), "signed_in")

	code, refused := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "browser"})
	msg, _ := refused["error"].(map[string]any)["message"].(string)
	if code != 400 || !strings.Contains(msg, `"browser"`) || !strings.Contains(msg, "chatgpt-headless") {
		t.Fatalf("unknown method = %d %v, want 400 naming the valid ids", code, refused)
	}

	code, refused = call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "claude", "method": "console"})
	msg, _ = refused["error"].(map[string]any)["message"].(string)
	if code != 400 || !strings.Contains(msg, "one sign-in method") {
		t.Fatalf("method for a tool with one = %d %v, want 400", code, refused)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["pending"] != nil {
		t.Fatalf("a refused method started a login: %d %v", code, st)
	}
}

// The status read lists the methods a tool offers, so a client can show them
// before it starts one. A tool with a single method lists none.
func TestSignInStatusListsTheMethods(t *testing.T) {
	call, _ := newSignInServer(t)
	code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode", nil)
	methods, _ := st["methods"].([]any)
	if code != 200 || len(methods) != 3 {
		t.Fatalf("opencode status = %d %v, want three methods", code, st)
	}
	first, _ := methods[0].(map[string]any)
	if first["id"] != "opencode" || first["key"] != true || first["default"] != nil {
		t.Fatalf("first method = %v, want OpenCode's own key method", first)
	}
	m, _ := methods[2].(map[string]any)
	if m["id"] != "chatgpt-headless" || m["label"] != "ChatGPT Pro/Plus (headless)" || m["default"] != true || m["key"] != nil {
		t.Fatalf("method = %v", m)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["methods"] != nil {
		t.Fatalf("claude status = %d %v, want no methods", code, st)
	}
}

// #2567: OpenCode's own providers sign in with the key the person types. The tool's
// login prints its key prompt; the sign-in waits in needs_key with the page that
// issues the key, the key goes to the tool's stdin through the code route, and the
// tool stores it in the selected home. The key is in no answer and no log line.
func TestOpenCodeOwnProviderSignInHandsTheKeyToTheTool(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	for _, tc := range []struct{ method, url, status, key string }{
		{"opencode", "https://opencode.ai/auth", "OpenCode Zen", "fixture-zen-key-0123456789abcdef"},
		{"opencode-go", "https://opencode.ai/console", "OpenCode Go", "fixture-go-key-fedcba9876543210"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			call, home := newSignInServer(t)
			var answers []string
			record := func(code int, body map[string]any) map[string]any {
				b, _ := json.Marshal(body)
				answers = append(answers, strconv.Itoa(code)+" "+string(b))
				return body
			}
			code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": tc.method, "account_ref": "ppf_zen"})
			record(code, flow)
			if code != 202 || flow["state"] != "needs_key" || flow["url"] != tc.url || flow["user_code"] != nil {
				t.Fatalf("start = %d %v, want needs_key with %s", code, flow, tc.url)
			}
			id := flow["id"].(string)
			code, done := call("POST", "/v1/m/agenttools/sign-in/"+id+"/code", map[string]string{"code": tc.key})
			record(code, done)
			if code != 202 || done["state"] != "signed_in" {
				t.Fatalf("key = %d %v, want signed_in", code, done)
			}
			stored, err := os.ReadFile(filepath.Join(home, "opencode", "ppf_zen", ".local", "share", "opencode", "auth.json"))
			if err != nil || !strings.Contains(string(stored), `"`+tc.method+`":{"type":"api","key":"`+tc.key+`"}`) {
				t.Fatalf("the tool did not store the typed key in the selected home: %v %q", err, stored)
			}
			code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode&account_ref=ppf_zen", nil)
			record(code, st)
			if code != 200 || st["signed_in"] != true || st["method"] != tc.status {
				t.Fatalf("status = %d %v, want signed in with %s", code, st, tc.status)
			}
			for _, a := range append(answers, logs.String()) {
				if strings.Contains(a, tc.key) {
					t.Fatalf("the key reached an answer or a log line: %s", a)
				}
			}
		})
	}
}

// A key method's sign-in takes the key only while it waits for one, and the
// device method still takes none.
func TestOpenCodeKeyIsRefusedOutsideItsPrompt(t *testing.T) {
	call, _ := newSignInServer(t)
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "chatgpt-headless"})
	if code != 202 || flow["state"] != "waiting" {
		t.Fatalf("device start = %d %v", code, flow)
	}
	if code, out := call("POST", "/v1/m/agenttools/sign-in/"+flow["id"].(string)+"/code", map[string]string{"code": "fixture-key"}); code != 409 {
		t.Fatalf("a key for the device method = %d %v, want 409 not_waiting", code, out)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil)
	_, flow = call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "opencode"})
	if code, out := call("POST", "/v1/m/agenttools/sign-in/"+flow["id"].(string)+"/code", map[string]string{"code": "fixture\rkey"}); code != 400 {
		t.Fatalf("a key with a line break = %d %v, want 400", code, out)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil)
}

// auth list names each stored credential by provider and type; OpenCode's own
// keys sign the tool in, and every login it holds is named.
func TestOpenCodeStatusReadsItsOwnProviderKeys(t *testing.T) {
	for _, tc := range []struct{ name, output, method string }{
		{"zen", "●  OpenCode api\n└  1 credentials", "OpenCode Zen"},
		{"go", "●  OpenCode Go api\n└  1 credentials", "OpenCode Go"},
		{"all", "●  OpenCode api\n│\n●  OpenCode Go api\n│\n●  OpenAI oauth\n└  3 credentials", "OpenCode Zen, OpenCode Go, ChatGPT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, _ := newSignInServer(t, func(_ *Module, bin string) {
				script := "#!/bin/sh\nprintf '%s\\n' '" + tc.output + "'\nexit 0\n"
				if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode", nil); code != 200 || st["signed_in"] != true || st["method"] != tc.method {
				t.Fatalf("status = %d %v, want signed in with %q", code, st, tc.method)
			}
		})
	}
}

// A key login that ends without storing the key does not sign in, even when the
// home already holds another OpenCode login: the outcome is the chosen method's
// own auth list line, not any login the tool lists.
func TestOpenCodeKeySignInNeedsTheChosenLogin(t *testing.T) {
	call, _ := newSignInServer(t, func(_ *Module, bin string) {
		script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
			"\"auth login\") printf '◆  Enter your API key\\n'; cr=$(printf '\\r'); while c=$(dd bs=1 count=1 2>/dev/null); [ -n \"$c\" ] && [ \"$c\" != \"$cr\" ]; do :; done; exit 0;;\n" +
			"\"auth list\") printf '●  OpenAI oauth\\n└  1 credentials\\n'; exit 0;;\nesac\nexit 2\n"
		if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "opencode"})
	if code != 202 || flow["state"] != "needs_key" {
		t.Fatalf("start = %d %v, want needs_key", code, flow)
	}
	code, done := call("POST", "/v1/m/agenttools/sign-in/"+flow["id"].(string)+"/code", map[string]string{"code": "fixture-unstored-key"})
	if msg, _ := done["message"].(string); code != 202 || done["state"] != "failed" || !strings.Contains(msg, "without storing the OpenCode Zen login") {
		t.Fatalf("key = %d %v, want failed: the tool stored no OpenCode key", code, done)
	}
}

// Only the exact auth list lines sign OpenCode in; a lookalike provider name does not.
func TestOpenCodeStatusIgnoresALookalikeLine(t *testing.T) {
	call, _ := newSignInServer(t, func(_ *Module, bin string) {
		script := "#!/bin/sh\nprintf '●  OpenCode Zen api\\n│\\n●  OpenAI api\\n└  2 credentials\\n'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode", nil); code != 200 || st["signed_in"] != false {
		t.Fatalf("status = %d %v, want not signed in", code, st)
	}
}

// Every OpenCode login method names the auth list line its sign-in must leave, so
// no method's outcome falls back to "any login".
func TestEveryOpenCodeLoginMethodHasItsStatusLine(t *testing.T) {
	facts, _ := driverfacts.Lookup("opencode")
	for _, id := range append(facts.LoginMethodIDs(), "") {
		if got := openCodeLoginFor("opencode", id); got == "" || got == id {
			t.Errorf("method %q has no auth list line", id)
		}
	}
	if got := openCodeLoginFor("claude", ""); got != "" {
		t.Errorf("claude = %q, want no OpenCode line", got)
	}
	if got := openCodeLoginFor("opencode", "unlisted"); got == "" {
		t.Errorf("an unlisted method = %q, want a line no auth list shows (fail closed)", got)
	}
}

// A key login whose prompt never appears (a tool that reworded it) fails with a
// reason after a short wait instead of staying "starting" until the sign-in expires.
func TestOpenCodeKeySignInFailsWhenTheToolNeverAsksForTheKey(t *testing.T) {
	prev := keyPromptWait
	keyPromptWait = 300 * time.Millisecond
	t.Cleanup(func() { keyPromptWait = prev })
	call, _ := newSignInServer(t, func(_ *Module, bin string) {
		script := "#!/bin/sh\n[ \"$1 $2\" = \"auth login\" ] || exit 2\nprintf '◆  Paste your token\\n'\nread -r x\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "opencode"})
	msg, _ := flow["message"].(string)
	if code != 202 || flow["state"] != "failed" || !strings.Contains(msg, "did not ask for the API key") {
		t.Fatalf("start = %d %v, want failed: the tool never showed its key prompt", code, flow)
	}
}

// A sign-in that reached its key prompt is not touched by the prompt wait.
func TestOpenCodeKeyPromptWaitSparesAPromptedSignIn(t *testing.T) {
	prev := keyPromptWait
	keyPromptWait = 300 * time.Millisecond
	t.Cleanup(func() { keyPromptWait = prev })
	call, _ := newSignInServer(t)
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "opencode", "method": "opencode"})
	if code != 202 || flow["state"] != "needs_key" {
		t.Fatalf("start = %d %v, want needs_key", code, flow)
	}
	time.Sleep(3 * keyPromptWait)
	if code, st := call("GET", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil); code != 200 || st["state"] != "needs_key" {
		t.Fatalf("after the wait = %d %v, want still needs_key", code, st)
	}
	call("DELETE", "/v1/m/agenttools/sign-in/"+flow["id"].(string), nil)
}
