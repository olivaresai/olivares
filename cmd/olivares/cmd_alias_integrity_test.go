// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// AN ALIAS THAT SAYS NOTHING NEW IS A DELETED ALIAS, AND NOTHING WAS MEASURING THAT.
//
// `finops` carried the British spelling of its US-named cost-centers command as an alias (see
// costCentersUKAlias below), registered so an operator or a runbook typing it still resolves.
// A mechanical spelling pass rewrote the ALIAS to the US spelling as well, leaving
// `Use: "cost-centers"` beside an identical alias. Cobra does not complain: an alias equal to
// the command name registers nothing, so the invocation the alias existed for stopped working
// with no error, no test and no gate — the generated CLI reference regenerated happily,
// because the page is derived from the binary and the binary now said the alias was the US
// spelling. A page derived from the binary cannot see a semantic change IN the binary.
//
// This is the CLASS, not the instance: the tree carries 114 aliases across 700 commands, any
// of which the same edit could empty. So the assertions below walk the whole tree.
//
// The two properties asserted here, each with a different way of being wrong:
//
//   - an alias must not equal its OWN command's name (the defect above);
//   - an alias must not equal a SIBLING's name, which is worse than useless — cobra resolves
//     by name first, so the alias is unreachable and reads as though it worked.
//
// A THIRD property is deliberately NOT asserted, and is written down here rather than left
// for the next reader to rediscover, because it is a live ambiguity this lane does not own:
// two siblings may currently claim the SAME alias, and which one answers is registration
// order. `simpleListCmd` (cmd_compliance.go:1632-1644) hardcodes `Aliases: ["list"]` for the
// noun command it builds, which is right when a parent has one such child and ambiguous when
// it has four. Measured on this tree: `compliance depth` has `us-law`, `sector`, `snapshots`
// and `drift` all claiming `list`, so `olivares compliance depth list` runs whichever was
// registered first (`us-law`), and `compliance dora` has the same clash between `incidents`
// and `registers`. Asserting it would make this file red over a decision about someone
// else's command surface — which of the four `list` means — so it is REPORTED, not enforced.
//
// Hidden commands are walked too. A hidden command is still typed by whoever knows it, and
// `olivares commands` exists precisely because a `--help` walk does not see the whole tree.

// walkCommandTree visits c and every descendant, hidden ones included.
func walkCommandTree(c *cobra.Command, visit func(*cobra.Command)) {
	visit(c)
	for _, child := range c.Commands() {
		walkCommandTree(child, visit)
	}
}

func TestNoAliasDuplicatesItsOwnCommandName(t *testing.T) {
	root := newRootCmd()
	checked, aliases := 0, 0
	walkCommandTree(root, func(c *cobra.Command) {
		checked++
		for _, a := range c.Aliases {
			aliases++
			if a == c.Name() {
				t.Errorf("%q lists %q as an alias of ITSELF: cobra registers nothing for it, so whatever spelling the alias used to accept no longer resolves and no error says so",
					c.CommandPath(), a)
			}
			if strings.TrimSpace(a) == "" {
				t.Errorf("%q has a blank alias, which can never be typed", c.CommandPath())
			}
		}
	})
	// The walk itself has to be shown to have happened, or an empty tree passes every
	// assertion above. These are floors, not exact counts: the tree grows.
	if checked < 400 {
		t.Fatalf("the walk saw %d commands, far fewer than this binary has — it did not traverse the tree, so nothing above was checked", checked)
	}
	if aliases < 50 {
		t.Fatalf("the walk saw %d aliases; the tree carries far more, so this measured almost nothing", aliases)
	}
	t.Logf("walked %d commands and %d aliases", checked, aliases)
}

func TestNoAliasCollidesWithASiblingName(t *testing.T) {
	root := newRootCmd()
	pairs := 0
	walkCommandTree(root, func(parent *cobra.Command) {
		names := map[string]string{} // sibling name -> path
		children := parent.Commands()
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, c := range children {
			names[c.Name()] = c.CommandPath()
		}
		for _, c := range children {
			for _, a := range c.Aliases {
				if a == c.Name() {
					continue // reported by the test above; not double-counted here
				}
				pairs++
				if owner, ok := names[a]; ok {
					t.Errorf("%q claims the alias %q, which is already the NAME of %q: cobra resolves the name first, so the alias is unreachable while reading as though it worked",
						c.CommandPath(), a, owner)
				}
			}
		}
	})
	if pairs < 50 {
		t.Fatalf("only %d alias/sibling pairs were compared; the walk did not reach the tree and nothing above was checked", pairs)
	}
}

// costCentersUKAlias is the invocation under test, named once. It is a literal the US-locale
// spell linter flags and must: the linter cannot tell an accepted invocation from prose, and
// the tree's mechanism for that is `.golangci.yml`'s ignore-rules (`mitre`, `mosquitto`),
// which is the integration lane's call rather than this lane's. Naming it once keeps the count of
// unavoidable findings at one instead of four.
const costCentersUKAlias = "cost-centres"

