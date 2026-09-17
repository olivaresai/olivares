// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strconv"
	"strings"
)

// BudgetEvidenceProducer is the registered module allowed to publish this
// extension. Comparing an untrusted Source string to it does NOT authenticate
// anything: the runtime must enforce ownership at module and source ingress.
const BudgetEvidenceProducer = "olivares.finops"

// BudgetAlertEvidenceSummary carries no money, configuration or raw errors.
// DetailHash on its FindingReport identifies the committed alert envelope, or
// the separate diagnostic domain when Kind is finops_budget_evaluation_incomplete.
// Converters must preserve a present invalid summary, including its zero value.
type BudgetAlertEvidenceSummary struct {
	SchemaVersion uint32   `json:"schema_version"`
	AlertID       string   `json:"alert_id,omitempty"`
	AmountClass   string   `json:"amount_class"`
	Crossing      string   `json:"crossing"`
	Causes        []string `json:"causes,omitempty"`
	DigestVersion uint32   `json:"digest_version"`
}

// Clone copies the new mutable fields before publication or conversion.
func (b *BudgetAlertEvidenceSummary) Clone() *BudgetAlertEvidenceSummary {
	if b == nil {
		return nil
	}
	c := *b
	c.Causes = append([]string(nil), b.Causes...)
	return &c
}

// BudgetEvidenceValidity classifies only the bounded wire shape. It does not
// check origin, tenant, durable row, arithmetic, policy, freshness or authority
// to act. Those checks belong to the receiving runtime and FinOps consumer.
// A future version stays invalid/present; it must never fall back to legacy.
func (f FindingReport) BudgetEvidenceValidity() string {
	b := f.BudgetEvidence
	if b == nil {
		return "absent"
	}
	if b.SchemaVersion != 1 || b.DigestVersion != 1 || !budgetHex(f.DetailHash, 64) ||
		f.SubjectKind != "budget" || len(b.Causes) > 36 {
		return "invalid"
	}
	for i, c := range b.Causes {
		if !budgetEvidenceCause(c) || (i > 0 && b.Causes[i-1] >= c) {
			return "invalid"
		}
	}
	switch f.Kind {
	case "finops_budget", "finops_budget_cap":
		if !budgetEvidenceID(b.AlertID) || !budgetEvidenceID(f.SubjectRef) || b.Crossing != "proven" ||
			(b.AmountClass != "exact" && b.AmountClass != "lower_bound") ||
			(b.AmountClass == "lower_bound" && len(b.Causes) == 0) {
			return "invalid"
		}
	case "finops_budget_evaluation_incomplete":
		if b.AlertID != "" || b.AmountClass != "unknown" || b.Crossing != "unproven" || len(b.Causes) == 0 {
			return "invalid"
		}
		// The existing catalogue diagnostic has no single budget. No other
		// diagnostic may silently omit the subject it claims to describe.
		if !budgetEvidenceID(f.SubjectRef) && !(f.SubjectRef == "" && len(b.Causes) == 1 && b.Causes[0] == "budget_census_truncated") {
			return "invalid"
		}
	default:
		return "invalid"
	}
	return "structurally_valid"
}

// BudgetEvidenceFields is the minimal notification projection. It preserves
// invalid presence without forwarding unvalidated strings. A caller must check
// producer origin separately before forwarding a structurally valid summary.
func (f FindingReport) BudgetEvidenceFields() map[string]string {
	validity := f.BudgetEvidenceValidity()
	if validity == "absent" {
		return nil
	}
	fields := map[string]string{"budget_evidence_validation": validity}
	if validity != "structurally_valid" {
		fields["budget_evidence_amount_class"] = "unknown"
		fields["budget_evidence_crossing"] = "unproven"
		return fields
	}
	b := f.BudgetEvidence
	fields["budget_evidence_schema_version"] = strconv.FormatUint(uint64(b.SchemaVersion), 10)
	fields["budget_evidence_digest_version"] = strconv.FormatUint(uint64(b.DigestVersion), 10)
	fields["budget_evidence_amount_class"] = b.AmountClass
	fields["budget_evidence_crossing"] = b.Crossing
	if b.AlertID != "" {
		fields["budget_evidence_alert_id"] = b.AlertID
	}
	if len(b.Causes) > 0 {
		fields["budget_evidence_causes"] = strings.Join(b.Causes, ",")
	}
	return fields
}

// The engine emits canonical nonzero UUID strings. This checks their bounded
// representation only; it does not resolve an ID or prove any record exists.
func budgetEvidenceID(s string) bool {
	if len(s) != 36 || s == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
		} else if !budgetHex(s[i:i+1], 1) {
			return false
		}
	}
	return true
}

func budgetHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for i := range s {
		if !(s[i] >= '0' && s[i] <= '9') && !(s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// Closed A4.2 v1 vocabulary, including diagnostic/configuration causes. These
// are classifications, never arbitrary store errors or an external attempt state.
func budgetEvidenceCause(c string) bool {
	switch c {
	case "scope_tenant_mismatch", "scope_group_unresolved", "scope_dimension_unsupported",
		"scope_predicate_unsupported", "scope_unresolved", "window_invalid", "cost_read_failed",
		"cost_scan_truncated", "cost_cursor_missing", "cost_cursor_not_advancing", "cost_cursor_cycle",
		"cost_row_malformed", "cost_row_outside_requested_window", "cost_row_other_tenant",
		"cost_row_tenant_malformed", "cost_row_outside_requested_scope", "cost_row_dimension_malformed",
		"cost_row_outside_requested_provenance", "static_reservation_unknown", "static_reservation_negative",
		"dynamic_reservation_unknown", "dynamic_reservation_scan_incomplete", "dynamic_reservation_unverified",
		"dynamic_reservation_negative", "dynamic_reservation_state_unclassified", "threshold_not_finite",
		"threshold_out_of_range", "limit_not_positive", "amount_not_decidable", "budget_census_truncated",
		"threshold_identity_unrepresentable", "budget_limit_malformed", "budget_static_reserved_malformed",
		"budget_static_reserved_negative", "budget_limit_null", "budget_static_reserved_null":
		return true
	default:
		return false
	}
}
