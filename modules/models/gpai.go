// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// FIN-13 — GPAI model-provider compliance-posture tracking. A
// vendor-neutral control plane that brokers many providers is exactly where a
// customer expects the UPSTREAM general-purpose-AI (GPAI) compliance posture of
// each provider to be visible, as third-party/supplier compliance evidence
// feeding ISO/IEC 42001 A.10.3 (Suppliers) and NIST AI RMF GOVERN-6.1 / MAP-4.1.
//
// It is operator-ATTESTED and HONEST: every field is what the tenant's operator
// recorded about a provider (a "claim"), with a separate `verified` flag set only
// when the operator independently confirmed it against the provider's published
// material — mirroring the residency attestation (compliance/residency.go). The
// module NEVER asserts a provider's conformance it cannot verify (docs/SECURITY-HARDENING.md).
//
// The posture fields track the EU AI Act GPAI obligations and the (voluntary)
// GPAI Code of Practice, verified against the European Commission's published
// guidance (2026-06, recorded in the verification ledger): obligations apply since
// 2025-08-02; Commission enforcement (with fines) applies from 2026-08-02.

import (
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GPAIPostureKind is the per-provider GPAI compliance-posture entity. It lives in
// the modelprovider catalog (this module) and is probed by modules/compliance.
const GPAIPostureKind model.Kind = "models.gpai_posture"

const gpaiPostureTable = "models_gpai_posture"

// GPAI posture columns. The boolean fields are operator-recorded claims; the
// verified flag promotes the whole record from claim to operator-verified.
const (
	colGPAIProvider   = "provider_ref"
	colGPAICoP        = "cop_signatory"
	colGPAITechDocs   = "technical_docs"
	colGPAITrainData  = "training_data_summary"
	colGPAICopyright  = "copyright_policy"
	colGPAIDownstream = "downstream_info"
	colGPAISystemic   = "systemic_risk"
	colGPAISafety     = "safety_report"
	colGPAIVerified   = "verified"
	colGPAIMethod     = "verification_method"
	colGPAIAttestedBy = "attested_by"
	colGPAIAttestedAt = "attested_at"
	colGPAINote       = "note"
)

// registerGPAISchema registers the FIN-13 posture entity (one row per provider
// ref per tenant; the unique index leads with tenant_id).
func registerGPAISchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  GPAIPostureKind,
		Table: gpaiPostureTable,
		Fields: []model.FieldSpec{
			{Name: colGPAIProvider, Kind: model.KindText, Indexed: true},
			{Name: colGPAICoP, Kind: model.KindBool},
			{Name: colGPAITechDocs, Kind: model.KindBool},
			{Name: colGPAITrainData, Kind: model.KindBool},
			{Name: colGPAICopyright, Kind: model.KindBool},
			{Name: colGPAIDownstream, Kind: model.KindBool},
			{Name: colGPAISystemic, Kind: model.KindBool},
			{Name: colGPAISafety, Kind: model.KindBool},
			{Name: colGPAIVerified, Kind: model.KindBool, Indexed: true},
			{Name: colGPAIMethod, Kind: model.KindText, Nullable: true},
			{Name: colGPAIAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colGPAIAttestedAt, Kind: model.KindText, Nullable: true},
			{Name: colGPAINote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name: "models_gpai_posture_uniq", Columns: []string{model.ColTenantID, colGPAIProvider}, Unique: true,
		}},
	})
}