// TestTheBritishSpellingOfCostCentresStillResolves is the instance witness beside the class
// one: it asserts the concrete invocation, because "no alias duplicates its name" would also
// be satisfied by deleting the alias outright.
func TestTheBritishSpellingOfCostCentresStillResolves(t *testing.T) {
	root := newRootCmd()

	// FIRING DIRECTION: the British spelling resolves, and to the right command.
	got, _, err := root.Find([]string{"finops", costCentersUKAlias, "ls"})
	if err != nil {
		t.Fatalf("`olivares finops %s ls` does not resolve: %v", costCentersUKAlias, err)
	}
	if got.Parent() == nil || got.Parent().Name() != "cost-centers" {
		t.Fatalf("`%s ls` resolved to %q, whose parent is not the cost-centers command", costCentersUKAlias, got.CommandPath())
	}

	// NOT-FIRING DIRECTION: a spelling nobody registered must NOT resolve. Without this,
	// a `Find` that fell back to the parent on any unknown word would satisfy the above.
	if c, _, err := root.Find([]string{"finops", costCentersUKAlias + "z", "ls"}); err == nil && c.Name() == "ls" {
		t.Fatal("an unregistered spelling resolved to the same command, so Find is not discriminating and the assertion above proves nothing")
	}
}

// THIS TREE HAS ONE NAME FOR LISTING, AND THREE COMMANDS DID NOT KNOW IT.
//
// Measured on 2026-08-21 over the built command tree: the listing verb is spelled `ls` almost
// everywhere, and most of those carry `list` as an alias so the other habit still resolves.
// Three commands were named `list` and carried NO alias, so `ls` — the spelling the rest of the
// binary teaches — answered `unknown command` on exactly those three:
//
//	olivares work ls                            unknown command "ls" for "olivares work"
//	olivares work protocol-binding spec ls       unknown command "ls" for "... spec"
//	olivares work protocol-binding binding ls    unknown command "ls" for "... binding"
//
// That is not a missing feature, it is an inconsistency the operator pays for: nothing in the
// help tells you which of the two names this particular subtree wants, so you find out by
// getting an error. The three now carry `ls`, and `list` keeps working — an alias is added,
// nothing is renamed, so no script breaks.
//
// A SECOND ASYMMETRY IS REPORTED AND NOT ASSERTED, following this file's habit of writing down
// what it declines to enforce: some `ls`-named commands carry no `list` alias. That direction is
// weaker — the canonical spelling already resolves there — and enforcing it would edit dozens of
// commands this lane does not own. The counts are logged by the test below so the next reader
// sees today's number instead of this comment's.
func TestEveryListCommandAlsoAnswersToLs(t *testing.T) {
	root := newRootCmd()
	namedList, namedLs, lsAlsoAnswersList := 0, 0, 0
	walkCommandTree(root, func(c *cobra.Command) {
		switch c.Name() {
		case "list":
			namedList++
			if !slices.Contains(c.Aliases, "ls") {
				t.Errorf("%q is named `list` and does not answer to `ls`, which is how the rest of this binary spells listing: an operator who learned `ls` everywhere else gets `unknown command` here and nothing told them why",
					c.CommandPath())
			}
		case "ls":
			namedLs++
			if slices.Contains(c.Aliases, "list") {
				lsAlsoAnswersList++
			}
		}
	})
	// Floors, not exact counts. Without them an empty walk satisfies every assertion above,
	// which is the failure mode this file already guards against twice.
	if namedLs < 50 {
		t.Fatalf("the walk found %d commands named `ls`; this binary has far more, so the walk did not happen and nothing above was checked", namedLs)
	}
	if namedList == 0 {
		t.Fatalf("the walk found no command named `list`, so the assertion above never ran on anything")
	}
	t.Logf("listing verbs: %d named `ls` (%d of them also answer `list`), %d named `list`", namedLs, lsAlsoAnswersList, namedList)
}

// TestWorkLsResolvesToWorkList is the instance witness beside the class one, for the same reason
// the cost-centers pair exists: "every `list` carries `ls`" is also satisfied by deleting the
// three `list` commands, and this pins the invocation the backlog row actually named.
func TestWorkLsResolvesToWorkList(t *testing.T) {
	root := newRootCmd()

	// FIRING DIRECTION: `ls` resolves, and to the listing command rather than to its parent.
	got, _, err := root.Find([]string{"work", "ls"})
	if err != nil {
		t.Fatalf("`olivares work ls` does not resolve: %v", err)
	}
	if got.Name() != "list" {
		t.Fatalf("`olivares work ls` resolved to %q, not to `work list`", got.CommandPath())
	}

	// NOT-FIRING DIRECTION, and it is the one that matters here: cobra's Find returns the
	// deepest command it matched, so an unknown word yields the PARENT with no error. Without
	// this case the assertion above would pass against a tree with no alias at all.
	if c, _, err := root.Find([]string{"work", "lsz"}); err == nil && c.Name() == "list" {
		t.Fatal("an unregistered spelling also resolved to `work list`, so Find is not discriminating and the assertion above proves nothing")
	}
}
