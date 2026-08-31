// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
)

// checkpointsOf pulls the checkpoints object out of a /v1/audit/verify reply.
func checkpointsOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	cps, ok := r.body["checkpoints"].(map[string]any)
	if !ok {
		t.Fatalf("verify reply carries no checkpoints object: %s", r.raw)
	}
	return cps
}

// TestAuditVerifyVirginLedgerIsPendingNotFailed is the first-boot case measured
// against a clean install: a brand-new tenant has no checkpoint yet because the
// scheduler's interval has not elapsed, and that is CORRECT, not a defect. The
// reply must say so in a field a renderer can act on — `checkpoints.ok` alone
// cannot, because it reads false here and false for a tampered ledger too.
//
// `ok` stays false on purpose (attesting nothing is not a pass); it is
// `status` that carries the third answer.
func TestAuditVerifyVirginLedgerIsPendingNotFailed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	cps := checkpointsOf(t, h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant)))
	if got := cps["status"]; got != string(audit.CheckpointStatusPending) {
		t.Fatalf("virgin ledger checkpoints.status = %v, want %q (body %v)", got, audit.CheckpointStatusPending, cps)
	}
	if cps["count"].(float64) != 0 {
		t.Fatalf("expected zero checkpoints on a virgin ledger; got %v", cps["count"])
	}
	if cps["reason"] != audit.ReasonNoCheckpoints {
		t.Fatalf("virgin ledger reason = %v, want %q", cps["reason"], audit.ReasonNoCheckpoints)
	}
	// The strict boolean is unchanged — flipping it to true would be the same lie
	// in the other direction and would hide a ledger whose checkpoints are gone.
	if cps["ok"] != false {
		t.Fatalf("checkpoints.ok must stay false with nothing attested; got %v", cps["ok"])
	}
	// The overall verdict is still healthy: structural verification proved the chain.
	r := h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant))
	if r.body["ok"] != true {
		t.Fatalf("a structurally intact virgin ledger must verify ok; got %s", r.raw)
	}
}

// TestAuditVerifyCheckpointedLedgerIsOK is the second answer: once a checkpoint
// exists and verifies, the status is "ok" — so "pending" is not a value the
// endpoint gets stuck on.
func TestAuditVerifyCheckpointedLedgerIsOK(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if _, _, err := h.signer.Checkpoint(context.Background(), h.st, tenant); err != nil {
		t.Fatal(err)
	}

	cps := checkpointsOf(t, h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant)))
	if got := cps["status"]; got != string(audit.CheckpointStatusOK) {
		t.Fatalf("checkpointed ledger status = %v, want %q (body %v)", got, audit.CheckpointStatusOK, cps)
	}
	if cps["ok"] != true || cps["count"].(float64) < 1 {
		t.Fatalf("expected a verified checkpoint; got %v", cps)
	}
}

// TestAuditVerifyForgedCheckpointStaysFailed is the assertion the calm "pending"
// state has to earn. A checkpoint written by a key the engine does NOT hold — the
// shape of an attacker who appends an attestation over a rewritten head — must
// still come back failed, with the endpoint's overall verdict false. If this ever
// passes as "pending" or "ok", the fix has become a cover-up.
func TestAuditVerifyForgedCheckpointStaysFailed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	forgeCheckpoint(t, h, tenant)

	r := h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant))
	cps := checkpointsOf(t, r)
	if got := cps["status"]; got != string(audit.CheckpointStatusFailed) {
		t.Fatalf("forged checkpoint status = %v, want %q (body %s)", got, audit.CheckpointStatusFailed, r.raw)
	}
	if cps["ok"] != false {
		t.Fatalf("forged checkpoint must not report ok; got %v", cps)
	}
	if cps["reason"] != "checkpoint-sig-invalid" {
		t.Fatalf("forged checkpoint reason = %v, want checkpoint-sig-invalid", cps["reason"])
	}
	if n, _ := cps["first_bad_seq"].(float64); n <= 0 {
		t.Fatalf("forged checkpoint must name the offending seq; got %v", cps["first_bad_seq"])
	}
	// And the endpoint's headline verdict must go red with it.
	if r.body["ok"] != false {
		t.Fatalf("overall verdict must be false with a forged checkpoint; got %s", r.raw)
	}
}

// TestAuditVerifyForgedCheckpointOnPreviouslyPendingLedger closes the specific
// escape this change could have opened: a ledger that WAS pending and then gains a
// bad checkpoint must flip from calm to loud. It measures both states on the same
// tenant, so a status wired to a constant cannot pass.
func TestAuditVerifyForgedCheckpointOnPreviouslyPendingLedger(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	before := checkpointsOf(t, h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant)))
	if before["status"] != string(audit.CheckpointStatusPending) {
		t.Fatalf("precondition: expected pending, got %v", before)
	}

	forgeCheckpoint(t, h, tenant)

	after := checkpointsOf(t, h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant)))
	if after["status"] != string(audit.CheckpointStatusFailed) {
		t.Fatalf("after forgery: status = %v, want failed (%v)", after["status"], after)
	}
	if before["status"] == after["status"] {
		t.Fatalf("status did not discriminate the two ledgers (both %v)", after["status"])
	}
}

// forgeCheckpoint appends a REAL checkpoint event signed by a key the engine does
// not hold. The event is well-formed and lands through the ordinary append path
// (so the store's own immutability triggers are satisfied) — only its signature is
// unverifiable, which is exactly what a fabricated attestation looks like.
func forgeCheckpoint(t *testing.T, h *harness, tenant model.TenantID) {
	t.Helper()
	_, foreignPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := audit.NewSigner(foreignPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, cerr := foreign.Checkpoint(context.Background(), h.st, tenant); cerr != nil || !ok {
		t.Fatalf("forge checkpoint = (%v, %v)", ok, cerr)
	}
}
