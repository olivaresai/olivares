// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"slices"
	"testing"
)

// The tools that can sign in are the rows with a SignIn: nothing else lists them.
// A tool whose row gains a SignIn joins the set with no other edit.
func TestSignInToolsAreTheRowsThatDeclareASignIn(t *testing.T) {
	if got, want := SignInKeys(), []string{"claude", "codex", "grok", "opencode", "gemini-cli"}; !slices.Equal(got, want) {
		t.Fatalf("sign-in keys = %v, want %v", got, want)
	}
	rows = append(rows, Facts{Key: "newtool", Name: "New Tool", Program: "newtool", SignIn: SignInDevice})
	t.Cleanup(func() { rows = rows[:len(rows)-1] })
	keys := SignInKeys()
	if !slices.Contains(keys, "newtool") || slices.Contains(keys, "ollama") {
		t.Fatalf("sign-in keys = %v, want newtool in and ollama out", keys)
	}
	names := SignInNames()
	if len(names) != len(keys) || names[len(names)-1] != "New Tool" {
		t.Fatalf("sign-in names = %v, want one per key, in the same order", names)
	}
}

func TestJoinList(t *testing.T) {
	for _, c := range []struct {
		items []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a or b"},
		{[]string{"a", "b", "c"}, "a, b or c"},
	} {
		if got := JoinList(c.items, "or"); got != c.want {
			t.Errorf("JoinList(%v) = %q, want %q", c.items, got, c.want)
		}
	}
	if got := JoinList([]string{"Claude Code", "Codex", "Grok Build", "OpenCode"}, "and"); got != "Claude Code, Codex, Grok Build and OpenCode" {
		t.Errorf("and-list = %q", got)
	}
}

// The tools a session can run are the rows with a Session; the resolve refusal
// names them from here.
func TestSessionKeysAreTheRowsThatDeclareASession(t *testing.T) {
	if got, want := SessionKeys(), []string{"claude", "codex", "grok", "opencode", "gemini-cli"}; !slices.Equal(got, want) {
		t.Fatalf("session keys = %v, want %v", got, want)
	}
}
