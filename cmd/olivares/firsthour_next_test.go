// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// A rule adopted from the reference CLIs: every first-hour command's help
// ends with the next command in the path.

// TestEveryFirstHourNextCommandNamesARealCommand is the gate. It walks the REAL
// cobra tree, so a command that is renamed or moved under another parent fails
// here instead of silently losing its help section.
func TestEveryFirstHourNextCommandNamesARealCommand(t *testing.T) {
	root := newRootCmd()
	if missing := applyFirstHourNextCommands(root); len(missing) != 0 {
		t.Fatalf("the first-hour path names commands this binary does not have: %v", missing)
	}

	// And the other direction: the command each one POINTS AT must resolve too.
	// A next step that cannot be run is worse than none — the operator types it
	// and gets "unknown command".
	for from, next := range firstHourNextCommands {
		args := commandWords(next)
		if len(args) == 0 {
			t.Fatalf("%q has an empty next command", from)
		}
		target, _, err := root.Find(args)
		if err != nil {
			t.Errorf("%q points at %q, which does not resolve: %v", from, next, err)
			continue
		}
		if got := commandPathWithoutBinary(target); got != strings.Join(args, " ") {
			t.Errorf("%q points at %q, which resolves to %q — cobra fell back to a parent",
				from, next, got)
		}
	}
}

// commandWords is the literal command words of a next step, stopping at the
// first flag or <placeholder>. Those are for the operator to fill in; only the
// verbs are looked up.
func commandWords(next string) []string {
	var words []string
	for _, w := range strings.Fields(next) {
		if w == "olivares" {
			continue
		}
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") {
			break
		}
		words = append(words, w)
	}
	return words
}

// TestFirstHourHelpEndsWithTheNextCommand drives help the way an operator does.
func TestFirstHourHelpEndsWithTheNextCommand(t *testing.T) {
	for _, tc := range []struct{ path, next string }{
		{"quickstart", "olivares first-boot"},
		{"first-boot", "olivares doctor"},
		{"doctor", "olivares agent tool detect"},
		{"agent deploy", "olivares agent session create --provider-profile <ref>"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got := helpFor(t, strings.Fields(tc.path)...)
			if !strings.Contains(got, "Next:") {
				t.Fatalf("no next-step section:\n%s", got)
			}
			if !strings.Contains(got, tc.next) {
				t.Fatalf("want the next command %q:\n%s", tc.next, got)
			}
			// It is the LAST thing an operator reads, which is the whole point.
			if tail := strings.TrimSpace(got); !strings.HasSuffix(tail, tc.next) {
				t.Fatalf("the next command is not the last line:\n%s", got)
			}
		})
	}
}

// TestExemptCommandsGetNoNextSection: the section is a claim, not decoration. The
// verbs on the exempt list say nothing rather than inventing one — and the list is
// a map with a reason per entry, so "nothing follows this" and "nobody got to
// this" stop looking identical.
func TestExemptCommandsGetNoNextSection(t *testing.T) {
	for _, path := range []string{"completion", "uninstall", "openapi", "claude-hook"} {
		if _, ok := nextCommandExempt[path]; !ok {
			t.Fatalf("%s is no longer exempt; this case is asserting the wrong thing", path)
		}
		if got := helpFor(t, strings.Fields(path)...); strings.Contains(got, "Next:") {
			t.Errorf("%s invented a next step:\n%s", path, got)
		}
	}
}

