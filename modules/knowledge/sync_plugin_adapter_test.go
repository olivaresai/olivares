// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

type fakePluginContentSource struct {
	mu      sync.Mutex
	order   []string
	docs    map[string]sdk.Document
	pages   []sdk.DeltaPage
	pageIdx int
	acls    map[string]sdk.ACLResult
}

func newFakePluginContentSource(docs []sdk.Document) *fakePluginContentSource {
	s := &fakePluginContentSource{acls: map[string]sdk.ACLResult{}}
	s.setDocs(docs)
	return s
}

func (s *fakePluginContentSource) setDocs(docs []sdk.Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = s.order[:0]
	s.docs = make(map[string]sdk.Document, len(docs))
	for _, doc := range docs {
		s.order = append(s.order, doc.DocID)
		s.docs[doc.DocID] = doc
	}
}

func (s *fakePluginContentSource) setPages(pages []sdk.DeltaPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = pages
	s.pageIdx = 0
}

func (s *fakePluginContentSource) setACL(docID string, acl sdk.ACLResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acls[docID] = acl
}

func (s *fakePluginContentSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       "test.plugin-content",
		Type:       sdk.TypeContentSource,
		APIVersion: sdk.APIVersion,
		Surfaces:   []string{"knowledge.document"},
	}
}
func (s *fakePluginContentSource) Open(context.Context, sdk.Config) error { return nil }
func (s *fakePluginContentSource) Close(context.Context) error            { return nil }

func (s *fakePluginContentSource) List(_ context.Context, _ string) ([]sdk.DocRef, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]sdk.DocRef, 0, len(s.order))
	for _, id := range s.order {
		doc := s.docs[id]
		refs = append(refs, sdk.DocRef{
			DocID: doc.DocID, Title: doc.Title, ContentType: doc.ContentType, ModifiedAt: doc.ModifiedAt,
		})
	}
	return refs, "", nil
}

func (s *fakePluginContentSource) Fetch(_ context.Context, docID string) (sdk.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[docID]
	if !ok {
		return sdk.Document{}, errors.New("plugin content source: not found")
	}
	return doc, nil
}

func (s *fakePluginContentSource) DeltaList(_ context.Context, _ string) (sdk.DeltaPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pageIdx >= len(s.pages) {
		return sdk.DeltaPage{}, nil
	}
	page := s.pages[s.pageIdx]
	s.pageIdx++
	return page, nil
}

func (s *fakePluginContentSource) FetchACL(_ context.Context, docID string) (sdk.ACLResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if acl, ok := s.acls[docID]; ok {
		return acl, nil
	}
	return sdk.ACLResult{}, nil
}

type pluginBackedContentSource struct {
	src sdk.ContentSource
}

func (s pluginBackedContentSource) Descriptor() sdk.Descriptor { return s.src.Descriptor() }
func (s pluginBackedContentSource) Kind() contentsource.ContentClass {
	return contentsource.ClassDocument
}
func (s pluginBackedContentSource) Open(ctx context.Context, cfg sdk.Config) error {
	return s.src.Open(ctx, cfg)
}
func (s pluginBackedContentSource) Close(ctx context.Context) error { return s.src.Close(ctx) }
func (s pluginBackedContentSource) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	refs, next, err := s.src.List(ctx, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]contentsource.DocRef, len(refs))
	for i, ref := range refs {
		out[i] = contentsource.DocRef{
			DocID: ref.DocID, Title: ref.Title, ContentType: ref.ContentType, ModifiedAt: ref.ModifiedAt,
		}
	}
	return out, next, nil
}
func (s pluginBackedContentSource) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	doc, err := s.src.Fetch(ctx, docID)
	if err != nil {
		return contentsource.Document{}, err
	}
	return contentsource.Document{
		Source:         contentsource.SourceKind(doc.Source),
		DocID:          doc.DocID,
		Title:          doc.Title,
		Body:           string(doc.Body),
		ContentType:    doc.ContentType,
		ACL:            doc.ACL,
		Classification: doc.Classification,
		SpaceRef:       doc.SpaceRef,
		ModifiedAt:     doc.ModifiedAt,
		Attributes:     doc.Attributes,
		ExternalLabels: doc.ExternalLabels,
	}, nil
}

type pluginBackedLiveSource struct {
	pluginBackedContentSource
	live sdk.DeltaContentSource
}

