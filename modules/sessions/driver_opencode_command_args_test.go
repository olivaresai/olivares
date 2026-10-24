// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// #2998: OpenCode expands a /command's arguments into its template and then runs
// every !`shell` in it and reads every @file, before and despite the session's
// bash, read and external_directory rules (measured over ACP on 1.19.3: a marker
// was created with bash denied, and a file outside the folder reached the model
// with read denied). A plain prompt is not expanded. Such a prompt is refused
// before it reaches the tool.
func TestOpenCodeInputRefusesInterpolationInCommandArguments(t *testing.T) {
	for _, text := range []string{
		"/review !`touch /tmp/x`",
		"  /review main.go !`curl example.test`",
		"/review @.env",
		"/review (@../secret.txt)",
		"/review\t@/etc/passwd",
		// OpenCode finds the command after trimming any leading space (measured:
		// NBSP, BOM, vertical tab, U+3000 and blank lines each still expanded).
		"\u00a0/review !`touch /tmp/x`",
		"\ufeff/review @.env",
		"\v/review !`touch /tmp/x`",
		"\u3000/review !`touch /tmp/x`",
		"\n\n/review !`touch /tmp/x`",
		// An opening !` is closed by a backtick in the template (measured with
		// the template "Run `$ARGUMENTS` now.").
		"/review x` !`touch /tmp/x",
		// Review of #3010: a lone backtick closes an opener the template leaves
		// open, two arguments can join into one, and OpenCode reads $ in the
		// arguments as replacement patterns (or expands a typed $ARGUMENTS again).
		"/review x`",
		"/review ! `id`",
		"/review $HOME",
		"/review $ARGUMENTS",
		"/review the change by dev@example.test",
	} {
		t.Run(text, func(t *testing.T) {
			peer := newOpenCodePeer(t)
			if _, err := peer.answerHandshake("ses_args"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			before := len(peer.sentLines())
			attempted, err := peer.session.Input(context.Background(), text)
			var refusal *runErr
			if attempted || !errors.As(err, &refusal) || refusal.status != http.StatusBadRequest || !strings.Contains(err.Error(), "OpenCode") {
				t.Fatalf("input = %v %v, want a named 400 refusal", attempted, err)
			}
			if sent := peer.sentLines()[before:]; len(sent) != 0 {
				t.Fatalf("refused input reached the tool: %s", sent)
			}
		})
	}
}

func TestOpenCodeInputSendsCommandsAndPromptsWithoutInterpolation(t *testing.T) {
	for _, text := range []string{
		"/explain main.go",
		"/explain the parse function",
		"explain what !`date` would print", // a plain prompt is not expanded
		"read @README.md and summarize it", // nor are its file mentions
	} {
		t.Run(text, func(t *testing.T) {
			peer := newOpenCodePeer(t)
			if _, err := peer.answerHandshake("ses_args"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			done := make(chan error, 1)
			go func() { _, err := peer.session.Input(context.Background(), text); done <- err }()
			id, method, _ := peer.nextRequest()
			if method != acpMethodSessionPrompt {
				t.Fatalf("method = %q", method)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			peer.reply(id, map[string]any{"stopReason": "end_turn"})
		})
	}
}

// #3083: OpenCode's built-in /review is a subtask command: it runs as a subagent whatever
// the task rule says, and that subagent's approval requests never reach Olivares, so
// its turn waits forever (measured on 1.19.4 over ACP). It is refused by name.
func TestOpenCodeInputRefusesTheBuiltInSubtaskCommand(t *testing.T) {
	for _, text := range []string{"/review", "/review main.go", "  /review", "/review\tHEAD~1"} {
		t.Run(text, func(t *testing.T) {
			peer := newOpenCodePeer(t)
			if _, err := peer.answerHandshake("ses_args"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			before := len(peer.sentLines())
			attempted, err := peer.session.Input(context.Background(), text)
			var refusal *runErr
			if attempted || !errors.As(err, &refusal) || refusal.status != http.StatusBadRequest || !strings.Contains(err.Error(), "/review") {
				t.Fatalf("input = %v %v, want a 400 that names /review", attempted, err)
			}
			if sent := peer.sentLines()[before:]; len(sent) != 0 {
				t.Fatalf("refused input reached the tool: %s", sent)
			}
		})
	}
}
