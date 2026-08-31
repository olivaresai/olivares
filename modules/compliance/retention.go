// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the RETENTION engine (contract §3/§6/§7): per-class
// schedules (documented basis + window + disposition), the approval-gated enabling
// of disposition=purge (Sedona pillar 2: destruction runs under an APPROVED,
// repeatable schedule — the approval is at the POLICY, the run is routine), and the
// hold-checked sweep that disposes in bounded batches and seals an append-only
// retention_run certificate + self-audit per class with activity (pillar 3: the log
// of destruction). A legal hold beats ANY purge: tenant/class holds skip the whole
// class, mapped subject holds exclude fine-grained, an unmapped related subject
// hold skips the whole class — over-preservation is always the safe direction.

// Disposition and run-trigger vocabularies (§1/§2).
const (
	dispositionRetain = "retain"
	dispositionPurge  = "purge"

	runTriggerSweep  = "sweep"
	runTriggerManual = "manual"
)

// actionRetentionEnable is the governed action enabling a purge disposition opens
// an approval for (HIGH by default ⇒ ≥1 human approver + SoD; a tenant may raise it
// to CRITICAL by approval policy).
const actionRetentionEnable = "compliance.retention.enable"

// retentionEnableQuorum is the floor the module re-verifies independently of the
// gate: an approved decision with no approver evidence is denied.
const retentionEnableQuorum = 1

// Sweep bounds: a batch is one bounded transaction; the per-class iteration cap
// keeps a single run from scanning unboundedly (governance/sweep.go precedent). A
// capped run seals its certificate with truncated=true and the next run continues
// — deletes are idempotent under the age predicate.
const (
	maxSweepBatch      = 200
	maxSweepIterations = 50
)

// ---- DTOs ----------------------------------------------------------------------

// providerFloor is the §7 Covered-Models annotation carried by classes and policies
// of model_io classes: annotate, never reject — deleting our copy early is
// legitimate; PROMISING total deletion below the floor is not.
type providerFloor struct {
	ProviderFloorDays   int    `json:"provider_floor_days,omitempty"`
	ProviderFloorKnown  bool   `json:"provider_floor_known"`
	ProviderFloorSource string `json:"provider_floor_source,omitempty"`
}

type dataClassDTO struct {
	ID              string            `json:"id"`
	ExtKinds        []string          `json:"ext_kinds,omitempty"`
	AgeColumn       string            `json:"age_column,omitempty"`
	Purgeable       bool              `json:"purgeable"`
	ModelIO         bool              `json:"model_io"`
	RecommendedDays int               `json:"recommended_days,omitempty"`
	SubjectKinds    []string          `json:"subject_kinds,omitempty"`
	SubjectColumns  map[string]string `json:"subject_columns,omitempty"`
	Note            string            `json:"note,omitempty"`
	providerFloor
	// RegulatoryFloor is the enterprise retention floor in force for this class
	// (SEC 17a-4 / FINRA 4511 / CFTC 1.31); nil in the open-core build or when the
	// class carries no named floor.
	RegulatoryFloor *RetentionFloor `json:"regulatory_floor,omitempty"`
}

type retentionPolicyDTO struct {
	ID            string `json:"id"`
	DataClass     string `json:"data_class"`
	RetentionDays int64  `json:"retention_days"`
	Disposition   string `json:"disposition"`
	Enabled       bool   `json:"enabled"`
	Basis         string `json:"basis,omitempty"`
	ApprovalRef   string `json:"approval_ref,omitempty"`
	ModelIO       bool   `json:"model_io"`
	providerFloor
	// EffectiveDisclosureDays = max(retention_days, provider floor): the number a
	// tenant can honestly DISCLOSE as "gone everywhere after" (§7). 0 when the floor
	// is unknown or the class carries no model I/O.
	EffectiveDisclosureDays int64 `json:"effective_disclosure_days,omitempty"`
	// RegulatoryFloor is the enterprise retention floor in force for this class
	// (SEC 17a-4 / FINRA 4511 / CFTC 1.31); nil in the open-core build or when the
	// class carries no named floor.
	RegulatoryFloor *RetentionFloor `json:"regulatory_floor,omitempty"`
}

