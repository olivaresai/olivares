// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// A console lists the approved grants a ship can still spend: the unspent ones
// (consumed=false), each with the time the gate stops reusing it (spendable_until:
// decided_at plus the gate's reuse window).
func TestApprovalListSaysUntilWhenAnUnspentGrantIsReused(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")
	spent := h.approveHighAction(t, admin, editor, tenant)
	if r := h.consume(editor, tenant, spent, "ship-1"); r.code != http.StatusOK || r.body["granted"] != true {
		t.Fatalf("consume = %d %s", r.code, r.raw)
	}
	unspent := h.approveHighAction(t, admin, editor, tenant)

	// No window wired: the reads are as before.
	all := h.do("GET", govPath+"/approvals", editor, nil, tenantHdr(tenant))
	for _, it := range items(all) {
		if v, ok := it.(map[string]any)["spendable_until"]; ok {
			t.Fatalf("no reuse window is wired, yet spendable_until=%v", v)
		}
	}

	h.gov.UseGrantReuseWindow(func(model.TenantID) time.Duration { return time.Hour })
	list := h.do("GET", govPath+"/approvals?consumed=false", editor, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list consumed=false = %d %s", list.code, list.raw)
	}
	got := items(list)
	if len(got) != 1 || got[0].(map[string]any)["id"] != unspent {
		t.Fatalf("consumed=false must list only the unspent grant %s: %s", unspent, list.raw)
	}
	item := got[0].(map[string]any)
	decided, err := time.Parse(time.RFC3339Nano, item["decided_at"].(string))
	if err != nil {
		t.Fatalf("decided_at %v: %v", item["decided_at"], err)
	}
	until, err := time.Parse(time.RFC3339Nano, item["spendable_until"].(string))
	if err != nil {
		t.Fatalf("spendable_until %v: %v", item["spendable_until"], err)
	}
	if !until.Equal(decided.Add(time.Hour)) {
		t.Fatalf("spendable_until = %v, want decided_at %v plus the one-hour window", until, decided)
	}

	spentList := h.do("GET", govPath+"/approvals?consumed=true", editor, nil, tenantHdr(tenant))
	if s := items(spentList); len(s) != 1 || s[0].(map[string]any)["id"] != spent {
		t.Fatalf("consumed=true must list only the spent grant %s: %s", spent, spentList.raw)
	} else if v, ok := s[0].(map[string]any)["spendable_until"]; ok {
		t.Fatalf("a spent grant is reused by nobody, yet spendable_until=%v", v)
	}
	get := h.do("GET", govPath+"/approvals/"+unspent, editor, nil, tenantHdr(tenant))
	if get.body["spendable_until"] != item["spendable_until"] {
		t.Fatalf("the detail read says spendable_until=%v, the list %v", get.body["spendable_until"], item["spendable_until"])
	}
	if bad := h.do("GET", govPath+"/approvals?consumed=maybe", editor, nil, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Fatalf("consumed=maybe = %d, want 400: %s", bad.code, bad.raw)
	}
}
