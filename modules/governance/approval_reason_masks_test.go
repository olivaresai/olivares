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
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func onReasonMaskStores(t *testing.T, run func(*testing.T, *harness)) {
	t.Helper()
	for _, backend := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(backend), func(t *testing.T) {
			opts := harnessOpts{}
			if backend == store.EnginePostgres {
				pg := enginetest.IsolatedPostgres(t)
				opts = harnessOpts{engine: backend, dsn: pg.App, adminDSN: pg.Admin}
			}
			run(t, newHarnessWith(t, opts))
		})
	}
}

// The caller records offsets as it writes generated replacements, then shifts
// them only while composing the pre-pattern-clean reason. No marker discovery.
func generatedApprovalReason(prefix string) (string, []redact.GeneratedMaskSpan) {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("PASSWORD=")
	start := b.Len()
	b.WriteString("[secret env/password]")
	masks := []redact.GeneratedMaskSpan{{Start: start, End: b.Len()}}
	b.WriteString(" curl --token=")
	start = b.Len()
	b.WriteString("[secret env/token]")
	masks = append(masks, redact.GeneratedMaskSpan{Start: start, End: b.Len()})
	b.WriteString(" https://example.test")
	return b.String(), masks
}

func TestEngineApprovalReasonPreservesOnlyGeneratedMasks(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "reason-masks")
		principal := cancellationSession(t, h, tenant, admin, "reason-masks")
		service := h.gov.EngineApprovals()
		for _, prefix := range []string{"Bash command: ", "api_key=fixture-outside-value\nBash command: "} {
			reason, masks := generatedApprovalReason(prefix)
			expected := "Bash command: PASSWORD=[secret env/password] curl --token=[secret env/token] https://example.test"
			if strings.HasPrefix(prefix, "api_key=") {
				expected = "api_key=[REDACTED]\n" + expected
			}
			approved, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{
				Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "reviewed-command",
				SessionRef: principal.SessionIdentity, Reason: reason, ReasonMasks: masks,
			})
			if err != nil || approved.Reason != expected {
				t.Fatalf("reviewer preview differs from the proven display: got=%q error=%v", approved.Reason, err)
			}
			stored, err := service.Read(t.Context(), tenant, approved.ID)
			if err != nil || stored.Reason != expected {
				t.Fatalf("stored preview=%q error=%v", stored.Reason, err)
			}
			var meta string
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(t.Context(), 1, func(e model.AuditEvent) error {
					if e.Action == "governance.approval.create" && e.TargetID.String() == approved.ID {
						encoded, _ := json.Marshal(e.Meta)
						meta = string(encoded)
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if meta == "" || strings.Contains(meta, "fixture-outside-value") || strings.Contains(meta, "ReasonMasks") {
				t.Fatalf("approval audit retained reason or provenance: %q", meta)
			}
		}
	})
}

func TestEngineApprovalReasonRefusesInvalidMaskProvenance(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "reason-mask-refusal")
		principal := cancellationSession(t, h, tenant, admin, "invalid-mask")
		service := h.gov.EngineApprovals()
		reason, masks := generatedApprovalReason("Bash command: ")
		for _, bad := range [][]redact.GeneratedMaskSpan{
			{{Start: -1, End: 4}},
			{{Start: 0, End: len(reason) + 1}},
			{{Start: 0, End: len("Bash")}},
			{masks[0], masks[0]},
			{masks[1], masks[0]},
		} {
			if _, err := service.Request(t.Context(), tenant, principal, governance.ApprovalRequest{
				Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "bad-mask",
				SessionRef: principal.SessionIdentity, Reason: reason, ReasonMasks: bad,
			}); err == nil {
				t.Fatalf("invalid mask provenance created a human request: %+v", bad)
			}
		}
		items, _, err := service.List(t.Context(), tenant, "claude.tool.use", "pending", "")
		if err != nil || len(items) != 0 || contains(h.auditActions(tenant), "governance.approval.create") {
			t.Fatalf("invalid provenance left a request/effect: items=%d error=%v", len(items), err)
		}
	})
}

func TestApprovalReasonMaskProvenanceIsInternalOnly(t *testing.T) {
	onReasonMaskStores(t, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "reason-mask-wire")
		_, requester := h.roleUser(admin, tenant, "reason-mask-requester@x.io", "editor")
		reason, masks := generatedApprovalReason("Bash command: ")
		encoded, err := json.Marshal(governance.ApprovalRequest{Reason: reason, ReasonMasks: masks})
		if err != nil || strings.Contains(string(encoded), "ReasonMasks") || strings.Contains(string(encoded), "reason_masks") || strings.Contains(string(encoded), "Start") {
			t.Fatalf("internal provenance escaped into request JSON: %s error=%v", encoded, err)
		}
		for _, key := range []string{"ReasonMasks", "reason_masks"} {
			created := h.createApproval(requester, tenant, map[string]any{
				"action": "claude.tool.use", "subject_kind": "claude.tool", "subject_ref": "wire-marker",
				"reason": reason, key: masks,
			})
			if created.code == http.StatusBadRequest {
				continue // A strict decoder can refuse the unknown internal field.
			}
			if created.code != http.StatusCreated {
				t.Fatalf("ordinary REST request=%d %s", created.code, created.raw)
			}
			preview, _ := created.body["reason"].(string)
			if strings.Contains(preview, "[secret env/password]") || strings.Contains(preview, "[secret env/token]") {
				t.Fatalf("REST supplied a generated-marker exemption: %q", preview)
			}
		}
	})
}