type retentionRunDTO struct {
	ID               string `json:"id"`
	DataClass        string `json:"data_class"`
	Trigger          string `json:"trigger"`
	Cutoff           string `json:"cutoff"`
	Examined         int64  `json:"examined"`
	Purged           int64  `json:"purged"`
	ExcludedHeld     int64  `json:"excluded_held"`
	SkippedClassHold bool   `json:"skipped_class_hold"`
	Truncated        bool   `json:"truncated"`
	PolicyID         string `json:"policy_id"`
	ApprovalRef      string `json:"approval_ref,omitempty"`
	LedgerSeq        int64  `json:"ledger_seq"`
	LedgerHash       string `json:"ledger_hash,omitempty"`
	ManifestHash     string `json:"manifest_hash"`
	OccurredAt       string `json:"occurred_at"`
}

// floorFor resolves the §7 annotation for one class: only model_io classes carry
// the floor, and only a WIRED source can know it (un-wired ⇒ known=false, honest,
// never fabricated).
func (m *Module) floorFor(ctx context.Context, modelIO bool) providerFloor {
	if !modelIO || m.provider == nil {
		return providerFloor{}
	}
	days, source := m.provider.MaxForcedRetentionDays(ctx)
	return providerFloor{ProviderFloorDays: days, ProviderFloorKnown: true, ProviderFloorSource: source}
}

func recordToPolicyDTO(ctx context.Context, m *Module, tenant model.TenantID, rec model.Record) retentionPolicyDTO {
	dto := retentionPolicyDTO{
		ID:            rec.String(model.ColID),
		DataClass:     rec.String(colDataClass),
		RetentionDays: rec.Int(colRPDays),
		Disposition:   rec.String(colRPDisposition),
		Enabled:       rec.Bool(colRPEnabled),
		Basis:         rec.String(colRPBasis),
		ApprovalRef:   rec.String(colApprovalRef),
	}
	if dc, ok := dataClassByID[dto.DataClass]; ok {
		dto.ModelIO = dc.ModelIO
		dto.providerFloor = m.floorFor(ctx, dc.ModelIO)
		if dto.ProviderFloorKnown {
			dto.EffectiveDisclosureDays = dto.RetentionDays
			if int64(dto.ProviderFloorDays) > dto.EffectiveDisclosureDays {
				dto.EffectiveDisclosureDays = int64(dto.ProviderFloorDays)
			}
		}
	}
	// surface any enterprise regulatory floor in force for this class (nil in the
	// open-core build — m.governor is nil — so the JSON is byte-identical to today).
	if f, ok := m.floorFor248(ctx, tenant, dto.DataClass); ok {
		fc := f
		dto.RegulatoryFloor = &fc
		// A regulatory floor is a retained-at-LEAST minimum, so the honest "gone everywhere
		// after" disclosure number must not be SHORTER than it (else a tenant would disclose
		// data as gone while a floor still preserves it). EffectiveDisclosureDays becomes
		// max(retention_days, provider floor, regulatory floor).
		if dto.RetentionDays > dto.EffectiveDisclosureDays {
			dto.EffectiveDisclosureDays = dto.RetentionDays
		}
		if int64(f.MinDays) > dto.EffectiveDisclosureDays {
			dto.EffectiveDisclosureDays = int64(f.MinDays)
		}
	}
	return dto
}

// ---- handlers: registry + policies ----------------------------------------------

// handleRetentionClasses returns the fixed §2 registry with the advisory
// recommendations and the §7 provider-floor annotation.
func (m *Module) handleRetentionClasses(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	items := make([]dataClassDTO, 0, len(dataClassRegistry))
	for _, dc := range dataClassRegistry {
		kinds := make([]string, 0, len(dc.ExtKinds))
		for _, k := range dc.ExtKinds {
			kinds = append(kinds, string(k))
		}
		dto := dataClassDTO{
			ID: dc.ID, ExtKinds: kinds, AgeColumn: dc.AgeColumn,
			Purgeable: dc.Purgeable, ModelIO: dc.ModelIO,
			RecommendedDays: dc.RecommendedDays,
			SubjectKinds:    dc.SubjectKinds, SubjectColumns: dc.SubjectColumns,
			Note:          dc.Note,
			providerFloor: m.floorFor(r.Context(), dc.ModelIO),
		}
		// annotate the class with any enterprise regulatory floor in force (nil in
		// the open-core build — m.governor is nil — so the JSON is byte-identical).
		if f, ok := m.floorFor248(r.Context(), mc.Tenant, dc.ID); ok {
			fc := f
			dto.RegulatoryFloor = &fc
		}
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, listResponse[dataClassDTO]{Items: items})
}

