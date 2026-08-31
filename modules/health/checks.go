// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// createCheckInput declares a monitored subject. Only subject_kind and subject_ref
// are required; cadence/SLA default when omitted.
type createCheckInput struct {
	Name                    string `json:"name"`
	SubjectKind             string `json:"subject_kind"`
	SubjectRef              string `json:"subject_ref"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	SLATargetPPM            int64  `json:"sla_target_ppm"`
	DesiredStatus           string `json:"desired_status"`
}

// updateCheckInput patches a check's configuration (never its subject — the
// subject is the natural key — and never its runtime state).
type updateCheckInput struct {
	Name                    string `json:"name"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	// SLATargetPPM is a POINTER so the patch semantics match every other
	// field: an omitted sla_target_ppm keeps the stored target. The previous
	// int64 form could not distinguish "omitted" from "0" and silently zeroed
	// the SLA target on every partial update (contract fix).
	SLATargetPPM  *int64 `json:"sla_target_ppm"`
	DesiredStatus string `json:"desired_status"`
}

// reportInput is an active probe result an external health-checker or the agent
// itself posts. detail is a short, non-sensitive note (hashed in the ledger).
type reportInput struct {
	State     string `json:"state"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail"`
}

// validSubjectKind reports whether a subject kind is one this module monitors.
func validSubjectKind(k string) bool { return k == subjAgent || k == subjMCP }

// validDesiredStatus reports whether a desired status is one of the lifecycle set.
func validDesiredStatus(s string) bool {
	return s == "active" || s == "paused" || s == "retired"
}

// validReportState reports whether a posted probe state is an observable health
// state (a report cannot post "unknown").
func validReportState(s string) bool {
	return s == stateHealthy || s == stateDegraded || s == stateDown
}

// clampPPM bounds an SLA target to [0, 1_000_000].
func clampPPM(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > ppmFull {
		return ppmFull
	}
	return v
}

// handleCreateCheck declares a monitored subject. Creating a check is a privileged
// write, self-audited in the same transaction. A duplicate subject is a 409.
func (m *Module) handleCreateCheck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createCheckInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if !validSubjectKind(in.SubjectKind) || in.SubjectRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind must be agent|mcp and subject_ref is required"))
		return
	}
	desired := in.DesiredStatus
	if desired == "" {
		desired = "active"
	}
	if !validDesiredStatus(desired) {
		writeJSON(w, http.StatusBadRequest, errorBody("desired_status must be active|paused|retired"))
		return
	}
	interval := in.ExpectedIntervalSeconds
	if interval <= 0 {
		interval = defaultExpectedInterval
	}
	grace := in.GraceFactor
	if grace <= 0 {
		grace = defaultGraceFactor
	}

	rec := model.Record{
		colName:          clamp(in.Name, maxNameLen),
		colSubjectKind:   in.SubjectKind,
		colSubjectRef:    clamp(in.SubjectRef, maxRefLen),
		colExpectedIvl:   interval,
		colGraceFactor:   grace,
		colSLATargetPM:   clampPPM(in.SLATargetPPM),
		colDesiredStat:   desired,
		colOwnerActor:    mc.Principal.Actor(),
		colOwnerActorK:   mc.Principal.ActorKind(),
		colLastState:     stateUnknown,
		colLastLatency:   int64(0),
		colSLABreachOpen: false,
	}

	var dto statusDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toStatusDTO(created)
		return auditEvent(r.Context(), sc, mc, "health.check.create", checkKind, model.ID(dto.ID), map[string]any{
			"subject_kind": dto.SubjectKind, "subject_ref": clamp(dto.SubjectRef, maxRefLen), "sla_target_ppm": dto.SLATargetPPM,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.markSeen(mc.Tenant) // so the sweep scans this tenant even before any edge flows
	writeJSON(w, http.StatusCreated, dto)
}

// handleGetCheck returns one check projected as status.
func (m *Module) handleGetCheck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto statusDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toStatusDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleUpdateCheck patches a check's configuration. Privileged write, self-audited.
func (m *Module) handleUpdateCheck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in updateCheckInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.DesiredStatus != "" && !validDesiredStatus(in.DesiredStatus) {
		writeJSON(w, http.StatusBadRequest, errorBody("desired_status must be active|paused|retired"))
		return
	}
	var dto statusDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if in.Name != "" { // partial patch: an omitted name keeps the existing one
			rec[colName] = clamp(in.Name, maxNameLen)
		}
		if in.ExpectedIntervalSeconds > 0 {
			rec[colExpectedIvl] = in.ExpectedIntervalSeconds
		}
		if in.GraceFactor > 0 {
			rec[colGraceFactor] = in.GraceFactor
		}
		if in.SLATargetPPM != nil { // partial patch: omitted keeps the target
			rec[colSLATargetPM] = clampPPM(*in.SLATargetPPM)
		}
		if in.DesiredStatus != "" {
			rec[colDesiredStat] = in.DesiredStatus
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toStatusDTO(updated)
		return auditEvent(r.Context(), sc, mc, "health.check.update", checkKind, id, map[string]any{
			"desired_status": dto.DesiredStatus, "sla_target_ppm": dto.SLATargetPPM,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleDeleteCheck removes a check (hard delete). Admin-tier, self-audited.
func (m *Module) handleDeleteCheck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "health.check.delete", checkKind, id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// handleReport ingests an active probe result for a check and folds it into the
// subject's health. It is high-frequency automated ingestion (a health-checker
// posting on a cadence), so — like the liveness upserts — it is gated by RBAC at
// write time but NOT self-audited per call (that would flood the ledger). The bus
// finding and SSE snapshot are emitted after the transaction commits.
func (m *Module) handleReport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in reportInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if !validReportState(in.State) {
		writeJSON(w, http.StatusBadRequest, errorBody("state must be healthy|degraded|down"))
		return
	}
	latency := int64(-1)
	if in.LatencyMS >= 0 {
		latency = in.LatencyMS
	}
	at := m.clock.Now().Time()

	var t transition
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(checkKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		t, err = m.applyStateTx(r.Context(), sc, rec, in.State, causeReport, latency, in.Detail, at)
		if err != nil {
			return err
		}
		_, err = repo.Update(r.Context(), rec)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.markSeen(mc.Tenant)
	m.publishTransition(r.Context(), mc.Tenant, t)
	writeJSON(w, http.StatusOK, t.snapshot)
}
