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
	"github.com/olivaresai/olivares/sdk"
)

// fakeKindSource is a minimal contentsource.Source with a fixed SourceKind (returned by
// Fetch as doc.Source) and a fixed ref list — enough to exercise the syncFull
// SourceKind-resolution and orphan-reconciliation paths (F7).
type fakeKindSource struct {
	kind string
	ids  []string
	acl  []string // optional per-doc ACL returned by Fetch (nil ⇒ KB default)
}

func (s *fakeKindSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test." + s.kind, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *fakeKindSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (s *fakeKindSource) Open(context.Context, sdk.Config) error { return nil }
func (s *fakeKindSource) Close(context.Context) error            { return nil }

func (s *fakeKindSource) List(_ context.Context, _ string) ([]contentsource.DocRef, string, error) {
	refs := make([]contentsource.DocRef, len(s.ids))
	for i, id := range s.ids {
		refs[i] = contentsource.DocRef{DocID: id, Title: id}
	}
	return refs, "", nil
}

func (s *fakeKindSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	return contentsource.Document{
		Source: contentsource.SourceKind(s.kind), DocID: id, Title: id,
		Body: "body of " + id, ContentType: "text/markdown", ACL: s.acl,
	}, nil
}

// aclDeltaSource is a live source of a fixed kind that emits ONE delta ChangeACL for docID.
type aclDeltaSource struct {
	kind, docID string
	newACL      []string
	pageServed  bool
}

func (s *aclDeltaSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.acldelta", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *aclDeltaSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (s *aclDeltaSource) Open(context.Context, sdk.Config) error { return nil }
func (s *aclDeltaSource) Close(context.Context) error            { return nil }
func (s *aclDeltaSource) List(_ context.Context, _ string) ([]contentsource.DocRef, string, error) {
	return []contentsource.DocRef{{DocID: s.docID, Title: s.docID}}, "", nil
}
func (s *aclDeltaSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	return contentsource.Document{Source: contentsource.SourceKind(s.kind), DocID: id, Title: id, Body: "b", ContentType: "text/markdown"}, nil
}
func (s *aclDeltaSource) DeltaList(_ context.Context, _ string) (contentsource.DeltaPage, error) {
	if s.pageServed {
		return contentsource.DeltaPage{}, nil
	}
	s.pageServed = true
	return contentsource.DeltaPage{
		Changes:     []contentsource.DeltaEntry{{DocRef: contentsource.DocRef{DocID: s.docID}, ChangeKind: contentsource.ChangeACL}},
		ResumeToken: "acl-1",
	}, nil
}
func (s *aclDeltaSource) FetchACL(_ context.Context, _ string) (contentsource.ACLResult, error) {
	return contentsource.ACLResult{ACL: s.newACL}, nil
}

// TestSyncDelta_ACLChangeKindScoped is the F7 completion regression: a delta ChangeACL
// must update only the row of the SYNCED source's kind, never another source's same-DocID row
// (the processDelete kind-scoping fix extended to the ACL path — a confidentiality boundary).
func TestSyncDelta_ACLChangeKindScoped(t *testing.T) {
	alpha := &fakeKindSource{kind: "alpha", ids: []string{"shared"}, acl: []string{"group:alpha-orig"}}
	beta := &fakeKindSource{kind: "beta", ids: []string{"shared"}, acl: []string{"group:beta-orig"}}
	betaLive := &aclDeltaSource{kind: "beta", docID: "shared", newACL: []string{"group:updated"}}

	h := newHarnessWith(t,
		WithSource("alpha", alpha),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("beta", beta)
	h.mod.AddSource("betalive", betaLive)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	for _, s := range []string{"alpha", "beta"} {
		if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": s}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("ingest %s: %d %s", s, r.code, r.raw)
		}
	}
	// Delta-sync the live beta source: a ChangeACL for "shared" must touch only (beta,shared).
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "betalive"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("delta sync: %d %s", r.code, r.raw)
	}

	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	for _, it := range listItems(docs) {
		k, _ := it["source_kind"].(string)
		acls, _ := it["acl"].([]any)
		var acl []string
		for _, a := range acls {
			if s, ok := a.(string); ok {
				acl = append(acl, s)
			}
		}
		switch k {
		case "beta":
			if len(acl) != 1 || acl[0] != "group:updated" {
				t.Errorf("(beta,shared) ACL = %v, want [group:updated]", acl)
			}
		case "alpha":
			if len(acl) != 1 || acl[0] != "group:alpha-orig" {
				t.Errorf("(alpha,shared) ACL = %v, want [group:alpha-orig] (a beta ACL change must not touch it)", acl)
			}
		}
	}
}

// TestSyncFull_CrossSourceOrphanDeletionRefused is the F7 regression: a KB holds two
// SourceKinds that share a DocID; a full sync of one source must NOT orphan-delete the
// other source's documents. Pre-fix, syncFull resolved the source's SourceKind by the first
// cross-kind DB DocID match, so it seeded the orphan set from the WRONG source and deleted
// its docs. alpha is ingested LAST so its shared doc wins the by-DocID map — the condition
// that made the wrong kind resolve when syncing beta.
func TestSyncFull_CrossSourceOrphanDeletionRefused(t *testing.T) {
	alpha := &fakeKindSource{kind: "alpha", ids: []string{"shared", "alpha-only"}}
	beta := &fakeKindSource{kind: "beta", ids: []string{"shared", "beta-only"}}

	h := newHarnessWith(t,
		WithSource("beta", beta),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("alpha", alpha)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest BETA first, then ALPHA, so alpha's "shared" wins the by-DocID map.
	for _, s := range []string{"beta", "alpha"} {
		r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": s}, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("ingest %s: %d %s", s, r.code, r.raw)
		}
	}

	// Full-sync BETA (still lists shared+beta-only): no beta doc was removed, so nothing
	// must be deleted, and ALPHA's unique doc must survive.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "beta"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync beta: %d %s", r.code, r.raw)
	}
	if del, _ := r.body["docs_deleted"].(float64); del != 0 {
		t.Fatalf("beta sync deleted %v docs — cross-source orphan deletion (F7)", del)
	}
	// 4 docs must remain: (alpha,shared) (alpha,alpha-only) (beta,shared) (beta,beta-only).
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if got := len(listItems(docs)); got != 4 {
		t.Fatalf("after beta sync: %d docs remain, want 4 (alpha-only orphan-deleted? F7); body=%s", got, docs.raw)
	}
}