func (m *Module) handleListRetentionPolicies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	items := []retentionPolicyDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo)
		for _, rec := range recs {
			items = append(items, recordToPolicyDTO(r.Context(), m, mc.Tenant, rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[retentionPolicyDTO]{Items: items})
}

type putPolicyRequest struct {
	RetentionDays int    `json:"retention_days"`
	Disposition   string `json:"disposition"`
	Basis         string `json:"basis"`
	Enabled       *bool  `json:"enabled"`
}

// retentionPlanHash binds the §6 approval to the exact schedule the human approves
// (anti-TOCTOU): the same class, window and disposition — a re-PUT of the identical
// schedule inside the approval window finds the grant (gateOnce semantics in the
// adapter).
func retentionPlanHash(class string, days int, disposition string) string {
	return hashHex("retention|" + class + "|" + strconv.Itoa(days) + "|" + disposition)
}

// handlePutRetentionPolicy upserts a per-class schedule. A retain schedule (or a
// purge one kept disabled) persists directly — documenting retention is evidence,
// not danger. ENABLING disposition=purge is the gated act (§6): pending ⇒ 202 and
// the policy persists with enabled=false; approved (with ≥1 re-verified approver,
// plan-bound) ⇒ 200 enabled with the approval_ref; any other gate outcome persists
// the policy DISABLED and denies (deny-closed — the document stands, the purge
// stays off).
func (m *Module) handlePutRetentionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	class := strings.TrimSpace(chi.URLParam(r, "class"))
	var req putPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	disposition := strings.TrimSpace(strings.ToLower(req.Disposition))
	if msg := validateRetentionPolicy(class, disposition, req.RetentionDays); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	// a purge schedule shorter than a regulatory floor is refused at author time
	// (honest, early — the sweep would clamp it regardless). nil governor / no floor ⇒
	// no check (open-core default: any window 1..36500 is accepted, as today).
	if disposition == dispositionPurge {
		if f, ok := m.floorFor248(r.Context(), mc.Tenant, class); ok && req.RetentionDays < f.MinDays {
			writeJSON(w, http.StatusUnprocessableEntity, errorBody(
				"data_class "+class+" is under a "+f.Basis+" retention floor of "+
					strconv.Itoa(f.MinDays)+" days; a purge schedule shorter than the floor is not permitted"))
			return
		}
	}
	basis := clamp(strings.TrimSpace(req.Basis), maxNoteLen)
	enabled := req.Enabled == nil || *req.Enabled // default on: the schedule documents intent

	approvalRef := ""
	gateStatus := ""
	if disposition == dispositionPurge && enabled {
		planHash := retentionPlanHash(class, req.RetentionDays, disposition)
		dec, err := m.gate.Authorize(r.Context(), mc.Tenant, GateRequest{
			Action: actionRetentionEnable, SubjectKind: "retention_policy", SubjectRef: class,
			PlanHash:    planHash,
			Reason:      firstNonEmptyStr(basis, "enable retention purge for class "+class),
			RequestedBy: mc.Principal.Actor(),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("could not consult the approval gate"))
			return
		}
		gateStatus = dec.Status
		switch dec.Status {
		case GateStatusApproved:
			// Anti-TOCTOU + independent approver re-verification (defense in depth).
			if dec.PlanHash != planHash {
				writeJSON(w, http.StatusForbidden, errorBody("approval is not bound to this schedule (plan hash mismatch)"))
				return
			}
			// The quorum counts PEOPLE, never the credentials: an actor string names a
			// credential, and one human can hold several.
			if dec.Quorum() < retentionEnableQuorum {
				writeJSON(w, http.StatusForbidden, errorBody("approval lacks human approver evidence"))
				return
			}
			approvalRef = dec.ApprovalRef
		default:
			// Pending or denied: persist the schedule DISABLED (the safe direction),
			// then answer per outcome below.
			enabled = false
			approvalRef = ""
		}

		if dec.Status != GateStatusApproved {
			if err := m.upsertPolicy(r.Context(), mc, class, req.RetentionDays, disposition, basis, enabled, approvalRef, gateStatus); err != nil {
				writeStoreError(w, err)
				return
			}
			switch dec.Status {
			case GateStatusPending:
				writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending_approval", "approval_ref": dec.ApprovalRef})
			case GateStatusExpired:
				writeJSON(w, http.StatusConflict, errorBody("the enable approval expired; re-PUT to request again"))
			case GateStatusNoGate:
				writeJSON(w, http.StatusServiceUnavailable, errorBody("no approval gate is wired; enabling a purge disposition is denied (deny-closed)"))
			default:
				writeJSON(w, http.StatusForbidden, errorBody("enabling the purge disposition was rejected"))
			}
			return
		}
	}

	if err := m.upsertPolicy(r.Context(), mc, class, req.RetentionDays, disposition, basis, enabled, approvalRef, gateStatus); err != nil {
		writeStoreError(w, err)
		return
	}
	var dto retentionPolicyDTO
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(r.Context(), repo, eq(colDataClass, class))
		if err != nil || !found {
			return err
		}
		dto = recordToPolicyDTO(r.Context(), m, mc.Tenant, rec)
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// upsertPolicy persists the schedule (create or update by class) and self-audits
// the put in the same transaction.
func (m *Module) upsertPolicy(ctx context.Context, mc api.ModuleContext, class string, days int, disposition, basis string, enabled bool, approvalRef, gateStatus string) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		existing, found, err := findOne(ctx, repo, eq(colDataClass, class))
		if err != nil {
			return err
		}
		var saved model.Record
		if found {
			existing[colRPDays] = int64(days)
			existing[colRPDisposition] = disposition
			existing[colRPEnabled] = enabled
			existing[colRPBasis] = nullableText(basis)
			existing[colApprovalRef] = nullableText(approvalRef)
			saved, err = repo.Update(ctx, existing)
		} else {
			saved, err = repo.Create(ctx, model.Record{
				colDataClass:     class,
				colRPDays:        int64(days),
				colRPDisposition: disposition,
				colRPEnabled:     enabled,
				colRPBasis:       nullableText(basis),
				colApprovalRef:   nullableText(approvalRef),
			})
		}
		if err != nil {
			return err
		}
		meta := map[string]any{
			"data_class": class, "retention_days": days, "disposition": disposition, "enabled": enabled,
		}
		if approvalRef != "" {
			meta["approval_ref"] = approvalRef
		}
		if gateStatus != "" {
			meta["gate_status"] = gateStatus
		}
		return auditEvent(ctx, sc, mc, "compliance.retention.policy.put", retentionPolicyKind,
			model.ID(saved.String(model.ColID)), meta)
	})
}

