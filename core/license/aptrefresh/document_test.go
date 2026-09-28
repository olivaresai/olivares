// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// handoffFields is the exact field set of the handoff, Interface Q3 r2 §3.10 I5.
var handoffFields = []string{"at", "class", "code", "context", "credential", "cycle", "exp", "invocation", "outcome", "schema", "set", "suites"}

// issuedOnly are the fields that are null unless the outcome is issued.
var issuedOnly = []string{"class", "set", "suites", "context", "exp", "credential"}

func decodeHandoff(t *testing.T, data []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("the handoff is not a JSON object: %v (%q)", err, data)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(handoffFields, ",") {
		t.Fatalf("handoff fields %v, want exactly %v", keys, handoffFields)
	}
	return m
}

func testAnswer() Answer {
	return Answer{Class: "current", Set: "biz+reg", Suites: []string{"entitled-security", "entitled-stable"}, Context: testContext,
		Credential: "oad1.eyJzIjoib2xpdmFyZXMuYWkvYXB0LWRvd25sb2FkL3YxIn0.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Exp: testNow.Unix() + 86400}
}

// TestIssuedHandoffHasExactlyTheInterfaceFields: schema olivares.ai/apt-credential-handoff/v2 with exactly
// the fields of r2 §3.10 I5 and r3's invocation.
func TestIssuedHandoffHasExactlyTheInterfaceFields(t *testing.T) {
	inv := "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	a := testAnswer()
	data, err := Issued(validCycle, &inv, a, testNow).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	m := decodeHandoff(t, data)
	for k, want := range map[string]any{
		"schema": "olivares.ai/apt-credential-handoff/v2", "cycle": validCycle, "invocation": inv, "outcome": "issued",
		"code": nil, "class": "current", "set": "biz+reg", "context": testContext, "credential": a.Credential,
		"exp": json.Number(strconv.FormatInt(testNow.Unix()+86400, 10)), "at": "2026-09-27T03:00:00Z",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	if su, _ := m["suites"].([]any); len(su) != 2 || su[0] != "entitled-security" || su[1] != "entitled-stable" {
		t.Errorf("suites = %v", m["suites"])
	}
}

// TestNotIssuedHandoffNullsEveryIssuedField: class, set, suites, context, exp and credential are null
// unless the outcome is issued; outcome and code name the path.
func TestNotIssuedHandoffNullsEveryIssuedField(t *testing.T) {
	cases := []struct {
		outcome Outcome
		code    string
	}{
		{OutcomeNotBound, ""}, {OutcomePendingOtherOperation, ""}, {OutcomeRefused, "binding_denied"},
		{OutcomeRefused, "authority_denied"}, {OutcomeRefused, "security_set_unresolved"}, {OutcomeUnknown, ""},
		{OutcomeUnknown, "proof_invalid"}, {OutcomeUnavailable, "authority_unavailable"}, {OutcomeUnavailable, ""},
		{OutcomeIdentityMismatch, ""}, {OutcomeBusy, ""},
	}
	for _, tc := range cases {
		data, err := NotIssued(validCycle, nil, tc.outcome, tc.code, testNow).Marshal()
		if err != nil {
			t.Errorf("%s/%s: Marshal: %v", tc.outcome, tc.code, err)
			continue
		}
		m := decodeHandoff(t, data)
		if m["outcome"] != string(tc.outcome) || m["invocation"] != nil || m["cycle"] != validCycle {
			t.Errorf("%s: outcome %v, invocation %v, cycle %v", tc.outcome, m["outcome"], m["invocation"], m["cycle"])
		}
		if tc.code == "" && m["code"] != nil || tc.code != "" && m["code"] != tc.code {
			t.Errorf("%s: code %v, want %q", tc.outcome, m["code"], tc.code)
		}
		for _, k := range issuedOnly {
			if m[k] != nil {
				t.Errorf("%s: %s = %v, want null", tc.outcome, k, m[k])
			}
		}
	}
}

// TestMarshalRefusesABrokenDocument: the writer never publishes a document outside the schema.
func TestMarshalRefusesABrokenDocument(t *testing.T) {
	inv := "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	badInv := strings.ToUpper(inv)
	cred := testAnswer().Credential
	code := "binding_denied"
	cases := map[string]Document{
		"refused with a credential": func() Document {
			d := NotIssued(validCycle, nil, OutcomeRefused, code, testNow)
			d.Credential = &cred
			return d
		}(),
		"issued without a credential": func() Document { d := Issued(validCycle, &inv, testAnswer(), testNow); d.Credential = nil; return d }(),
		"issued with a code":          func() Document { d := Issued(validCycle, &inv, testAnswer(), testNow); d.Code = &code; return d }(),
		"an invalid cycle":            NotIssued("../x", nil, OutcomeUnknown, "", testNow),
		"an invalid invocation":       NotIssued(validCycle, &badInv, OutcomeUnknown, "", testNow),
		"an unknown outcome":          NotIssued(validCycle, nil, Outcome("done"), "", testNow),
		"another schema":              func() Document { d := NotIssued(validCycle, nil, OutcomeBusy, "", testNow); d.Schema = "v1"; return d }(),
		"a credential outside the grammar's alphabet": func() Document {
			d := Issued(validCycle, &inv, testAnswer(), testNow)
			bad := cred + "\n"
			d.Credential = &bad
			return d
		}(),
	}
	for name, d := range cases {
		if data, err := d.Marshal(); err == nil {
			t.Errorf("%s: Marshal accepted %q", name, data)
		}
	}
}

// TestLargestHandoffStaysUnderTheHelperBound: the largest complete handoff — a 1100-byte credential (the
// resource bound of r2 §3.19, above the 1073 bytes the grammar allows) and every other field at its
// maximum — stays under the helper's 4096-byte bound (r3 §3.1.5.5: about 1800 bytes by construction).
func TestLargestHandoffStaysUnderTheHelperBound(t *testing.T) {
	cred := CredentialPrefix + strings.Repeat("A", 1100-len(CredentialPrefix)-44) + "." + strings.Repeat("B", 43)
	if len(cred) != 1100 {
		t.Fatalf("credential fixture is %d bytes", len(cred))
	}
	inv := strings.Repeat("f", 32)
	set := strings.Repeat("z", maxSetBytes)
	class := "security-unresolved"
	ctx := strings.Repeat("e", 32)
	exp := int64(9007199254740991) // connect-v1's largest safe integer
	d := Document{
		Schema: Schema, Cycle: strings.Repeat("f", 32), Invocation: &inv, Outcome: OutcomeIssued,
		Class: &class, Set: &set, Suites: []string{"entitled-security", "entitled-stable"}, Context: &ctx, Exp: &exp,
		Credential: &cred, At: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Format(time.RFC3339),
	}
	data, err := d.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	m := decodeHandoff(t, data)
	if m["credential"] != cred || m["set"] != set {
		t.Fatal("the measured document does not carry the maximal credential and set")
	}
	t.Logf("largest complete handoff: %d bytes (bound %d)", len(data), MaxHandoffBytes)
	if len(data) >= MaxHandoffBytes {
		t.Fatalf("the largest handoff is %d bytes, not under %d", len(data), MaxHandoffBytes)
	}
}
