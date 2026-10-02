// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Configure can send empty containers for omitted command options. Saving those
// options must preserve an unchanged server's successful Test and enabled state.
func TestMCPGatewayConfigurePreservesTestedEnabledServer(t *testing.T) {
	ctx, actor, tenant := t.Context(), adminActor(), model.TenantID(model.NewID())
	svc := auth.NewMCPGatewayStore(testStore(t))
	snapshot, err := svc.PutServer(ctx, actor, tenant, 0, "", auth.MCPGatewayServerInput{Name: "Echo", Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.Servers[0].ID
	probe := auth.MCPGatewayProbe{State: "ok", TestedAt: "2026-10-01T21:00:00Z", Tools: []auth.MCPGatewayTool{{Name: "hu_echo", Fingerprint: strings.Repeat("b", 64), ReadOnly: true}}}
	snapshot, err = svc.SaveProbe(ctx, actor, tenant, snapshot.Version, id, probe)
	if err != nil {
		t.Fatal(err)
	}
	in := snapshot.Servers[0].MCPGatewayServerInput
	in.Enabled, in.AllowedTools = true, nil // Enable accepts the tested catalogue.
	snapshot, err = svc.PutServer(ctx, actor, tenant, snapshot.Version, id, in)
	if err != nil {
		t.Fatal(err)
	}
	policy := append([]auth.MCPGatewayToolPolicy(nil), snapshot.Servers[0].AllowedTools...)
	in = snapshot.Servers[0].MCPGatewayServerInput
	in.Name = "Echo renamed"
	in.Args, in.Env, in.EnvSecretRefs = []string{}, map[string]string{}, map[string]string{}
	snapshot, err = svc.PutServer(ctx, actor, tenant, snapshot.Version, id, in)
	if err != nil {
		t.Fatalf("unchanged Configure Save invalidated a tested enabled server: %v", err)
	}
	check := func() {
		t.Helper()
		snapshot, err = svc.Get(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		row := snapshot.Servers[0]
		if !row.Enabled || row.Name != "Echo renamed" || !reflect.DeepEqual(row.Probe, probe) || !reflect.DeepEqual(row.AllowedTools, policy) || !reflect.DeepEqual(row.Trust, auth.MCPGatewayTrust{}) {
			t.Fatal("Configure Save changed the enabled state, probe, tool policy or empty trust")
		}
	}
	check()
	// A second Save follows a GET, whose omitted empty options decode as nil.
	in = snapshot.Servers[0].MCPGatewayServerInput
	in.Args, in.Env, in.EnvSecretRefs, in.EgressCIDRs = nil, nil, nil, nil
	snapshot, err = svc.PutServer(ctx, actor, tenant, snapshot.Version, id, in)
	if err != nil {
		t.Fatalf("second unchanged Save: %v", err)
	}
	check()
	for _, change := range []struct {
		name  string
		alter func(*auth.MCPGatewayServerInput)
	}{
		{"command", func(in *auth.MCPGatewayServerInput) { in.Command = "python3" }},
		{"argument", func(in *auth.MCPGatewayServerInput) { in.Args = []string{"changed"} }},
		{"environment", func(in *auth.MCPGatewayServerInput) { in.Env = map[string]string{"ECHO_MODE": "changed"} }},
		{"secret reference", func(in *auth.MCPGatewayServerInput) {
			in.EnvSecretRefs = map[string]string{"ECHO_KEY": "store:mcp/echo"}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := snapshot.Servers[0].MCPGatewayServerInput
			change.alter(&changed)
			if _, err := svc.PutServer(ctx, actor, tenant, snapshot.Version, id, changed); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("a changed server inherited the old successful Test: %v", err)
			}
		})
	}
	check()
	// Explicit Disable remains an operator choice. A real argument change still
	// withdraws the old observation and needs another Test before Enable.
	in = snapshot.Servers[0].MCPGatewayServerInput
	in.Enabled, in.Args = false, []string{"changed"}
	snapshot, err = svc.PutServer(ctx, actor, tenant, snapshot.Version, id, in)
	if err != nil || snapshot.Servers[0].Enabled || snapshot.Servers[0].Probe.State != "never_tested" {
		t.Fatalf("changed disabled server retained its Test: %v", err)
	}
	in.Enabled = true
	if _, err := svc.PutServer(ctx, actor, tenant, snapshot.Version, id, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("Enable accepted the changed untested server: %v", err)
	}
	t.Run("changed HTTPS address grant withdraws Test", func(t *testing.T) {
		remote := mcpGatewayInput()
		remote.CredentialRef = ""
		created, err := svc.PutServer(ctx, actor, tenant, snapshot.Version, "", remote)
		if err != nil {
			t.Fatal(err)
		}
		remoteID := created.Servers[1].ID
		observed, err := svc.SaveProbe(ctx, actor, tenant, created.Version, remoteID, probe)
		if err != nil {
			t.Fatal(err)
		}
		remote.Enabled = true
		enabled, err := svc.PutServer(ctx, actor, tenant, observed.Version, remoteID, remote)
		if err != nil {
			t.Fatal(err)
		}
		remote = enabled.Servers[1].MCPGatewayServerInput
		remote.EgressCIDRs = []string{"127.0.0.0/8"}
		if _, err := svc.PutServer(ctx, actor, tenant, enabled.Version, remoteID, remote); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
			t.Fatalf("new address grant inherited an old Test: %v", err)
		}
		remote.Enabled = false
		disabled, err := svc.PutServer(ctx, actor, tenant, enabled.Version, remoteID, remote)
		if err != nil || disabled.Servers[1].Enabled || disabled.Servers[1].Probe.State != "never_tested" {
			t.Fatalf("new address grant retained the old Test after Disable: %v", err)
		}
	})
}
