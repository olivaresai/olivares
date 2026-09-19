// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestNoFlagUsageHijacksItsMetavariable forbids the class of help defect the
// first-hour walk measured, and it forbids it for the whole tree, not for the
// three flags that happened to be on the walk's path.
//
// THE MECHANISM, because a gate whose reason is not written becomes a gate
// somebody deletes. pflag.UnquoteUsage reads a BACK-QUOTE PAIR in a usage string
// as the flag's metavariable: it strips the back quotes and prints the text
// between them where the type would go. A usage string that quotes a command
// name the way this repository's prose does therefore renders a lie:
//
//	--quiet olivares status     (a BOOLEAN flag, from "…and `olivares status` reports…")
//	--probe --version           (a BOOLEAN flag, from "run `--version` for…")
//	--out -                     (a STRING flag, from "`-` means stdout")
//	--probe-path --version      (a stringArray, from "run `--version` on")
//
// MEASURED 2026-09-18 over the real tree: 833 command nodes, 25 flag occurrences
// carrying a back-quote pair, 17 distinct flags, FOUR of them booleans rendered
// as if they took an argument, three of them on the first-hour path.
//
// render.go has documented this trap since the sol-max contrast found it in ONE
// flag ("NO BACKTICKS. pflag reads a backtick PAIR…"). A comment beside one
// declaration protects that declaration. This walks every flag of every command,
// through the same runtime enumeration TestCLIRefDump uses, so a command added
// tomorrow is covered without anyone remembering it exists.
//
// The remedy is one character: write 'olivares status', not `olivares status`.
// If a flag ever genuinely needs a custom metavariable, pflag's own contract is
// that it comes from the back quotes — so declare it deliberately and add the
// flag to allowedCustomMetavariables below, with the reason.
func TestNoFlagUsageHijacksItsMetavariable(t *testing.T) {
	// allowedCustomMetavariables is EMPTY today, deliberately. An entry means "this
	// flag really does want pflag to print this word instead of its type", and it
	// must carry the reason beside it.
	allowedCustomMetavariables := map[string]string{}

	type finding struct{ path, flag, metavar, kind string }
	var found []finding
	seen := map[string]bool{}

	walk(t, newRootCmd(), func(path string, cmd *cobra.Command) {
		visit := func(f *pflag.Flag) {
			name, _ := pflag.UnquoteUsage(f)
			// UnquoteUsage returns the flag's TYPE when there are no back quotes,
			// and the quoted text when there are. The quoted text is exactly the
			// span between the back quotes, so the test for "hijacked" is: the
			// usage string contains a back-quote pair.
			if !strings.Contains(f.Usage, "`") {
				return
			}
			if _, ok := allowedCustomMetavariables[f.Name]; ok {
				return
			}
			key := f.Name + "\x00" + f.Usage
			if seen[key] {
				return
			}
			seen[key] = true
			found = append(found, finding{path: path, flag: f.Name, metavar: name, kind: f.Value.Type()})
		}
		cmd.LocalFlags().VisitAll(visit)
		cmd.PersistentFlags().VisitAll(visit)
	})

	if len(found) == 0 {
		return
	}
	sort.Slice(found, func(i, j int) bool { return found[i].flag < found[j].flag })
	var b strings.Builder
	fmt.Fprintf(&b, "%d flag usage string(s) hijack their own metavariable.\n", len(found))
	b.WriteString("pflag prints the text between a back-quote PAIR where the flag's type belongs,\n")
	b.WriteString("so --help reports something the flag does not accept. Use 'single quotes'.\n\n")
	for _, f := range found {
		fmt.Fprintf(&b, "  %s --%s (%s) renders as: --%s %s\n", f.path, f.flag, f.kind, f.flag, f.metavar)
	}
	t.Fatal(b.String())
}

// walk visits every command in the tree once, depth first, with its full path.
func walk(t *testing.T, root *cobra.Command, fn func(path string, cmd *cobra.Command)) {
	t.Helper()
	var rec func(cmd *cobra.Command, prefix string)
	rec = func(cmd *cobra.Command, prefix string) {
		path := strings.TrimSpace(prefix + " " + cmd.Name())
		fn(path, cmd)
		for _, child := range cmd.Commands() {
			rec(child, path)
		}
	}
	rec(root, "")
}

// TestFlagMetavariableGateSeesABackQuotePair is the gate's own control positive:
// without it, a gate that stopped enumerating would report a clean tree.
func TestFlagMetavariableGateSeesABackQuotePair(t *testing.T) {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	var b bool
	fs.BoolVar(&b, "quiet", false, "hold the checks back (and `olivares status` reports the same posture)")
	name, _ := pflag.UnquoteUsage(fs.Lookup("quiet"))
	if name != "olivares status" {
		t.Fatalf("pflag no longer hijacks a back-quote pair (got metavariable %q); this gate's premise is gone and it must be re-derived, not deleted", name)
	}
	fs2 := pflag.NewFlagSet("clean", pflag.ContinueOnError)
	fs2.BoolVar(&b, "quiet", false, "hold the checks back (and 'olivares status' reports the same posture)")
	if name, _ := pflag.UnquoteUsage(fs2.Lookup("quiet")); name != "" {
		t.Fatalf("a boolean flag with no back quotes must have an empty metavariable, got %q", name)
	}
}
