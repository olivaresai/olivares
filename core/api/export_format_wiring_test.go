// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// TestExportTerminatorMatchesTheFormatFamily: the export stream ends with a
// terminator that confirms the consumer got the whole thing. A JSON-per-line
// format must end with a JSON object — a `#` comment line would make the last
// line unparseable for exactly the consumers that parse every line. This is
// anchored to the engine's format list so a new JSON format cannot be added
// without deciding which family it belongs to.
func TestExportTerminatorMatchesTheFormatFamily(t *testing.T) {
	jsonPerLine := map[audit.Format]bool{
		audit.FormatCEF:           false,
		audit.FormatLEEF:          false,
		audit.FormatSyslog:        false,
		audit.FormatOTLP:          true,
		audit.FormatOTLPEnvelope:  true,
		audit.FormatOTLPLogRecord: true,
		audit.FormatOCSF:          true,
	}
	// Driven by the registry, not by the map: a newly registered format with no
	// entry above fails here instead of quietly skipping its assertion.
	for _, f := range audit.Formats() {
		wantJSON, classified := jsonPerLine[f]
		if !classified {
			t.Errorf("format %q is registered but this test does not classify it — "+
				"decide whether its stream terminator is JSON or a comment", f)
			continue
		}
		got := exportTerminator(f, 3, 99)
		isJSON := strings.HasPrefix(got, "{")
		if isJSON != wantJSON {
			t.Errorf("terminator for %q = %q (json=%v), want json=%v", f, got, isJSON, wantJSON)
		}
		if wantJSON {
			// The terminator must be a parseable object, not just start with "{".
			var obj map[string]any
			if err := json.Unmarshal([]byte(got), &obj); err != nil {
				t.Errorf("terminator for %q is not a JSON object: %v (%q)", f, err, got)
			} else if obj["export_complete"] != true {
				t.Errorf("terminator for %q must assert export_complete: %q", f, got)
			}
		}
	}
	if len(jsonPerLine) != len(audit.Formats()) {
		t.Errorf("this test classifies %d formats but the registry has %d",
			len(jsonPerLine), len(audit.Formats()))
	}
}
