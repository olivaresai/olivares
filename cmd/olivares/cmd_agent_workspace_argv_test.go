// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/codex/session"
)

// EVERY GOVERNED-WORKSPACE VERB IS INVOKED HERE, AND THAT IS THE WHOLE POINT.
//
// `matriz-valor-cli.py` reads coverage from the argv literals a test writes: a verb followed
// by another token, each SEPARATELY quoted. Five verbs of this area — files, mkdir, mv,
// rm-workspace, stat — had no such literal anywhere and came out NO MEDIDO on the release
// matrix. Not a formatting detail: the matrix is what says whether the CLI surface is
// exercised before a cut, and five governed file operations were counted as untested.
//
// What each case asserts is deliberately SMALL: that the verb parses its own shape and
// refuses what it must, without a server. `--server https://127.0.0.1:9` is a port nothing
// listens on, so anything that reached the network would fail loudly instead of passing
// quietly — the same trick cmd_confirm_test.go uses for the destructive verbs.
//
// It does NOT assert what each verb does once connected. That belongs with a fixture server
// and is a different test; claiming it here would be a witness whose name promises more than
// it checks.

var workspaceVerbArgv = []struct {
	name string
	argv []string
}{
	{"agent workspace files", []string{
		"agent", "workspace", "files", "ws-123",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"agent workspace stat", []string{
		"agent", "workspace", "stat", "ws-123", "notes/readme.md",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"agent workspace mkdir", []string{
		"agent", "workspace", "mkdir", "ws-123", "notes/drafts",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"agent workspace mv", []string{
		"agent", "workspace", "mv", "ws-123", "notes/a.md", "notes/b.md",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"agent workspace rm-workspace", []string{
		"agent", "workspace", "rm-workspace", "ws-123",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
}

// The last two verbs the matrix counted as untested are not workspace verbs, but they belong
// in the same table: they had no argv literal either, and the fix is identical. Kept apart so
// the name of the table above stays true to what it holds.
//
//	threatintel  a parent with subcommands (verify/apply/pull/sign): the argv names one
//
// ⛔ codex-hook IS NOT IN THIS TABLE, BUT IT IS NO LONGER UNEXERCISED — see
// TestCodexHookRunsInProcessForEveryKnownEvent at the bottom of this file. It stays out of the
// table because it reads stdin and the table's harness does not feed one.
//
// This comment used to say the opposite: that codex-hook could have no argv literal ANYWHERE
// because os.Exit killed the test binary. A Codex sol max contrast refuted it with the line
// numbers (cmd_codexhook.go:106-110): the os.Exit is CONDITIONAL, and for every event in the
// closed known set the code stays 0 even on a DENY. The claim was measured once, on the branch
// that does exit non-zero, and then generalised to the whole verb without re-checking.
var otherUnexercisedVerbs = []struct {
	name string
	argv []string
}{
	{"threatintel verify", []string{"threatintel", "verify", "catalog.json"}},
}

// TestOtherUnexercisedVerbsParse covers the same ground as the workspace table for the two
// verbs that were NO MEDIDO outside that area. Same contract: a usage error means the verb's
// shape moved; anything else is the command doing its job and failing on an unreachable
// endpoint or an absent file, which is the expected end of the road here.
func TestOtherUnexercisedVerbsParse(t *testing.T) {
	for _, c := range otherUnexercisedVerbs {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, c.argv...)
			if err == nil {
				return
			}
			for _, usage := range []string{
				"unknown command", "unknown flag", "unknown shorthand",
				"accepts ", "requires at least", "invalid argument",
			} {
				if strings.Contains(err.Error(), usage) || strings.Contains(out, usage) {
					t.Fatalf("%s is a USAGE error, so the verb's shape moved: %v\n%s", usage, err, out)
				}
			}
		})
	}
}

// TestWorkspaceVerbsParseTheirOwnShape runs each verb with a COMPLETE argv and requires that
// whatever comes back is not a usage error. A verb that renamed a flag, lost a positional or
// stopped being registered fails here; one that merely cannot reach the server does not,
// because unreachable is the expected end of the road at port 9.
func TestWorkspaceVerbsParseTheirOwnShape(t *testing.T) {
	for _, c := range workspaceVerbArgv {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, c.argv...)
			if err == nil {
				return // parsed and ran; nothing to prove beyond that here
			}
			// The failure must come from the network, not from the parser.
			for _, usage := range []string{
				"unknown command", "unknown flag", "unknown shorthand",
				"accepts ", "requires at least", "invalid argument",
			} {
				if strings.Contains(err.Error(), usage) || strings.Contains(out, usage) {
					t.Fatalf("%s is a USAGE error, so the verb's shape moved: %v\n%s", usage, err, out)
				}
			}
		})
	}
}

