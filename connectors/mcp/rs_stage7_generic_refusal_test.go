// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// P-3 (hub review of this PR, 2026-08-01), pinned at the ROUTE rather than at the type.
//
// TestCoreMRTRRefusalTotalShape proves coreMRTRRefusal answers correctly for every value
// including one this build does not map. That is necessary and NOT sufficient: it says
// nothing about whether the generic dispatch route ASKS the right question. The route used
// to switch on the value with two named cases and no default, so a third answer fell
// through to the relay path — while elicitation (rs.go:4283) and sampling (rs.go:4478)
// asked ref.refused() and denied it. An open enum consumed by enumeration is fail-open by
// extension, and this stage widened that enum from two values to three.
//
// A fourth class cannot be driven through ServeHTTP without first adding it to production
// code, and a property that can only be checked by editing the thing under test is not
// checked. So refuseCoreMRTR takes the classification as a parameter, and this test hands
// it one the build does not map.
//
// RED FIRST, measured: with refuseCoreMRTR's body replaced by the original
// `switch ref { case coreMRTRUnsanctioned: … case coreMRTRDuplicated: … }; return false`,
// this test fails — 200-with-no-refusal instead of 502, and no audit record.
func TestGenericMRTRRefusesAnUnmappedClass(t *testing.T) {
	const future = coreMRTRDuplicated + 1

	for _, tc := range []struct {
		name string
		ref  coreMRTRRefusal
		want bool
	}{
		{"admitted relays", coreMRTRAdmitted, false},
		{"unsanctioned refuses", coreMRTRUnsanctioned, true},
		{"duplicated refuses", coreMRTRDuplicated, true},
		// The row this file exists for.
		{"a class this build does not map refuses", future, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aud := &capturingAuditor{}
			rs := &ResourceServer{auditor: aud, now: func() time.Time { return time.Unix(0, 0).UTC() }}
			rec := httptest.NewRecorder()

			got := rs.refuseCoreMRTR(context.Background(), rec, rsRequest{Method: "prompts/get"},
				validatedToken{Subject: "s"}, tc.ref, "trace")

			if got != tc.want {
				t.Fatalf("refuseCoreMRTR = %v, want %v (body: %s)", got, tc.want, rec.Body.String())
			}
			if !tc.want {
				if len(aud.decisions) != 0 {
					t.Errorf("an admitted classification recorded a decision: %+v", aud.decisions)
				}
				return
			}
			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", rec.Code)
			}
			if len(aud.decisions) != 1 {
				t.Fatalf("refusals must leave exactly one record; got %d", len(aud.decisions))
			}
			d := aud.decisions[0]
			if d.Allowed {
				t.Error("a refusal recorded an ALLOWED decision")
			}
			// Its OWN controlled class, never another's — the record is what the ledger
			// and every downstream query key on.
			if !strings.Contains(d.Reason, tc.ref.reason()) {
				t.Errorf("record %q does not carry the reason class %q", d.Reason, tc.ref.reason())
			}
			// M-1R2: a refusal that is not the single-exact-literal case may never affirm
			// that literal, on the wire or in the ledger.
			if tc.ref != coreMRTRUnsanctioned {
				if strings.Contains(rec.Body.String(), "input_required") {
					t.Errorf("the wire affirms a literal this refusal did not observe: %s", rec.Body.String())
				}
				if strings.Contains(d.Reason, "input_required") {
					t.Errorf("the record affirms a literal this refusal did not observe: %q", d.Reason)
				}
			}
		})
	}
}
