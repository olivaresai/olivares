// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestExamplesInvokeRealCommandsAndFlags checks every `Examples:` block in the
// tree against the tree itself: the command path must resolve, and each long
// flag it passes must exist on the command it is passed to (or be inherited).
//
// It exists because help text is the one part of a CLI nothing else validates.
// A renamed flag leaves the old name in an example forever, and an example
// written from memory can name a flag that never existed — which is exactly how
// this file came to be: `olivares evals gate --config … --candidates …` was
// written into the `evals` group help during and the real flags are
// --suite/--subject/--outputs.
func TestExamplesInvokeRealCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	checked := 0
	walkCommands(root, func(cmd *cobra.Command) {
		for _, line := range strings.Split(cmd.Example, "\n") {
			invocation, ok := invocationFrom(line)
			if !ok {
				continue
			}
			checked++
			verifyInvocation(t, root, cmd.CommandPath(), invocation)
		}
	})
	// A guard on the guard: if the extraction silently stops matching, the test
	// would pass by checking nothing.
	if checked < 100 {
		t.Fatalf("only %d example invocations extracted; the extractor is not seeing the help text", checked)
	}
	t.Logf("verified %d example invocations", checked)
}

// invocationFrom pulls the `olivares …` words out of one example line, or
// reports that the line carries no invocation to check (a comment, a shell
// continuation, prose, or a pipeline whose olivares part is not leading).
func invocationFrom(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "sudo ")
	if !strings.HasPrefix(s, "olivares ") {
		return nil, false
	}
	// Stop at the first shell metacharacter: everything after it belongs to the
	// shell, not to this command's flag set.
	for _, stop := range []string{"|", ">", "<", ";", "&&", "\\"} {
		if i := strings.Index(s, stop); i >= 0 {
			s = s[:i]
		}
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return nil, false
	}
	return fields[1:], true
}

// resolveInvocation walks the leading subcommand path of an example's words and
// returns the command it names, or nil when the path does not resolve.
func resolveInvocation(root *cobra.Command, words []string) *cobra.Command {
	// Split the leading subcommand path from the arguments/flags.
	var path []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			break
		}
		if _, _, err := root.Find(append(append([]string{}, path...), w)); err != nil {
			break
		}
		candidate := append(append([]string{}, path...), w)
		found, _, _ := root.Find(candidate)
		if found == nil || found.CommandPath() != "olivares "+strings.Join(candidate, " ") {
			break
		}
		path = candidate
	}
	target, _, err := root.Find(path)
	if err != nil || target == nil {
		return nil
	}
	return target
}

// flagKnownTo reports whether name is a flag of target or one inherited from
// the tree above it.
func flagKnownTo(root, target *cobra.Command, name string) bool {
	return target.Flags().Lookup(name) != nil || target.InheritedFlags().Lookup(name) != nil ||
		root.PersistentFlags().Lookup(name) != nil
}

// longFlagName returns the name of a long flag word ("--name" or "--name=value"),
// or reports that the word is not one ("--" alone, a short flag, an argument).
func longFlagName(w string) (string, bool) {
	if !strings.HasPrefix(w, "--") || w == "--" {
		return "", false
	}
	name := strings.TrimPrefix(w, "--")
	if i := strings.Index(name, "="); i >= 0 {
		name = name[:i]
	}
	return name, name != ""
}

func verifyInvocation(t *testing.T, root *cobra.Command, owner string, words []string) {
	t.Helper()
	target := resolveInvocation(root, words)
	if target == nil {
		t.Errorf("%s: example names a command that does not resolve: olivares %s",
			owner, strings.Join(words, " "))
		return
	}
	for _, w := range words {
		name, ok := longFlagName(w)
		if !ok {
			continue
		}
		if !flagKnownTo(root, target, name) {
			t.Errorf("%s: example passes --%s to %q, which has no such flag: olivares %s",
				owner, name, target.CommandPath(), strings.Join(words, " "))
		}
	}
}

