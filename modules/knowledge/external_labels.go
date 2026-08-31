// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// aclStaleSentinel is set as a document's/chunk's ACL when an ACL refresh fails
// during sync. No identity's groups contain this value, so the chunk is excluded
// from every retrieval until the next successful sync.
const aclStaleSentinel = "__acl_stale"

// loadExternalLabels returns a map of document-id → external label list for all
// documents in kbID that have external labels. Called inside an open read scope.
func loadExternalLabels(ctx context.Context, sc store.Scope, kbID model.ID) (map[string][]string, error) {
	repo, err := sc.Ext(extLabelKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colKBRef, kbID.String()))
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(recs))
	for _, rec := range recs {
		s := rec.String(colLabels)
		if strings.TrimSpace(s) == "" {
			continue
		}
		var labels []string
		if err := json.Unmarshal([]byte(s), &labels); err != nil {
			out[rec.String(colDocRef)] = []string{aclStaleSentinel}
			continue
		}
		if len(labels) > 0 {
			out[rec.String(colDocRef)] = labels
		}
	}
	return out, nil
}

// externalLabelsAllowed reports whether the identity (given its clearances) may
// see a chunk whose document carries the given external labels. The rule is
// deny-closed: if a document has ANY external labels and none match a clearance,
// the chunk is denied. Documents with no external labels are always allowed
// (external labels are an opt-in restriction, not a required-to-pass gate).
func externalLabelsAllowed(labels []string, clearances []string) bool {
	if len(labels) == 0 {
		return true // no labels — unrestricted
	}
	if len(clearances) == 0 {
		return false // labels present but identity has no clearance declared
	}
	for _, label := range labels {
		if labelMatchesClearance(label, clearances) {
			return true
		}
	}
	return false
}

// labelMatchesClearance reports whether label is covered by any entry in
// clearances. Matching rules:
//   - Exact match: "purview:confidential" matches "purview:confidential".
//   - Prefix wildcard: "purview:*" matches any label with the "purview:" prefix
//     (e.g. "purview:confidential", "purview:highly-confidential").
func labelMatchesClearance(label string, clearances []string) bool {
	for _, c := range clearances {
		if c == label {
			return true
		}
		if strings.HasSuffix(c, ":*") {
			prefix := strings.TrimSuffix(c, "*")
			if strings.HasPrefix(label, prefix) {
				return true
			}
		}
	}
	return false
}

// upsertExternalLabel writes (or replaces) the external label row for docID
// inside the caller's transaction. Called inside a write scope during ingest.
func upsertExternalLabel(ctx context.Context, sc store.Scope, docID, kbRef, sourceKind string, labels []string) error {
	repo, err := sc.Ext(extLabelKind)
	if err != nil {
		return err
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	existing, ok, err := findOne(ctx, repo, eq(colDocRef, docID))
	if err != nil {
		return err
	}
	if ok {
		existing[colLabels] = string(labelsJSON)
		existing[colSourceKind] = sourceKind
		_, err = repo.Update(ctx, existing)
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colDocRef:     docID,
		colKBRef:      kbRef,
		colLabels:     string(labelsJSON),
		colSourceKind: sourceKind,
	})
	return err
}

// deleteExternalLabel removes the external label row for docID (idempotent).
// Called during ingest when a document no longer carries external labels, so a
// stale label row does not ghost-restrict the newly re-ingested content.
func deleteExternalLabel(ctx context.Context, sc store.Scope, docID string) error {
	repo, err := sc.Ext(extLabelKind)
	if err != nil {
		return err
	}
	existing, ok, err := findOne(ctx, repo, eq(colDocRef, docID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return repo.Delete(ctx, model.ID(existing.String(model.ColID)))
}
