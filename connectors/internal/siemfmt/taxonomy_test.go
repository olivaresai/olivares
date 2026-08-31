// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// taxonomyNotification mirrors what the notify bridge emits for a multi-taxonomy
// finding: the three framework axes ride as comma-joined Fields keys.
func taxonomyNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "indirect prompt injection",
		Severity: model.SeverityCritical,
		Tenant:   "acme",
		Fields: map[string]string{
			"kind":      "guardrail",
			"owasp_llm": "LLM01:2025",
			"owasp_asi": "ASI01",
			"atlas":     "AML.T0051.001",
		},
		Time: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC),
	}
}

// TestTaxonomyAxesPropagateToEveryFormat verifies the DoD: an indirect-injection
// finding's three taxonomy axes (LLM01:2025 + ASI01 + AML.T0051.001) reach EVERY SIEM
// format siemfmt emits — the text formats carry them as fields, OCSF under `unmapped`,
// ASIM under AdditionalFields — so no SOC loses a framework dimension.
func TestTaxonomyAxesPropagateToEveryFormat(t *testing.T) {
	n := taxonomyNotification()
	want := []string{"LLM01:2025", "ASI01", "AML.T0051.001"}

	checks := map[string]string{
		"CEF":        CEF(DefaultDevice(), n),
		"LEEF":       LEEF(DefaultDevice(), n),
		"Syslog5424": Syslog5424(DefaultDevice(), SyslogOptions{}, n),
	}
	if b, err := OTLPLogJSON(DefaultDevice(), n); err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	} else {
		checks["OTLP"] = string(b)
	}
	if b, err := OCSF(DefaultDevice(), n); err != nil {
		t.Fatalf("OCSF: %v", err)
	} else {
		checks["OCSF"] = string(b)
	}
	if b, err := ASIMAgentEvent(DefaultDevice(), n); err != nil {
		t.Fatalf("ASIM: %v", err)
	} else {
		checks["ASIM"] = string(b)
	}

	for format, out := range checks {
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s output does not carry taxonomy id %q:\n%s", format, w, out)
			}
		}
		// And the axis keys themselves must be present (so a SOC can filter on them).
		for _, key := range []string{"owasp_llm", "owasp_asi", "atlas"} {
			if !strings.Contains(out, key) {
				t.Errorf("%s output is missing axis key %q", format, key)
			}
		}
	}
}
