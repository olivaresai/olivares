// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type inventoryAuditStore struct {
	store.Store
	mode   string
	drafts []model.AuditDraft
}

func (s *inventoryAuditStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(inventoryAuditScope{AuthScope: as, log: inventoryAuditLog{AuditLog: as.Audit(), owner: s}})
	})
}

type inventoryAuditScope struct {
	store.AuthScope
	log store.AuditLog
}

func (s inventoryAuditScope) Audit() store.AuditLog { return s.log }

type inventoryAuditLog struct {
	store.AuditLog
	owner *inventoryAuditStore
}

func (l inventoryAuditLog) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	if d.Action == "mcp_gateway.read" {
		l.owner.drafts = append(l.owner.drafts, d)
		switch l.owner.mode {
		case "drop":
			return model.AuditEvent{}, nil
		case "fail":
			return model.AuditEvent{}, errors.New("fixture audit unavailable")
		}
	}
	return l.AuditLog.Append(ctx, d)
}

func TestMCPGatewayInventoryReadRequiresDurableAudit(t *testing.T) {
	var st *inventoryAuditStore
	h := newHarnessOpts(t, func(o *api.Options) {
		st = &inventoryAuditStore{Store: o.Store}
		o.Store = st
		o.MCPGateway = &gatewayServiceFixture{MCPGatewayStore: auth.NewMCPGatewayStore(st)}
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-read-durability")
	viewer := h.mkMember(root, "read-audit@mcp.test", "fixturepass1", auth.RoleViewer, tenant)
	for _, mode := range []string{"drop", "fail"} {
		st.mode = mode
		got := h.do("GET", "/v1/console/mcp-gateway", root, nil, tenantHdr(tenant))
		if got.code != http.StatusServiceUnavailable || got.body["servers"] != nil || got.body["source"] != nil {
			t.Fatalf("%s released inventory: status=%d", mode, got.code)
		}
	}
	before := len(st.drafts)
	if got := h.do("GET", "/v1/console/mcp-gateway", viewer, nil, tenantHdr(tenant)); got.code != http.StatusForbidden || len(st.drafts) != before {
		t.Fatal("unauthorized read created a gateway read audit")
	}
	st.mode = ""
	got := h.do("GET", "/v1/console/mcp-gateway", root, nil, tenantHdr(tenant))
	if got.code != http.StatusOK || len(st.drafts) != before+1 {
		t.Fatal("successful read did not seal exactly one audit")
	}
	d := st.drafts[len(st.drafts)-1]
	if d.ActorKind != model.ActorUser || d.Actor == "" || d.Meta["tenant_id"] != tenant.String() || d.Meta["version"] != int64(0) || len(d.Meta) != 3 {
		t.Fatal("read audit identity/scope/version metadata changed")
	}
}
