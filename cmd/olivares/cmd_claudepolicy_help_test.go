// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The Community help of `claude-policy` must not present the Business verbs as if they worked here:
// their fleet routes answer 501 in Community (claudepolicy_fleet_noenterprise.go). Each says so in its
// Short and in its Long, and the root example leads with the two verbs Community serves. The Business
// build keeps its help as written.
const (
	claudePolicyShortMarker = "(requires Business)"
	claudePolicyLongMarker  = "Requires Business."
)

// claudePolicyBusinessPaths pins which help texts carry the marker, verb by verb, including the
// subcommands of `versions`.
var claudePolicyBusinessPaths = [][]string{
	{"publish"}, {"versions"}, {"versions", "ls"}, {"versions", "get"},
	{"distribution"}, {"artifact"}, {"checkin"}, {"pull"},
}

func claudePolicyCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cp, _, err := newRootCmd().Find([]string{"claude-policy"})
	if err != nil || cp.Name() != "claude-policy" {
		t.Fatalf("claude-policy is not invocable: %v", err)
	}
	if len(path) == 0 {
		return cp
	}
	found, _, err := cp.Find(path)
	if err != nil || found == cp {
		t.Fatalf("claude-policy %s is not invocable: %v", strings.Join(path, " "), err)
	}
	return found
}

func TestClaudePolicyCommunityHelpLeadsWithTheVerbsCommunityServes(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "community"

	example := claudePolicyCommand(t).Example
	lines := strings.Split(strings.TrimSpace(example), "\n")
	if len(lines) != 2 ||
		!strings.Contains(lines[0], "claude-policy validate ") ||
		!strings.Contains(lines[1], "claude-policy dry-run ") {
		t.Fatalf("the Community root example must lead with validate and dry-run, got:\n%s", example)
	}
	for _, verb := range []string{"publish", "versions", "distribution", "artifact", "checkin", "pull"} {
		if strings.Contains(example, "claude-policy "+verb+" ") {
			t.Errorf("the Community root example advertises the Business verb %q:\n%s", verb, example)
		}
	}

	root := claudePolicyCommand(t)
	if !strings.Contains(root.Short, "Business") {
		t.Errorf("the Community root Short still presents publishing as available: %q", root.Short)
	}
	if want := "require Business: publish, versions, distribution, artifact, checkin, pull."; !strings.HasSuffix(root.Long, want) {
		t.Errorf("the Community root Long must end by naming the Business verbs (%q):\n%s", want, root.Long)
	}

	for _, path := range claudePolicyBusinessPaths {
		cmd := claudePolicyCommand(t, path...)
		if strings.Contains(cmd.Long, "501") || strings.Contains(cmd.Long, "HTTP") {
			t.Errorf("claude-policy %s: the note carries HTTP vocabulary (edition.go: no HTTP status in Business wording):\n%s", strings.Join(path, " "), cmd.Long)
		}
		if !strings.Contains(cmd.Short, claudePolicyShortMarker) {
			t.Errorf("claude-policy %s: Short does not say it requires Business: %q", strings.Join(path, " "), cmd.Short)
		}
		if !strings.Contains(cmd.Long, pricingURL) {
			t.Errorf("claude-policy %s: the note does not point at the pricing page:\n%s", strings.Join(path, " "), cmd.Long)
		}
		if !strings.Contains(cmd.Long, claudePolicyLongMarker) {
			t.Errorf("claude-policy %s: Long does not say it requires Business:\n%s", strings.Join(path, " "), cmd.Long)
		}
	}

	for _, path := range [][]string{{"validate"}, {"dry-run"}} {
		cmd := claudePolicyCommand(t, path...)
		if text := cmd.Short + "\n" + cmd.Long; strings.Contains(text, "Business") {
			t.Errorf("claude-policy %s is served in Community but its help mentions Business:\n%s", strings.Join(path, " "), text)
		}
	}
}

func TestClaudePolicyBusinessBuildKeepsItsExamplesAndHelp(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "enterprise"

	example := claudePolicyCommand(t).Example
	for _, want := range []string{"claude-policy publish hooks", "claude-policy distribution managed-settings", "claude-policy versions ls managed-settings"} {
		if !strings.Contains(example, want) {
			t.Errorf("the Business root example lost %q:\n%s", want, example)
		}
	}

	root := claudePolicyCommand(t)
	if strings.Contains(root.Short, "Business") || strings.Contains(root.Long, "Business") {
		t.Errorf("the Business root help must not say claude-policy verbs require Business:\nShort: %q\nLong:\n%s", root.Short, root.Long)
	}

	for _, path := range claudePolicyBusinessPaths {
		cmd := claudePolicyCommand(t, path...)
		if strings.Contains(cmd.Short, claudePolicyShortMarker) || strings.Contains(cmd.Long, claudePolicyLongMarker) {
			t.Errorf("the Business build must not say claude-policy %s requires Business:\nShort: %q\nLong:\n%s",
				strings.Join(path, " "), cmd.Short, cmd.Long)
		}
	}
}