// TestWorkspaceVerbsRefuseAnUnknownSubverb is the calibration: if the test above passed
// because runCLI swallows everything, this one would pass too — and it must not.
//
// It asserts the REFUSAL, not a particular wording, and that is a measured decision rather
// than a loosening. With no flags the CLI names the real problem:
//
//	agent workspace teleport                          -> unknown command "teleport" for "olivares agent workspace"
//	agent workspace teleport --server https://…       -> unknown flag: --server
//
// Cobra stops parsing subcommands at the unknown word, so `teleport` becomes an ARGUMENT to
// `workspace` and the first flag that `workspace` does not own is what gets blamed. The
// refusal is correct in both cases; the second message points at the wrong token. Pinning the
// wording here would have made this witness fail for a reason it is not about — and hidden
// the finding inside a red that looks like my own bug.
func TestWorkspaceVerbsRefuseAnUnknownSubverb(t *testing.T) {
	if _, err := runCLI(t, "agent", "workspace", "teleport"); err == nil {
		t.Fatal("an unknown workspace subverb must be refused, not accepted")
	} else if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("without flags the refusal must name the unknown COMMAND, got: %v", err)
	}
	if _, err := runCLI(t, "agent", "workspace", "teleport",
		"--server", "https://127.0.0.1:9", "--token", "t"); err == nil {
		t.Fatal("an unknown subverb with flags must still be refused")
	}
}

// runCLIStdin is runCLI with an input to read. codex-hook is the only verb in this file that
// consumes stdin, and runCLI leaves cmd.InOrStdin() pointing at the TEST BINARY's own stdin —
// which is not a controlled input and would make the result depend on how the suite was
// launched.
func runCLIStdin(t *testing.T, in string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(in))
	root.SetArgs(args)
	_, err := root.ExecuteC()
	return out.String(), err
}

// TestCodexHookRunsInProcessForEveryKnownEvent closes the last NO MEDIDO verb, and it exists
// because a Codex sol max contrast REFUTED the reason this file used to give for leaving it out.
//
// What that comment claimed: codex-hook calls os.Exit, so invoking it in-process kills the test
// binary, so it can have no argv literal anywhere.
//
// What is actually true (cmd_codexhook.go:106-110): the os.Exit is CONDITIONAL — it only fires
// when res.ExitCode != 0. And session.ExitCodeFor (render.go:225-230) returns non-zero on
// exactly one branch: a decision that blocks on an event the connector does not know. For every
// event in the closed set, the exit code stays 0 even when the verdict is DENY, because there
// the stdout shape is the verified mechanism. So the whole known set is reachable in-process.
//
// The cited precedent was wrong too: runCLIExit (exitcode_rewrap_test.go) is not a subprocess,
// it calls root.ExecuteC in this same process.
//
// HOW THIS TEST FAILS, which is the part worth trusting: if any known event starts returning a
// non-zero code, os.Exit fires and THE TEST BINARY DIES — no verdict, no subtest output, the
// whole package reports `exit status N`. That is not a subtle red; it is the loudest failure
// this suite can produce. The assertions below cover the quieter regressions.
func TestCodexHookRunsInProcessForEveryKnownEvent(t *testing.T) {
	events := session.KnownEvents()
	if len(events) == 0 {
		t.Fatal("the known-event set is empty, so this test would pass without exercising anything")
	}
	for _, event := range events {
		t.Run(event, func(t *testing.T) {
			in := `{"hook_event_name":` + strconv.Quote(event) + `,"session_id":"s948"}`
			out, err := runCLIStdin(t, in, "codex-hook", "--endpoint", "http://127.0.0.1:9")
			for _, usage := range []string{
				"unknown command", "unknown flag", "unknown shorthand",
				"accepts ", "requires at least", "invalid argument",
			} {
				if (err != nil && strings.Contains(err.Error(), usage)) || strings.Contains(out, usage) {
					t.Fatalf("%s is a USAGE error, so codex-hook's shape moved: %v\n%s", usage, err, out)
				}
			}
			// A hook that writes nothing is read by Codex as "no objection" (cmd_codexhook.go:98),
			// so silence here would be a governed path failing open.
			if strings.TrimSpace(out) == "" {
				t.Fatalf("codex-hook wrote NOTHING for %q; Codex reads silence as no objection", event)
			}
		})
	}
}

// TestCodexHookUnknownEventNeedsASubprocess records, as an executable note, WHY the
// deny-closed branch is not exercised in-process here — and checks the one half of it that can
// be checked without a process boundary.
//
// The unknown-event branch is the ONLY one where session.ExitCodeFor returns non-zero, so
// running it through runCLIStdin would fire os.Exit and kill this binary. Covering it for real
// needs the subprocess harness this package already has (cmd_security_unstamped_test.go:66-92,
// with the CLI trampoline in cmd_security_drill_test.go:16-20). That is a different test from
// this file's contract, and writing it is tracked work, not a thing to fake here.
//
// What IS checked: that the fixture used by that future test is genuinely outside the known
// set. Without this, the subprocess test could be written against an event that quietly became
// known and would pass while proving nothing.
func TestCodexHookUnknownEventNeedsASubprocess(t *testing.T) {
	if session.IsKnownEvent("S948FutureEvent") {
		t.Fatal("the fixture event is in the known set, so the deny-closed test it guards proves nothing")
	}
}
