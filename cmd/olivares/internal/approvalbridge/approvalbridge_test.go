// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/voice"
)

// --- unit: deny-closed encoding + status mapping ----------------------------------

func TestApprovalBridgePlanBindingRoundTrip(t *testing.T) {
	cases := []struct{ subject, plan string }{
		{"svc/api", "a1b2c3d4e5f6"},
		{"agent-7", ""},                    // security posture: no plan hash
		{"weird#plan=looking", "deadbeef"}, // subject contains the marker; the appended hash still wins
	}
	for _, c := range cases {
		enc := EncodeSubjectRef(c.subject, c.plan)
		if got := decodePlanHash(enc); got != c.plan {
			t.Fatalf("decodePlanHash(encode(%q,%q))=%q, want %q", c.subject, c.plan, got, c.plan)
		}
	}
	// With no plan hash the subject is stored verbatim (so a human sees a clean subject).
	if enc := EncodeSubjectRef("pii", ""); enc != "pii" {
		t.Fatalf("empty-plan encode = %q, want verbatim", enc)
	}
}

func TestApprovalBridgeStatusMappingDeniesByDefault(t *testing.T) {
	// Only "approved" authorizes; every other value — including unknown, canceled and
	// the empty zero value — is a deny, for all three two-phase gates.
	if deployGateStatus(Approved) != deploy.StatusApproved {
		t.Fatal("approved must map to StatusApproved")
	}
	for _, s := range []string{Pending, Rejected, Canceled, Expired, NoGate, "garbage", ""} {
		if (deploy.GateDecision{Status: deployGateStatus(s)}).Allowed() {
			t.Fatalf("deploy %q must not be allowed", s)
		}
		if (orchestration.GateDecision{Status: orchestrationGateStatus(s)}).Allowed() {
			t.Fatalf("orchestration %q must not be allowed", s)
		}
		if (voice.GateDecision{Status: voiceGateStatus(s)}).Allowed() {
			t.Fatalf("voice %q must not be allowed", s)
		}
	}
	// security: only approved is Approved; no_gate is the one ungoverned case.
	if !securityDecision(Approved).Approved || !securityDecision(Approved).Governed {
		t.Fatal("approved must be approved+governed")
	}
	for _, s := range []string{Pending, Rejected, Canceled, Expired, NoGate, "garbage"} {
		if securityDecision(s).Approved {
			t.Fatalf("security %q must not approve", s)
		}
	}
	if securityDecision(NoGate).Governed {
		t.Fatal("no_gate (unconfigured) must be reported ungoverned")
	}
	if !securityDecision(Pending).Governed {
		t.Fatal("a real pending decision is governed")
	}
}

func TestApprovalBridgeApprovedGrantWindow(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	b := &Bridge{Clock: func() time.Time { return base }}
	cred := ServiceCred{ExpiresIn: 3600} // 1h grant window
	fresh := ApprovalView{Status: Approved, DecidedAt: model.NewTimestamp(base.Add(-30 * time.Minute)).String()}
	stale := ApprovalView{Status: Approved, DecidedAt: model.NewTimestamp(base.Add(-2 * time.Hour)).String()}

	// Pending is always reusable (idempotent open). Approved is reusable ONLY for the
	// one-shot security gate (reuseApproved) AND only inside its grant window.
	if !b.reusable(cred, ApprovalView{Status: Pending}, false) {
		t.Fatal("pending must be reusable")
	}
	if b.reusable(cred, fresh, false) {
		t.Fatal("two-phase gate must NOT reuse an approved approval")
	}
	if !b.reusable(cred, fresh, true) {
		t.Fatal("security gate must reuse a fresh approved grant")
	}
	if b.reusable(cred, stale, true) {
		t.Fatal("an approved grant past its window must NOT be reused (time-box)")
	}
	// Fail-closed: an unparseable/empty decided_at or a zero window is never reusable.
	if b.reusable(cred, ApprovalView{Status: Approved, DecidedAt: "garbage"}, true) {
		t.Fatal("unparseable decided_at must fail closed")
	}
	if b.reusable(cred, ApprovalView{Status: Approved, DecidedAt: ""}, true) {
		t.Fatal("empty decided_at must fail closed")
	}
	if b.reusable(ServiceCred{ExpiresIn: 0}, fresh, true) {
		t.Fatal("a zero grant window must never reuse an approved approval")
	}
	// Terminal states are never reusable.
	for _, s := range []string{Rejected, Expired, Canceled, "garbage", ""} {
		if b.reusable(cred, ApprovalView{Status: s, DecidedAt: fresh.DecidedAt}, true) {
			t.Fatalf("status %q must not be reusable", s)
		}
	}
}
