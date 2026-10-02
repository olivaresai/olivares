// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestMCPManagementLocalTestEnableAndSecretReferences(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	in := auth.MCPGatewayServerInput{Name: "Local fixture", Command: os.Args[0], Args: []string{"-test.run=^TestManagedStdioFixture$"}, Env: map[string]string{"MCP_FIXTURE": "1"}}
	out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	id := out.Servers[0].ID
	if out.Servers[0].Enabled || out.Servers[0].Transport != "stdio" {
		t.Fatal("local server did not start disabled")
	}
	out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, id)
	if err != nil || out.Servers[0].Probe.State != "ok" || len(out.Servers[0].Probe.Tools) != 1 {
		t.Fatalf("probe=%+v err=%v", out, err)
	}
	in = out.Servers[0].MCPGatewayServerInput
	in.Enabled = true
	in.AllowedTools = nil
	out, err = m.PutServer(t.Context(), p, f.tenant, out.Version, id, in)
	if err != nil || !out.Servers[0].Enabled || len(out.Servers[0].AllowedTools) != 1 || !out.Servers[0].AllowedTools[0].Destructive {
		t.Fatalf("enable=%+v err=%v", out, err)
	}
	in.EnvSecretRefs = map[string]string{"MCP_SECRET": "store:mcp/missing"}
	if _, err := m.PutServer(t.Context(), p, f.tenant, out.Version, id, in); err != auth.ErrSecretNotFound {
		t.Fatal("missing environment secret was not refused")
	}
}
