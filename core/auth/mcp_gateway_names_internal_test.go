// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A roster written before names were unique can hold two servers of one name.
// Both stay editable, so either can be turned off or renamed; the rule only
// stops a new or renamed server from taking a held name.
func TestMCPGatewayRosterWithDuplicateNamesStaysEditable(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewMCPGatewayStore(st)
	tenant := model.TenantID(model.NewID())
	actor := Principal{Kind: KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true, DisplayName: "admin"}
	in := MCPGatewayServerInput{Name: "Files", Transport: "streamable_http", URL: "https://tools.example.test/mcp", EgressCIDRs: []string{}, AllowedTools: []MCPGatewayToolPolicy{}}
	first, second := model.NewID().String(), model.NewID().String()
	snap, err := svc.mutate(ctx, actor, tenant, 0, "mcp_gateway.server.create", first, func(cfg *MCPGatewayConfig) error {
		for _, id := range []string{first, second} {
			cfg.Servers = append(cfg.Servers, MCPGatewayServer{MCPGatewayServerInput: in, ID: id, Probe: MCPGatewayProbe{State: "never_tested", Tools: []MCPGatewayTool{}}})
		}
		return nil
	})
	if err != nil || len(snap.Servers) != 2 {
		t.Fatalf("legacy roster: %+v %v", snap, err)
	}

	edit := in
	edit.URL = "https://renewed.example.test/mcp"
	if snap, err = svc.PutServer(ctx, actor, tenant, snap.Version, first, edit); err != nil {
		t.Fatalf("editing one of two same-named servers under its own name: %v", err)
	}
	away := in
	away.Name = "Other"
	if snap, err = svc.PutServer(ctx, actor, tenant, snap.Version, second, away); err != nil {
		t.Fatalf("renaming a duplicate away: %v", err)
	}
	held := in
	held.Name = "files"
	if _, err = svc.PutServer(ctx, actor, tenant, snap.Version, second, held); !errors.Is(err, ErrMCPGatewayNameTaken) {
		t.Fatalf("renaming back onto the held name = %v, want ErrMCPGatewayNameTaken", err)
	}
	if _, err = svc.PutServer(ctx, actor, tenant, snap.Version, model.NewID().String(), held); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown id with a held name = %v, want not found", err)
	}
}
