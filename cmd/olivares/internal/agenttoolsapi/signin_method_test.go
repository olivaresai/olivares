// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"strings"
	"testing"
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
	if code != 200 || len(methods) != 1 {
		t.Fatalf("opencode status = %d %v, want one method", code, st)
	}
	m, _ := methods[0].(map[string]any)
	if m["id"] != "chatgpt-headless" || m["label"] != "ChatGPT Pro/Plus (headless)" || m["default"] != true {
		t.Fatalf("method = %v", m)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=claude", nil); code != 200 || st["methods"] != nil {
		t.Fatalf("claude status = %d %v, want no methods", code, st)
	}
}