// handleDeleteRetentionPolicy removes a schedule. Stopping a purge is the SAFE
// direction, so there is no gate — only the self-audit.
func (m *Module) handleDeleteRetentionPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	class := strings.TrimSpace(chi.URLParam(r, "class"))
	// in compliance mode the schedule is sealed as examinable evidence and cannot
	// be removed through the API (the records themselves stay protected by the floor +
	// the WORM object lock regardless). nil governor / governance mode ⇒ today's free
	// delete (stopping a purge stays the safe, ungated direction).
	if f, ok := m.floorFor248(r.Context(), mc.Tenant, class); ok && f.Mode == RetentionModeCompliance {
		writeJSON(w, http.StatusForbidden, errorBody(
			"data_class "+class+" is in compliance mode under "+f.Basis+
				"; the retention schedule is sealed and cannot be deleted (relax via governance mode)"))
		return
	}
	notFound := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(r.Context(), repo, eq(colDataClass, class))
		if err != nil {
			return err
		}
		if !found {
			notFound = true
			return nil
		}
		id := model.ID(rec.String(model.ColID))
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.retention.policy.delete", retentionPolicyKind, id, map[string]any{
			"data_class": class,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (m *Module) handleListRetentionRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if class := strings.TrimSpace(r.URL.Query().Get("class")); class != "" {
		filters = append(filters, eq(colDataClass, class))
	}
	items := []retentionRunDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(retentionRunKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, retentionRunDTO{
				ID:               rec.String(model.ColID),
				DataClass:        rec.String(colDataClass),
				Trigger:          rec.String(colRRTrigger),
				Cutoff:           rec.String(colRRCutoff),
				Examined:         rec.Int(colRRExamined),
				Purged:           rec.Int(colRRPurged),
				ExcludedHeld:     rec.Int(colRRExcluded),
				SkippedClassHold: rec.Bool(colRRSkipped),
				Truncated:        rec.Bool(colRRTruncated),
				PolicyID:         rec.String(colRRPolicyID),
				ApprovalRef:      rec.String(colApprovalRef),
				LedgerSeq:        rec.Int(colLedgerSeq),
				LedgerHash:       rec.String(colLedgerHash),
				ManifestHash:     rec.String(colManifestHash),
				OccurredAt:       rec.String(model.ColCreatedAt),
			})
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[retentionRunDTO]{Items: items})
}

