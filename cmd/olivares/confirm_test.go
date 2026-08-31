// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// destructiveVerbs are the invocations that delete something. Each must refuse
// to act in a non-interactive session unless --yes states the intent.
//
// The arguments are deliberately complete enough to reach the confirmation and
// no further: the point is that the prompt happens BEFORE anything is contacted
// or opened, so a refusal cannot have already done half the work.
var destructiveVerbs = []struct {
	name string
	argv []string
}{
	{"agent workspace rm --recursive", []string{
		"agent", "workspace", "rm", "ws-123", "obsolete", "--recursive",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"secrets rm", []string{"secrets", "rm", "--name", "vault/token"}},
	{"sources rm", []string{"sources", "rm", "--name", "vault-prod"}},
	{"eventing subscriptions rm", []string{
		"eventing", "subscriptions", "rm",
		"--tenant", "00000000-0000-0000-0000-000000000000", "--id", "sub-123"}},
}

// TestDestructiveVerbsRefuseWithoutConsent is the deny-closed half: with no
// terminal to ask, the command must not proceed. `agent workspace rm
// --recursive` reached os.RemoveAll on the server with no confirmation at all
// before.
func TestDestructiveVerbsRefuseWithoutConsent(t *testing.T) {
	for _, tc := range destructiveVerbs {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			// A bytes.Buffer is not a terminal — the same answer a pipe gets.
			root.SetIn(strings.NewReader("y\ny\ny\n"))
			root.SetArgs(tc.argv)
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("`olivares %s` proceeded with no confirmation", strings.Join(tc.argv, " "))
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("the refusal must name the way to state intent, got: %v", err)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Errorf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
		})
	}
}

// TestDestructiveVerbsAcceptExplicitYes proves the refusal is about CONSENT and
// not a command that simply cannot run: with --yes each verb gets past the gate
// and fails on its own terms (no installation, no server), never on the prompt.
func TestDestructiveVerbsAcceptExplicitYes(t *testing.T) {
	for _, tc := range destructiveVerbs {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(append(append([]string{}, tc.argv...), "--yes"))
			_, err := root.ExecuteC()
			if err != nil && strings.Contains(err.Error(), "confirm") {
				t.Fatalf("--yes did not satisfy the confirmation: %v", err)
			}
		})
	}
}

// TestAPipeIsNotConsent is the case that actually bites, and the one the
// bytes.Buffer tests above cannot reach: `yes | olivares agent workspace rm …
// --recursive`. There stdin IS an *os.File, so the type check passes and the
// answer depends entirely on the terminal probe. The first implementation used
// os.ModeCharDevice, which /dev/null and some pipes satisfy — it would have read
// a stream of "y" as a human agreeing.
func TestAPipeIsNotConsent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Skipf("os.Pipe unavailable: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	go func() {
		defer func() { _ = w.Close() }()
		for range 8 {
			if _, werr := io.WriteString(w, "y\n"); werr != nil {
				return
			}
		}
	}()

	if interactiveStdin(r) {
		t.Fatal("a pipe must not be treated as a terminal")
	}
	cmd := &cobra.Command{}
	var errb bytes.Buffer
	cmd.SetErr(&errb)
	cmd.SetIn(r)
	if err := confirmDestructive(cmd, false, "delete the whole subtree"); err == nil {
		t.Fatal("a stream of \"y\" from a pipe was accepted as consent")
	}
}

// TestConfirmDestructiveOnATerminal exercises the branch a pipe cannot reach.
func TestConfirmDestructiveOnATerminal(t *testing.T) {
	restore := interactiveStdin
	t.Cleanup(func() { interactiveStdin = restore })
	interactiveStdin = func(io.Reader) bool { return true }

	for _, tc := range []struct {
		answer string
		wantOK bool
	}{
		{"y\n", true}, {"yes\n", true}, {"YES\n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"maybe\n", false},
	} {
		t.Run(strings.TrimSpace(tc.answer)+"|", func(t *testing.T) {
			cmd := &cobra.Command{}
			var errb bytes.Buffer
			cmd.SetErr(&errb)
			cmd.SetIn(strings.NewReader(tc.answer))
			err := confirmDestructive(cmd, false, "delete everything")
			if tc.wantOK && err != nil {
				t.Fatalf("answer %q must confirm, got: %v", tc.answer, err)
			}
			if !tc.wantOK && err == nil {
				t.Fatalf("answer %q must NOT confirm", tc.answer)
			}
			if !strings.Contains(errb.String(), "delete everything") {
				t.Errorf("the prompt must name the target, got: %q", errb.String())
			}
		})
	}
}
