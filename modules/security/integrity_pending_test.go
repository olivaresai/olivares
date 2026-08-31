// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// TestIntegrityVerifyVirginLedgerIsPending is the first-boot case on the security
// console's own verify endpoint. A tenant created minutes ago has no checkpoint —
// the scheduler's interval has not elapsed — and the panel must be able to tell
// that apart from a tampered ledger. `checkpoints_ok` cannot: it is false for both.
func TestIntegrityVerifyVirginLedgerIsPending(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/m/security/integrity/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	// A key IS wired here, so this is not the separate "unavailable" answer.
	if r.body["checkpoints_verified"] != true {
		t.Fatalf("expected checkpoints_verified=true (a key is wired); got %s", r.raw)
	}
	if got := r.body["checkpoint_status"]; got != string(audit.CheckpointStatusPending) {
		t.Fatalf("virgin ledger checkpoint_status = %v, want %q (body %s)", got, audit.CheckpointStatusPending, r.raw)
	}
	if n, _ := r.body["checkpoints"].(float64); n != 0 {
		t.Fatalf("expected zero checkpoints; got %v", r.body["checkpoints"])
	}
	if r.body["checkpoint_reason"] != audit.ReasonNoCheckpoints {
		t.Fatalf("reason = %v, want %q", r.body["checkpoint_reason"], audit.ReasonNoCheckpoints)
	}
	// The chain itself is proven — that is why "pending" is honest here.
	if r.body["chain_ok"] != true {
		t.Fatalf("expected chain_ok=true on a healthy virgin ledger; got %s", r.raw)
	}
}

// TestIntegrityVerifyCheckpointedIsOK is the second answer: a real, verifying
// checkpoint reports "ok", so "pending" is not a value the endpoint gets stuck on.
func TestIntegrityVerifyCheckpointedIsOK(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.do("POST", "/v1/m/security/cases", admin, map[string]any{"title": "x"}, tenantHdr(tenant))
	h.checkpointTenant(tenant)

	r := h.do("GET", "/v1/m/security/integrity/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	if got := r.body["checkpoint_status"]; got != string(audit.CheckpointStatusOK) {
		t.Fatalf("checkpointed ledger checkpoint_status = %v, want %q (body %s)", got, audit.CheckpointStatusOK, r.raw)
	}
	if r.body["checkpoints_ok"] != true {
		t.Fatalf("expected checkpoints_ok=true; got %s", r.raw)
	}
}

// TestIntegrityVerifyForgedCheckpointStaysFailed is what the calm "pending" state
// has to earn on this surface: a checkpoint the engine's key cannot verify — a
// fabricated attestation — must still report "failed", so the console keeps
// painting it red. If this ever reports "pending" or "ok", the fix is a cover-up.
func TestIntegrityVerifyForgedCheckpointStaysFailed(t *testing.T) {
	// A harness whose module verifies against a key that did NOT sign the
	// checkpoint: the same inauthentic-attestation shape as an injected checkpoint.
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	h := newHarness(t, func(_ ed25519.PublicKey) []Option { return []Option{WithCheckpointKey(wrongPub)} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.do("POST", "/v1/m/security/cases", admin, map[string]any{"title": "y"}, tenantHdr(tenant))

	// Before the checkpoint exists this very tenant is "pending" — the calm state.
	pre := h.do("GET", "/v1/m/security/integrity/verify", admin, nil, tenantHdr(tenant))
	if got := pre.body["checkpoint_status"]; got != string(audit.CheckpointStatusPending) {
		t.Fatalf("precondition: checkpoint_status = %v, want pending (%s)", got, pre.raw)
	}

	h.checkpointTenant(tenant)

	post := h.do("GET", "/v1/m/security/integrity/verify", admin, nil, tenantHdr(tenant))
	if post.code != http.StatusOK {
		t.Fatalf("verify = %d %s", post.code, post.raw)
	}
	if got := post.body["checkpoint_status"]; got != string(audit.CheckpointStatusFailed) {
		t.Fatalf("forged checkpoint checkpoint_status = %v, want %q (body %s)", got, audit.CheckpointStatusFailed, post.raw)
	}
	if post.body["checkpoints_ok"] != false {
		t.Fatalf("forged checkpoint must not report checkpoints_ok=true; got %s", post.raw)
	}
	if reason, _ := post.body["checkpoint_reason"].(string); reason == "" || reason == audit.ReasonNoCheckpoints {
		t.Fatalf("forged checkpoint reason = %q, want a real failure reason", reason)
	}
	// The same tenant, two states, two answers: the field discriminates.
	if pre.body["checkpoint_status"] == post.body["checkpoint_status"] {
		t.Fatalf("checkpoint_status did not discriminate pending from forged (both %v)", post.body["checkpoint_status"])
	}
}

// TestIntegrityVerifyNoKeyOmitsStatus keeps the fourth answer separate. With no
// checkpoint key wired nothing was verified at all, so the status field must be
// ABSENT rather than reporting a verdict the engine never reached —
// `checkpoints_verified:false` remains the console's "unavailable" signal.
func TestIntegrityVerifyNoKeyOmitsStatus(t *testing.T) {
	h := newHarness(t, func(_ ed25519.PublicKey) []Option { return nil }) // no checkpoint key
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/m/security/integrity/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	if r.body["checkpoints_verified"] != false {
		t.Fatalf("expected checkpoints_verified=false (no key wired); got %s", r.raw)
	}
	if got, present := r.body["checkpoint_status"]; present {
		t.Fatalf("checkpoint_status must be absent when nothing was verified; got %v", got)
	}
}