// gpaiPostureDTO is one provider's attested GPAI compliance posture. The fields
// map to the EU AI Act GPAI obligations / Code of Practice commitments.
type gpaiPostureDTO struct {
	ID          string `json:"id,omitempty"`
	ProviderRef string `json:"provider_ref"`
	// CoPSignatory: the provider signed the (voluntary) GPAI Code of Practice.
	CoPSignatory bool `json:"cop_signatory"`
	// TechnicalDocs: the provider published up-to-date technical documentation
	// (the Model Documentation Form).
	TechnicalDocs bool `json:"technical_docs"`
	// TrainingDataSummary: the provider published the public training-data summary
	// (the AI Office template).
	TrainingDataSummary bool `json:"training_data_summary"`
	// CopyrightPolicy: the provider maintains a copyright policy honoring
	// machine-readable rights reservations.
	CopyrightPolicy bool `json:"copyright_policy"`
	// DownstreamInfo: the provider shares the information downstream deployers need.
	DownstreamInfo bool `json:"downstream_info"`
	// SystemicRisk: the model is classified as GPAI with systemic risk.
	SystemicRisk bool `json:"systemic_risk"`
	// SafetyReport: a Safety & Security Model Report exists (systemic-risk models).
	SafetyReport bool `json:"safety_report"`
	// Verified is the honesty flag: false = self-reported claim; true = the
	// operator independently verified the posture against published material.
	Verified bool `json:"verified"`
	// VerificationMethod records HOW it was verified (when Verified).
	VerificationMethod string `json:"verification_method,omitempty"`
	AttestedBy         string `json:"attested_by,omitempty"`
	AttestedAt         string `json:"attested_at,omitempty"`
	Note               string `json:"note,omitempty"`
}

func (d gpaiPostureDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colGPAIProvider: d.ProviderRef, colGPAICoP: d.CoPSignatory, colGPAITechDocs: d.TechnicalDocs,
		colGPAITrainData: d.TrainingDataSummary, colGPAICopyright: d.CopyrightPolicy, colGPAIDownstream: d.DownstreamInfo,
		colGPAISystemic: d.SystemicRisk, colGPAISafety: d.SafetyReport, colGPAIVerified: d.Verified,
		colGPAIMethod: trimClamp(d.VerificationMethod), colGPAIAttestedBy: actor, colGPAIAttestedAt: at,
		colGPAINote: trimClamp(d.Note),
	}
}

func toGPAIPostureDTO(rec model.Record) gpaiPostureDTO {
	return gpaiPostureDTO{
		ID: rec.String(model.ColID), ProviderRef: rec.String(colGPAIProvider),
		CoPSignatory: rec.Bool(colGPAICoP), TechnicalDocs: rec.Bool(colGPAITechDocs),
		TrainingDataSummary: rec.Bool(colGPAITrainData), CopyrightPolicy: rec.Bool(colGPAICopyright),
		DownstreamInfo: rec.Bool(colGPAIDownstream), SystemicRisk: rec.Bool(colGPAISystemic),
		SafetyReport: rec.Bool(colGPAISafety), Verified: rec.Bool(colGPAIVerified),
		VerificationMethod: rec.String(colGPAIMethod), AttestedBy: rec.String(colGPAIAttestedBy),
		AttestedAt: rec.String(colGPAIAttestedAt), Note: rec.String(colGPAINote),
	}
}

// handleListGPAIPosture lists the attested GPAI posture of brokered providers,
// optionally filtered by provider_ref or verified.
func (m *Module) handleListGPAIPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("provider_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colGPAIProvider, v))
	}
	out := listResponse[gpaiPostureDTO]{Items: []gpaiPostureDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(GPAIPostureKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGPAIPostureDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAttestGPAIPosture records (or replaces) a provider's GPAI posture. It is
// an upsert keyed by provider_ref: one posture per provider per tenant. The
// attestation is attributed to the real principal in the audit ledger.
func (m *Module) handleAttestGPAIPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in gpaiPostureDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ProviderRef = trimClamp(in.ProviderRef)
	if in.ProviderRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("provider_ref is required"))
		return
	}
	var out gpaiPostureDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(GPAIPostureKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		existing, page, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colGPAIProvider, in.ProviderRef)}, Limit: 1})
		_ = page
		if err != nil {
			return err
		}
		var rec model.Record
		if len(existing) > 0 {
			rec = existing[0]
			for k, v := range in.toRecord(actor, at) {
				rec[k] = v
			}
			rec, err = repo.Update(r.Context(), rec)
		} else {
			rec, err = repo.Create(r.Context(), in.toRecord(actor, at))
		}
		if err != nil {
			return err
		}
		out = toGPAIPostureDTO(rec)
		return auditOwned(r.Context(), sc, mc, GPAIPostureKind, "attest", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
