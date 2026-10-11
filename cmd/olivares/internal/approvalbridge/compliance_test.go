// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"testing"

	"github.com/olivaresai/olivares/modules/compliance"
)

// --- unit: status mapping + deny-closed encoding -----------------------------

func TestComplianceGateStatusMappingDeniesByDefault(t *testing.T) {
	if complianceGateStatus(Approved) != compliance.GateStatusApproved {
		t.Fatal("approved must map to gate_approved")
	}
	cases := map[string]string{
		Pending: compliance.GateStatusPending,
		// Break-glass is unreachable on this gate (GateOnceNoBreakGlass only) but
		// must NEVER map to approved: no emergency lifts a preservation order.
		BreakGlass: compliance.GateStatusPending,
		Rejected:   compliance.GateStatusRejected,
		Canceled:   compliance.GateStatusRejected,
		Expired:    compliance.GateStatusExpired,
		NoGate:     compliance.GateStatusNoGate,
		"garbage":  compliance.GateStatusNoGate,
		"":         compliance.GateStatusNoGate,
	}
	for in, want := range cases {
		if got := complianceGateStatus(in); got != want {
			t.Fatalf("complianceGateStatus(%q) = %q, want %q", in, got, want)
		}
		if in != Approved && complianceGateStatus(in) == compliance.GateStatusApproved {
			t.Fatalf("%q must not authorize", in)
		}
	}
}
