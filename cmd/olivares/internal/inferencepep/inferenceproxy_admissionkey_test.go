// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/sdk"
)

// TestProxyAdmissionIntegrityRefusalIsAReservationFault: admission refuses a key whose row
// failed its integrity check in every posture. The proxy answers 503 with that reason, no
// retry, and classes it as a reservation fault: no hold could be taken, and no policy
// refused the call.
func TestProxyAdmissionIntegrityRefusalIsAReservationFault(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	bg.bc = finops.BudgetCheck{Allowed: false, Action: "block", Reason: finops.ReasonAdmissionIntegrity}
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable || dec.Reason != finops.ReasonAdmissionIntegrity ||
		dec.Headers["x-should-retry"] != "false" {
		t.Fatalf("integrity refusal = %+v, want 503 %q with no retry", dec, finops.ReasonAdmissionIntegrity)
	}
	_, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer")
	if ok || deny.code != gateCodeBudget || deny.class != sdk.FailureReservationFault {
		t.Fatalf("integrity deny = code %q class %q, want %q %q", deny.code, deny.class, gateCodeBudget, sdk.FailureReservationFault)
	}
}
