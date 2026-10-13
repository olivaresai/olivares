// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCLIHelpCompleteness(t *testing.T) {
	root := newRootCmd()
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		path := cmd.CommandPath()
		if strings.TrimSpace(cmd.Short) == "" {
			t.Errorf("command %q has an empty Short description", path)
		}
		if cmd.RunE != nil || cmd.Run != nil {
			if strings.TrimSpace(cmd.Long) == "" {
				t.Errorf("runnable command %q has an empty Long description", path)
			}
			if strings.TrimSpace(cmd.Example) == "" {
				t.Errorf("runnable command %q has an empty Example", path)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func TestToolLifecycleHelp(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want []string
	}{
		{"rm", []string{"Remove an installed tool version", "Examples:", "olivares tool rm codex --version 0.134.0"}},
		{"pin", []string{"Select an installed version", "Examples:", "olivares tool pin codex --workspace default --version 0.134.0", "olivares tool pin codex --agent my-agent --version 0.134.0", "olivares tool pin codex --workspace default --clear"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			help := helpFor(t, "tool", tc.verb)
			for _, want := range tc.want {
				if !strings.Contains(help, want) {
					t.Errorf("help missing %q:\n%s", want, help)
				}
			}
		})
	}
}