// flagsAfterTerminatorProblems reports each flag the resolved command defines
// that the invocation passes after its -- terminator, where cobra hands it to
// the wrapped command instead of olivares. It returns no problems when the
// invocation carries no terminator or does not resolve (the other tests report
// an unresolvable path).
func flagsAfterTerminatorProblems(root *cobra.Command, owner string, words []string) []string {
	terminator := -1
	for i, w := range words {
		if w == "--" {
			terminator = i
			break
		}
	}
	if terminator < 0 {
		return nil
	}
	target := resolveInvocation(root, words)
	if target == nil {
		return nil
	}
	var problems []string
	for _, w := range words[terminator+1:] {
		name, ok := longFlagName(w)
		if !ok {
			continue
		}
		if flagKnownTo(root, target, name) {
			problems = append(problems, fmt.Sprintf("%s: example passes --%s after the -- terminator, where it goes to the wrapped command instead of olivares; move it before the --: olivares %s",
				owner, name, strings.Join(words, " ")))
		}
	}
	return problems
}

// TestExamplesKeepOlivaresFlagsBeforeTheTerminator is the placement half of
// TestExamplesInvokeRealCommandsAndFlags: a flag that exists is not a flag that
// is heard. After a bare -- terminator cobra hands every word to the wrapped
// command, so an olivares flag written after it is stored as a server argument
// and never takes effect — which is how `mcp add --help` shipped an example
// that failed with the engine's inline-credential refusal (#572).
func TestExamplesKeepOlivaresFlagsBeforeTheTerminator(t *testing.T) {
	root := newRootCmd()
	checked := 0
	walkCommands(root, func(cmd *cobra.Command) {
		for _, line := range strings.Split(cmd.Example, "\n") {
			invocation, ok := invocationFrom(line)
			if !ok || !slices.Contains(invocation, "--") {
				continue
			}
			checked++
			for _, problem := range flagsAfterTerminatorProblems(root, cmd.CommandPath(), invocation) {
				t.Error(problem)
			}
		}
	})
	// A guard on the guard: the check must keep seeing the invocations that
	// carry a -- terminator (the mcp group example, both mcp add examples and
	// the mcp secret example — four today).
	if checked < 4 {
		t.Fatalf("only %d example invocations with a -- terminator extracted; the extractor is not seeing the help text", checked)
	}
	t.Logf("checked %d example invocations with a -- terminator", checked)
}

// TestFlagsBeforeTerminatorCheckFires is the positive control the walk above
// cannot give it: no real example misplaces a flag today, so the detection
// itself is proven on synthetic invocations — the #572 shape, the documented
// order, and a long flag only the wrapped command defines.
func TestFlagsBeforeTerminatorCheckFires(t *testing.T) {
	root := newRootCmd()
	if add, _, _ := root.Find([]string{"mcp", "add"}); add == nil || add.CommandPath() != "olivares mcp add" {
		t.Fatal("mcp add does not resolve in the real tree")
	}
	broken := flagsAfterTerminatorProblems(root, "positive control",
		[]string{"mcp", "add", "github", "--", "github-mcp-server", "stdio", "--secret-env", "GITHUB_TOKEN=store:mcp/github"})
	if len(broken) != 1 || !strings.Contains(broken[0], "--secret-env") {
		t.Fatalf("the misplaced --secret-env after -- was not reported; problems=%q", broken)
	}
	for name, invocation := range map[string][]string{
		"documented order":                {"mcp", "add", "github", "--secret-env", "GITHUB_TOKEN=store:mcp/github", "--", "github-mcp-server", "stdio"},
		"wrapped command's own long flag": {"mcp", "add", "files", "--", "npx", "-y", "server-fs", "--depth", "2"},
	} {
		if problems := flagsAfterTerminatorProblems(root, name, invocation); len(problems) > 0 {
			t.Fatalf("%s was reported as misplaced: %q", name, problems)
		}
	}
}

var sudoPrefix = regexp.MustCompile(`^sudo( -u \S+)? `)

// attributedInvocations returns the `olivares …` invocations in text, joining
// shell continuations and taking the part after a pipe, so a block such as
// `echo x | olivares secrets put …` or a command wrapped over two lines is
// seen whole.
func attributedInvocations(text string) [][]string {
	var out [][]string
	text = strings.ReplaceAll(text, "\\\n", " ")
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if i := strings.LastIndex(s, "| "); i >= 0 {
			s = strings.TrimSpace(s[i+2:])
		}
		s = sudoPrefix.ReplaceAllString(s, "")
		if !strings.HasPrefix(s, "olivares ") {
			continue
		}
		// A redirect or separator ends the invocation; a <placeholder> does not.
		for _, stop := range []string{" > ", " < ", " >> ", ";", "&&"} {
			if i := strings.Index(s, stop); i >= 0 {
				s = s[:i]
			}
		}
		out = append(out, strings.Fields(s)[1:])
	}
	return out
}

