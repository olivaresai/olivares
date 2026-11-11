// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestAuditFilteredListNewestFirst: ?order=desc reads the tenant's ledger BACKWARDS with
// the same filters, pages by next_from, never shows another tenant's entries, and the
// default order stays oldest first.
func TestAuditFilteredListNewestFirst(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-desc")
	other := h.createOrg(admin, "audit-desc-other")
	appendAuditDrafts(t, h, other, model.AuditDraft{Actor: "desc", Action: "wanted.other-tenant"})
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "desc", Action: "wanted.one"},
		model.AuditDraft{Actor: "desc", Action: "noise.one"},
		model.AuditDraft{Actor: "desc", Action: "wanted.two"},
		model.AuditDraft{Actor: "desc", Action: "noise.two"},
		model.AuditDraft{Actor: "desc", Action: "wanted.three"},
	)

	q := url.Values{"order": {"desc"}, "limit": {"2"}, "action": {"wanted."}}
	first := h.do("GET", "/v1/audit?"+q.Encode(), admin, nil, tenantHdr(tenant))
	if first.code != http.StatusOK {
		t.Fatalf("first page = %d %s", first.code, first.raw)
	}
	if got, want := auditResponseSeqs(t, first), []int64{events[4].Seq, events[2].Seq}; !slices.Equal(got, want) {
		t.Fatalf("first page sequences = %v, want newest first %v", got, want)
	}
	if first.body["has_more"] != true || first.body["scan_complete"] != false {
		t.Fatalf("first page honesty fields = %#v", first.body)
	}
	next := auditResponseInt64(t, first, "next_from")
	if want := events[2].Seq - 1; next != want {
		t.Fatalf("next_from = %d, want one below the last returned = %d", next, want)
	}

	q.Set("from", strconv.FormatInt(next, 10))
	second := h.do("GET", "/v1/audit?"+q.Encode(), admin, nil, tenantHdr(tenant))
	if second.code != http.StatusOK {
		t.Fatalf("second page = %d %s", second.code, second.raw)
	}
	if got, want := auditResponseSeqs(t, second), []int64{events[0].Seq}; !slices.Equal(got, want) {
		t.Fatalf("second page sequences = %v, want %v (and nothing of the other tenant)", got, want)
	}
	if second.body["has_more"] != false || second.body["scan_complete"] != true {
		t.Fatalf("second page honesty fields = %#v", second.body)
	}

	asc := h.do("GET", "/v1/audit?limit=2&action=wanted.", admin, nil, tenantHdr(tenant))
	if got, want := auditResponseSeqs(t, asc), []int64{events[0].Seq, events[2].Seq}; asc.code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("default order = %d %v, want oldest first %v", asc.code, got, want)
	}
	if bad := h.do("GET", "/v1/audit?order=sideways", admin, nil, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Fatalf("order=sideways = %d, want 400", bad.code)
	}
}
