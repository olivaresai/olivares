// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package eventing

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestOTLPRemapWarningFiresOncePerAffectedSubscription pins the pre-1.0
// breaking-correction safeguard: exactly the stored subscriptions whose
// sink_format is the exact spelling "otlp" AND that deliver audit.recorded
// events get ONE structured warning naming the old and new wire shape — never
// a repeat, never for the alias spelling (whose meaning did not change), never
// for other formats or event types, and never a panic without a logger.
func TestOTLPRemapWarningFiresOncePerAffectedSubscription(t *testing.T) {
	var buf bytes.Buffer
	m := &Module{log: slog.New(slog.NewTextHandler(&buf, nil))}

	// Unaffected combinations: no warning.
	m.warnOTLPRemapOnce("sub-a", "finding.reported", "otlp")
	m.warnOTLPRemapOnce("sub-a", "audit.recorded", "otlp_envelope")
	m.warnOTLPRemapOnce("sub-a", "audit.recorded", "ocsf")
	if buf.Len() != 0 {
		t.Fatalf("warning fired for an unaffected combination: %s", buf.String())
	}

	// The affected combination warns once, with the shapes named.
	m.warnOTLPRemapOnce("sub-a", "audit.recorded", "otlp")
	first := buf.String()
	if first == "" {
		t.Fatal("no warning for a stored otlp subscription delivering audit.recorded")
	}
	for _, want := range []string{"sub-a", "bare LogRecord projection", "ExportLogsServiceRequest envelope", "otlp_log_record"} {
		if !strings.Contains(first, want) {
			t.Errorf("warning missing %q: %s", want, first)
		}
	}

	// Repeats for the same subscription stay silent; a different subscription
	// gets its own single warning.
	m.warnOTLPRemapOnce("sub-a", "audit.recorded", "otlp")
	if buf.String() != first {
		t.Fatalf("the warning repeated for the same subscription:\n%s", buf.String())
	}
	m.warnOTLPRemapOnce("sub-b", "audit.recorded", "otlp")
	if got := strings.Count(buf.String(), "sub-b"); got != 1 {
		t.Fatalf("want exactly one warning for sub-b, got %d:\n%s", got, buf.String())
	}

	// A module with no logger must not panic (the field is nil until the
	// runtime wires the host logger).
	bare := &Module{}
	bare.warnOTLPRemapOnce("sub-c", "audit.recorded", "otlp")
}
