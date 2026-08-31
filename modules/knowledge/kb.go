// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Embed policies (the colEmbedPolicy column) — the egress/quality gate (the red
// line). They are validated against the wired embedder at KB create, KB update and
// every ingest (defense in depth), so chunk text never leaves the perimeter when
// the policy forbids it.
const (
	// embedLocalOnly forbids egress: the embedder MUST be local (AllowsEgress=false).
	// This is the air-gap / residency-locked posture — zero egress, guaranteed.
	embedLocalOnly = "local_only"
	// embedModelBacked requires a semantic (model-backed) embedder: the local-hash
	// fallback is refused so quality is never silently degraded; egress to the
	// governed provider is allowed and recorded in lineage.
	embedModelBacked = "model_backed"
	// embedAuto accepts whatever embedder is wired (the default).
	embedAuto = "auto"
)

// KB statuses (the colStatus column).
const (
	kbActive   = "active"
	kbArchived = "archived"
)

// kbRequest is the typed KB create/update body. It carries no secret; default_acl
// holds permission references only.
type kbRequest struct {
	Name            string   `json:"name"`
	Classification  string   `json:"classification,omitempty"`
	ResidencyRegion string   `json:"residency_region,omitempty"`
	EmbedPolicy     string   `json:"embed_policy,omitempty"`
	DefaultACL      []string `json:"default_acl,omitempty"`
	Status          string   `json:"status,omitempty"`
}

// kbDTO is the KB as returned by the API. embed_model is surfaced read-only so a
// reader always knows whether the vectors are semantic or the local-hash fallback.
type kbDTO struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Classification  string   `json:"classification"`
	ResidencyRegion string   `json:"residency_region"`
	EmbedPolicy     string   `json:"embed_policy"`
	EmbedModel      string   `json:"embed_model"`
	Dim             int64    `json:"dim"`
	DefaultACL      []string `json:"default_acl"`
	Status          string   `json:"status"`
	DocCount        int64    `json:"doc_count"`
	ChunkCount      int64    `json:"chunk_count"`
}

func toKBDTO(rec model.Record) kbDTO {
	return kbDTO{
		ID: rec.String(model.ColID), Name: rec.String(colName), Classification: rec.String(colClassif),
		ResidencyRegion: rec.String(colResidency), EmbedPolicy: rec.String(colEmbedPolicy),
		EmbedModel: rec.String(colEmbedModel), Dim: rec.Int(colDim), DefaultACL: unmarshalStrings(rec, colDefaultACL),
		Status: rec.String(colStatus), DocCount: rec.Int(colDocCount), ChunkCount: rec.Int(colChunkCount),
	}
}

// validateEmbedPolicy returns a non-empty message when policy is unknown or
// conflicts with the wired embedder (the red-line gate). It is the single place
// the embed-policy↔egress rule lives so create/update/ingest all agree.
func (m *Module) validateEmbedPolicy(policy string) string {
	switch policy {
	case embedLocalOnly:
		if m.embedder.AllowsEgress() {
			return "embed_policy=local_only forbids egress, but the wired embedder sends content to a provider; wire a local embedder or change the policy"
		}
	case embedModelBacked:
		if m.embedder.ModelRef() == LocalHashModelRef {
			return "embed_policy=model_backed requires a semantic embedder, but only the local-hash fallback is wired; wire an embedder or change the policy"
		}
	case embedAuto:
		// accepts whatever is wired
	default:
		return "embed_policy must be one of local_only, model_backed, auto"
	}
	return ""
}

// embedderRegion returns an egressing embedder's declared data-residency region
// (the provider's inference_geo, lower-cased), or "" when the embedder does not
// declare one. It is an OPTIONAL capability (only a model-backed, egressing adapter
// implements Region(); the local zero-egress embedder never egresses, so its region
// is moot). The composition root's claudeEmbedderAdapter declares it from
// OLIVARES_EMBEDDINGS_GEO (env-contract).
func embedderRegion(e Embedder) string {
	if r, ok := e.(interface{ Region() string }); ok {
		return strings.ToLower(strings.TrimSpace(r.Region()))
	}
	return ""
}

// residencyEgressForbidden reports whether embedding a KB of the given residency
// region with the WIRED embedder would cross a data-residency boundary: the KB is
// region-locked (≠ ""/"global"), the embedder EGRESSES, and the embedder's declared
// provider region does not match the KB region (or is undeclared). Such a pairing is
// REFUSED at KB create/update, ingest and retrieval (defense in depth) so the chunk
// text and the query never leave the KB's residency boundary (docs/SECURITY-HARDENING.md — el dato
// NUNCA sale del perímetro; the air-gap/residency guarantee).
//
// This is the EGRESS residency gate (no cross-border EMBED), DISTINCT from and
// composed with the retrieval-time identity-vs-KB residency gate (no cross-border
// READ, retrieval.go). Listón: inference_geo ∈ {global, us, not_available} and
// the Workspace data-residency geo is a SEPARATE control from inference routing —
// choosing inference_geo alone does NOT satisfy a residency-locked KB; the embedder
// must be provably in-region, else egress is refused (fail closed).
func (m *Module) residencyEgressForbidden(kbRegion string) bool {
	if !isRegionLocked(kbRegion) {
		return false
	}
	if !m.embedder.AllowsEgress() {
		return false // a local, zero-egress embedder never leaves the perimeter
	}
	geo := embedderRegion(m.embedder)
	return geo == "" || geo != strings.ToLower(strings.TrimSpace(kbRegion))
}

