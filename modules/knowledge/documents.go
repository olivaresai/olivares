// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// documentDTO is one ingested document's metadata + provenance. It carries NO body
// (the body lives only as redacted chunks); redaction_count records how many
// secrets the module removed on ingest.
type documentDTO struct {
	ID             string   `json:"id"`
	KBRef          string   `json:"kb_ref"`
	SourceKind     string   `json:"source_kind"`
	SourceRef      string   `json:"source_ref"`
	SourceMode     string   `json:"source_mode"`
	SourceDocID    string   `json:"source_doc_id"`
	Title          string   `json:"title"`
	ContentType    string   `json:"content_type"`
	Classification string   `json:"classification"`
	Residency      string   `json:"residency_region"`
	ACL            []string `json:"acl"`
	ContentHash    string   `json:"content_hash"`
	RedactionCount int64    `json:"redaction_count"`
	ChunkCount     int64    `json:"chunk_count"`
	SpaceRef       string   `json:"space_ref,omitempty"`
	Status         string   `json:"status"`
}

func toDocumentDTO(rec model.Record) documentDTO {
	return documentDTO{
		ID: rec.String(model.ColID), KBRef: rec.String(colKBRef), SourceKind: rec.String(colSourceKind),
		SourceRef: rec.String(colSourceRef), SourceMode: recordSourceMode(rec),
		SourceDocID: rec.String(colSourceDocID), Title: rec.String(colTitle),
		ContentType: rec.String(colContentType), Classification: rec.String(colClassif), Residency: rec.String(colResidency),
		ACL: unmarshalStrings(rec, colACL), ContentHash: rec.String(colContentHash), RedactionCount: rec.Int(colRedactCount),
		ChunkCount: rec.Int(colDocChunkCnt), SpaceRef: rec.String(colSpaceRef), Status: rec.String(colStatus),
	}
}

// handleListDocuments lists a KB's ingested documents (metadata only; no body).
func (m *Module) handleListDocuments(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colKBRef, id.String()))
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	out := listResponse[documentDTO]{Items: []documentDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDocumentDTO(rec))
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

// handleGetDocument returns one document's metadata + provenance (no body).
func (m *Module) handleGetDocument(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out documentDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toDocumentDTO(rec)
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
