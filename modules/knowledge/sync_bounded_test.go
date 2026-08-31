// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// fakePagedSource is a synthetic contentsource.PagedSource whose refs are GENERATED, not
// materialized — so a test can point the reconciliation at a virtually huge corpus while
// the source only ever holds one bounded page. It records the largest page it returned and
// how many ListPage calls it served, which is how the streaming property is asserted.
type fakePagedSource struct {
	n           int   // total virtual docs: doc-0 .. doc-(n-1)
	maxPageSeen int64 // largest single page returned
	listCalls   int64 // number of ListPage calls served
	failFirst   bool  // if set, the first ListPage call returns an error (mid-stream abort proxy)
}

func (s *fakePagedSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.paged-source", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *fakePagedSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (s *fakePagedSource) Open(context.Context, sdk.Config) error { return nil }
func (s *fakePagedSource) Close(context.Context) error            { return nil }

func (s *fakePagedSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	return contentsource.Document{
		Source: "test.paged", DocID: id, Title: id,
		Body: "body of " + id, ContentType: "text/markdown",
	}, nil
}

// List (the fallback) is the unbounded drain the wire used to force; a PagedSource must
// never be reduced to it. Present only to satisfy the interface.
func (s *fakePagedSource) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	refs, next, _, err := s.ListPage(ctx, cursor, s.n, 0)
	return refs, next, err
}

func (s *fakePagedSource) ListPage(ctx context.Context, cursor string, maxItems, _ int) ([]contentsource.DocRef, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", false, err // a canceled context cuts the paging immediately
	}
	atomic.AddInt64(&s.listCalls, 1)
	if s.failFirst {
		return nil, "", false, errors.New("fake paged source: mid-stream abort")
	}
	off := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d", &off); err != nil {
			return nil, "", false, fmt.Errorf("bad cursor %q", cursor)
		}
	}
	end := s.n
	if maxItems > 0 && off+maxItems < end {
		end = off + maxItems
	}
	refs := make([]contentsource.DocRef, 0, end-off)
	for i := off; i < end; i++ {
		refs = append(refs, contentsource.DocRef{DocID: fmt.Sprintf("doc-%d", i), Title: fmt.Sprintf("doc-%d", i)})
	}
	for {
		cur := atomic.LoadInt64(&s.maxPageSeen)
		if int64(len(refs)) <= cur || atomic.CompareAndSwapInt64(&s.maxPageSeen, cur, int64(len(refs))) {
			break
		}
	}
	next := ""
	if end < s.n {
		next = fmt.Sprintf("%d", end)
	}
	return refs, next, true, nil // this fake always enumerates a complete page
}

var _ contentsource.PagedSource = (*fakePagedSource)(nil)

func pagedHarness(t *testing.T, src *fakePagedSource) (h *harness, tenant model.TenantID, editor, kbID string) {
	t.Helper()
	h = newHarnessWith(t,
		WithSource("paged", src),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	admin := h.adminLogin()
	tenant = h.createOrg(admin, "acme")
	editor = h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID = h.mustKB(editor, tenant, map[string]any{"name": "kb1"})
	return h, tenant, editor, kbID
}

// TestSyncFull_BoundedPaging proves the reconciliation STREAMS the source in bounded pages
// (never drained into one slice) and still ingests every doc. n spans several pages, so a
// regression that re-materialized the corpus would surface as one max-size page.
func TestSyncFull_BoundedPaging(t *testing.T) {
	const n = syncListMaxItems + 60 // 2 pages: 1000 + 60
	src := &fakePagedSource{n: n}
	h, tenant, editor, kbID := pagedHarness(t, src)

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "paged"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}
	if ds, _ := r.body["docs_synced"].(float64); int(ds) != n {
		t.Fatalf("docs_synced = %v, want %d (every ref ingested via streaming)", r.body["docs_synced"], n)
	}
	if got := atomic.LoadInt64(&src.maxPageSeen); got > syncListMaxItems {
		t.Fatalf("a single page held %d refs > cap %d — the source was NOT paged with bounds", got, syncListMaxItems)
	}
	if got := atomic.LoadInt64(&src.listCalls); got < 2 {
		t.Fatalf("expected >=2 bounded ListPage calls for %d docs, got %d (corpus drained?)", n, got)
	}
}

// TestSyncFull_StopsMidStream proves the streaming loop propagates a list error (the same
// path a canceled context takes — ListPage returns the error) and stops promptly instead
// of draining the corpus.
func TestSyncFull_StopsMidStream(t *testing.T) {
	// The first list call errors, so nothing is drained and the test is fast.
	src := &fakePagedSource{n: 1_000_000, failFirst: true}
	h, tenant, editor, kbID := pagedHarness(t, src)

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "paged"}, tenantHdr(tenant))
	if r.code == http.StatusOK {
		t.Fatalf("sync must fail on a mid-stream list error, got 200: %s", r.raw)
	}
	if got := atomic.LoadInt64(&src.listCalls); got > 2 {
		t.Fatalf("aborted sync kept paging a 1M-doc corpus: %d ListPage calls", got)
	}
}
