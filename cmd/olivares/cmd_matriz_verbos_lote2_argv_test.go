// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// EIGHT MORE VERBS OF THE RELEASE MATRIX GET AN ARGV LITERAL HERE, AND THAT IS THE POINT.
//
// `scripts/matriz-valor-cli.py` reads coverage from the argv literals a test writes: a verb
// followed by another token, each SEPARATELY quoted. These eight had no such literal anywhere
// in cmd/olivares/*_test.go, so the release matrix counted them NO MEDIDO or partial. That is
// not a formatting detail — the matrix is what says whether the CLI surface is exercised
// before a cut, and `setup`, `migrate` and `support` are on the first path a new operator walks.
//
// ⛔ WHAT EACH CASE ASSERTS IS DELIBERATELY SMALL, and saying so is the point: that the verb
// is registered, parses its own shape and refuses what it must. It does NOT assert what the
// verb does once it reaches a server or a database. Claiming that here would be a witness
// whose name promises more than it checks, which is the defect this repository measures most.
//
// TWO HARNESS TRICKS, both borrowed rather than invented:
//
//   - `--server https://127.0.0.1:9` (and `--endpoint`) is a port nothing listens on, so
//     anything that reached the network fails loudly instead of passing quietly. Same trick as
//     cmd_confirm_test.go for the destructive verbs.
//   - every verb with a LOCAL side effect writes into t.TempDir(). `setup` really does write an
//     env file and a secrets directory, and `migrate status` really does open a store; pointing
//     them at a temp directory is what makes running them honest instead of dangerous.
//
// NOT IN THIS TABLE, and each for a stated reason rather than by omission:
//
//	quickstart      starts listeners and blocks; it needs a different harness, not this one
//	governed-rag    writes a live content-source config whose destination is not a flag I can
//	                point at a temp dir with confidence
//	grok-hook       reads stdin, and this table's harness feeds none (the same reason
//	                codex-hook sits outside the table)
//	ddil            its useful subverbs want a server AND a signing key; --help alone would
//	                credit the verb without exercising anything, and that is gaming the counter
var lote2VerbArgv = []struct {
	name string
	argv func(tmp string) []string
}{
	// Pure generation, no network, no filesystem: these must SUCCEED, and the sibling test
	// below asserts they actually emit a script rather than an empty string.
	{"completion bash", func(string) []string { return []string{"completion", "bash"} }},
	{"completion zsh", func(string) []string { return []string{"completion", "zsh"} }},
	{"completion fish", func(string) []string { return []string{"completion", "fish"} }},

	// Network-bound: complete argv, unreachable port.
	{"capabilities skills", func(string) []string {
		return []string{"capabilities", "skills",
			"--server", "https://127.0.0.1:9", "--token", "t",
			"--tenant", "00000000-0000-0000-0000-000000000000"}
	}},
	{"capabilities tools", func(string) []string {
		return []string{"capabilities", "tools",
			"--server", "https://127.0.0.1:9", "--token", "t",
			"--tenant", "00000000-0000-0000-0000-000000000000"}
	}},
	{"evals gate", func(string) []string {
		return []string{"evals", "gate",
			"--server", "https://127.0.0.1:9", "--token", "t",
			"--tenant", "00000000-0000-0000-0000-000000000000"}
	}},
	{"support bundle", func(tmp string) []string {
		return []string{"support", "bundle",
			"--out", filepath.Join(tmp, "support.tar.gz"),
			"--server", "https://127.0.0.1:9"}
	}},
	{"release export-mirror", func(tmp string) []string {
		return []string{"release", "export-mirror",
			"--endpoint", "https://127.0.0.1:9", "--token", "t",
			"--set", "biz", "--out", filepath.Join(tmp, "mirror")}
	}},

	// Local: real work, contained in t.TempDir().
	{"commands", func(string) []string { return []string{"commands", "--output", "text"} }},
	{"migrate status", func(tmp string) []string {
		return []string{"migrate", "status", "--engine", "sqlite", "--data-dir", tmp}
	}},
	{"setup", func(tmp string) []string {
		return []string{"setup", "--force",
			"--out", filepath.Join(tmp, "olivares.env"),
			"--secrets-dir", filepath.Join(tmp, "secrets")}
	}},
}

// usageMarkers are the strings cobra uses when the SHAPE is wrong: a renamed flag, a lost
// positional, a verb that stopped being registered. A network or database failure is a
// different thing and does not fail this test — unreachable is the expected end of the road
// at port 9.
var usageMarkers = []string{
	"unknown command", "unknown flag", "unknown shorthand",
	"accepts ", "requires at least", "invalid argument", "required flag",
}

func TestLote2VerbsParseTheirOwnShape(t *testing.T) {
	for _, c := range lote2VerbArgv {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, c.argv(t.TempDir())...)
			if err == nil {
				return // parsed and ran; nothing more is claimed here
			}
			for _, usage := range usageMarkers {
				if strings.Contains(err.Error(), usage) || strings.Contains(out, usage) {
					t.Fatalf("%q is a USAGE error, so the verb's shape moved: %v\n%s", usage, err, out)
				}
			}
		})
	}
}

// TestCompletionActuallyEmitsAScript is the half the table above cannot assert. The three
// completion cases pass the table by NOT failing, and a verb that returned nothing at all
// would pass it too. This one requires the bytes.
func TestCompletionActuallyEmitsAScript(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			out, err := runCLI(t, "completion", shell)
			if err != nil {
				t.Fatalf("completion %s must succeed offline: %v", shell, err)
			}
			if len(out) < 200 {
				t.Fatalf("completion %s emitted %d bytes; that is not a completion script:\n%s",
					shell, len(out), out)
			}
			if !strings.Contains(out, "olivares") {
				t.Fatalf("completion %s never names the binary it completes:\n%s", shell, out)
			}
		})
	}
}

// TestLote2RefusesAnUnknownSubverb is the CALIBRATION: if the table above passed because
// runCLI swallows everything, this would pass too — and it must not.
//
// It asserts the REFUSAL and, without flags, that the message names the unknown COMMAND.
// With flags cobra stops parsing subcommands at the unknown word and blames the first flag
// the parent does not own, so pinning the wording there would make this witness fail for a
// reason it is not about (measured on the table before it, same behaviour).
func TestLote2RefusesAnUnknownSubverb(t *testing.T) {
	if _, err := runCLI(t, "capabilities", "teleport"); err == nil {
		t.Fatal("an unknown capabilities subverb must be refused, not accepted")
	} else if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("without flags the refusal must name the unknown COMMAND, got: %v", err)
	}
	if _, err := runCLI(t, "completion", "klingon"); err == nil {
		t.Fatal("completion must refuse a shell it cannot generate")
	}
}

// TestCommandsPrintsTheWholeTree is the half the table cannot assert. `commands` is the hidden
// diagnostic the release smoke diffs between the packaged artifact and a source build to catch a
// stale binary — so what matters is that it prints the WHOLE tree, hidden commands included, and
// the table would pass on an empty output just as happily.
func TestCommandsPrintsTheWholeTree(t *testing.T) {
	out, err := runCLI(t, "commands", "--output", "text")
	if err != nil {
		t.Fatalf("commands must succeed offline: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 50 {
		t.Fatalf("commands printed %d paths; this binary has far more:\n%s", len(lines), out)
	}
	// One visible verb, and one HIDDEN one — `commands` hides itself, so finding it proves the
	// walk does not stop at what --help shows.
	for _, want := range []string{"olivares completion", "olivares commands"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the command tree never names %q:\n%s", want, out)
		}
	}
}