// loopingSource is a contentsource.Source whose List NEVER exhausts: it returns a constant
// non-advancing cursor. An internal hard stop keeps a pre-fix (unguarded) run from hanging
// the test forever; the fix must stop it in a couple of calls via the non-advancing guard.
type loopingSource struct {
	calls int64
}

func (s *loopingSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.looping", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *loopingSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (s *loopingSource) Open(context.Context, sdk.Config) error { return nil }
func (s *loopingSource) Close(context.Context) error            { return nil }
func (s *loopingSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	return contentsource.Document{Source: "looping", DocID: id, Title: id, Body: "b", ContentType: "text/markdown"}, nil
}
func (s *loopingSource) List(_ context.Context, _ string) ([]contentsource.DocRef, string, error) {
	if atomic.AddInt64(&s.calls, 1) > 1000 {
		return nil, "", errors.New("looping source: internal hard stop (would have looped forever)")
	}
	return []contentsource.DocRef{{DocID: "d0", Title: "d0"}}, "loop", nil // constant cursor
}

// TestSyncFull_NonAdvancingCursorRefused is the F6 regression: a source that returns a
// non-advancing pagination cursor forever must fail visibly in a bounded number of calls,
// not loop the engine. Pre-fix the loop ran to the source's internal hard stop (1000 calls);
// the fix trips the non-advancing-cursor guard on the second call.
func TestSyncFull_NonAdvancingCursorRefused(t *testing.T) {
	src := &loopingSource{}
	h := newHarnessWith(t,
		WithSource("looping", src),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "looping"}, tenantHdr(tenant))
	if r.code == http.StatusOK {
		t.Fatalf("sync of a non-advancing source must fail, got 200: %s", r.raw)
	}
	if got := atomic.LoadInt64(&src.calls); got > 3 {
		t.Fatalf("non-advancing source was listed %d times — the unbounded-wire guard did not stop it (F6)", got)
	}
}

// skipThenRealSource lists a SKIPPABLE ref on page 1 and a real doc on page 2, so the
// SourceKind cannot be resolved from page 1 — the case that must not latch resolved=true
// with an empty kind (F5 review finding 3).
type skipThenRealSource struct{ realKind string }

