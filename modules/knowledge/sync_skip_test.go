// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// skippingSource returns contentsource.ErrSkipDocument (wrapped) for the marked
// DocIDs — the "detected but not ingestable" signal a content connector emits for a
// binary / non-extractable rich document. The rest Fetch normally.
type skippingSource struct {
	*fakeSource
	skip map[string]bool
}

func (s *skippingSource) Fetch(ctx context.Context, id string) (contentsource.Document, error) {
	if s.skip[id] {
		return contentsource.Document{}, fmt.Errorf("fake binary %s: %w", id, contentsource.ErrSkipDocument)
	}
	return s.fakeSource.Fetch(ctx, id)
}

// completenessSource is a static source that reports whether its listing is complete
// (the contentsource.CompletenessReporter capability a partial filesystem walk uses).
type completenessSource struct {
	*fakeSource
	complete bool
}

func (s *completenessSource) ListingComplete() bool { return s.complete }

// TestSyncFull_IncompleteListingDefersDeletes proves the highest-stakes safety gate: a
// source whose listing is INCOMPLETE (a partial tree walk / transient read error) must
// NOT drive orphan deletion — an absent document may be one the source failed to
// enumerate, not one that was removed. Deleting it would destroy data on a source blip.
func TestSyncFull_IncompleteListingDefersDeletes(t *testing.T) {
	allDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "document one"},
		{Source: "testsrc", DocID: "d2", Body: "document two"},
		{Source: "testsrc", DocID: "d3", Body: "document three"},
	}
	twoDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "document one"},
		{Source: "testsrc", DocID: "d2", Body: "document two"},
	}

	h := newHarnessWith(t,
		WithSource("testsrc", newFakeSource(allDocs)),
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest all three.
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("initial ingest: %d %s", r.code, r.raw)
	}

	// Re-register with only d1,d2 but the listing is INCOMPLETE (d3 might just be
	// un-enumerated). Sync must DEFER the delete: d3 survives.
	h.addSource("testsrc", &completenessSource{fakeSource: newFakeSource(twoDocs), complete: false})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync (incomplete): %d %s", r.code, r.raw)
	}
	if d, _ := r.body["docs_deleted"].(float64); d != 0 {
		t.Errorf("docs_deleted = %v, want 0 (incomplete listing must defer)", r.body["docs_deleted"])
	}
	if def, _ := r.body["deletes_deferred"].(bool); !def {
		t.Errorf("deletes_deferred = %v, want true", r.body["deletes_deferred"])
	}
	if items := listItems(h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))); len(items) != 3 {
		t.Fatalf("after incomplete sync: want 3 docs still present (no false delete), got %d", len(items))
	}

	// Now the SAME two-doc listing but reported COMPLETE: the orphan (d3) is deleted.
	h.addSource("testsrc", &completenessSource{fakeSource: newFakeSource(twoDocs), complete: true})
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync (complete): %d %s", r.code, r.raw)
	}
	if d, _ := r.body["docs_deleted"].(float64); d != 1 {
		t.Errorf("docs_deleted = %v, want 1 (complete listing reconciles the removal)", r.body["docs_deleted"])
	}
	if def, _ := r.body["deletes_deferred"].(bool); def {
		t.Errorf("deletes_deferred = %v, want false on a complete listing", r.body["deletes_deferred"])
	}
	if items := listItems(h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))); len(items) != 2 {
		t.Fatalf("after complete sync: want 2 docs (d3 reconciled away), got %d", len(items))
	}
}

// TestSyncFull_SkipsAreCountedNotFatal proves the ingest loop treats a classified
// per-document skip as a counted skip (docs_skipped), never a fatal abort — including
// when the skipped document sorts FIRST (the probe-fetch that resolves SourceKind must
// step over it). The good documents on either side are still ingested.
func TestSyncFull_SkipsAreCountedNotFatal(t *testing.T) {
	// The skip doc (dskip) is FIRST so it is also the probe-fetch's first candidate.
	docs := []contentsource.Document{
		{Source: "testsrc", DocID: "dskip", Body: "binary blob"},
		{Source: "testsrc", DocID: "d1", Body: "document one"},
		{Source: "testsrc", DocID: "d2", Body: "document two"},
	}
	src := &skippingSource{fakeSource: newFakeSource(docs), skip: map[string]bool{"dskip": true}}

	h := newHarnessWith(t,
		WithSource("testsrc", src),
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Full reconciliation over an empty KB: ingest the new docs, skip the binary one.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}
	if d, _ := r.body["docs_synced"].(float64); d != 2 {
		t.Errorf("docs_synced = %v, want 2 (full: %s)", r.body["docs_synced"], r.raw)
	}
	if s, _ := r.body["docs_skipped"].(float64); s != 1 {
		t.Errorf("docs_skipped = %v, want 1 (full: %s)", r.body["docs_skipped"], r.raw)
	}
	if errs, ok := r.body["errors"].([]any); ok && len(errs) != 0 {
		t.Errorf("errors = %v, want none (a skip must not be an error)", errs)
	}

	// The two good documents are stored; the skipped one is not.
	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 2 {
		t.Fatalf("after sync: want 2 stored docs, got %d (body: %s)", len(items), docsResp.raw)
	}
}
