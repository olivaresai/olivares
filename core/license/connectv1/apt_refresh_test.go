// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package connectv1

import (
	"strings"
	"testing"
)

// The literals below are the Interface's wire names (Q3 r2 §3.1.1.1), written out so a renamed constant
// cannot move the wire with it.

// TestAptRefreshIsBoundToItsOwnRoute: the challenge operation apt-refresh is bound to POST
// /connect/apt-refresh only, so its challenge cannot be spent on /connect/refresh.
func TestAptRefreshIsBoundToItsOwnRoute(t *testing.T) {
	m, p, err := IntendedRoute("apt-refresh", "dep_1")
	if err != nil || m != "POST" || p != "/connect/apt-refresh" {
		t.Fatalf("IntendedRoute(apt-refresh) = %s %s %v, want POST /connect/apt-refresh", m, p, err)
	}
	if _, rp, _ := IntendedRoute("refresh", "dep_1"); rp == p {
		t.Fatal("refresh and apt-refresh share a route")
	}
}

// TestAptRefreshRefusalCodesAreKnown: the service's set refusal (R11) is in the vocabulary, so the
// handoff names it instead of unknown.
func TestAptRefreshRefusalCodesAreKnown(t *testing.T) {
	c := ErrorCode("security_set_unresolved")
	if !c.Known() || c.Display() != c {
		t.Fatalf("%s is not a known connect-v1 code (displayed %q)", c, c.Display())
	}
	for _, known := range []ErrorCode{"binding_denied", "authority_denied", "credential_reissue_required", "generation_stale",
		"operation_conflict", "authority_unavailable", "provider_adapter_missing", "proof_invalid"} {
		if !known.Known() {
			t.Errorf("%s is not known", known)
		}
	}
}

// TestRefusalReasonReadsOneToken (F3): a refusal body's reason is read only as one lowercase token of at
// most 64 bytes. Anything else reads as no reason, on which the product never acts as R7.
func TestRefusalReasonReadsOneToken(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, tc := range []struct{ body, want string }{
		{`{"error":"authority_denied","action":"x","reason":"revoked"}`, "revoked"},
		{`{"error":"authority_denied","action":"x","reason":"provenance_unproven"}`, "provenance_unproven"},
		{`{"error":"authority_denied","action":"x","reason":"` + long + `"}`, long},
		{`{"error":"authority_denied","action":"x"}`, ""},
		{`{"error":"authority_denied","reason":null}`, ""},
		{`{"error":"authority_denied","reason":7}`, ""},
		{`{"error":"authority_denied","reason":""}`, ""},
		{`{"error":"authority_denied","reason":"Revoked"}`, ""},
		{`{"error":"authority_denied","reason":"revoked refunded"}`, ""},
		{`{"error":"authority_denied","reason":" revoked"}`, ""},
		{`{"error":"authority_denied","reason":"revoked\n"}`, ""},
		{`{"error":"authority_denied","reason":"` + long + `a"}`, ""},
	} {
		obj, err := ParseStrictObject([]byte(tc.body), BodyMax)
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if got := RefusalReason(obj); got != tc.want {
			t.Errorf("RefusalReason(%s) = %q, want %q", tc.body, got, tc.want)
		}
	}
}