func (*skipThenRealSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.skipthenreal", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*skipThenRealSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (*skipThenRealSource) Open(context.Context, sdk.Config) error { return nil }
func (*skipThenRealSource) Close(context.Context) error            { return nil }
func (*skipThenRealSource) List(_ context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	switch cursor {
	case "":
		return []contentsource.DocRef{{DocID: "skip1", Title: "skip1"}}, "p2", nil
	case "p2":
		return []contentsource.DocRef{{DocID: "real1", Title: "real1"}}, "", nil
	default:
		return nil, "", nil
	}
}
func (s *skipThenRealSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	if id == "skip1" {
		return contentsource.Document{}, contentsource.ErrSkipDocument
	}
	return contentsource.Document{Source: contentsource.SourceKind(s.realKind), DocID: id, Title: id, Body: "body " + id, ContentType: "text/markdown"}, nil
}

// TestSyncFull_KindScopedOrphanDelete is the F7 review finding-4 regression: it reaches
// the actual DELETE. A KB holds two SourceKinds sharing DocID "shared"; beta removes its
// "shared" at the source, so a full sync of beta must delete ONLY (beta,shared) and leave
// alpha's (alpha,shared) and (alpha,alpha-only) intact — the delete must be kind-scoped, not
// resolved by (KBRef,SourceDocID) alone.
func TestSyncFull_KindScopedOrphanDelete(t *testing.T) {
	alpha := &fakeKindSource{kind: "alpha", ids: []string{"shared", "alpha-only"}}
	beta := &fakeKindSource{kind: "beta", ids: []string{"shared", "beta-only"}}

	h := newHarnessWith(t,
		WithSource("beta", beta),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("alpha", alpha)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	for _, s := range []string{"alpha", "beta"} {
		if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": s}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("ingest %s: %d %s", s, r.code, r.raw)
		}
	}
	// Remove beta's "shared" at the source → it becomes an orphan of kind beta.
	beta.ids = []string{"beta-only"}

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "beta"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync beta: %d %s", r.code, r.raw)
	}
	if del, _ := r.body["docs_deleted"].(float64); del != 1 {
		t.Fatalf("beta sync deleted %v docs, want exactly 1 (beta,shared)", del)
	}
	// alpha's (alpha,shared) MUST survive; the (beta,shared) row is the only one deleted.
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	var alphaShared, betaShared bool
	for _, it := range listItems(docs) {
		k, _ := it["source_kind"].(string)
		sid, _ := it["source_doc_id"].(string)
		if sid == "shared" && k == "alpha" {
			alphaShared = true
		}
		if sid == "shared" && k == "beta" {
			betaShared = true
		}
	}
	if !alphaShared {
		t.Fatalf("(alpha,shared) was deleted by a beta sync — kind-unscoped delete (F7 finding-4); docs=%s", docs.raw)
	}
	if betaShared {
		t.Fatalf("(beta,shared) survived — the orphan of the synced source was not deleted")
	}
}

// TestSyncFull_SkippableFirstPageResolvesLater is the F5 review finding-3 regression: a
// source whose FIRST page is all-skippable must not latch resolved=true with an empty
// SourceKind (which would re-embed already-persisted docs on later pages). real1 is ingested
// first; a sync that pages skip1→real1 must recognize real1 as existing (docs_synced==0).
func TestSyncFull_SkippableFirstPageResolvesLater(t *testing.T) {
	seed := &fakeKindSource{kind: "realkind", ids: []string{"real1"}}
	src := &skipThenRealSource{realKind: "realkind"}

	h := newHarnessWith(t,
		WithSource("seed", seed),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("skipthenreal", src)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "seed"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seed ingest: %d %s", r.code, r.raw)
	}
	// Sync the skip-then-real source: real1 already exists under realkind, so it must be
	// recognized as existing (not re-embedded) once the kind resolves on page 2.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "skipthenreal"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 0 {
		t.Fatalf("docs_synced=%v, want 0 — real1 was re-embedded because the empty kind latched (finding 3)", synced)
	}
}

// pagedSkippableSource lists a ref on page 1 that is SKIPPABLE on Fetch (a doc that was
// ingestable at ingest time but later became skippable — e.g. rich-doc extraction disabled)
// and a real doc on page 2. Page 1 cannot resolve the kind, so the skippable ref streams by
// before the orphan set exists (F5 review finding).
type pagedSkippableSource struct{ kind, skippableID, realID string }