// handleRetentionSweep runs the tenant's sweep NOW (the same code the engine loop
// runs), attributed to the calling admin, and returns the summary.
func (m *Module) handleRetentionSweep(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	sum, err := m.runRetention(r.Context(), mc.Tenant, runTriggerManual, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// ---- the sweep -------------------------------------------------------------------

// RetentionSummary is what one tenant-scoped sweep pass did.
type RetentionSummary struct {
	Trigger           string                 `json:"trigger"`
	Classes           []RetentionClassResult `json:"classes"`
	Examined          int64                  `json:"examined"`
	Purged            int64                  `json:"purged"`
	ExcludedHeld      int64                  `json:"excluded_held"`
	SkippedClassHolds int                    `json:"skipped_class_holds"`
	Truncated         bool                   `json:"truncated"`
}

// RetentionClassResult is one class's outcome within a sweep pass.
type RetentionClassResult struct {
	DataClass        string `json:"data_class"`
	Cutoff           string `json:"cutoff"`
	Examined         int64  `json:"examined"`
	Purged           int64  `json:"purged"`
	ExcludedHeld     int64  `json:"excluded_held"`
	SkippedClassHold bool   `json:"skipped_class_hold"`
	Truncated        bool   `json:"truncated"`
	RunID            string `json:"run_id,omitempty"`
}

// RunRetention executes the tenant-scoped retention sweep for the engine loop
// (cmd/olivares, §6): SOLE enabled purge schedules, hold-checked, bounded
// batches, an append-only certificate + self-audit per class with activity. The
// loop has no ModuleContext, so the pass is attributed to the system actor; the
// HTTP handler runs the same code attributed to the calling admin.
func (m *Module) RunRetention(ctx context.Context, tenant model.TenantID) (RetentionSummary, error) {
	return m.runRetention(ctx, tenant, runTriggerSweep, "system", model.ActorSystem)
}

func (m *Module) runRetention(ctx context.Context, tenant model.TenantID, trigger, actor, actorKind string) (RetentionSummary, error) {
	sum := RetentionSummary{Trigger: trigger}
	if m.data == nil {
		return sum, errors.New("compliance: no data handle; cannot run retention")
	}

	// Load the enabled purge schedules once (the run's worklist). Holds are NOT
	// snapshotted here: each batch transaction re-evaluates the active holds so a
	// hold set mid-run stops destruction from its very next batch (LÍNEA ROJA: a
	// hold beats any purge; ante duda, no se borra).
	type policyRow struct {
		id    string
		class string
		days  int64
		ref   string
	}
	var policies []policyRow
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(retentionPolicyKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo, eq(colRPDisposition, dispositionPurge), eq(colRPEnabled, true))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			policies = append(policies, policyRow{
				id:    rec.String(model.ColID),
				class: rec.String(colDataClass),
				days:  rec.Int(colRPDays),
				ref:   rec.String(colApprovalRef),
			})
		}
		return nil
	}); err != nil {
		return sum, err
	}

	now := m.clock.Now()
	for _, pol := range policies {
		dc, ok := dataClassByID[pol.class]
		if !ok || !dc.Purgeable || dc.AgeColumn == "" {
			continue // defensive: a stored policy can never out-vote the registry
		}
		// clamp the sweep window UP to any enterprise regulatory floor — the sweep
		// must never delete a row younger than the floor. nil governor / no floor ⇒
		// days == pol.days ⇒ byte-identical cutoff (open-core default). The clamp shows
		// up in the sealed run certificate's cutoff, so the floor is auditable.
		days := pol.days
		if f, ok := m.floorFor248(ctx, tenant, dc.ID); ok && int64(f.MinDays) > days {
			days = int64(f.MinDays)
		}
		cutoff := model.NewTimestamp(now.Time().AddDate(0, 0, -int(days)))
		res := RetentionClassResult{DataClass: dc.ID, Cutoff: cutoff.String()}

		iterations := 0
	kinds:
		for _, kind := range dc.ExtKinds {
			cursor := ""
			for {
				if iterations >= maxSweepIterations {
					res.Truncated = true
					break kinds
				}
				iterations++
				stop := false
				err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					// Hold check INSIDE the batch transaction (consistent with the
					// deletes; §6 order: holds first, then candidates).
					holds, err := activeHoldRows(ctx, sc)
					if err != nil {
						return err
					}
					skip, heldRefs := classifyHoldsForClass(holds, dc)
					if skip {
						res.SkippedClassHold = true
						stop = true
						return nil
					}
					repo, err := sc.Ext(kind)
					if err != nil {
						if errors.Is(err, store.ErrUnknownEntity) {
							// The owning module is not registered in this deployment:
							// nothing of this kind exists to dispose of (honest no-op).
							stop = true
							return nil
						}
						return err
					}
					// Candidates: age column < cutoff. The canonical fixed-width
					// timestamps order lexically (core/model/time.go), so OpLt on the
					// text column is the correct age predicate.
					recs, page, err := repo.List(ctx, model.Query{
						Filters: []model.Filter{{Column: dc.AgeColumn, Op: model.OpLt, Value: cutoff.String()}},
						Limit:   maxSweepBatch,
						Cursor:  cursor,
					})
					if err != nil {
						return err
					}
					for _, rec := range recs {
						res.Examined++
						if subjectHeld(rec, dc, heldRefs) {
							res.ExcludedHeld++ // preserved under a mapped subject hold
							continue
						}
						if err := repo.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
							return err
						}
						res.Purged++
					}
					cursor = page.Cursor
					if !page.HasMore || page.Cursor == "" {
						stop = true
					}
					return nil
				})
				if err != nil {
					return sum, err
				}
				if res.SkippedClassHold {
					break kinds
				}
				if stop {
					break
				}
			}
		}

		// Seal the certificate + the self-audit ONLY when the class had activity
		// (rows examined, or a hold skipped it): routine empty passes leave no rows
		// and no audit noise — the certificate IS the destruction log (§6).
		if res.Examined > 0 || res.SkippedClassHold {
			if err := m.sealRetentionRun(ctx, tenant, trigger, actor, actorKind, pol.id, pol.ref, &res); err != nil {
				return sum, err
			}
		}
		sum.Classes = append(sum.Classes, res)
		sum.Examined += res.Examined
		sum.Purged += res.Purged
		sum.ExcludedHeld += res.ExcludedHeld
		if res.SkippedClassHold {
			sum.SkippedClassHolds++
		}
		sum.Truncated = sum.Truncated || res.Truncated
	}
	return sum, nil
}

