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

// The alias form is documented (docs/contracts/mcp-gateway-management.md), so a
// change to it is a change to a published tool name.
func TestManagedToolAliasFormIsPublished(t *testing.T) {
	if got := managedToolAlias("01a14608-f7fc-7ecb-9138-6cf6c48c4261", "hu_echo"); got != "mcp_01a14608f7fc7ecb91386cf6c48c4261_hu_echo" {
		t.Fatalf("alias = %q", got)
	}
}

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