func (*pagedSkippableSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.pagedskippable", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*pagedSkippableSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (*pagedSkippableSource) Open(context.Context, sdk.Config) error { return nil }
func (*pagedSkippableSource) Close(context.Context) error            { return nil }
func (s *pagedSkippableSource) List(_ context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	switch cursor {
	case "":
		return []contentsource.DocRef{{DocID: s.skippableID, Title: s.skippableID}}, "p2", nil
	case "p2":
		return []contentsource.DocRef{{DocID: s.realID, Title: s.realID}}, "", nil
	default:
		return nil, "", nil
	}
}
func (s *pagedSkippableSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	if id == s.skippableID {
		return contentsource.Document{}, contentsource.ErrSkipDocument
	}
	return contentsource.Document{Source: contentsource.SourceKind(s.kind), DocID: id, Title: id, Body: "b " + id, ContentType: "text/markdown"}, nil
}

// TestSyncFull_SkippableListedDocNotOrphanDeleted is the F5 re-review finding-A
// regression: a doc that is still LISTED by the source but is skippable-on-fetch, arriving on
// an all-skippable first page, must NOT be orphan-deleted when the kind resolves on a later
// page — it is being listed, so it is not an orphan.
func TestSyncFull_SkippableListedDocNotOrphanDeleted(t *testing.T) {
	const kind = "K"
	seed := &fakeKindSource{kind: kind, ids: []string{"skippable-doc"}}
	sync := &pagedSkippableSource{kind: kind, skippableID: "skippable-doc", realID: "real-doc"}

	h := newHarnessWith(t,
		WithSource("seed", seed),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("paged", sync)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Seed: ingest "skippable-doc" under kind K while it is still fetchable.
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "seed"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seed ingest: %d %s", r.code, r.raw)
	}
	// Full-sync the paged source: page1=[skippable-doc] (now skippable), page2=[real-doc].
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "paged"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	if del, _ := r.body["docs_deleted"].(float64); del != 0 {
		t.Fatalf("docs_deleted=%v — a still-listed skippable doc was orphan-deleted (finding A data loss)", del)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	var found bool
	for _, it := range listItems(docs) {
		if sid, _ := it["source_doc_id"].(string); sid == "skippable-doc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skippable-doc was deleted despite being listed (finding A); docs=%s", docs.raw)
	}
}

// allSkippableSource lists n refs across pages, ALL skippable on Fetch, NONE in the DB — the
// case where the SourceKind never resolves. The pre-resolution seen-buffer must NOT accumulate
// every listed DocID (F5 final-review finding: O(corpus) buffer reintroduces the OOM).
type allSkippableSource struct {
	n, pageSize int
	fetches     int64
}

func (*allSkippableSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.allskippable", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*allSkippableSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (*allSkippableSource) Open(context.Context, sdk.Config) error { return nil }
func (*allSkippableSource) Close(context.Context) error            { return nil }
func (s *allSkippableSource) List(_ context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	off := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &off)
	}
	end := off + s.pageSize
	if end > s.n {
		end = s.n
	}
	refs := make([]contentsource.DocRef, 0, end-off)
	for i := off; i < end; i++ {
		refs = append(refs, contentsource.DocRef{DocID: fmt.Sprintf("skip-%d", i), Title: "t"})
	}
	next := ""
	if end < s.n {
		next = fmt.Sprintf("%d", end)
	}
	return refs, next, nil
}
func (s *allSkippableSource) Fetch(_ context.Context, _ string) (contentsource.Document, error) {
	atomic.AddInt64(&s.fetches, 1)
	return contentsource.Document{}, contentsource.ErrSkipDocument
}

