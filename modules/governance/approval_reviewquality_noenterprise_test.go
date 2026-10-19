// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package governance_test

import (
	"net/http"
	"testing"
)

// The approval review-quality report is a Business capability. Community keeps the
// published route and answers 501 business_required, for the report and its export,
// instead of letting the path fall into GET /approvals/{id}.
func TestCommunityApprovalReviewQualityRequiresBusiness(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, query := range []string{"", "?format=csv", "?min_decisions=5"} {
		r := h.do("GET", govPath+"/approvals/review-quality"+query, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Fatalf("review-quality%s = %d %s, want 501", query, r.code, r.raw)
		}
		errBody, _ := r.body["error"].(map[string]any)
		if errBody["code"] != "business_required" {
			t.Fatalf("review-quality%s error = %s, want code business_required", query, r.raw)
		}
	}
}
