// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestMCPManagementLocalFixtureLaunchesConfinedChild(t *testing.T) {
	m, _, _ := mcpManagementFixture(t)
	spec := sessions.LaunchSpec{
		Program: os.Args[0], Args: []string{"-test.run=^TestManagedStdioFixture$"},
		Dir: t.TempDir(), Isolation: sessions.IsolationNative, WaitDelay: time.Second,
		Env: []sessions.EnvVar{{Name: "MCP_FIXTURE", Value: "1"}},
	}
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	client, err := launchManagedStdio(t.Context(), sessions.NewProcRunner(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := client.confinement()["mode"]; got != confine.ModeLandlock {
		t.Fatalf("local MCP child confinement = %v, want %s", got, confine.ModeLandlock)
	}
}

func TestMCPManagementLocalTestRefusesMissingConfinement(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	m.eng.sessionsMod = nil
	in := auth.MCPGatewayServerInput{Name: "Unconfined fixture", Command: os.Args[0], Args: []string{"-test.run=^TestManagedStdioFixture$"}, Env: map[string]string{"MCP_FIXTURE": "1"}}
	out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, out.Servers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	probe := out.Servers[0].Probe
	if probe.State != "unreachable" || probe.Reason != "process_start" || !strings.Contains(probe.Detail, "this server has no session confinement policy") || len(probe.Tools) != 0 {
		t.Fatalf("missing confinement did not refuse the local probe: %+v", probe)
	}
}

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
	if _, err := m.PutServer(t.Context(), p, f.tenant, out.Version, id, in); !errors.Is(err, auth.ErrSecretNotFound) ||
		!strings.Contains(err.Error(), `"mcp/missing"`) {
		t.Fatalf("missing environment secret was not refused by name: %v", err)
	}
}