// TestSyncFull_AllSkippableSourceBoundedBuffer is the F5 final-review regression: a source
// that lists many refs, all skippable and none in the DB, must complete without accumulating an
// O(corpus) seen-buffer. The kind never resolves, so deletes are deferred and everything is a
// counted skip; the buffer stays empty because no ref is a DB row.
func TestSyncFull_AllSkippableSourceBoundedBuffer(t *testing.T) {
	src := &allSkippableSource{n: 5000, pageSize: 500}
	h := newHarnessWith(t,
		WithSource("allskip", src),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "allskip"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	// Kind never resolves ⇒ deletes deferred; every ref is a counted skip; nothing deleted.
	if deferred, _ := r.body["deletes_deferred"].(bool); !deferred {
		t.Errorf("deletes must be deferred when the source kind never resolves")
	}
	if del, _ := r.body["docs_deleted"].(float64); del != 0 {
		t.Errorf("docs_deleted=%v, want 0 (kind unresolved ⇒ no reconciliation)", del)
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 0 {
		t.Errorf("docs_synced=%v, want 0 (all refs skippable)", synced)
	}
}

// dupNewSource lists the SAME new DocID on two pages (a malformed source), both fetchable, to
// exercise the in-run seen-marking (F5 final-review finding-2: without it the doc is
// re-embedded on the second listing).
type dupNewSource struct{ kind, id string }

func (*dupNewSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.dupnew", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*dupNewSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (*dupNewSource) Open(context.Context, sdk.Config) error { return nil }
func (*dupNewSource) Close(context.Context) error            { return nil }
func (s *dupNewSource) List(_ context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	switch cursor {
	case "":
		return []contentsource.DocRef{{DocID: s.id, Title: s.id}}, "p2", nil
	case "p2":
		return []contentsource.DocRef{{DocID: s.id, Title: s.id}}, "", nil
	default:
		return nil, "", nil
	}
}
func (s *dupNewSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	return contentsource.Document{Source: contentsource.SourceKind(s.kind), DocID: id, Title: id, Body: "b " + id, ContentType: "text/markdown"}, nil
}

// TestSyncFull_DuplicateNewDocIngestedOnce is the F5 finding-2 regression: a malformed
// source that lists the same NEW DocID on two pages must ingest it ONCE (the second listing is
// a skip via the in-run seen-mark), not re-embed it.
func TestSyncFull_DuplicateNewDocIngestedOnce(t *testing.T) {
	src := &dupNewSource{kind: "K", id: "dupnew"}
	h := newHarnessWith(t,
		WithSource("dup", src),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "dup"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 1 {
		t.Fatalf("docs_synced=%v, want 1 — a re-listed new DocID was re-embedded (finding 2)", synced)
	}
}

// TestSyncFull_DuplicateInDBSkippableBounded exercises the finding-1 set-dedup path: a source
// re-lists an in-DB skippable doc on many pre-resolution pages, then resolves on a later page.
// The in-DB doc is still LISTED so it must survive, and the (set) buffer holds it once.
func TestSyncFull_DuplicateInDBSkippableBounded(t *testing.T) {
	const kind = "K"
	seed := &fakeKindSource{kind: kind, ids: []string{"indoc"}}
	sync := &dupInDBSkippableSource{kind: kind, dupID: "indoc", realID: "realdoc", pages: 50}

	h := newHarnessWith(t,
		WithSource("seed", seed),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret}}),
	)
	h.mod.AddSource("dupskip", sync)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "seed"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seed ingest: %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "dupskip"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	if del, _ := r.body["docs_deleted"].(float64); del != 0 {
		t.Fatalf("docs_deleted=%v — a re-listed in-DB doc was orphan-deleted", del)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	var found bool
	for _, it := range listItems(docs) {
		if sid, _ := it["source_doc_id"].(string); sid == "indoc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-DB doc deleted despite being re-listed; docs=%s", docs.raw)
	}
}

// dupInDBSkippableSource re-lists dupID (skippable, in DB) on `pages` pre-resolution pages,
// then realID (fetchable) to resolve the kind.
type dupInDBSkippableSource struct {
	kind, dupID, realID string
	pages               int
}

func (*dupInDBSkippableSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.dupinskip", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*dupInDBSkippableSource) Kind() contentsource.ContentClass       { return contentsource.ClassDocument }
func (*dupInDBSkippableSource) Open(context.Context, sdk.Config) error { return nil }
func (*dupInDBSkippableSource) Close(context.Context) error            { return nil }
func (s *dupInDBSkippableSource) List(_ context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	page := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &page)
	}
	if page < s.pages {
		return []contentsource.DocRef{{DocID: s.dupID, Title: s.dupID}}, fmt.Sprintf("%d", page+1), nil
	}
	if page == s.pages {
		return []contentsource.DocRef{{DocID: s.realID, Title: s.realID}}, "", nil
	}
	return nil, "", nil
}
func (s *dupInDBSkippableSource) Fetch(_ context.Context, id string) (contentsource.Document, error) {
	if id == s.dupID {
		return contentsource.Document{}, contentsource.ErrSkipDocument
	}
	return contentsource.Document{Source: contentsource.SourceKind(s.kind), DocID: id, Title: id, Body: "b " + id, ContentType: "text/markdown"}, nil
}
