// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"
)

// E2e: through the REAL composition-root wiring (buildModules + the
// api.Options.Recorder seam + the bus), break-glass activation is captured by
// the privileged-session recorder, the grant is bound to its recording session,
// the replay reconstructs the activation frame, the chain verifies, and the
// forced post-review seals the bound session via the reviewed finding.
func TestBreakGlassRecordingE2E(t *testing.T) {
	h := newHarness(t)
	grant := h.activateBreakGlassE2E(t, "deploy.*", "second approver unreachable (INC-7)")

	// The grant is bound to a recording session (the BindGrant linkage).
	sessions := h.getJSON(h.adminToken, h.tenantA, "/v1/m/recording/sessions?grant="+grant)
	items, _ := sessions["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected exactly one recording session bound to grant %s, got %v", grant, sessions)
	}
	sess := items[0].(map[string]any)
	id := sess["id"].(string)
	if sess["subject_kind"] != "user" || sess["frames_written"].(float64) < 1 {
		t.Fatalf("bound session must capture the activation frame: %v", sess)
	}

	// Replay reconstructs the activation action on the governance surface.
	replay := h.getJSON(h.adminToken, h.tenantA, "/v1/m/recording/sessions/"+id+"/replay?limit=100")
	frames, _ := replay["frames"].(map[string]any)["items"].([]any)
	foundActivate := false
	for _, fr := range frames {
		f := fr.(map[string]any)
		if f["namespace"] == "governance" && f["method"] == "POST" && f["pattern"] == "/breakglass" && f["outcome"] == "allowed" {
			foundActivate = true
		}
	}
	if !foundActivate {
		t.Fatalf("replay must contain the activation frame: %v", frames)
	}

	// The frame chain and its ledger anchors verify.
	verify := h.getJSON(h.adminToken, h.tenantA, "/v1/m/recording/sessions/"+id+"/verify")
	if verify["ok"] != true || verify["anchors_ok"] != true {
		t.Fatalf("verify = %v", verify)
	}

	// Close the emergency loop: revoke + post-review by a DIFFERENT human. A 200
	// means the recorder seal committed in the same transaction as the review.
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+grant+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", code, body)
	}
	_, reviewerTok := h.createApprover(t, "rec-rev@bridge.test")
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+grant+"/review", reviewerTok, h.tenantA, map[string]any{"note": "reviewed with its recording"}); code != http.StatusOK {
		t.Fatalf("review = %d: %s", code, body)
	}

	got := h.getJSON(h.adminToken, h.tenantA, "/v1/m/recording/sessions/"+id)
	if got["status"] != "sealed" || got["seal_reason"] != "breakglass_review" || got["seal_seq"].(float64) <= 0 {
		t.Fatalf("review returned 200 without its committed recording seal: %v", got)
	}
}
