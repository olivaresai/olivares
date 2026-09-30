// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api_test

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
)

func TestMCPGatewayInventoryReadMustAudit(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.MCPGateway = &gatewayServiceFixture{MCPGatewayStore: auth.NewMCPGatewayStore(o.Store)}
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "sr-mcp-audit")
	count := func() int {
		n := 0
		err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
			return as.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error { n++; return nil })
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	res := h.do("GET", "/v1/console/mcp-gateway", root, nil, tenantHdr(tenant))
	if res.code != 200 {
		t.Fatalf("inventory status=%d", res.code)
	}
	if count() == before {
		t.Fatal("successful new MCP gateway inventory route returns tenant configuration without any durable audit event")
	}
}
