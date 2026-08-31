// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestBuildNotificationProjectsTaxonomy verifies the chokepoint: the notify
// bridge copies a finding's three taxonomy axes onto the Notification.Fields as
// deterministic, comma-joined, sorted id lists under the canonical keys — and omits a
// key for an empty axis.
func TestBuildNotificationProjectsTaxonomy(t *testing.T) {
	m := &Module{}
	s := signal{
		eventType:   "finding.reported",
		kind:        "guardrail",
		severity:    sdkmodel.SeverityCritical,
		subjectKind: "agent",
		subjectRef:  "agent-7",
		title:       "indirect prompt injection",
		// Intentionally unsorted input to prove buildNotification's projection sorts it.
		owaspLLM: []string{"LLM01:2025"},
		owaspASI: []string{"ASI01"},
		atlas:    []string{"AML.T0051.001"},
	}
	n := m.buildNotification(model.TenantID("acme"), s, pending{routeName: "r", dedupKey: "d"})

	if n.Fields[sdkmodel.FieldOWASPLLM] != "LLM01:2025" {
		t.Errorf("owasp_llm = %q, want LLM01:2025", n.Fields[sdkmodel.FieldOWASPLLM])
	}
	if n.Fields[sdkmodel.FieldOWASPASI] != "ASI01" {
		t.Errorf("owasp_asi = %q, want ASI01", n.Fields[sdkmodel.FieldOWASPASI])
	}
	if n.Fields[sdkmodel.FieldATLAS] != "AML.T0051.001" {
		t.Errorf("atlas = %q, want AML.T0051.001", n.Fields[sdkmodel.FieldATLAS])
	}
}

// TestBuildNotificationOmitsEmptyAxes verifies a finding with no framework reference
// emits no taxonomy keys (byte-identical to pre for untagged findings).
func TestBuildNotificationOmitsEmptyAxes(t *testing.T) {
	m := &Module{}
	s := signal{eventType: "finding.reported", kind: "anomaly", title: "drift"}
	n := m.buildNotification(model.TenantID("acme"), s, pending{routeName: "r", dedupKey: "d"})
	for _, k := range []string{sdkmodel.FieldOWASPLLM, sdkmodel.FieldOWASPASI, sdkmodel.FieldATLAS} {
		if _, ok := n.Fields[k]; ok {
			t.Errorf("untagged finding emitted taxonomy key %q", k)
		}
	}
}

// TestJoinTaxonomyDeterministic verifies the join sorts, de-dups and drops empties so
// the SIEM output is byte-stable regardless of producer ordering.
func TestJoinTaxonomyDeterministic(t *testing.T) {
	got := joinTaxonomy([]string{"ASI02", "ASI01", "ASI02", ""})
	if got != "ASI01,ASI02" {
		t.Fatalf("joinTaxonomy = %q, want ASI01,ASI02", got)
	}
	if joinTaxonomy(nil) != "" {
		t.Fatal("joinTaxonomy(nil) must be empty")
	}
}
