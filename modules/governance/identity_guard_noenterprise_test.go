//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
)

func assertAuthorizedIdentityRead(t *testing.T, r resp) {
	t.Helper()
	if r.code != http.StatusNotImplemented || !strings.Contains(r.raw, "Business") {
		t.Fatalf("authorized Community identity route = %d %s, want explicit Business 501", r.code, r.raw)
	}
}

func TestCommunityWIFRefusesEvenWithConfiguredGraph(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	for _, graph := range []*claudewif.WIFGraph{nil, {Issuers: []claudewif.WIFIssuer{{ID: "fdis_private"}}}} {
		h.wif.graph = graph
		r := h.do("GET", "/v1/m/identity/wif", tok, nil, tenantHdr(tenant))
		assertAuthorizedIdentityRead(t, r)
		if strings.Contains(r.raw, "fdis_private") || r.body["issuers"] != nil {
			t.Fatalf("Community exposed a WIF graph: %s", r.raw)
		}
	}
	if contains(h.auditActions(tenant), "governance.identity.wif.read") {
		t.Fatal("refused WIF read must not claim a successful graph read")
	}
}