// residencyEgressMessage is the single client message for a residency↔egress
// conflict (mirrors validateEmbedPolicy's single-source discipline).
func residencyEgressMessage(region string) string {
	return "residency_region=" + region + " forbids cross-region egress, but the wired embedder sends content to a provider in a different or undeclared region; use embed_policy=local_only, change the region, or wire an in-region embedder (set OLIVARES_EMBEDDINGS_GEO)"
}

// normalizeKBRequest fills defaults and validates the request, returning a
// non-empty client message on failure.
func (m *Module) normalizeKBRequest(req *kbRequest, isCreate bool) string {
	req.Name = strings.TrimSpace(req.Name)
	if isCreate && req.Name == "" {
		return "name is required"
	}
	if len(req.Name) > maxNameLen {
		return "name too long"
	}
	if containsSecret(req.Name) {
		return "name must not contain a secret"
	}
	if req.Classification == "" {
		req.Classification = classInternal // default to internal (deny-closed), never public
	}
	req.Classification = normClass(req.Classification)
	if _, ok := classificationRank[req.Classification]; !ok {
		return "classification must be one of public, internal, confidential, secret"
	}
	if req.ResidencyRegion == "" {
		req.ResidencyRegion = "global"
	}
	if len(req.ResidencyRegion) > maxNameLen {
		return "residency_region too long"
	}
	if req.EmbedPolicy == "" {
		req.EmbedPolicy = embedAuto
	}
	if msg := m.validateEmbedPolicy(req.EmbedPolicy); msg != "" {
		return msg
	}
	// Residency↔egress gate (B3, defense in depth): a region-locked KB may not be
	// declared while an out-of-region/undeclared egressing embedder is wired — its
	// content would later cross the residency boundary at ingest/query.
	if m.residencyEgressForbidden(req.ResidencyRegion) {
		return residencyEgressMessage(req.ResidencyRegion)
	}
	if len(req.DefaultACL) > 256 {
		return "too many default_acl entries"
	}
	for _, a := range req.DefaultACL {
		if len(a) > maxRefLen || containsSecret(a) {
			return "default_acl entry invalid (too long or contains a secret)"
		}
	}
	if req.Status == "" {
		req.Status = kbActive
	}
	if req.Status != kbActive && req.Status != kbArchived {
		return "status must be active or archived"
	}
	return ""
}

