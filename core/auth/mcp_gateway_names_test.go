// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A tenant has at most one MCP server per name. A second one is refused, never
// merged into or substituted for the first, and the refusal names the holder.
func TestMCPGatewayServerNamesAreUniquePerTenant(t *testing.T) {
	ctx := context.Background()
	svc := auth.NewMCPGatewayStore(testStore(t))
	a, b := model.TenantID(model.NewID()), model.TenantID(model.NewID())
	first, err := svc.PutServer(ctx, adminActor(), a, 0, "", mcpGatewayInput())
	if err != nil {
		t.Fatal(err)
	}
	held := first.Servers[0].ID

	for _, name := range []string{"Example", "example", "EXAMPLE"} {
		in := mcpGatewayInput()
		in.Name = name
		in.URL = "https://other.example.test/mcp"
		_, err := svc.PutServer(ctx, adminActor(), a, first.Version, "", in)
		if !errors.Is(err, auth.ErrMCPGatewayNameTaken) {
			t.Fatalf("create %q = %v, want ErrMCPGatewayNameTaken", name, err)
		}
		if !strings.Contains(err.Error(), held) {
			t.Fatalf("refusal for %q does not name the server that holds it: %v", name, err)
		}
	}
	got, err := svc.Get(ctx, a)
	if err != nil || got.Version != first.Version || len(got.Servers) != 1 || got.Servers[0].URL != mcpGatewayInput().URL {
		t.Fatalf("a refused duplicate changed the roster: %+v %v", got, err)
	}

	// Renaming a second server onto a held name is the same refusal.
	other := mcpGatewayInput()
	other.Name = "Other"
	second, err := svc.PutServer(ctx, adminActor(), a, first.Version, "", other)
	if err != nil {
		t.Fatal(err)
	}
	other.Name = "example"
	if _, err := svc.PutServer(ctx, adminActor(), a, second.Version, second.Servers[1].ID, other); !errors.Is(err, auth.ErrMCPGatewayNameTaken) {
		t.Fatalf("rename onto a held name = %v", err)
	}

	// A server keeps its own name on update, and another tenant may reuse it.
	same := mcpGatewayInput()
	same.URL = "https://renewed.example.test/mcp"
	if _, err := svc.PutServer(ctx, adminActor(), a, second.Version, held, same); err != nil {
		t.Fatalf("update under its own name: %v", err)
	}
	if _, err := svc.PutServer(ctx, adminActor(), b, 0, "", mcpGatewayInput()); err != nil {
		t.Fatalf("same name in another tenant: %v", err)
	}
}
