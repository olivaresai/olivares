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