// TestEveryTopLevelVerbNamesItsNextCommandOrDeclaresWhyNot walks the REAL cobra
// tree, which is the only enumeration that cannot go stale as commands are added.
//
// MEASURED 2026-09-18: nine commands on the first-hour path named their next
// command and the other sixty-odd top-level verbs ended their help with examples
// of themselves. That is the same defect the first-hour table was written for,
// still true everywhere the first hour does not reach — and the reason it stayed
// true is that nothing enumerated it.
//
// A verb passes by being in a table or by being on the exempt list WITH a reason.
// Silence fails, which is the point: a new top-level verb has to make the choice.
func TestEveryTopLevelVerbNamesItsNextCommandOrDeclaresWhyNot(t *testing.T) {
	root := newRootCmd()
	if missing := installFirstHourHelp(root); len(missing) > 0 {
		t.Fatalf("the next-command tables name paths this binary does not have: %v", missing)
	}
	seen := 0
	for _, cmd := range root.Commands() {
		if cmd.Hidden || !cmd.IsAvailableCommand() {
			continue
		}
		seen++
		name := cmd.Name()
		if next := nextCommandFor(cmd); next != "" {
			continue
		}
		reason, ok := nextCommandExempt[name]
		if !ok {
			t.Errorf("top-level verb %q names no next command and is not on the exempt list. "+
				"Add it to topLevelNextCommands, or to nextCommandExempt with the reason.", name)
			continue
		}
		if len(strings.TrimSpace(reason)) < 20 {
			t.Errorf("top-level verb %q is exempt with a reason too short to be one: %q", name, reason)
		}
	}
	// A floor, for the same reason every scan in this tree has one: a walk that
	// stopped seeing the tree would report zero failures and look like success.
	if seen < 50 {
		t.Fatalf("the walk saw %d top-level verbs and this binary has dozens; "+
			"a walk that cannot see the tree cannot declare it clean", seen)
	}
}

// TestEveryNextCommandResolvesInThisBinary is the half that stops a next command
// from naming a verb that does not exist.
//
// A line of help prose cannot be checked. An annotation can: every value in both
// tables is stripped of its binary name, its flags and its <placeholders>, and
// what is left has to resolve against the real tree. A renamed verb turns into a
// red test here instead of into a printed instruction that fails for the operator.
func TestEveryNextCommandResolvesInThisBinary(t *testing.T) {
	root := newRootCmd()
	installFirstHourHelp(root)
	checked := 0
	for _, table := range []map[string]string{firstHourNextCommands, topLevelNextCommands} {
		for from, next := range table {
			words := commandWordsOf(next)
			if len(words) == 0 {
				t.Errorf("%s: next command %q names nothing to run", from, next)
				continue
			}
			target, _, err := root.Find(words)
			if err != nil {
				t.Errorf("%s: next command %q does not resolve: %v", from, next, err)
				continue
			}
			got := commandPathWithoutBinary(target)
			gotWords := strings.Fields(got)
			if len(gotWords) > len(words) || strings.Join(words[:min(len(gotWords), len(words))], " ") != got {
				t.Errorf("%s: next command %q resolved to %q, so it names a verb this binary "+
					"does not have and cobra fell back to the nearest parent", from, next, got)
				continue
			}
			// The words cobra did NOT consume are either arguments the command
			// takes — `olivares work list items` — or a verb this binary does not
			// have, which is the whole failure this test exists for. A command
			// with subcommands answers that itself: an unconsumed word there was
			// meant to be one of them.
			if leftover := words[len(gotWords):]; len(leftover) > 0 {
				if target.HasSubCommands() {
					t.Errorf("%s: next command %q names %q, which is not a subcommand of %q",
						from, next, strings.Join(leftover, " "), got)
				} else if err := target.ValidateArgs(leftover); err != nil {
					t.Errorf("%s: next command %q: %q does not accept %q: %v",
						from, next, got, strings.Join(leftover, " "), err)
				}
			}
			checked++
		}
	}
	if checked < 60 {
		t.Fatalf("only %d next commands were resolved; the tables hold more than that, so "+
			"something stopped the walk", checked)
	}
}

// commandWordsOf strips a printed next command down to the words before its first
// flag or <placeholder>: the binary name comes off, and what is left is the verbs
// plus any literal positional argument the line prints (`work list items`). The
// caller separates the two by asking cobra which words it consumed.
func commandWordsOf(next string) []string {
	var words []string
	for i, w := range strings.Fields(next) {
		if i == 0 && w == "olivares" {
			continue
		}
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") {
			break
		}
		words = append(words, w)
	}
	return words
}

