// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package eventing

import (
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// TestSinkFormatsAreTheCatalogEventingSubset: the allow-list DERIVES from the
// sdk/siemwire catalog, and this test pins the relationship in both directions
// plus the one deliberate asymmetry against the ledger registry. Before the
// catalog, this test asserted "eventing == ledger registry + json"; the catalog
// remap made that relationship subset MATH rather than a coincidence of two
// hand-maintained lists: eventing == (ledger − otlp_log_record) + json.
func TestSinkFormatsAreTheCatalogEventingSubset(t *testing.T) {
	set := siemwire.EventingSinkFormats()
	for _, f := range set.Tokens() {
		if !validSinkFormat(string(f)) {
			t.Errorf("sink rejects %q, which the catalog's eventing subset declares", f)
		}
	}
	if got, want := len(sinkFormats), len(set.Tokens()); got != want {
		t.Errorf("sinkFormats has %d entries, want %d from the catalog; got %v",
			got, want, sinkFormatKeys())
	}
	if validSinkFormat("not-a-format") {
		t.Error("the allow-list must stay closed")
	}
	// "" means "use the per-sink default" and must stay allowed; the default the
	// engine applies is the catalog's surface default.
	if !validSinkFormat("") {
		t.Error("an unset format must fall back to the per-sink default")
	}
	if set.Default() != siemwire.TokenOCSF {
		// OCSF has been the SIEM-sink default since the sink profile shipped.
		t.Errorf("eventing surface default = %q, want ocsf", set.Default())
	}

	// The deliberate asymmetry against the ledger registry, stated as math:
	// every ledger format EXCEPT otlp_log_record is declarable (a sink POSTs one
	// rendered body per event and a bare LogRecord line is not an OTLP /v1/logs
	// request), and json is declarable ONLY here (the structured passthrough
	// modules/siemforward intercepts before any audit encoder runs).
	for _, f := range audit.Formats() {
		declarable := validSinkFormat(string(f))
		if f == audit.FormatOTLPLogRecord {
			if declarable {
				t.Errorf("sink accepts %q — the bare projection is a pull-export capability, excluded by design", f)
			}
			continue
		}
		if !declarable {
			t.Errorf("sink rejects %q, which the ledger export can produce", f)
		}
	}
	if !validSinkFormat("json") {
		t.Error("json (the structured passthrough) must stay declarable")
	}
	if audit.ValidFormat("json") {
		t.Error("json must NOT be a ledger export format — it exists only on the eventing surface")
	}
}

// sinkFormatKeys renders the allow-list in sorted order so a failure message reads
// the same on every run (map iteration order is randomized).
func sinkFormatKeys() []string {
	keys := make([]string, 0, len(sinkFormats))
	for f := range sinkFormats {
		keys = append(keys, f)
	}
	sort.Strings(keys)
	return keys
}
