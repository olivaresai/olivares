// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// edition.go is how the CLI answers a Business capability that is not there: one
// sentence that names the feature and where it is described, exit code 9, and no
// HTTP status or build vocabulary (Root's decision on N1 RU-02, 2026-10-01: `mcp
// pins ls` answered "requires the enterprise add-on (HTTP 501)").

// pricingURL is the public page that describes the Business capabilities.
const pricingURL = "https://olivares.ai/pricing"

// errNotInEdition marks a refusal that means "this is a Business capability that
// this build or this engine does not have". Every site that meets one returns
// notInEdition(); the sentence is written once, by installEditionAnswers.
var errNotInEdition = errors.New("this is a Business feature")

func notInEdition() error { return exitcode.New(exitcode.Edition, errNotInEdition) }

// paidCommands are the command paths whose every verb is a Business capability,
// with the name the sentence uses. A Community build keeps them out of the help;
// they stay invocable, so a script gets the sentence instead of "unknown command".
var paidCommands = map[string]string{
	"mcp pins":            "MCP tool pinning",
	"reporting schedules": "Report scheduling",
	"threatintel":         "The threat-intel feed",
	"hooks":               "Hook hardening",
}

// editionNames names the paid verbs of groups that are otherwise open: those groups
// stay in the help, and only a refused verb gets the sentence.
var editionNames = map[string]string{
	"compliance": "Compliance Packs",
}

// installEditionAnswers hides the paid groups in a Community build and makes every
// command answer errNotInEdition with "<feature> is a Business feature: <url>".
func installEditionAnswers(root *cobra.Command) {
	walkCommands(root, func(c *cobra.Command) {
		if _, paid := paidCommands[commandPathWithoutBinary(c)]; paid && !enterpriseAddOnsLinked {
			c.Hidden = true
		}
		if c.RunE == nil {
			return
		}
		run := c.RunE
		c.RunE = func(cmd *cobra.Command, args []string) error {
			err := run(cmd, args)
			if errors.Is(err, errNotInEdition) {
				return sentence(exitcode.Edition, "%s is a Business feature: %s", paidFeatureName(cmd), pricingURL)
			}
			return err
		}
	})
}

// paidFeatureName is the feature of the nearest paid command at or above c.
func paidFeatureName(c *cobra.Command) string {
	for x := c; x != nil; x = x.Parent() {
		if name := paidCommands[commandPathWithoutBinary(x)]; name != "" {
			return name
		}
		if name := editionNames[commandPathWithoutBinary(x)]; name != "" {
			return name
		}
	}
	return "This capability"
}