func helpFor(t *testing.T, path ...string) string {
	t.Helper()
	root := newRootCmd()
	installFirstHourHelp(root)
	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Help(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestEveryNextCommandIsRunnableAsPrinted is the half TestEveryNextCommandResolves
// cannot see, and the defect measured on 2026-09-18: four of the 63 next commands
// RESOLVED and still could not be run as printed.
//
//	olivares claude-agents sessions events  exit 2  accepts 1 arg(s), received 0
//	olivares claude-policy distribution     exit 2  accepts 1 arg(s), received 0
//	olivares ddil verify                    exit 2  required flag(s) "bundle", "pubkey" not set
//	olivares work list                      exit 2  list requires exactly one of items|decisions|leases
//
// A resolution test cannot see that, because the path resolves. What decides it is
// cobra's own admission check: parse the flags the line prints, then ask the
// command whether its REQUIRED flags are set and whether it accepts the positional
// arguments that are left. That is the same check the binary runs before it does
// any work, so a line that passes here is a line an operator can paste.
//
// A <placeholder> is filled in by the operator, so it stands for a real argument
// here — and the test below proves the placeholders are load-bearing rather than
// decoration.
func TestEveryNextCommandIsRunnableAsPrinted(t *testing.T) {
	checked := 0
	for _, table := range []struct {
		name    string
		entries map[string]string
		// readOnly is the extra rule topLevelNextCommands carries and the
		// first-hour path does not: that table's entries are the READ verb of a
		// group, so none of them may be a write verb. The first-hour path is an
		// ORDERED WALK and half of it changes something on purpose — `agent tool
		// install`, `agent deploy`, `agent session create` — so applying the same
		// rule there would ask the first hour to stop installing anything.
		readOnly bool
	}{
		{name: "firstHourNextCommands", entries: firstHourNextCommands},
		{name: "topLevelNextCommands", entries: topLevelNextCommands, readOnly: true},
	} {
		for from, next := range table.entries {
			target, err := admits(next)
			if err != nil {
				t.Errorf("%s[%s]: next command %q cannot be run as printed: %v",
					table.name, from, next, err)
				continue
			}
			if table.readOnly {
				if reason, isWrite := nextCommandWriteVerbs[target.Name()]; isWrite {
					t.Errorf("%s[%s]: next command %q resolves to the write verb %q (%s). "+
						"This table names the READ verb an operator runs to SEE what is there; "+
						"pointing at a write verb teaches them to change something before they "+
						"have looked.", table.name, from, next, target.Name(), reason)
				}
			}
			checked++
		}
	}
	if checked < 60 {
		t.Fatalf("only %d next commands were admitted; the tables hold more than that, so "+
			"something stopped the walk", checked)
	}
}

// nextCommandWriteVerbs is topLevelNextCommands' own rule made enumerable: the
// verbs that CHANGE something, each with what it changes.
//
// It is a closed list and not a heuristic. "A verb that is not obviously a read" is
// a judgement a test cannot make — `audit verify`, `db check`, `evals gate`,
// `config validate` and `support bundle` all sound like work and all of them only
// look — so the list names what a verb DOES, and a verb this binary grows later
// that is not on it is read by default. The failure that follows from getting it
// wrong is one-directional and cheap: a missing entry lets one write verb through,
// and a wrong entry is a red test with the verb named in it.
var nextCommandWriteVerbs = map[string]string{
	"add":       "creates a record",
	"apply":     "writes the state it was given",
	"create":    "creates a record",
	"delete":    "destroys a record",
	"deploy":    "starts something running",
	"disable":   "changes a policy",
	"enable":    "changes a policy",
	"import":    "writes records from a document",
	"ingest":    "writes samples",
	"init":      "writes an installation",
	"install":   "writes to this host",
	"purge":     "destroys records",
	"restart":   "interrupts a running service",
	"revoke":    "destroys a credential",
	"rm":        "destroys a record",
	"rotate":    "replaces a credential",
	"set":       "overwrites a value",
	"start":     "starts something running",
	"stop":      "stops a running service",
	"uninstall": "removes this binary",
	"update":    "overwrites a record",
	"upload":    "sends a document to the control plane",
}

// admits reports whether a printed command line would get past cobra's admission
// checks in this binary — it resolves, its flags parse, its required flags are set
// and its positional arguments are accepted — AND whether what it resolved to is a
// command at all. It returns the resolved command so the caller can judge the verb.
//
// THE SECOND HALF IS NOT COBRA'S, and it is the defect measured on 2026-09-19.
// Cobra admits a CONTAINER: `olivares finops cost` resolved, took no
// arguments, needed no flags, and run as printed it printed help and exited 0.
// Every one of cobra's checks passed and nothing happened.
//
// AND "HAS NO RunE" IS NOT THE PREDICATE IN THIS BINARY, which is worth writing
// down because it is the obvious test and it is VACUOUS HERE: measured while this
// test was being written, `olivares finops cost` reports RunE != nil, and so does
// `finops`, and so does the root. enforceSubcommandContract installs a stub RunE on
// every group in the tree — it has to, because cobra never validates the arguments
// of a command it considers non-runnable (subcommand_contract.go:24-51). The stub
// carries groupStubAnnotation exactly so that a reader can tell it from a verb, and
// isGroupStub is that reader. A no-RunE check would have passed on the very line
// this test exists to catch.
//
// It builds its OWN root every time. ParseFlags marks flags Changed on the command
// it parsed, and a shared tree would let one line's flags satisfy another's.
func admits(next string) (*cobra.Command, error) {
	root := newRootCmd()
	installFirstHourHelp(root)
	args := strings.Fields(next)
	if len(args) == 0 || args[0] != "olivares" {
		return nil, fmt.Errorf("a next command is printed as `olivares …`, this one is %q", next)
	}
	args = args[1:]
	target, rest, err := root.Find(args)
	if err != nil {
		return nil, err
	}
	if err := target.ParseFlags(rest); err != nil {
		return target, err
	}
	if err := target.ValidateRequiredFlags(); err != nil {
		return target, err
	}
	if err := target.ValidateFlagGroups(); err != nil {
		return target, err
	}
	if err := target.ValidateArgs(target.Flags().Args()); err != nil {
		return target, err
	}
	if isGroupStub(target) || (target.Run == nil && target.RunE == nil) {
		return target, fmt.Errorf("%q is a container of %d subcommands, not a command: run as "+
			"printed it prints help and exits 0, which an operator reads as success",
			commandPathWithoutBinary(target), len(target.Commands()))
	}
	return target, nil
}

// TestNextCommandPlaceholdersAreLoadBearing is the control on the test above. A
// placeholder or a flag that could be deleted without the line failing is
// decoration, and a test that admitted the line either way would prove nothing.
//
// Every next command that carries a <placeholder> or a flag is stripped down to its
// verbs alone; at least one of those stripped forms MUST be refused, or this file
// is asserting a property the tree does not have. The four measured on 2026-09-18
// are named explicitly, because those are the ones whose repair this test exists
// to hold.
func TestNextCommandPlaceholdersAreLoadBearing(t *testing.T) {
	// The `stripped` column is, literally, the four lines that were run offline.
	for _, tc := range []struct{ printed, stripped string }{
		{"olivares claude-agents sessions events <session-id>", "olivares claude-agents sessions events"},
		{"olivares claude-policy distribution <surface>", "olivares claude-policy distribution"},
		{"olivares ddil verify --bundle <bundle> --pubkey <pubkey>", "olivares ddil verify"},
		{"olivares work list items", "olivares work list"},
		// Added by the container repair of 2026-09-19: `knowledge documents` is the
		// only one of the thirteen whose group has no list verb, so its read verb
		// takes an argument and the line has to print a placeholder for it.
		{"olivares knowledge documents get <document-id>", "olivares knowledge documents get"},
	} {
		t.Run(tc.printed, func(t *testing.T) {
			if _, err := admits(tc.printed); err != nil {
				t.Fatalf("the repaired line is still refused: %v", err)
			}
			if _, err := admits(tc.stripped); err == nil {
				t.Errorf("%q is admitted with nothing filled in, so what %q adds is decoration",
					tc.stripped, tc.printed)
			}
		})
	}

	// And the four are really in the tables, so this test cannot pass by asserting
	// something about lines nobody prints.
	for path, want := range map[string]string{
		"claude-agents": "olivares claude-agents sessions events <session-id>",
		"claude-policy": "olivares claude-policy distribution <surface>",
		"ddil":          "olivares ddil verify --bundle <bundle> --pubkey <pubkey>",
		"work":          "olivares work list items",
		"knowledge":     "olivares knowledge documents get <document-id>",
	} {
		if got := topLevelNextCommands[path]; got != want {
			t.Errorf("topLevelNextCommands[%q] = %q, want %q", path, got, want)
		}
	}
}

// TestFirstHourHelpSurvivesConcurrentRootBuilds builds the root command on
// several goroutines at once and renders help on each, which is what the parallel
// tests of this package do on every run.
//
// MEASURED 2026-09-25: installFirstHourHelp registered its template function with
// cobra.AddTemplateFunc, so every root command wrote cobra's package-level
// template map, unlocked, and every help render read it. A pull-request run
// aborted the whole test binary with "fatal error: concurrent map writes" from
// two parallel tests that each built a root command. The registration now runs
// once, in init.
//
// It has two phases because each one reaches the defect a different way. The
// first is the path that failed: newRootCmd on every goroutine at once, then a
// help render. The second calls installFirstHourHelp again on every root after
// one barrier, as helpFor and admits do. Nothing orders the goroutines between
// that barrier and the call, so under -race a write to a process-wide map inside
// it is reported on any schedule, not only when two goroutines happen to collide.
func TestFirstHourHelpSurvivesConcurrentRootBuilds(t *testing.T) {
	t.Parallel()
	cases := []struct{ path, next string }{
		{"quickstart", "olivares first-boot"},
		{"first-boot", "olivares doctor"},
		{"doctor", "olivares agent tool detect"},
		{"agent deploy", "olivares agent session create --provider-profile <ref>"},
	}
	const builders = 8
	roots := make([]*cobra.Command, builders)

	// together releases every goroutine at the same instant and waits for all of
	// them, so the calls inside step overlap instead of running one after another.
	together := func(step func(i int)) {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range builders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				step(i)
			}()
		}
		close(start)
		wg.Wait()
	}
	// helpEndsWithNext renders help the way an operator reads it. It reports with
	// t.Errorf, which is safe from any goroutine; t.Fatal is not.
	helpEndsWithNext := func(phase string, i int) {
		tc := cases[i%len(cases)]
		cmd, _, err := roots[i].Find(strings.Fields(tc.path))
		if err != nil {
			t.Errorf("%s, builder %d: find %q: %v", phase, i, tc.path, err)
			return
		}
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Help(); err != nil {
			t.Errorf("%s, builder %d: help for %q: %v", phase, i, tc.path, err)
			return
		}
		got := out.String()
		if !strings.Contains(got, "Next:") || !strings.HasSuffix(strings.TrimSpace(got), tc.next) {
			t.Errorf("%s, builder %d: help for %q does not end with the next command %q:\n%s",
				phase, i, tc.path, tc.next, got)
		}
	}

	together(func(i int) {
		roots[i] = newRootCmd()
		helpEndsWithNext("concurrent newRootCmd", i)
	})
	together(func(i int) {
		if missing := installFirstHourHelp(roots[i]); len(missing) > 0 {
			t.Errorf("concurrent installFirstHourHelp, builder %d: the tables name paths "+
				"this binary does not have: %v", i, missing)
		}
		helpEndsWithNext("concurrent installFirstHourHelp", i)
	})
}
