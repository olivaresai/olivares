// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file makes the self-hosted structural advantage legible (docs/SECURITY-HARDENING.md): a
// per-region residency ATTESTATION (data stays inside the customer's perimeter — the
// GDPR/EU/air-gap argument) and a SCAN that turns EXISTING egress signals (module
// VIII's data lineage, via the LineageSource seam or read inline) into a
// residency-violation Finding + a bus signal. It consumes inventory/
// topology; it captures nothing new. busResidencyViolation is the FindingReport.Kind
// routing key deliver.
const busResidencyViolation = "compliance_residency_violation"

// colCostInferenceGeo mirrors finops' inference_geo column on its cost read-model
// (costSampleExtKind, declared in capabilities.go). The residency scan reads
// that entity INLINE — the same cross-module, probe-by-KIND pattern already used for
// the lineage and cost-sample probes — to fold OBSERVED inference geos into the pin
// coherence check: a tenant whose inference crosses its pinned region is flagged.
const colCostInferenceGeo = "inference_geo"

// colCostWorkspaceRef mirrors finops' workspace_ref attribution dimension on the same
// cost read-model, so the workspace-geo drift scan can scope OBSERVED inference
// geos to the workspace they were attributed to.
const colCostWorkspaceRef = "workspace_ref"

// Columns of the models module's workspace-residency mirror
// (workspaceResidencyExtKind, declared in capabilities.go) the drift scan reads:
// the workspace ref and its PERMITTED inference geos (comma-separated lowercase, as
// mirrored from the Workspace Admin API; EMPTY = unrestricted/unreported → never a
// violation, there is no permitted set to drift from).
const (
	colWSWorkspaceRef = "workspace_ref"
	colWSAllowedGeos  = "allowed_geos"
)

type residencyDTO struct {
	ID                 string   `json:"id"`
	Region             string   `json:"region"`
	Perimeter          string   `json:"perimeter"`
	SelfHosted         bool     `json:"self_hosted"`
	EncryptionAtRest   bool     `json:"encryption_at_rest"`
	DataClasses        []string `json:"data_classes,omitempty"`
	AttestedBy         string   `json:"attested_by"`
	AttestedAt         string   `json:"attested_at"`
	ViolationsObserved int64    `json:"violations_observed"`
	LastChecked        string   `json:"last_checked,omitempty"`
	Note               string   `json:"note,omitempty"`
}

type attestRequest struct {
	Region           string   `json:"region"`
	Perimeter        string   `json:"perimeter"`
	SelfHosted       *bool    `json:"self_hosted"`
	EncryptionAtRest *bool    `json:"encryption_at_rest"`
	DataClasses      []string `json:"data_classes"`
	Note             string   `json:"note"`
}

