// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// tenantconfig_test.go pins the deny-closed policy for an operator-configured or
// decision-carried tenant reference: ABSENT is legitimate ("no fixed tenant"), PRESENT
// AND INVALID is refused — where invalid means unparseable, the nil UUID, or the
// reserved SYSTEM tenant.
//
// The system-tenant leg is the one that had no coverage anywhere: model.ParseTenantID
// has an EXPLICIT special case for it (core/model/ids.go:56-58) that returns a nil
// error, and SystemTenantID is non-zero by design (ids.go:28), so the widespread
// `err == nil && !tid.IsZero()` shape admits it.

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestParseBusinessTenantAbsentIsNotAnError: a blank reference is the documented
// "no fixed tenant" configuration (inferenceproxy.go's `tenant` field: "" = infer from
// the credential). It must be reported as ABSENT, never as invalid — conflating the two
// would break every deployment that leaves the field out.
func TestParseBusinessTenantAbsentIsNotAnError(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		tid, present, err := parseBusinessTenant("tenant", raw)
		if err != nil {
			t.Errorf("parseBusinessTenant(%q): err = %v, want nil (absent is legitimate)", raw, err)
		}
		if present {
			t.Errorf("parseBusinessTenant(%q): present = true, want false", raw)
		}
		if !tid.IsZero() {
			t.Errorf("parseBusinessTenant(%q): tid = %q, want the zero tenant", raw, tid)
		}
	}
}

// TestParseBusinessTenantAcceptsABusinessTenant: the happy path returns the parsed
// tenant and reports it present, with surrounding whitespace trimmed.
func TestParseBusinessTenantAcceptsABusinessTenant(t *testing.T) {
	want := model.NewTenantID()
	for _, raw := range []string{want.String(), "  " + want.String() + "  "} {
		tid, present, err := parseBusinessTenant("tenant", raw)
		if err != nil {
			t.Fatalf("parseBusinessTenant(%q): unexpected err = %v", raw, err)
		}
		if !present {
			t.Errorf("parseBusinessTenant(%q): present = false, want true", raw)
		}
		if tid != want {
			t.Errorf("parseBusinessTenant(%q): tid = %q, want %q", raw, tid, want)
		}
	}
}

// TestParseBusinessTenantRejectsMalformed: an unparseable non-empty value is a
// misconfiguration, not an absence.
func TestParseBusinessTenantRejectsMalformed(t *testing.T) {
	tid, present, err := parseBusinessTenant("tenant", "not-a-uuid")
	if err == nil {
		t.Fatal("parseBusinessTenant(\"not-a-uuid\"): err = nil, want a rejection")
	}
	if present {
		t.Error("a rejected reference must not be reported present")
	}
	if !tid.IsZero() {
		t.Errorf("a rejected reference must yield the zero tenant; got %q", tid)
	}
}

// TestParseBusinessTenantRejectsNilUUID: the all-zero UUID parses cleanly but is the
// "unset" sentinel — present-and-unset is a misconfiguration, not an absence.
func TestParseBusinessTenantRejectsNilUUID(t *testing.T) {
	const nilUUID = "00000000-0000-0000-0000-000000000000"
	if _, err := model.ParseTenantID(nilUUID); err != nil {
		t.Fatalf("premise: ParseTenantID(nil uuid) must succeed for this test to mean anything; got %v", err)
	}
	if _, present, err := parseBusinessTenant("tenant", nilUUID); err == nil || present {
		t.Errorf("nil UUID: present=%v err=%v, want refused", present, err)
	}
}

// TestParseBusinessTenantRejectsSystemTenant is the D2 closer. The premise assertions
// are the point: they prove the OLD shape (`err == nil && !tid.IsZero()`) admits the
// system tenant, so this test cannot pass vacuously.
func TestParseBusinessTenantRejectsSystemTenant(t *testing.T) {
	raw := model.SystemTenantID.String()

	// Premise 1: ParseTenantID returns NO error for the system tenant (ids.go:56-58).
	parsed, perr := model.ParseTenantID(raw)
	if perr != nil {
		t.Fatalf("premise: ParseTenantID(system) must not error; got %v", perr)
	}
	// Premise 2: the system tenant is non-zero (ids.go:28), so IsZero does not filter it.
	if parsed.IsZero() {
		t.Fatal("premise: the system tenant must be non-zero for this defect to exist")
	}

	tid, present, err := parseBusinessTenant("tenant", raw)
	if err == nil {
		t.Fatal("system tenant: err = nil, want a rejection (a reserved tenant is not a business tenant)")
	}
	if present {
		t.Error("the system tenant must not be reported present")
	}
	if !tid.IsZero() {
		t.Errorf("a rejected system tenant must yield the zero tenant; got %q", tid)
	}
}

// TestParseBusinessTenantErrorNamesFieldAndValue: the operator has to be able to find
// the typo. An error that says only "invalid tenant" costs a grep of the whole config.
func TestParseBusinessTenantErrorNamesFieldAndValue(t *testing.T) {
	// Assembled from pieces so a literal-rewriting sweep cannot make this pass vacuously.
	field := "voice_call." + "tenant"
	value := "zzzz" + "-broken"
	_, _, err := parseBusinessTenant(field, value)
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("error %q does not name the field %q", err, field)
	}
	if !strings.Contains(err.Error(), value) {
		t.Errorf("error %q does not name the offending value %q", err, value)
	}
}