func (s pluginBackedLiveSource) DeltaList(ctx context.Context, cursor string) (contentsource.DeltaPage, error) {
	page, err := s.live.DeltaList(ctx, cursor)
	if err != nil {
		return contentsource.DeltaPage{}, err
	}
	out := contentsource.DeltaPage{
		Changes:     make([]contentsource.DeltaEntry, len(page.Changes)),
		NextToken:   page.NextToken,
		ResumeToken: page.ResumeToken,
		Expired:     page.Expired,
	}
	for i, change := range page.Changes {
		out.Changes[i] = contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       change.DocRef.DocID,
				Title:       change.DocRef.Title,
				ContentType: change.DocRef.ContentType,
				ModifiedAt:  change.DocRef.ModifiedAt,
			},
			ChangeKind: contentsource.ChangeKind(change.ChangeKind),
		}
	}
	return out, nil
}

func (s pluginBackedLiveSource) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	acl, err := s.live.FetchACL(ctx, docID)
	if err != nil {
		return contentsource.ACLResult{}, err
	}
	return contentsource.ACLResult{
		ACL:            acl.ACL,
		ExternalLabels: acl.ExternalLabels,
		Classification: acl.Classification,
	}, nil
}

func TestSyncPluginBackedAdapterFullAndDelta(t *testing.T) {
	when := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	pluginSrc := newFakePluginContentSource([]sdk.Document{
		{Source: "pluginsrc", DocID: "d1", Title: "One", Body: []byte("initial body one"), ModifiedAt: when},
		{Source: "pluginsrc", DocID: "d2", Title: "Two", Body: []byte("initial body two"), ACL: []string{"group:engineering"}, ModifiedAt: when},
		{Source: "pluginsrc", DocID: "d3", Title: "Three", Body: []byte("initial body three"), ModifiedAt: when},
	})
	adapter := pluginBackedLiveSource{
		pluginBackedContentSource: pluginBackedContentSource{src: pluginSrc},
		live:                      pluginSrc,
	}

	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"group:engineering"}, Clearance: classSecret,
	}}))
	h.mod.AddSource("pluginsrc", adapter)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-plugin-sync")
	editor := h.roleToken(admin, tenant, "ed@acme-plugin-sync.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "plugin-kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "pluginsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("initial ingest: got %d %s", r.code, r.raw)
	}
	if docs, _ := r.body["documents"].(float64); int(docs) != 3 {
		t.Fatalf("initial ingest documents = %v, want 3 (body: %s)", r.body["documents"], r.raw)
	}

	pluginSrc.setDocs([]sdk.Document{
		{Source: "pluginsrc", DocID: "d1", Title: "One", Body: []byte("updated body one"), ModifiedAt: when.Add(time.Hour)},
		{Source: "pluginsrc", DocID: "d2", Title: "Two", Body: []byte("initial body two"), ACL: []string{"group:engineering"}, ModifiedAt: when},
	})
	pluginSrc.setACL("d2", sdk.ACLResult{ACL: []string{"group:unknown"}, Classification: "internal"})
	pluginSrc.setPages([]sdk.DeltaPage{{
		Changes: []sdk.Change{
			{ChangeKind: sdk.ChangeContent, DocRef: sdk.DocRef{DocID: "d1"}},
			{ChangeKind: sdk.ChangeACL, DocRef: sdk.DocRef{DocID: "d2"}},
			{ChangeKind: sdk.ChangeDeleted, DocRef: sdk.DocRef{DocID: "d3"}},
		},
		ResumeToken: "plugin-delta-1",
	}})

	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "pluginsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("delta sync: got %d %s", r.code, r.raw)
	}
	if synced, _ := r.body["docs_synced"].(float64); int(synced) != 1 {
		t.Errorf("docs_synced = %v, want 1", r.body["docs_synced"])
	}
	if refreshed, _ := r.body["acls_refreshed"].(float64); int(refreshed) != 1 {
		t.Errorf("acls_refreshed = %v, want 1", r.body["acls_refreshed"])
	}
	if deleted, _ := r.body["docs_deleted"].(float64); int(deleted) != 1 {
		t.Errorf("docs_deleted = %v, want 1", r.body["docs_deleted"])
	}
	if full, _ := r.body["full_reconciliation"].(bool); full {
		t.Error("plugin-backed live adapter should use delta sync, not full reconciliation")
	}
	if got := h.syncToken(t, tenant, kbID, "pluginsrc"); got != "plugin-delta-1" {
		t.Fatalf("sync token = %q, want plugin-delta-1", got)
	}

	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 2 {
		t.Fatalf("after plugin-backed sync: want 2 docs, got %d (body: %s)", len(items), docsResp.raw)
	}
}
