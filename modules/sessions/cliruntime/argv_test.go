// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import (
	"strings"
	"testing"
)

func TestLaunchArgs_ClaudeIsStreamJSONNotRemoteControl(t *testing.T) {
	args, err := LaunchArgs(KindClaude, LaunchRequest{PermissionMode: "plan", ResumeID: "sess-1", Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, need := range []string{"--input-format", "stream-json", "--output-format", "stream-json", "--resume", "sess-1", "--permission-mode", "plan"} {
		if !containsArg(args, need) {
			t.Errorf("claude argv missing %q: %s", need, joined)
		}
	}
	if containsArg(args, "--remote-control") {
		t.Fatal("claude operate argv must not produce remote-control")
	}
}

func TestLaunchArgs_CodexIsOwnedStdioAppServer(t *testing.T) {
	args, err := LaunchArgs(KindCodex, LaunchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "app-server --listen stdio://" {
		t.Fatalf("codex argv = %v", args)
	}
	for _, forbidden := range []string{"--remote", "serve", "daemon"} {
		if containsArg(args, forbidden) {
			t.Errorf("codex argv contains adoption/daemon form %q", forbidden)
		}
	}
}

func TestLaunchArgs_GrokIsOwnedNonLeaderStdio(t *testing.T) {
	args, err := LaunchArgs(KindGrok, LaunchRequest{Model: "grok-4", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "agent") || !strings.Contains(joined, "--no-leader") || !strings.HasSuffix(joined, "stdio") {
		t.Fatalf("grok argv = %v", args)
	}
	if containsArg(args, "--leader") || containsArg(args, "serve") {
		t.Fatalf("grok argv must not produce a leader or server form: %v", args)
	}
}

func TestLaunchArgs_UnknownKind(t *testing.T) {
	if _, err := LaunchArgs("not-a-vendor", LaunchRequest{}); err == nil {
		t.Fatal("want ErrUnknownKind")
	}
}

func TestHomeEnv_DoesNotCrossKinds(t *testing.T) {
	claude := HomeEnv(KindClaude, "/u/claude", "/c/claude")
	codex := HomeEnv(KindCodex, "/u/codex", "/c/codex")
	if got := valueOf(claude, "CLAUDE_CONFIG_DIR"); got != "/c/claude" {
		t.Fatalf("claude config home = %q", got)
	}
	if got := valueOf(codex, "CODEX_HOME"); got != "/c/codex" {
		t.Fatalf("codex config home = %q", got)
	}
	if valueOf(claude, "CODEX_HOME") != "" || valueOf(codex, "CLAUDE_CONFIG_DIR") != "" {
		t.Fatal("a kind must not receive another vendor's configuration-home variable")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func valueOf(env []EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// TestLaunchTransportIsDeclaredForEveryKindAndOnlyThose keeps the transport
// table and the argv table on the same three kinds. A kind with an argv and no
// declared transport would refuse to launch at all; a kind with a transport and
// no argv would launch with none.
func TestLaunchTransportIsDeclaredForEveryKindAndOnlyThose(t *testing.T) {
	for _, kind := range []string{KindClaude, KindCodex, KindGrok} {
		if got := LaunchTransport(kind); got != TransportStdio {
			t.Fatalf("LaunchTransport(%q) = %q, want %q — every owned operate argv is a stdio protocol", kind, got, TransportStdio)
		}
		if _, err := LaunchArgs(kind, LaunchRequest{}); err != nil {
			t.Fatalf("LaunchArgs(%q): %v", kind, err)
		}
	}
	if got := LaunchTransport("mystery"); got != "" {
		t.Fatalf("LaunchTransport of an unknown kind = %q, want the empty transport", got)
	}
}
