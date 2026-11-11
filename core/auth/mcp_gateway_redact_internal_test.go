// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A server saved before the save-time refusal can hold argument and environment
// values under credential names. Its read shows none of them and keeps its shape;
// a write that sends the read back keeps them; a changed value replaces one; and a
// marker with no stored value behind it is refused, never saved.
func TestMCPGatewayLegacyServerReadsRedactedAndKeepsItsValues(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewMCPGatewayStore(st)
	tenant := model.TenantID(model.NewID())
	actor := Principal{Kind: KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true, DisplayName: "admin"}
	legacy := MCPGatewayServerInput{Name: "Legacy", Transport: "stdio", Command: "npx",
		Args:        []string{"-y", "server", "--key", "legacy-arg-pair", "--sig=legacy-arg-inline", "--upstream=https://u.test/x?region=legacy-query"},
		Env:         map[string]string{"MODE": "legacy-env-public", "SIG": "legacy-env-sig"},
		EgressCIDRs: []string{}, AllowedTools: []MCPGatewayToolPolicy{}}
	id := model.NewID().String()
	if _, err := svc.mutate(ctx, actor, tenant, 0, "mcp_gateway.server.create", id, func(cfg *MCPGatewayConfig) error {
		cfg.Servers = append(cfg.Servers, MCPGatewayServer{MCPGatewayServerInput: legacy, ID: id, Probe: MCPGatewayProbe{State: "never_tested", Tools: []MCPGatewayTool{}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.Get(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	read := snap.Redacted()
	whole, _ := json.Marshal(snap)
	shown, _ := json.Marshal(read)
	if strings.Contains(string(shown), "legacy-") {
		t.Fatalf("read returns a saved value: %s", shown)
	}
	var wholeShape, shownShape map[string]any
	_ = json.Unmarshal(whole, &wholeShape)
	_ = json.Unmarshal(shown, &shownShape)
	server := func(m map[string]any) map[string]any { return m["servers"].([]any)[0].(map[string]any) }
	if !slices.Equal(slices.Sorted(maps.Keys(server(wholeShape))), slices.Sorted(maps.Keys(server(shownShape)))) {
		t.Fatalf("read changed shape: %s against %s", shown, whole)
	}
	want := []string{"-y", "server", "--key", "<redacted>", "--sig=<redacted>", "--upstream=https://u.test/x?region=<redacted>"}
	if got := read.Servers[0].Args; !slices.Equal(got, want) {
		t.Fatalf("redacted args = %q, want %q", got, want)
	}
	if snap.Servers[0].Env["MODE"] != "legacy-env-public" {
		t.Fatal("redaction changed the snapshot it was given")
	}

	in := read.Servers[0].MCPGatewayServerInput
	out, err := svc.PutServer(ctx, actor, tenant, snap.Version, id, in)
	if err != nil {
		t.Fatalf("write back of the read: %v", err)
	}
	stored := out.Servers[0]
	if !slices.Equal(stored.Args, legacy.Args) || !maps.Equal(stored.Env, legacy.Env) || stored.Probe.State != "never_tested" {
		t.Fatalf("write back did not keep the stored values: %+v", stored.MCPGatewayServerInput)
	}

	in.Env = maps.Clone(in.Env)
	in.Env["MODE"] = "replaced-public"
	out, err = svc.PutServer(ctx, actor, tenant, out.Version, id, in)
	if err != nil || out.Servers[0].Env["MODE"] != "replaced-public" || out.Servers[0].Env["SIG"] != "legacy-env-sig" {
		t.Fatalf("a changed value did not replace only its own: %+v %v", out.Servers, err)
	}

	for name, alter := range map[string]func(*MCPGatewayServerInput){
		"env without a stored value":   func(in *MCPGatewayServerInput) { in.Env = map[string]string{"NEW": "<redacted>"} },
		"argument moved off its place": func(in *MCPGatewayServerInput) { in.Args = append([]string{"--first"}, in.Args...) },
		"marker inside a new value": func(in *MCPGatewayServerInput) {
			in.Args = []string{"--upstream=https://other.test/?region=<redacted>"}
		},
	} {
		changed := read.Servers[0].MCPGatewayServerInput
		changed.Args, changed.Env = slices.Clone(changed.Args), maps.Clone(changed.Env)
		alter(&changed)
		if _, err := svc.PutServer(ctx, actor, tenant, out.Version, id, changed); !errors.Is(err, ErrMCPGatewayInvalid) {
			t.Fatalf("%s: marker saved: %v", name, err)
		}
	}
	if _, err := svc.PutServer(ctx, actor, tenant, out.Version, "", MCPGatewayServerInput{Name: "New", Command: "npx", Env: map[string]string{"MODE": "<redacted>"}}); !errors.Is(err, ErrMCPGatewayInvalid) {
		t.Fatalf("marker saved on create: %v", err)
	}
}