// sealRetentionRun writes the append-only certificate (ledger-anchored, with the
// canonical run-summary hash) and the "compliance.retention.purge" self-audit in
// ONE final transaction. Counts and references only — never content.
func (m *Module) sealRetentionRun(ctx context.Context, tenant model.TenantID, trigger, actor, actorKind, policyID, approvalRef string, res *RetentionClassResult) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		var seq int64
		hash := ""
		if ok {
			seq, hash = head.Seq, hex.EncodeToString(head.Hash)
		}
		manifest := hashHex(strings.Join([]string{
			"retention_run", res.DataClass, trigger, res.Cutoff,
			"examined=" + itoa(res.Examined), "purged=" + itoa(res.Purged),
			"excluded_held=" + itoa(res.ExcludedHeld),
			"skipped_class_hold=" + strconv.FormatBool(res.SkippedClassHold),
			"truncated=" + strconv.FormatBool(res.Truncated),
			"policy=" + policyID, "approval=" + approvalRef,
		}, "|"))
		repo, err := sc.Ext(retentionRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colDataClass:    res.DataClass,
			colRRTrigger:    trigger,
			colRRCutoff:     res.Cutoff,
			colRRExamined:   res.Examined,
			colRRPurged:     res.Purged,
			colRRExcluded:   res.ExcludedHeld,
			colRRSkipped:    res.SkippedClassHold,
			colRRTruncated:  res.Truncated,
			colRRPolicyID:   policyID,
			colApprovalRef:  nullableText(approvalRef),
			colLedgerSeq:    seq,
			colLedgerHash:   nullableText(hash),
			colManifestHash: manifest,
		})
		if err != nil {
			return err
		}
		res.RunID = rec.String(model.ColID)
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      actor,
			ActorKind:  actorKind,
			Action:     "compliance.retention.purge",
			TargetKind: retentionRunKind,
			TargetID:   model.ID(res.RunID),
			Meta: map[string]any{
				"data_class": res.DataClass, "trigger": trigger, "cutoff": res.Cutoff,
				"examined": res.Examined, "purged": res.Purged, "excluded_held": res.ExcludedHeld,
				"skipped_class_hold": res.SkippedClassHold, "truncated": res.Truncated,
			},
		})
		return err
	})
}

