// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// noStopGuard is a kill-switch guard that never reports a stop (a configured, healthy
// estate switch) — enough to exercise the mount-time governance-reachability check.
type noStopGuard struct{}

func (noStopGuard) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return governance.StopState{}, nil
}

// TestF06MCPMountRefusesKillSwitchWithoutTenant is the F-06 red repro (piece 3): a mount
// that has a kill switch configured but NO tenant to key it on would forward around the
// estate stop. It must be REFUSED at build time (deny-closed), not merely warned.
func TestF06MCPMountRefusesKillSwitchWithoutTenant(t *testing.T) {
	eng := &engine{killSwitch: noStopGuard{}}
	base := func() *mcpGatewayConfig {
		return &mcpGatewayConfig{
			Resource:    "https://mcp.example.com/mcp",
			UpstreamURL: "https://upstream.example.com/mcp", // upstream != nil ⇒ actuating surface
			Tools:       []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		}
	}

	// No tenant + kill switch + actuating upstream ⇒ refuse to mount.
	cfg := base()
	cfg.Tenant = ""
	if _, _, err := buildMCPResourceServer(eng, cfg, discardLogger()); err == nil {
		t.Fatal("F-06: a kill-switch mount with no tenant must be refused, got nil error")
	} else if !strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("refusal must name the kill-switch reachability, got %v", err)
	}

	// Control: WITH a tenant the kill-switch reachability check passes (any later error — e.g.
	// missing issuer trust — must NOT be the kill-switch refusal).
	cfgT := base()
	cfgT.Tenant = model.NewTenantID().String()
	if _, _, err := buildMCPResourceServer(eng, cfgT, discardLogger()); err != nil && strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("a tenant-keyed mount must not be refused for the kill switch, got %v", err)
	}
}
