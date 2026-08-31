// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"encoding/json"
	"testing"
)

// The whole contract's safety rests on the zero value being a DENY: a PDP (or a decoding
// error) that yields a zero DecisionVerdict must never read as allowed.
func TestDecisionVerdictZeroValueDenies(t *testing.T) {
	var v DecisionVerdict
	if v.IsAllowed() {
		t.Fatal("zero-value DecisionVerdict must deny (IsAllowed=false)")
	}
	// A verdict decoded from an empty/omitted decision field is also a deny.
	if err := json.Unmarshal([]byte(`{"decision_id":"d1"}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.IsAllowed() {
		t.Fatal("verdict with omitted decision must deny")
	}
}

func TestIsAllowed(t *testing.T) {
	for _, tc := range []struct {
		d    Decision
		want bool
	}{
		{DecisionAllow, true},
		{DecisionModify, true},
		{DecisionDeny, false},
		{"", false},
		{"garbage", false}, // an unknown decision fails closed
	} {
		if got := (DecisionVerdict{Decision: tc.d}).IsAllowed(); got != tc.want {
			t.Errorf("IsAllowed(%q) = %v, want %v", tc.d, got, tc.want)
		}
	}
}

// A verdict must round-trip through JSON with the wire field names the cross-language PEPs
// (e.g. the Python LiteLLM callback) depend on.
func TestDecisionVerdictJSONWire(t *testing.T) {
	in := DecisionVerdict{
		ProtocolVersion: ProtocolVersion, Decision: DecisionModify, DecisionID: "dec-1", Nonce: "n1",
		ReasonCode: "prompt_dlp_redacted", EffectiveRequestDigest: "abc", ReservationID: "r1",
		Obligations: []Obligation{{ID: "o1", Kind: "redact_request", Required: true, Phase: PhasePreForward}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"decision":"modify"`, `"decision_id":"dec-1"`, `"reason_code"`, `"effective_request_digest"`, `"reservation_id"`, `"obligations"`} {
		if !contains(string(b), key) {
			t.Errorf("wire JSON missing %s: %s", key, b)
		}
	}
	var out DecisionVerdict
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.IsAllowed() || out.DecisionID != "dec-1" || len(out.Obligations) != 1 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