// loadKB loads a KB record by id within the caller's scope (tenant-pinned, so a
// cross-tenant id is simply not found — IDOR-safe by construction).
func loadKB(ctx context.Context, sc store.Scope, id model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(baseKind)
	if err != nil {
		return nil, false, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return rec, true, nil
}

// handleCreateKB declares a knowledge base. It records the embed_model/dim of the
// CURRENTLY wired embedder and enforces the embed-policy↔egress gate.
func (m *Module) handleCreateKB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req kbRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := m.normalizeKBRequest(&req, true); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out kbDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		if _, ok, err := findOne(r.Context(), repo, eq(colName, req.Name)); err != nil {
			return err
		} else if ok {
			return store.ErrConflict
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colName: req.Name, colClassif: req.Classification, colResidency: req.ResidencyRegion,
			colEmbedPolicy: req.EmbedPolicy, colEmbedModel: m.embedder.ModelRef(), colDim: int64(m.embedder.Dim()),
			colDefaultACL: marshalStrings(req.DefaultACL), colOwnerRef: mc.Principal.Actor(),
			colStatus: req.Status, colDocCount: int64(0), colChunkCount: int64(0),
		})
		if err != nil {
			return err
		}
		out = toKBDTO(rec)
		// NO name (not even hashed) in the ledger meta: meta is hash-covered and
		// WORM-archived — a free-text KB name (which an operator can make
		// person-identifying) would be unerasable forever, and an UNKEYED hash of a
		// low-entropy name is dictionary-confirmable, which is not erasure either
		// (docs/SECURITY-HARDENING.md; the kb.update meta already omits it). The event's TargetID
		// is the durable reference; the name lives on the mutable row.
		return auditEvent(r.Context(), sc, mc, "knowledge.kb.create", baseKind, model.ID(out.ID), map[string]any{
			"classification": req.Classification, "residency": req.ResidencyRegion, "embed_policy": req.EmbedPolicy,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateKB updates a KB's governance settings (classification/residency/
// embed policy/default ACL/status), version-checked, re-validating the embed gate.
func (m *Module) handleUpdateKB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req kbRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var out kbDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if req.Name == "" {
			req.Name = rec.String(colName)
		}
		if msg := m.normalizeKBRequest(&req, false); msg != "" {
			return &clientError{msg}
		}
		rec[colName] = req.Name
		rec[colClassif] = req.Classification
		rec[colResidency] = req.ResidencyRegion
		rec[colEmbedPolicy] = req.EmbedPolicy
		rec[colDefaultACL] = marshalStrings(req.DefaultACL)
		rec[colStatus] = req.Status
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toKBDTO(updated)
		return auditEvent(r.Context(), sc, mc, "knowledge.kb.update", baseKind, id, map[string]any{
			"classification": req.Classification, "residency": req.ResidencyRegion, "embed_policy": req.EmbedPolicy,
		})
	})
	if ce, ok := err.(*clientError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(ce.msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetKB returns one KB.
func (m *Module) handleGetKB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out kbDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, ok, err := loadKB(r.Context(), sc, id)
		if err != nil || !ok {
			return err
		}
		found, out = true, toKBDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListKBs lists knowledge bases, optionally filtered by status.
func (m *Module) handleListKBs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	out := listResponse[kbDTO]{Items: []kbDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toKBDTO(rec))
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

// handleDeleteKB deletes a KB and CASCADES its documents, chunks and document
// sensitivity labels in one transaction (no orphans). Admin-tier. The
// append-only lineage and pii_scan rows are retained (they are evidence;
// deleting them would erase the non-exfiltration/discovery record).
func (m *Module) handleDeleteKB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	// an active legal hold vetoes the delete, checked BEFORE the
	// transaction — subject ("kb", id) + class knowledge.content in one call
	// (the §4 matching rule covers tenant-wide and class holds too).
	if m.enforceHold(r.Context(), w, mc.Tenant, holdSubjectKB, id.String(), holdClassKnowledgeContent) {
		return
	}
	// the cascade also destroys every DOCUMENT, and a ("document", <id>)
	// subject-hold covers documents the ("kb", id) check above cannot see — so
	// each one is enumerated (a read) and gated BEFORE any deletion (deny before
	// any side effect, the two-phase precedent). Any held document blocks
	// the whole delete; a gate error denies it (503, fail closed).
	if m.holdGate != nil {
		var docIDs []string
		if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(documentKind)
			if err != nil {
				return err
			}
			docs, err := listAll(r.Context(), repo, eq(colKBRef, id.String()))
			if err != nil {
				return err
			}
			for _, d := range docs {
				docIDs = append(docIDs, d.String(model.ColID))
			}
			return nil
		}); err != nil {
			writeStoreError(w, err)
			return
		}
		if m.enforceDocumentHolds(r.Context(), w, mc.Tenant, docIDs) {
			return
		}
	}
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		kbRepo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		if _, err := kbRepo.Get(r.Context(), id); err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found = true
		if err := m.cascadeDeleteKB(r.Context(), sc, id); err != nil {
			return err
		}
		if err := kbRepo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.kb.delete", baseKind, id, map[string]any{"cascade": "documents+chunks+labels"})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// cascadeDeleteKB removes every chunk, document and document sensitivity label
// of a KB within the caller's tx. Labels are CURRENT-state mutable metadata of
// their subjects, so they go with the documents — unlike lineage and pii_scan
// rows (append-only evidence, deliberately retained).
func (m *Module) cascadeDeleteKB(ctx context.Context, sc store.Scope, kbID model.ID) error {
	chunkRepo, err := sc.Ext(chunkKind)
	if err != nil {
		return err
	}
	chunks, err := listAll(ctx, chunkRepo, eq(colKBRef, kbID.String()))
	if err != nil {
		return err
	}
	for _, c := range chunks {
		if err := chunkRepo.Delete(ctx, model.ID(c.String(model.ColID))); err != nil {
			return err
		}
	}
	docRepo, err := sc.Ext(documentKind)
	if err != nil {
		return err
	}
	docs, err := listAll(ctx, docRepo, eq(colKBRef, kbID.String()))
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := docRepo.Delete(ctx, model.ID(d.String(model.ColID))); err != nil {
			return err
		}
	}
	labelRepo, err := sc.Ext(labelKind)
	if err != nil {
		return err
	}
	labels, err := listAll(ctx, labelRepo, eq(colSubjectKind, subjectDocument), eq(colKBRef, kbID.String()))
	if err != nil {
		return err
	}
	for _, l := range labels {
		if err := labelRepo.Delete(ctx, model.ID(l.String(model.ColID))); err != nil {
			return err
		}
	}
	return nil
}

// clientError carries a 400-class message out of a Mutate closure.
type clientError struct{ msg string }

func (e *clientError) Error() string { return e.msg }
