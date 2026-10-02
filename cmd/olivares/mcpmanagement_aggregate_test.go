// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
)

func TestManagedMCPAliasesAreBoundedAndSeparateServers(t *testing.T) {
	a, b := model.NewID().String(), model.NewID().String()
	for _, name := range []string{"read_file", strings.Repeat("x", 128)} {
		alias := managedToolAlias(a, name)
		if len(alias) > 128 || alias == managedToolAlias(b, name) || alias != managedToolAlias(a, name) {
			t.Fatal("tool namespace is not stable, unique and bounded")
		}
		if _, err := mcpc.NewToolset([]mcpc.ToolPolicy{{Name: alias}}); err != nil {
			t.Fatal(err)
		}
	}
	if managedToolAlias(a, strings.Repeat("x", 127)+"a") == managedToolAlias(a, strings.Repeat("x", 127)+"b") {
		t.Fatal("truncated tools collide")
	}
}
