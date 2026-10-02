// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestApprovalStructuredReviewRoundTripPreservesGeneratedMasks(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "structured-review")
		principal := cancellationSession(t, h, tenant, admin, "structured-review")
		text, masks := generatedApprovalReason("")
		want := "PASSWORD=[secret env/password] curl --token=[secret env/token] https://example.test"
		in := governance.ApprovalRequest{
			Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "reviewed-command", SessionRef: principal.SessionIdentity,
			Reason: "The reviewer uses structured facts", Review: &governance.ApprovalReview{Tool: "Bash", Text: text}, ReviewMasks: masks,
		}
		service := h.gov.EngineApprovals()
		created, err := service.Request(t.Context(), tenant, principal, in)
		if err != nil || created.Review == nil || created.Review.Tool != "Bash" || created.Review.Text != want {
			t.Fatalf("created structured facts=%+v error=%v", created.Review, err)
		}
		if in.Review.Text != text || in.ReviewMasks[0] != masks[0] {
			t.Fatal("normalization mutated caller-owned facts or provenance")
		}
		stored, err := service.Read(t.Context(), tenant, created.ID)
		if err != nil || stored.Review == nil || *stored.Review != *created.Review {
			t.Fatal("stored reviewed display changed")
		}
		for _, route := range []string{govPath + "/approvals/" + created.ID, govPath + "/approvals?status=pending"} {
			out := h.do(http.MethodGet, route, admin, nil, tenantHdr(tenant))
			if out.code != http.StatusOK || !strings.Contains(out.raw, `"review"`) || !strings.Contains(out.raw, want) {
				t.Fatal("REST detail/list lost the structured facts")
			}
			if strings.Contains(out.raw, "ReviewMasks") || strings.Contains(out.raw, "review_masks") || strings.Contains(out.raw, `"Start"`) {
				t.Fatal("generated provenance escaped onto the wire")
			}
		}
		if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(model.Kind("governance.approval"))
			if err != nil {
				return err
			}
			rec, err := repo.Get(t.Context(), model.ID(created.ID))
			if err != nil {
				return err
			}
			var persisted governance.ApprovalReview
			if json.Unmarshal([]byte(rec.String("review")), &persisted) != nil || persisted.Tool != "Bash" || persisted.Text != want {
				t.Fatal("SQL review differs from the proven reviewer display")
			}
			if strings.Contains(rec.String("review"), "Masks") || strings.Contains(rec.String("review"), `"Start"`) {
				t.Fatal("SQL retained marker provenance")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApprovalStructuredReviewRejectsInvalidOrForgedProvenance(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "invalid-review")
		principal := cancellationSession(t, h, tenant, admin, "invalid-review")
		text, masks := generatedApprovalReason("")
		service := h.gov.EngineApprovals()
		for _, bad := range [][]redact.GeneratedMaskSpan{{{Start: -1, End: 4}}, {{Start: 0, End: len(text) + 1}}, {{Start: 0, End: 4}}, {masks[0], masks[0]}, {masks[1], masks[0]}} {
			if _, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SessionRef: principal.SessionIdentity, Review: &governance.ApprovalReview{Tool: "Bash", Text: text}, ReviewMasks: bad}); err == nil {
				t.Fatal("invalid review provenance reached the queue")
			}
		}
		items, _, err := service.List(t.Context(), tenant, "claude.tool.use", "pending", "")
		if err != nil || len(items) != 0 || contains(h.auditActions(tenant), "governance.approval.create") {
			t.Fatal("invalid provenance left an approval or audit effect")
		}
		_, requester := h.roleUser(admin, tenant, "review-wire@x.io", "editor")
		out := h.createApproval(requester, tenant, map[string]any{"action": "claude.tool.use", "subject_kind": "claude.tool", "review": map[string]any{"tool": "Bash", "text": text}, "review_masks": masks, "ReviewMasks": masks})
		if out.code == http.StatusBadRequest {
			return
		}
		if out.code != http.StatusCreated || strings.Contains(out.raw, "[secret env/password]") || strings.Contains(out.raw, "[secret env/token]") {
			t.Fatal("REST supplied a generated-marker exemption")
		}
	})
}

func TestApprovalStructuredReviewLegacyRowsRemainValid(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "legacy-review")
		_, requester := h.roleUser(admin, tenant, "legacy-review@x.io", "editor")
		created := h.createApproval(requester, tenant, map[string]any{"action": "claude.tool.use", "subject_kind": "claude.tool", "reason": "Legacy request"})
		if created.code != http.StatusCreated {
			t.Fatal("legacy request refused", created.code)
		}
		id := created.body["id"].(string)
		for _, route := range []string{govPath + "/approvals/" + id, govPath + "/approvals?status=pending"} {
			out := h.do(http.MethodGet, route, admin, nil, tenantHdr(tenant))
			if out.code != http.StatusOK || strings.Contains(out.raw, `"review"`) || !strings.Contains(out.raw, "Legacy request") {
				t.Fatal("legacy request lost its existing read behavior")
			}
		}
		out := h.do(http.MethodPost, govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve"}, tenantHdr(tenant))
		if out.code != http.StatusOK || out.body["status"] != "approved" || strings.Contains(out.raw, `"review"`) {
			t.Fatal("legacy decision behavior changed")
		}
	})
}
