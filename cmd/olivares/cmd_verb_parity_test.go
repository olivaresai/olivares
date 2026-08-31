// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// verbParityDoc carries the reasoning behind every row below. The failure messages
// point at it so the next person meets the decision, not just the assertion.
const verbParityDoc = "docs/CLI-VERB-PARITY.md"

// absentByDecision are verbs a CRUD census reports as missing and that are missing
// ON PURPOSE. Each row is a decision this repository has taken, not an omission
// waiting to be corrected.
//
// WHAT THIS TEST IS FOR, because a test that asserts an absence looks strange at
// first: a deliberate gap that is only written in a document is indistinguishable
// from an oversight to the next person holding the same census — and that person
// re-proposes it, re-measures it, and burns the afternoon that this file exists to
// give back. Adding one of these verbs must therefore be a decision taken twice:
// once in the code, once by deleting the row here and reading why it was there.
//
// It is NOT a claim that the capability is absent. `secrets` DOES have a store-level
// Get (cmd_secrets.go:179 calls it); `dr` DOES have an offsite Delete
// (core/dr/offsite.go:185). What is deliberate is that neither is exposed as an
// operator verb, and the reasons are in the doc.
var absentByDecision = []struct {
	parent string // full command path of the group, exactly as cobra prints it
	verb   string // the verb that must NOT exist under it
	why    string
}{
	{"olivares secrets", "get", "a get would print a sealed credential; `ls` gives names and hints, never values"},
	{"olivares dr", "rm", "the offsite mirror is the last copy: deletion is GFS-retention-driven only"},
	{"olivares compliance holds", "rm", "a hold is RELEASED, never deleted — the record is the evidence"},
	{"olivares compliance erasure", "rm", "an erasure request is the proof a GDPR duty was answered"},
	{"olivares work", "rm", "durable work is append-only: item.cancel / item.archive / decision.revoke"},
}

// domainVerbCreates are the verbs a census misses because the domain calls the
// create something else. Pinned so a rename cannot silently make the parity doc
// wrong — and so the "not missing" verdicts stay true rather than merely written.
var domainVerbCreates = []struct {
	path string
	why  string
}{
	{"olivares secrets put", "create-or-update for a sealed secret"},
	{"olivares sources set", "create-or-update for a roster source"},
	{"olivares mcp pins approve", "approving a fingerprint is what creates the pin"},
	{"olivares compliance holds place", "placing the hold is the create"},
	{"olivares compliance erasure request", "registering the request is the create"},
	{"olivares dr push", "uploading a bundle is the offsite create"},
	{"olivares agent workspace add", "registering the directory is the create"},
	{"olivares work apply", "every work mutation crosses validate/plan/apply"},
}

func TestVerbsAbsentByDecisionStayAbsent(t *testing.T) {
	root := newRootCmd()
	for _, row := range absentByDecision {
		// POSITIVE CONTROL FIRST. Without it this whole test is vacuous: a renamed
		// or mistyped parent makes "the verb is not under it" trivially true, and
		// the suite would report green while checking nothing at all.
		parent := resolveCommandPath(t, root, row.parent)
		if parent == nil {
			continue
		}
		if !parent.HasSubCommands() {
			t.Errorf("%s has no subcommands at all — this row can no longer prove anything about %q (see %s)",
				row.parent, row.verb, verbParityDoc)
			continue
		}
		for _, child := range parent.Commands() {
			names := append([]string{child.Name()}, child.Aliases...)
			for _, name := range names {
				if name != row.verb {
					continue
				}
				t.Errorf("%s %s now exists, and its absence was a DECISION: %s.\n"+
					"If that decision has changed, change it deliberately: delete this row and update %s in the same commit.",
					row.parent, row.verb, row.why, verbParityDoc)
			}
		}
	}
}

func TestDomainVerbCreatesStillExist(t *testing.T) {
	root := newRootCmd()
	for _, row := range domainVerbCreates {
		if cmd := resolveCommandPath(t, root, row.path); cmd == nil {
			t.Errorf("%s no longer resolves; %s records it as the create verb (%s) and would now be wrong",
				row.path, verbParityDoc, row.why)
		}
	}
}

// resolveCommandPath resolves a full "olivares a b c" path to its command, or fails
// the test. It compares CommandPath rather than trusting Find's nearest match:
// Find returns the deepest command it COULD resolve, so a typo in the last element
// silently yields the parent — which is precisely how an absence-assertion turns
// vacuous.
func resolveCommandPath(t *testing.T, root *cobra.Command, path string) *cobra.Command {
	t.Helper()
	words := strings.Fields(path)
	if len(words) == 0 || words[0] != "olivares" {
		t.Fatalf("malformed command path %q: it must start with olivares", path)
	}
	found, _, err := root.Find(words[1:])
	if err != nil || found == nil || found.CommandPath() != path {
		got := "<nil>"
		if found != nil {
			got = found.CommandPath()
		}
		t.Errorf("command path %q does not resolve (nearest: %s, err: %v)", path, got, err)
		return nil
	}
	return found
}
