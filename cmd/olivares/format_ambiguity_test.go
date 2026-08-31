// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// semanticFormatCommands are the command paths where `--format` selects an
// EXPORT format and is fully supported.
var semanticFormatCommands = [][]string{
	{"audit", "export"},
	{"findings", "export"},
}

// TestSemanticFormatFlagIsNotDeprecated is the E8 guarantee. `--format` means
// two different things in this CLI: a legacy spelling of -o/--output on
// eventing/hookpep/config/superadmin, and a real export-format selector on
// `audit export` (cef|leef|syslog|otlp|ocsf) and `findings export` (sarif).
//
// The hazard is not the collision itself, it is the lesson: an operator who
// reads "--format is deprecated" on one command and applies it to their SIEM
// export breaks it. So the semantic flag must never be the deprecated alias, and
// must never carry the deprecation wording.
func TestSemanticFormatFlagIsNotDeprecated(t *testing.T) {
	root := newRootCmd()
	for _, path := range semanticFormatCommands {
		name := strings.Join(path, " ")
		cmd, _, err := root.Find(path)
		if err != nil || cmd == nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		flag := cmd.Flags().Lookup("format")
		if flag == nil {
			t.Fatalf("%q lost its --format flag; scripted SIEM exports depend on it", name)
		}
		if strings.Contains(strings.ToLower(flag.Usage), "deprecated alias") {
			t.Errorf("%q --format is the EXPORT format, but its help calls it a deprecated alias: %s",
				name, flag.Usage)
		}
		if _, isAlias := flag.Value.(*deprecatedOutputAlias); isAlias {
			t.Errorf("%q --format is wired to the deprecated output alias, so it can no longer "+
				"select an export format", name)
		}
	}
}

// TestSemanticFormatFlagEmitsNoDeprecationWarning is the behavioral half: using
// it must print nothing about deprecation.
func TestSemanticFormatFlagEmitsNoDeprecationWarning(t *testing.T) {
	t.Chdir(t.TempDir())
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"findings", "export", "--format", "sarif"})
	_, _ = root.ExecuteC() // it will fail for want of a server; the warning is what matters
	if strings.Contains(errb.String(), "deprecated") {
		t.Fatalf("`findings export --format sarif` warned about deprecation:\n%s", errb.String())
	}
}

// TestDeprecatedFormatWarningSaysWhereItApplies pins the other side: the warning
// an operator DOES see must scope itself, so the rule they learn is the true one.
func TestDeprecatedFormatWarningSaysWhereItApplies(t *testing.T) {
	warning := deprecationWarningFor("format")
	for _, phrase := range []string{"ON THIS COMMAND", "audit export", "findings export"} {
		if !strings.Contains(warning, phrase) {
			t.Errorf("the deprecation warning must mention %q so it is not read as a rule about "+
				"every --format in the CLI; got: %s", phrase, warning)
		}
	}
	if !strings.Contains(deprecatedFormatFlagHelp, "NOT the export-format flag") {
		t.Errorf("the alias's own help must name the collision: %s", deprecatedFormatFlagHelp)
	}
	// Render the REAL help, not the constant. Checking the constant is how the
	// first version of this test passed while `secrets ls --help` showed
	// `--format audit export`: pflag reads a backtick pair in a usage string as
	// the flag's METAVARIABLE, so quoting the command names broke the rendering
	// of the very help this unit exists to make trustworthy.
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"secrets", "ls", "--help"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("secrets ls --help: %v", err)
	}
	if !strings.Contains(out.String(), "--format string") {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, "--format") {
				t.Fatalf("the rendered alias help has the wrong metavariable "+
					"(a backtick pair in the usage string becomes one):\n%s", strings.TrimSpace(line))
			}
		}
		t.Fatal("the rendered help does not mention --format at all")
	}
	// --json has no twin: telling its user about `audit export` would be noise.
	if strings.Contains(deprecationWarningFor("json"), "audit export") {
		t.Errorf("the --json warning must not carry the --format collision note: %s",
			deprecationWarningFor("json"))
	}
}

// TestDeprecatedFormatAliasStillWorks: naming the collision must not break the
// legacy spelling that operators already have in scripts.
func TestDeprecatedFormatAliasStillWorks(t *testing.T) {
	cmd := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return nil }}
	addDeprecatedFormatFlag(cmd, false)
	var errb bytes.Buffer
	cmd.SetErr(&errb)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--format json must still be accepted: %v", err)
	}
	if got, _ := selectedOutput(cmd); got != "json" {
		t.Fatalf("--format json selected %q, want json", got)
	}
	if !strings.Contains(errb.String(), "deprecated") {
		t.Errorf("using the alias must still warn:\n%s", errb.String())
	}
}
