// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
)

const hipaaTechnicalDisclaimer = "HIPAA Security Rule technical-safeguards gap report for 45 CFR §164.312(a)-(e). Technical mapping only; NOT a HIPAA compliance certification and NOT legal advice."

type hipaaGapReportDTO struct {
	Framework   string               `json:"framework"`
	Name        string               `json:"name"`
	Authority   string               `json:"authority"`
	GeneratedAt string               `json:"generated_at"`
	Summary     AssessmentSummary    `json:"summary"`
	Controls    []hipaaControlGapDTO `json:"controls"`
	Disclaimer  string               `json:"disclaimer"`
}

type hipaaControlGapDTO struct {
	ControlID   string        `json:"control_id"`
	Citation    string        `json:"citation"`
	Title       string        `json:"title"`
	Status      ControlStatus `json:"status"`
	Requirement string        `json:"requirement"`
	Criterion   string        `json:"criterion"`
	// The two capability lists are partitioned with append, so whichever side is
	// empty for a control is a nil slice; JSONArray keeps that side [] instead of
	// null (a fully-satisfied control had missing_capabilities:null, and a
	// fully-unsatisfied one has present_capabilities:null).
	PresentCapabilities api.JSONArray[CapabilityKey]      `json:"present_capabilities"`
	MissingCapabilities api.JSONArray[CapabilityKey]      `json:"missing_capabilities"`
	Evidence            api.JSONArray[CapabilityEvidence] `json:"evidence"`
	Gap                 string                            `json:"gap,omitempty"`
	RecommendedAction   string                            `json:"recommended_action,omitempty"`
}

func (m *Module) handleHIPAAGapReport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	report, err := m.hipaaGapReport(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (m *Module) hipaaGapReport(ctx context.Context, mc api.ModuleContext) (hipaaGapReportDTO, error) {
	s, err := m.gatherFor(ctx, mc)
	if err != nil {
		return hipaaGapReportDTO{}, err
	}
	fw := hipaaTechnicalFramework()
	fa := assessFramework(fw, evaluateCapabilities(s))
	out := hipaaGapReportDTO{
		Framework:   fw.ID,
		Name:        fw.Name,
		Authority:   fw.Authority,
		GeneratedAt: m.clock.Now().String(),
		Summary:     fa.Summary,
		Disclaimer:  hipaaTechnicalDisclaimer,
	}
	for _, ca := range fa.Controls {
		row := hipaaControlGapDTO{
			ControlID:   ca.ControlID,
			Citation:    hipaaCitation(ca.ControlID),
			Title:       ca.Title,
			Status:      ca.Status,
			Requirement: ca.Requirement,
			Criterion:   ca.Criterion,
			Evidence:    ca.Capabilities,
		}
		for _, ev := range ca.Capabilities {
			if ev.State == EvidencePresent {
				row.PresentCapabilities = append(row.PresentCapabilities, ev.Key)
			} else {
				row.MissingCapabilities = append(row.MissingCapabilities, ev.Key)
			}
		}
		if len(row.MissingCapabilities) > 0 {
			row.Gap = "missing evidence for " + capabilityKeysString(row.MissingCapabilities)
			row.RecommendedAction = hipaaRecommendedAction(ca.ControlID)
		}
		out.Controls = append(out.Controls, row)
	}
	return out, nil
}

func hipaaTechnicalFramework() Framework {
	return Framework{
		ID:         "hipaa_technical_safeguards",
		Name:       "HIPAA Security Rule — Technical Safeguards",
		Version:    "45 CFR §164.312",
		Authority:  "U.S. Department of Health and Human Services (HHS), Office for Civil Rights (OCR)",
		Disclaimer: hipaaTechnicalDisclaimer,
		Controls: []Control{
			{
				ID:          "164.312(a)",
				Title:       "Access control",
				Requirement: "Implement technical policies and procedures for electronic information systems that maintain ePHI to allow access only to authorized persons or software programs.",
				Criterion:   "Engine RBAC, governed identities, observed access edges and least-privilege drift evidence.",
				Capabilities: []CapabilityKey{
					"access_control_rbac",
					"identity_governance",
					"access_observability",
					"least_privilege_drift",
				},
			},
			{
				ID:          "164.312(b)",
				Title:       "Audit controls",
				Requirement: "Implement hardware, software, and procedural mechanisms that record and examine activity in systems that contain or use ePHI.",
				Criterion:   "Append-only audit trail, live hash-chain verification and WORM/SIEM export capability.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_export",
				},
			},
			{
				ID:          "164.312(c)",
				Title:       "Integrity",
				Requirement: "Implement policies and procedures to protect ePHI from improper alteration or destruction.",
				Criterion:   "Tamper-evident audit integrity, immutable evidence design and governed change records.",
				Capabilities: []CapabilityKey{
					"audit_integrity",
					"audit_immutability",
					"change_management",
				},
			},
			{
				ID:          "164.312(d)",
				Title:       "Person or entity authentication",
				Requirement: "Implement procedures to verify that a person or entity seeking access to ePHI is the one claimed.",
				Criterion:   "RBAC/session authentication posture plus governed human and non-human identities.",
				Capabilities: []CapabilityKey{
					"access_control_rbac",
					"identity_governance",
					"secure_defaults",
				},
			},
			{
				ID:          "164.312(e)",
				Title:       "Transmission security",
				Requirement: "Implement technical security measures to guard against unauthorized access to ePHI transmitted over an electronic communications network.",
				Criterion:   "TLS/mTLS transport posture for engine-controlled channels.",
				Capabilities: []CapabilityKey{
					"encryption_transit",
				},
			},
		},
	}
}

func hipaaCitation(id string) string {
	switch id {
	case "164.312(a)":
		return "45 CFR §164.312(a)"
	case "164.312(b)":
		return "45 CFR §164.312(b)"
	case "164.312(c)":
		return "45 CFR §164.312(c)"
	case "164.312(d)":
		return "45 CFR §164.312(d)"
	case "164.312(e)":
		return "45 CFR §164.312(e)"
	default:
		return id
	}
}

func hipaaRecommendedAction(id string) string {
	switch id {
	case "164.312(a)":
		return "Record governed identities/policies and access edges, then review least-privilege drift."
	case "164.312(b)":
		return "Generate at least one tenant audit event and run live audit verification; configure archival/export where required."
	case "164.312(c)":
		return "Record governed deployment/change evidence and verify the audit chain."
	case "164.312(d)":
		return "Provision governed identities and enforce authenticated access paths for users and NHIs."
	case "164.312(e)":
		return "Keep TLS/mTLS enabled on engine-controlled channels and document any external channel outside the plane."
	default:
		return "Collect operational evidence for the missing capabilities."
	}
}

func capabilityKeysString(keys []CapabilityKey) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}