// requireAttributionFlags reports an invocation of a command that registers
// addLocalActorFlags without both --actor and --reason: that command refuses to
// run without them, so an example that omits them is a command that fails as
// written. It reports whether the invocation was of such a command, so a caller
// can fail when it recognises none (the --actor usage text is how it recognises
// them, and rewording that text must not silently disable the check).
func requireAttributionFlags(t *testing.T, root *cobra.Command, owner string, words []string) bool {
	t.Helper()
	target := resolveInvocation(root, words)
	if target == nil {
		return false
	}
	probe := &cobra.Command{}
	var actor, reason string
	addLocalActorFlags(probe, &actor, &reason)
	flag := target.Flags().Lookup("actor")
	if flag == nil || flag.Usage != probe.Flags().Lookup("actor").Usage {
		return false // not a privileged offline command
	}
	given := map[string]bool{}
	for _, w := range words {
		if name, ok := strings.CutPrefix(w, "--"); ok {
			given[strings.SplitN(name, "=", 2)[0]] = true
		}
	}
	for _, want := range []string{"actor", "reason"} {
		if !given[want] {
			t.Errorf("%s: %q refuses to run without --%s; the example omits it: olivares %s",
				owner, target.CommandPath(), want, strings.Join(words, " "))
		}
	}
	return true
}

// TestPrivilegedOfflineExamplesCarryAttribution is the other half of
// TestExamplesInvokeRealCommandsAndFlags: a flag that exists is not a flag that
// is enough. --actor and --reason are deliberately not cobra-required (see
// localactor.go), so cobra's own required-flag display cannot flag the gap, and
// the examples of secrets, sources and superadmin drifted from it (#512).
func TestPrivilegedOfflineExamplesCarryAttribution(t *testing.T) {
	root := newRootCmd()
	checked, privileged := 0, 0
	walkCommands(root, func(cmd *cobra.Command) {
		for _, words := range attributedInvocations(cmd.Example) {
			checked++
			if requireAttributionFlags(t, root, cmd.CommandPath()+" Example", words) {
				privileged++
			}
		}
	})
	if checked < 100 {
		t.Fatalf("only %d example invocations extracted; the extractor is not seeing the help text", checked)
	}
	// secrets put/rotate/rm, sources set/rm and superadmin enable/disable.
	if privileged < 7 {
		t.Fatalf("only %d examples of privileged offline commands recognised; the detection no longer sees them", privileged)
	}
}

// TestGovernedRAGNextStepsCarryAttribution covers the same defect in printed
// output: `quickstart governed-rag` tells the operator to run secrets put.
func TestGovernedRAGNextStepsCarryAttribution(t *testing.T) {
	root := newRootCmd()
	var out strings.Builder
	printGovernedRAGNextSteps(&out, quickstartGovernedRAGOptions{
		dataDir:       "/var/lib/olivares",
		credentialRef: "store:s3/prod-runbooks-read",
	}, governedRAGQuickstartPaths{})
	seen := 0
	for _, words := range attributedInvocations(out.String()) {
		if requireAttributionFlags(t, root, "quickstart governed-rag next steps", words) {
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("the next steps print no privileged offline command (secrets put); update this test with them")
	}
}

// TestPrivilegedOfflineDocsCarryAttribution applies the same rule to the fenced
// shell blocks of the docs (every locale of the how-to pages included): a
// documented `secrets put` that cannot run is the defect #512 reported.
func TestPrivilegedOfflineDocsCarryAttribution(t *testing.T) {
	root := newRootCmd()
	privileged := 0
	for _, dir := range []string{"../../docs", "../../docs-site/src/content/docs"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
				return err
			}
			// The generated CLI reference fences each command's bare usage line
			// and lists its flags in a table; it carries no runnable example.
			if filepath.Base(path) == "cli.md" && filepath.Base(filepath.Dir(path)) == "reference" {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			inFence := false
			var block []string
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "```") {
					if inFence {
						for _, words := range attributedInvocations(strings.Join(block, "\n")) {
							if requireAttributionFlags(t, root, path, words) {
								privileged++
							}
						}
						block = nil
					}
					inFence = !inFence
					continue
				}
				if inFence {
					block = append(block, line)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if privileged < 20 {
		t.Fatalf("only %d documented privileged offline commands recognised; the scan no longer sees the docs", privileged)
	}
}
