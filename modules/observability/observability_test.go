// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"testing"
	"time"
)

// TestModuleIdentity pins the module's registry identity and the declared
// permission set (the verb-tier grants the engine derives from them).
func TestModuleIdentity(t *testing.T) {
	m := New()
	if d := m.Descriptor(); d.Name != "olivares.observability" {
		t.Fatalf("Descriptor().Name = %q", d.Name)
	}
	if m.APINamespace() != "observability" {
		t.Fatalf("APINamespace() = %q", m.APINamespace())
	}
	perms := m.Permissions()
	want := map[string]bool{
		"observability:health:read":      true,
		"observability:traces:read":      true,
		"observability:attestation:read": true,
	}
	if len(perms) != len(want) {
		t.Fatalf("Permissions() = %v", perms)
	}
	for _, p := range perms {
		if !want[string(p)] {
			t.Fatalf("unexpected permission %q", p)
		}
	}
}

// TestHelperValidation covers the hex/clamp/format helpers' edges.
func TestHelperValidation(t *testing.T) {
	if !isLowerHex("4bf92f3577b34da6a3ce929d0e0e4736", 32) {
		t.Fatal("valid trace id rejected")
	}
	for _, bad := range []string{"", "4BF92F3577B34DA6A3CE929D0E0E4736", "4bf92f3577b34da6a3ce929d0e0e473", "zzf92f3577b34da6a3ce929d0e0e4736"} {
		if isLowerHex(bad, 32) {
			t.Fatalf("invalid id %q accepted", bad)
		}
	}
	// The cap INCLUDES the ellipsis marker: 3 runes total, cut on rune
	// boundaries (never bytes).
	if got := clampStr("héllo", 3); got != "hé…" {
		t.Fatalf("clampStr = %q (cap includes the marker; runes, not bytes)", got)
	}
	if got := clampStr("hél", 3); got != "hél" {
		t.Fatalf("clampStr = %q (at-cap input must pass through uncut)", got)
	}
	if rfc3339(time.Time{}) != "" {
		t.Fatal("zero instant must format empty (omitempty stays absent), never the epoch")
	}
	if got := rfc3339(time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)); got != "2026-06-12T12:00:00Z" {
		t.Fatalf("rfc3339 = %q", got)
	}
}

// TestTraceMetaParsing covers the canonical-meta fast path and validation.
func TestTraceMetaParsing(t *testing.T) {
	cases := []struct {
		meta    string
		wantOK  bool
		traceID string
		spanID  string
	}{
		{`{}`, false, "", ""},
		{``, false, "", ""},
		{`{"k":"v"}`, false, "", ""},
		{`{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}`, true, "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		// Valid trace, invalid span: the event still joins the trace window.
		{`{"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"XYZ"}`, true, "4bf92f3577b34da6a3ce929d0e0e4736", ""},
		// Invalid trace id forms are skipped, never normalized.
		{`{"trace_id":"4BF92F3577B34DA6A3CE929D0E0E4736","span_id":"00f067aa0ba902b7"}`, false, "", ""},
		{`{"trace_id":"short"}`, false, "", ""},
		{`not json`, false, "", ""},
	}
	for _, c := range cases {
		traceID, spanID, ok := traceIDsFromMeta(c.meta)
		if ok != c.wantOK || traceID != c.traceID || spanID != c.spanID {
			t.Fatalf("traceIDsFromMeta(%q) = %q,%q,%v", c.meta, traceID, spanID, ok)
		}
	}
}
