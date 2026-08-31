// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Guard-profile values for the retrieval guard axis. ACL-aware is the implicit
// default; public_only is the deliberate, dual-controlled downgrade.
const (
	GuardProfileACLAware   = "acl_aware"
	GuardProfilePublicOnly = "public_only"
)

// GuardPosture is the runtime read shape consumed by the composition root. It is
// intentionally source-scope-neutral: no workspace, agent-group, credential or
// binding semantics live here.
type GuardPosture struct {
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
	Profile    string `json:"profile"`
	Reason     string `json:"reason,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
}

type guardPostureDTO = GuardPosture

func defaultGuardPosture(sourceType, sourceRef string) GuardPosture {
	return GuardPosture{SourceType: sourceType, SourceRef: sourceRef, Profile: GuardProfileACLAware}
}

func toGuardPostureDTO(rec model.Record) guardPostureDTO {
	return guardPostureDTO{
		SourceType: rec.String(colSourceType),
		SourceRef:  rec.String(colSourceRef),
		Profile:    normalizeGuardProfile(rec.String(colGuardProfile)),
		Reason:     rec.String(colGuardReason),
		UpdatedBy:  rec.String(colGuardUpdatedBy),
	}
}

func normalizeGuardProfile(s string) string {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case GuardProfilePublicOnly:
		return GuardProfilePublicOnly
	default:
		return GuardProfileACLAware
	}
}

type guardPostureInput struct {
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
	Profile    string `json:"profile"`
	Reason     string `json:"reason,omitempty"`
}

func (in *guardPostureInput) validate() string {
	in.SourceType = strings.TrimSpace(strings.ToLower(in.SourceType))
	if in.SourceType == "" {
		in.SourceType = sourceKnowledge
	}
	if in.SourceType != sourceKnowledge {
		return "source_type must be knowledge for retrieval guard posture"
	}
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	if in.SourceRef == "" {
		return "source_ref (knowledge base name) is required"
	}
	switch strings.TrimSpace(strings.ToLower(in.Profile)) {
	case "":
		return "profile is required"
	case GuardProfileACLAware:
		in.Profile = GuardProfileACLAware
	case GuardProfilePublicOnly, "disable_acl":
		in.Profile = GuardProfilePublicOnly
	default:
		return "profile must be acl_aware or public_only"
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Reason) > maxNoteLen {
		return "reason is too long"
	}
	return ""
}

// handleListGuardPostures lists active guard-posture overrides. With no row, a KB
// remains ACL-aware by default, so only explicit overrides are listed.
func (m *Module) handleListGuardPostures(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{
		{"source_type", colSourceType}, {"source_ref", colSourceRef}, {"profile", colGuardProfile},
	} {
		if v := strings.TrimSpace(r.URL.Query().Get(f.param)); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[guardPostureDTO]{Items: []guardPostureDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(guardPostureKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGuardPostureDTO(rec))
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

// handlePutGuardPosture is the separate ACL/guard posture surface. Relaxing the
// guard to public_only creates a pending posture request. Tightening back to
// acl_aware applies immediately and is audited.
func (m *Module) handlePutGuardPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in guardPostureInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if in.Profile == GuardProfilePublicOnly {
		reason := in.Reason
		if reason == "" {
			reason = "knowledge retrieval guard downgraded to public_only (ACL awareness disabled)"
		}
		var out postureRequestDTO
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			pr, perr := m.createPostureRequest(r.Context(), sc, mc, postureOpGuardPublicOnly, in.SourceType, in.SourceRef, "", reason, nil)
			out = pr
			return perr
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, out)
		return
	}

	var out guardPostureDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		next, err := applyGuardPosture(r.Context(), sc, in.SourceType, in.SourceRef, GuardProfileACLAware, in.Reason, mc.Principal.Actor())
		if err != nil {
			return err
		}
		out = next
		return auditGuardPosture(r.Context(), sc, mc, "tighten", out)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func auditGuardPosture(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, gp GuardPosture) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "sourcescope.guard_posture." + verb,
		TargetKind: guardPostureKind,
		Meta: map[string]any{
			"source_type": gp.SourceType,
			"source_ref":  gp.SourceRef,
			"profile":     gp.Profile,
			"reason":      gp.Reason,
		},
	})
	return err
}

func findGuardPosture(ctx context.Context, sc store.Scope, sourceType, sourceRef string) (model.Record, bool, error) {
	recs, err := allExt(ctx, sc, guardPostureKind, eq(colSourceType, sourceType), eq(colSourceRef, sourceRef))
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

func applyGuardPosture(ctx context.Context, sc store.Scope, sourceType, sourceRef, profile, reason, updatedBy string) (guardPostureDTO, error) {
	repo, err := sc.Ext(guardPostureKind)
	if err != nil {
		return guardPostureDTO{}, err
	}
	sourceType = strings.TrimSpace(strings.ToLower(sourceType))
	sourceRef = strings.TrimSpace(sourceRef)
	profile = normalizeGuardProfile(profile)
	reason = strings.TrimSpace(reason)
	updatedBy = strings.TrimSpace(updatedBy)

	rec, ok, err := findGuardPosture(ctx, sc, sourceType, sourceRef)
	if err != nil {
		return guardPostureDTO{}, err
	}
	if profile == GuardProfileACLAware {
		if ok {
			if err := repo.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
				return guardPostureDTO{}, err
			}
		}
		return guardPostureDTO{SourceType: sourceType, SourceRef: sourceRef, Profile: GuardProfileACLAware, Reason: reason, UpdatedBy: updatedBy}, nil
	}
	fields := model.Record{
		colSourceType:     sourceType,
		colSourceRef:      sourceRef,
		colGuardProfile:   GuardProfilePublicOnly,
		colGuardReason:    reason,
		colGuardUpdatedBy: updatedBy,
	}
	if ok {
		for k, v := range fields {
			rec[k] = v
		}
		rec, err = repo.Update(ctx, rec)
	} else {
		rec, err = repo.Create(ctx, fields)
	}
	if err != nil {
		return guardPostureDTO{}, err
	}
	return toGuardPostureDTO(rec), nil
}

// GuardPosture reads the current retrieval-guard posture for one source. Missing
// row means the secure default: ACL/clearance/region-aware.
func (r *Resolver) GuardPosture(ctx context.Context, tenant model.TenantID, sourceType, sourceRef string) (GuardPosture, error) {
	data := r.m.moduleData()
	if data == nil {
		return defaultGuardPosture(sourceType, sourceRef), errNotReady
	}
	sourceType = strings.TrimSpace(strings.ToLower(sourceType))
	sourceRef = strings.TrimSpace(sourceRef)
	var out GuardPosture
	if err := data.View(ctx, tenant, func(sc store.Scope) error {
		rec, ok, err := findGuardPosture(ctx, sc, sourceType, sourceRef)
		if err != nil {
			return err
		}
		if !ok {
			out = defaultGuardPosture(sourceType, sourceRef)
			return nil
		}
		out = toGuardPostureDTO(rec)
		return nil
	}); err != nil {
		return defaultGuardPosture(sourceType, sourceRef), err
	}
	return out, nil
}

// ListGuardPostures returns explicit guard-posture overrides for one tenant.
func (r *Resolver) ListGuardPostures(ctx context.Context, tenant model.TenantID) ([]GuardPosture, error) {
	data := r.m.moduleData()
	if data == nil {
		return nil, errNotReady
	}
	var out []GuardPosture
	if err := data.View(ctx, tenant, func(sc store.Scope) error {
		recs, err := allExt(ctx, sc, guardPostureKind)
		if err != nil {
			return err
		}
		out = make([]GuardPosture, 0, len(recs))
		for _, rec := range recs {
			out = append(out, toGuardPostureDTO(rec))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}