// activeHoldRows loads the tenant's ACTIVE holds (a small set) for the in-batch
// hold check.
func activeHoldRows(ctx context.Context, sc store.Scope) ([]model.Record, error) {
	repo, err := sc.Ext(legalHoldKind)
	if err != nil {
		return nil, err
	}
	return listAll(ctx, repo, eq(colStatus, holdStatusActive))
}

// classifyHoldsForClass applies the §6 order to one class: a tenant or matching
// data_class hold — or a RELATED subject-kind hold the registry maps no column for
// — skips the WHOLE class (conservative over-preservation); mapped subject holds
// accumulate per-kind ref sets for fine-grained row exclusion. Subject holds of
// kinds unrelated to the class never block it.
func classifyHoldsForClass(holds []model.Record, dc dataClass) (skip bool, heldRefs map[string]map[string]bool) {
	related := map[string]bool{}
	for _, k := range dc.SubjectKinds {
		related[k] = true
	}
	for _, h := range holds {
		switch h.String(colLHScopeKind) {
		case holdScopeTenant:
			return true, nil
		case holdScopeClass:
			if h.String(colDataClass) == dc.ID {
				return true, nil
			}
		case holdScopeSubject:
			kind := h.String(colSubjectKind)
			if !related[kind] {
				continue
			}
			col, mapped := dc.SubjectColumns[kind]
			if !mapped {
				return true, nil // related subject kind without a column mapping
			}
			if heldRefs == nil {
				heldRefs = map[string]map[string]bool{}
			}
			if heldRefs[col] == nil {
				heldRefs[col] = map[string]bool{}
			}
			heldRefs[col][h.String(colSubjectRef)] = true
		}
	}
	return false, heldRefs
}

// subjectHeld reports whether a candidate row is preserved by a mapped subject
// hold (its mapped column carries a held ref).
func subjectHeld(rec model.Record, dc dataClass, heldRefs map[string]map[string]bool) bool {
	if len(heldRefs) == 0 {
		return false
	}
	for _, col := range dc.SubjectColumns {
		if refs := heldRefs[col]; refs != nil && refs[rec.String(col)] {
			return true
		}
	}
	return false
}

// firstNonEmptyStr returns the first non-empty string.
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