// handleAttestResidency records (or updates) a per-region residency attestation.
func (m *Module) handleAttestResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req attestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	region := clamp(strings.TrimSpace(req.Region), maxNameLen)
	if region == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("region is required"))
		return
	}
	perimeter := clamp(strings.TrimSpace(req.Perimeter), maxNameLen)
	if perimeter == "" {
		perimeter = "self-hosted"
	}
	selfHosted := req.SelfHosted == nil || *req.SelfHosted // default true: the self-hosted posture
	encAtRest := req.EncryptionAtRest != nil && *req.EncryptionAtRest
	classes := clampStrings(req.DataClasses, 32, maxNameLen)

	var dto residencyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(residencyKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		existing, err := listAll(r.Context(), repo, eq(colRegion, region))
		if err != nil {
			return err
		}
		var saved model.Record
		if len(existing) > 0 {
			rec := existing[0]
			rec[colPerimeter] = perimeter
			rec[colSelfHosted] = selfHosted
			rec[colEncAtRest] = encAtRest
			rec[colDataClasses] = encodeJSON(classes)
			rec[colAttestedBy] = mc.Principal.Actor()
			rec[colAttestedAt] = now.String()
			rec[colResidencyNote] = nullableText(clamp(strings.TrimSpace(req.Note), maxNoteLen))
			saved, err = repo.Update(r.Context(), rec)
		} else {
			saved, err = repo.Create(r.Context(), model.Record{
				colRegion:        region,
				colPerimeter:     perimeter,
				colSelfHosted:    selfHosted,
				colEncAtRest:     encAtRest,
				colDataClasses:   encodeJSON(classes),
				colAttestedBy:    mc.Principal.Actor(),
				colAttestedAt:    now.String(),
				colViolations:    int64(0),
				colResidencyNote: nullableText(clamp(strings.TrimSpace(req.Note), maxNoteLen)),
			})
		}
		if err != nil {
			return err
		}
		dto = recordToResidencyDTO(saved)
		return auditEvent(r.Context(), sc, mc, "compliance.residency.attest", residencyKind, model.ID(saved.String(model.ColID)), map[string]any{
			"region": region, "perimeter": perimeter, "self_hosted": selfHosted,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

type scanReport struct {
	RegionsChecked int            `json:"regions_checked"`
	EgressSignals  int            `json:"egress_signals"`
	Violations     int64          `json:"violations"`
	Findings       int            `json:"findings_emitted"`
	Signals        []EgressSignal `json:"signals,omitempty"`
	// Residency-pin coherence: the tenant's control-plane region pin
	// (orgs.data_region), whether it lacks a backing self-hosted attestation, and how
	// many observed inference geos cross the pinned region.
	PinnedRegion        string `json:"pinned_region,omitempty"`
	AttestationGap      bool   `json:"attestation_gap,omitempty"`
	InferenceViolations int    `json:"inference_violations,omitempty"`
	// Workspace-geo drift: how many distinct (workspace, geo) pairs observed
	// inference OUTSIDE the workspace's PERMITTED inference geos
	// (models.workspace_residency vs finops.cost_sample).
	WorkspaceGeoViolations int    `json:"workspace_geo_violations,omitempty"`
	Disclaimer             string `json:"disclaimer"`
}

// handleScanResidency scans EXISTING egress signals against the self-hosted
// attestations and, where data left a self-hosted perimeter, records a violation,
// emits a core Finding + a bus signal, and updates the attestation. It
// creates no new capture; it correlates module VIII's lineage with the attestation.
func (m *Module) handleScanResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var report scanReport
	report.Disclaimer = reportDisclaimer
	var toPublish []sdkmodel.FindingReport

	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		signals, err := m.egressSignals(r.Context(), sc, mc.Tenant)
		if err != nil {
			return err
		}
		report.EgressSignals = len(signals)
		report.Signals = signals

		repo, err := sc.Ext(residencyKind)
		if err != nil {
			return err
		}
		rows, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		// The egress signals are tenant-global (lineage is not region-tagged): each
		// self-hosted region's perimeter is violated by the SAME set of egress events.
		// report.Violations is therefore the count of distinct egress events (set once
		// below), not the per-region sum — summing would multiply by the region count.
		egress := int64(len(signals))
		for _, rec := range rows {
			if !rec.Bool(colSelfHosted) {
				continue // egress is not a violation where the region is not attested self-hosted
			}
			report.RegionsChecked++
			rec[colViolations] = egress
			rec[colLastChecked] = now.String()
			updated, uerr := repo.Update(r.Context(), rec)
			if uerr != nil {
				return uerr
			}
			if egress == 0 {
				continue
			}
			region := updated.String(colRegion)
			title := clamp("Residency: data egress out of self-hosted perimeter ("+region+")", maxNameLen)
			detail := region + "|egress=" + itoa(egress)
			if _, ferr := sc.Findings().Create(r.Context(), model.Finding{
				Kind: "residency_violation", Severity: model.SeverityHigh, Status: model.FindingOpen,
				Source: Name, SubjectKind: "tenant", SubjectID: model.ID(mc.Tenant.String()),
				Title: title, DetailHash: hashBytes(detail), OccurredAt: now,
			}); ferr != nil {
				return ferr
			}
			report.Findings++
			toPublish = append(toPublish, sdkmodel.FindingReport{
				Kind: busResidencyViolation, Severity: sdkmodel.SeverityHigh,
				SubjectKind: "tenant", SubjectRef: clamp(region, maxRefLen),
				Title: title, DetailHash: hashHex(detail), OccurredAt: now.Time(),
			})
		}
		// The tenant-level violation count is the number of distinct egress events,
		// only when at least one self-hosted region was actually checked.
		if report.RegionsChecked > 0 {
			report.Violations = egress
		}

		// residency-PIN coherence. orgs.data_region is the authoritative "where
		// this tenant's data must live"; cross-check it against the self-hosted
		// ATTESTATIONS (a pin must be backed by an attestation for that region) and the
		// observed INFERENCE geos (inference must stay in the pinned region — coordinating
		// with the model-level inference_geo residency). Both reuse the residency_violation
		// Finding + the compliance_residency_violation bus signal. An unpinned tenant has
		// no residency requirement and is skipped.
		emitViolation := func(title, detail, subjectRef string) error {
			title = clamp(title, maxNameLen)
			if _, ferr := sc.Findings().Create(r.Context(), model.Finding{
				Kind: "residency_violation", Severity: model.SeverityHigh, Status: model.FindingOpen,
				Source: Name, SubjectKind: "tenant", SubjectID: model.ID(mc.Tenant.String()),
				Title: title, DetailHash: hashBytes(detail), OccurredAt: now,
			}); ferr != nil {
				return ferr
			}
			report.Findings++
			toPublish = append(toPublish, sdkmodel.FindingReport{
				Kind: busResidencyViolation, Severity: sdkmodel.SeverityHigh,
				SubjectKind: "tenant", SubjectRef: clamp(subjectRef, maxRefLen),
				Title: title, DetailHash: hashHex(detail), OccurredAt: now.Time(),
			})
			return nil
		}
		org, oerr := sc.Org(r.Context())
		if oerr != nil {
			return oerr
		}
		pin := strings.ToLower(strings.TrimSpace(org.DataRegion))
		report.PinnedRegion = pin
		if pin != "" {
			attested := false
			for _, rec := range rows {
				if rec.Bool(colSelfHosted) && strings.EqualFold(strings.TrimSpace(rec.String(colRegion)), pin) {
					attested = true
					break
				}
			}
			if !attested {
				if err := emitViolation("Residency: tenant pinned to region "+pin+" has no self-hosted attestation",
					"pin="+pin+"|no_self_hosted_attestation", pin); err != nil {
					return err
				}
				report.AttestationGap = true
			}
			geos, gerr := m.distinctInferenceGeos(r.Context(), sc)
			if gerr != nil {
				return gerr
			}
			for _, geo := range geos {
				if residency.InferenceGeoCompatible(pin, geo) {
					continue
				}
				if err := emitViolation("Residency: inference geo "+geo+" crosses the pinned region "+pin,
					"pin="+pin+"|inference_geo="+geo, pin); err != nil {
					return err
				}
				report.InferenceViolations++
			}
		}

		// Anthropic WORKSPACE geo drift — PERMITTED vs OBSERVED, the
		// least-privilege drift shape applied to inference residency. The models module
		// mirrors each workspace's allowed inference geos from the Workspace Admin API
		// (models.workspace_residency, probed by KIND); finops' cost samples carry the
		// geo each inference was OBSERVED in, per workspace. Membership, NOT the
		// single-pin equality of InferenceGeoCompatible: a workspace may permit several
		// geos. An observed geo outside the permitted set — including "not_available"
		// and any unknown value, where compliance cannot be proven (deny-closed, the
		// Strictness) — is drift, deduped per (workspace, geo). A workspace whose
		// allowed_geos is EMPTY is unrestricted/unreported and never violates. Both
		// reuse the residency_violation Finding + bus signal above.
		wsRows, werr := m.workspaceResidencyRows(r.Context(), sc)
		if werr != nil {
			return werr
		}
		var restricted []model.Record
		for _, rec := range wsRows {
			if strings.TrimSpace(rec.String(colWSWorkspaceRef)) != "" &&
				strings.TrimSpace(rec.String(colWSAllowedGeos)) != "" {
				restricted = append(restricted, rec)
			}
		}
		if len(restricted) > 0 {
			geosByWS, gerr := m.distinctInferenceGeosByWorkspace(r.Context(), sc)
			if gerr != nil {
				return gerr
			}
			seenDrift := map[string]bool{}
			for _, rec := range restricted {
				ref := strings.TrimSpace(rec.String(colWSWorkspaceRef))
				allowedCSV := strings.TrimSpace(rec.String(colWSAllowedGeos))
				allowed := map[string]bool{}
				for _, g := range strings.Split(allowedCSV, ",") {
					if g = strings.ToLower(strings.TrimSpace(g)); g != "" {
						allowed[g] = true
					}
				}
				if len(allowed) == 0 {
					continue // separators/whitespace only: no geos listed → unreported, as EMPTY
				}
				for _, geo := range geosByWS[ref] {
					// geosByWS is already distinct per workspace; the seenDrift set
					// additionally guards a duplicated workspace row (the producer's
					// uniqueness is its contract, not ours to assume).
					if allowed[geo] || seenDrift[ref+"\x00"+geo] {
						continue
					}
					seenDrift[ref+"\x00"+geo] = true
					if err := emitViolation("Residency: inference geo "+geo+" outside workspace allowed geos ("+ref+")",
						"workspace="+ref+"|allowed="+allowedCSV+"|inference_geo="+geo, ref); err != nil {
						return err
					}
					report.WorkspaceGeoViolations++
				}
			}
		}

		return auditEvent(r.Context(), sc, mc, "compliance.residency.scan", residencyKind, "", map[string]any{
			"regions": report.RegionsChecked, "egress_signals": report.EgressSignals, "violations": report.Violations,
			"pinned_region": report.PinnedRegion, "inference_violations": report.InferenceViolations,
			"workspace_geo_violations": report.WorkspaceGeoViolations,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Publish the bus signals AFTER the transaction commits (best-effort; a publish
	// failure is logged, not fatal) so never deliver a signal for an
	// uncommitted finding.
	if m.host != nil {
		for _, rep := range toPublish {
			if perr := m.host.Publish(r.Context(), event.FromObservation(mc.Tenant.String(), Name, rep)); perr != nil {
				m.debugf("compliance: publish residency violation failed", "err", perr)
			}
		}
	}
	writeJSON(w, http.StatusOK, report)
}

func (m *Module) handleListResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var items []residencyDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(residencyKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo)
		for _, rec := range recs {
			items = append(items, recordToResidencyDTO(rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[residencyDTO]{Items: items})
}

// egressSignals returns existing perimeter-egress signals: the wired LineageSource if
// one is injected, else the knowledge data-lineage ext entity read INLINE (degrading
// honestly to none if module VIII is not registered).
func (m *Module) egressSignals(ctx context.Context, sc store.Scope, tenant model.TenantID) ([]EgressSignal, error) {
	if !m.isDefaultLineage() {
		return m.lineage.EgressSignals(ctx, tenant)
	}
	repo, err := sc.Ext(lineageExtKind)
	if err != nil {
		return nil, nil // module VIII not registered → no signal (honest)
	}
	recs, err := listAll(ctx, repo, eq("egress", true))
	if err != nil {
		return nil, err
	}
	out := make([]EgressSignal, 0, len(recs))
	for _, rec := range recs {
		out = append(out, EgressSignal{
			Source: string(lineageExtKind),
			Ref:    rec.String(model.ColID),
			Detail: "data-lineage egress (model_backed)",
		})
	}
	return out, nil
}

// distinctInferenceGeos reads the DISTINCT, non-empty inference geos OBSERVED for the
// bound tenant from module XI's (finops) cost read-model, read inline by KIND. It is
// how the residency scan folds the model-level inference_geo into the control-plane
// pin coherence check. finops not registered ⇒ no signal (honest), never a fabricated
// pass; the scan simply reports no inference violations.
func (m *Module) distinctInferenceGeos(ctx context.Context, sc store.Scope) ([]string, error) {
	repo, err := sc.Ext(costSampleExtKind)
	if err != nil {
		return nil, nil // module XI not registered → no inference signal (honest)
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, rec := range recs {
		g := strings.ToLower(strings.TrimSpace(rec.String(colCostInferenceGeo)))
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out, nil
}

// distinctInferenceGeosByWorkspace is the workspace-scoped variant of
// distinctInferenceGeos: the DISTINCT, non-empty inference geos observed PER
// workspace_ref, in one pass over the same finops cost read-model. Samples with an
// empty workspace_ref are skipped — the default workspace is not addressable in the
// Workspace Admin API, so it has no residency config (no PERMITTED set) to drift
// from. finops not registered ⇒ no signal (honest), exactly like distinctInferenceGeos.
func (m *Module) distinctInferenceGeosByWorkspace(ctx context.Context, sc store.Scope) (map[string][]string, error) {
	repo, err := sc.Ext(costSampleExtKind)
	if err != nil {
		return nil, nil // module XI not registered → no inference signal (honest)
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := map[string][]string{}
	for _, rec := range recs {
		ws := strings.TrimSpace(rec.String(colCostWorkspaceRef))
		g := strings.ToLower(strings.TrimSpace(rec.String(colCostInferenceGeo)))
		if ws == "" || g == "" || seen[ws+"\x00"+g] {
			continue
		}
		seen[ws+"\x00"+g] = true
		out[ws] = append(out[ws], g)
	}
	return out, nil
}

// workspaceResidencyRows reads the models module's per-workspace residency mirror for
// the bound tenant, by KIND (workspaceResidencyExtKind). models not registered ⇒ no
// signal (honest); the scan simply reports no workspace-geo drift.
func (m *Module) workspaceResidencyRows(ctx context.Context, sc store.Scope) ([]model.Record, error) {
	repo, err := sc.Ext(workspaceResidencyExtKind)
	if err != nil {
		return nil, nil // models module not registered → no workspace-geo signal (honest)
	}
	return listAll(ctx, repo)
}

func recordToResidencyDTO(rec model.Record) residencyDTO {
	return residencyDTO{
		ID:                 rec.String(model.ColID),
		Region:             rec.String(colRegion),
		Perimeter:          rec.String(colPerimeter),
		SelfHosted:         rec.Bool(colSelfHosted),
		EncryptionAtRest:   rec.Bool(colEncAtRest),
		DataClasses:        decodeStrings(rec.String(colDataClasses)),
		AttestedBy:         rec.String(colAttestedBy),
		AttestedAt:         rec.String(colAttestedAt),
		ViolationsObserved: rec.Int(colViolations),
		LastChecked:        rec.String(colLastChecked),
		Note:               rec.String(colResidencyNote),
	}
}

// clampStrings bounds a string slice's length and each element's length (minimal data).
func clampStrings(in []string, maxN, maxLen int) []string {
	if len(in) > maxN {
		in = in[:maxN]
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, clamp(s, maxLen))
		}
	}
	return out
}
