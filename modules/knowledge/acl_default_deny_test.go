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
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A document that arrives without an ACL is denied; "public" is an explicit
// per-source decision (#3030). These tests drive the real ingest, retrieval and
// scan routes: the identity has NO groups, so only an explicit "anyone" shows.

// publicFakeSource is a fakeSource whose operator declared it public.
type publicFakeSource struct{ *fakeSource }

func (publicFakeSource) Public() bool { return true }

func noGroupsGuard() Option {
	return WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classPublic}})
}

// rowsWithoutACL stands in for a pgcontent table that declares no ACL columns.
func rowsWithoutACL() []contentsource.Document {
	return []contentsource.Document{
		{Source: "pgcontent", DocID: "row1", Title: "row1", Body: "matrix retrieval orders row one"},
		{Source: "pgcontent", DocID: "row2", Title: "row2", Body: "matrix retrieval orders row two"},
	}
}

func ingestFromSource(t *testing.T, h *harness, token string, tenant model.TenantID, kbID, source string) {
	t.Helper()
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", token, map[string]any{"source": source}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest from %s = %d %s", source, r.code, r.raw)
	}
}

func auditActionCount(t *testing.T, h *harness, tenant model.TenantID, action string) int {
	t.Helper()
	n := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == action {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("audit walk: %v", err)
	}
	return n
}

func documentACLs(h *harness, tenant model.TenantID, kbID string) map[string][]string {
	out := map[string][]string{}
	for _, d := range h.extRecords(tenant, documentKind, eq(colKBRef, kbID)) {
		out[d.String(colSourceDocID)] = unmarshalStrings(d, colACL)
	}
	return out
}

func TestSourceDocumentWithoutACLIsDenied(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard(), WithSource("orders", newFakeSource(rowsWithoutACL())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "orders-kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, kbID, "orders")

	got := queryTitleSet(t, h, editor, tenant, kbID)
	if got["row1"] || got["row2"] {
		t.Fatalf("a source row with no ACL must be denied to an identity with no groups; got %v", got)
	}
	if auditActionCount(t, h, tenant, "knowledge.source.public") != 0 {
		t.Fatal("a source that is not public must leave no public-source audit entry")
	}
}

func TestPublicSourceDocumentWithoutACLIsRetrievedAndAudited(t *testing.T) {
	src := publicFakeSource{newFakeSource(rowsWithoutACL())}
	h := newHarnessWith(t, noGroupsGuard(), WithSource("orders", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "orders-kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, kbID, "orders")

	got := queryTitleSet(t, h, editor, tenant, kbID)
	if !got["row1"] || !got["row2"] {
		t.Fatalf("rows of a public source must be retrieved; got %v", got)
	}
	for id, acl := range documentACLs(h, tenant, kbID) {
		if len(acl) != 1 || acl[0] != "anyone" {
			t.Errorf("document %s ACL = %v, want the explicit [anyone]", id, acl)
		}
	}
	if n := auditActionCount(t, h, tenant, "knowledge.source.public"); n != 1 {
		t.Fatalf("public-source audit entries = %d, want 1 per ingest", n)
	}
}

func TestExplicitACLAndKBDefaultOutrankThePublicFlag(t *testing.T) {
	docs := []contentsource.Document{
		{Source: "pgcontent", DocID: "own", Title: "own", Body: "matrix retrieval own acl", ACL: []string{"group:hr"}},
		{Source: "pgcontent", DocID: "dflt", Title: "dflt", Body: "matrix retrieval kb default"},
	}
	h := newHarnessWith(t, WithSource("orders", publicFakeSource{newFakeSource(docs)}),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Groups: []string{"group:eng"}, Clearance: classPublic}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public", "default_acl": []string{"group:eng"}})
	ingestFromSource(t, h, editor, tenant, kbID, "orders")

	got := queryTitleSet(t, h, editor, tenant, kbID)
	if got["own"] || !got["dflt"] {
		t.Fatalf("the document ACL and the KB default_acl must decide, not public; got %v", got)
	}
	acls := documentACLs(h, tenant, kbID)
	if len(acls["dflt"]) != 1 || acls["dflt"][0] != "group:eng" {
		t.Errorf("KB default_acl must be kept, got %v", acls["dflt"])
	}
	if auditActionCount(t, h, tenant, "knowledge.source.public") != 0 {
		t.Error("nothing was made public, so no public-source audit entry is due")
	}
}

func TestDirectUploadWithoutACLStaysVisible(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public"})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "up1", "title": "up1", "body": "matrix retrieval uploaded by an admin"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("upload = %d %s", r.code, r.raw)
	}
	if got := queryTitleSet(t, h, editor, tenant, kbID); !got["up1"] {
		t.Fatalf("a direct upload without ACL keeps today's visibility; got %v", got)
	}
	if acl := documentACLs(h, tenant, kbID)["up1"]; len(acl) != 1 || acl[0] != "anyone" {
		t.Errorf("the upload must store the explicit [anyone], got %v", acl)
	}
}

// clearACLs rewrites every document and chunk of a KB to the empty ACL, the way
// rows written before #3030 look in a store.
func clearACLs(t *testing.T, h *harness, tenant model.TenantID, kbID string) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, kind := range []model.Kind{documentKind, chunkKind} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			recs, err := listAll(context.Background(), repo, eq(colKBRef, kbID))
			if err != nil {
				return err
			}
			for _, rec := range recs {
				rec[colACL] = "[]"
				if _, err := repo.Update(context.Background(), rec); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("clear ACLs: %v", err)
	}
}

func TestLegacyDirectUploadKeepsVisibilityAfterDefaultDeny(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard(), WithSource("orders", newFakeSource(rowsWithoutACL())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	upKB := h.mustKB(editor, tenant, map[string]any{"name": "uploads", "classification": "public"})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+upKB+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "old", "title": "old", "body": "matrix retrieval written before the change"}},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("upload = %d %s", r.code, r.raw)
	}
	connKB := h.mustKB(editor, tenant, map[string]any{"name": "orders-kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, connKB, "orders")
	// The store as a release before #3030 left it: every ACL empty.
	clearACLs(t, h, tenant, upKB)
	clearACLs(t, h, tenant, connKB)

	if got := queryTitleSet(t, h, editor, tenant, upKB); !got["old"] {
		t.Fatalf("a knowledge base created by direct upload before the change must be seen as before; got %v", got)
	}
	if got := queryTitleSet(t, h, editor, tenant, connKB); got["row1"] || got["row2"] {
		t.Fatalf("the migration must not touch connector sources; got %v", got)
	}
	if acl := documentACLs(h, tenant, upKB)["old"]; len(acl) != 1 || acl[0] != "anyone" {
		t.Errorf("the migration must write [anyone] into the upload, got %v", acl)
	}
	if acl := documentACLs(h, tenant, connKB)["row1"]; len(acl) != 0 {
		t.Errorf("a connector document must keep its empty ACL, got %v", acl)
	}
	if n := auditActionCount(t, h, tenant, "knowledge.acl.legacy_direct"); n != 1 {
		t.Errorf("the one-time migration must leave one audit entry, got %d", n)
	}
	// Idempotent: a second read changes nothing and audits nothing more.
	queryTitleSet(t, h, editor, tenant, upKB)
	if n := auditActionCount(t, h, tenant, "knowledge.acl.legacy_direct"); n != 1 {
		t.Errorf("the migration must run once, got %d audit entries", n)
	}
}

func TestEffectiveACL(t *testing.T) {
	grp, dflt := []string{"group:eng"}, []string{"group:ops"}
	cases := []struct {
		name        string
		own, kbDef  []string
		public, dir bool
		want        []string
		wantMadePub bool
		wantDenied  bool
	}{
		{name: "own wins over everything", own: grp, kbDef: dflt, public: true, dir: true, want: grp},
		{name: "kb default over public", kbDef: dflt, public: true, want: dflt},
		{name: "public source", public: true, want: []string{"anyone"}, wantMadePub: true},
		{name: "direct upload is not the ledger's public source", dir: true, want: []string{"anyone"}},
		{name: "nothing decided", wantDenied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, madePub := effectiveACL(tc.own, tc.kbDef, tc.public, tc.dir)
			if madePub != tc.wantMadePub {
				t.Errorf("madePublic = %v, want %v", madePub, tc.wantMadePub)
			}
			if tc.wantDenied {
				if aclAllows(got, []string{"group:eng"}) {
					t.Errorf("acl %v must deny every identity", got)
				}
				return
			}
			if len(got) != len(tc.want) || got[0] != tc.want[0] {
				t.Errorf("acl = %v, want %v", got, tc.want)
			}
		})
	}
}

// publicLiveSource is a fakeLiveSource whose operator declared it public.
type publicLiveSource struct{ *fakeLiveSource }

func (publicLiveSource) Public() bool { return true }

// A live ACL refresh that reports no ACL follows the same rule as ingest: denied,
// unless the source was declared public (and then the ledger says so).
func TestLiveACLRefreshWithoutACLFollowsTheSameRule(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%v", public), func(t *testing.T) {
			live := newFakeLiveSource([]contentsource.Document{
				{Source: "livesrc", DocID: "d1", Title: "d1", Body: "matrix retrieval d1", ACL: []string{"group:eng"}},
			})
			live.setACL("d1", contentsource.ACLResult{}) // the source now reports no ACL
			live.setPages([]contentsource.DeltaPage{{
				Changes:     []contentsource.DeltaEntry{{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeACL}},
				ResumeToken: "delta-1",
			}})
			var src contentsource.Source = live
			if public {
				src = publicLiveSource{live}
			}
			h := newHarnessWith(t, noGroupsGuard())
			h.mod.AddSource("livesrc", src)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
			kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public"})
			ingestFromSource(t, h, editor, tenant, kbID, "livesrc")
			if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "livesrc"}, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("sync = %d %s", r.code, r.raw)
			}

			visible := queryTitleSet(t, h, editor, tenant, kbID)["d1"]
			acl := documentACLs(h, tenant, kbID)["d1"]
			audited := auditActionCount(t, h, tenant, "knowledge.source.public")
			if public && (!visible || len(acl) != 1 || acl[0] != "anyone" || audited != 1) {
				t.Errorf("public source: visible=%v acl=%v public-audit=%d, want visible, [anyone], 1", visible, acl, audited)
			}
			if !public && (visible || len(acl) != 0 || audited != 0) {
				t.Errorf("non-public source: visible=%v acl=%v public-audit=%d, want denied, [], 0", visible, acl, audited)
			}
		})
	}
}

func TestScanSourceReportsDocumentsWithoutACL(t *testing.T) {
	src := newFakeSource([]contentsource.Document{
		{Source: "pgcontent", DocID: "a", Title: "a", Body: "plain row a"},
		{Source: "pgcontent", DocID: "b", Title: "b", Body: "plain row b"},
		{Source: "pgcontent", DocID: "c", Title: "c", Body: "plain row c", ACL: []string{"group:eng"}},
	})
	h := newDiscoveryHarness(t, WithSource("orders", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	r := h.do("POST", "/v1/m/knowledge/sources/orders/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("source scan = %d %s", r.code, r.raw)
	}
	if n, ok := r.body["documents_without_acl"].(float64); !ok || n != 2 {
		t.Fatalf("documents_without_acl = %v, want 2 (of 3 scanned)", r.body["documents_without_acl"])
	}
}

// ---- paths the first version of these tests did not reach -------------------------

// Each document a sync stores because its source is public is recorded in the
// same transaction, so a sync that fails later cannot leave one unrecorded.
func TestSyncFromAPublicSourceRecordsEachDocument(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard(), WithSource("orders", publicFakeSource{newFakeSource(rowsWithoutACL())}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "orders-kb", "classification": "public"})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "orders"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("sync = %d %s", r.code, r.raw)
	}
	if got := queryTitleSet(t, h, editor, tenant, kbID); !got["row1"] || !got["row2"] {
		t.Fatalf("rows synced from a public source must be retrieved; got %v", got)
	}
	if n := auditActionCount(t, h, tenant, "knowledge.source.public"); n != 2 {
		t.Fatalf("public-source ledger entries after a 2-row sync = %d, want 2", n)
	}
}

// A failed ACL fetch stores the deny-closed sentinel; a public source must never
// turn it into "anyone".
func TestPublicSourceNeverOpensAFailedACLRefresh(t *testing.T) {
	live := newFakeLiveSource([]contentsource.Document{
		{Source: "livesrc", DocID: "d1", Title: "d1", Body: "matrix retrieval d1", ACL: []string{"group:eng"}},
	})
	live.setACLErr("d1", errFakeNotFound)
	live.setPages([]contentsource.DeltaPage{{
		Changes:     []contentsource.DeltaEntry{{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeACL}},
		ResumeToken: "delta-1",
	}})
	h := newHarnessWith(t, noGroupsGuard())
	h.mod.AddSource("livesrc", publicLiveSource{live})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, kbID, "livesrc")
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "livesrc"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("sync = %d %s", r.code, r.raw)
	}
	if acl := documentACLs(h, tenant, kbID)["d1"]; len(acl) != 1 || acl[0] != aclStaleSentinel {
		t.Errorf("ACL after a failed refresh = %v, want [%s]", acl, aclStaleSentinel)
	}
	if queryTitleSet(t, h, editor, tenant, kbID)["d1"] {
		t.Error("a document whose ACL could not be refreshed must stay invisible")
	}
	if n := auditActionCount(t, h, tenant, "knowledge.source.public"); n != 0 {
		t.Errorf("nothing was made public, got %d ledger entries", n)
	}
}

func TestLegacyDirectUploadPredicate(t *testing.T) {
	cases := []struct {
		name, mode, kind, acl string
		want                  bool
	}{
		{"direct, empty list", "direct", "inline", "[]", true},
		{"direct, null", "direct", "x", "null", true},
		{"direct, blank", "direct", "x", "", true},
		{"older than the source_mode column", "", "inline", "[]", true},
		{"connector with no mode", "", "gdrive", "[]", false},
		{"export mode", "export", "inline", "[]", false},
		{"live mode", "live", "gdrive", "[]", false},
		{"already decided", "direct", "inline", `["group:eng"]`, false},
		{"a corrupt column is never widened", "direct", "inline", "{not json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := model.Record{colSourceMode: tc.mode, colSourceKind: tc.kind, colACL: tc.acl}
			if got := legacyDirectUpload(rec); got != tc.want {
				t.Errorf("legacyDirectUpload(mode=%q kind=%q acl=%q) = %v, want %v", tc.mode, tc.kind, tc.acl, got, tc.want)
			}
		})
	}
}

// One tenant's first read stamps that tenant only.
func TestLegacyStampIsPerTenant(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard())
	admin := h.adminLogin()
	type org struct {
		tenant model.TenantID
		editor string
		kb     string
	}
	orgs := map[string]*org{}
	for _, slug := range []string{"acme", "globex"} {
		o := &org{tenant: h.createOrg(admin, slug)}
		o.editor = h.roleToken(admin, o.tenant, "ed@"+slug+".com", "editor")
		o.kb = h.mustKB(o.editor, o.tenant, map[string]any{"name": "uploads", "classification": "public"})
		if r := h.do("POST", "/v1/m/knowledge/kbs/"+o.kb+"/ingest", o.editor, map[string]any{
			"documents": []map[string]any{{"source_doc_id": "old", "title": "old", "body": "matrix retrieval written before the change"}},
		}, tenantHdr(o.tenant)); r.code != http.StatusOK {
			t.Fatalf("upload for %s = %d %s", slug, r.code, r.raw)
		}
		clearACLs(t, h, o.tenant, o.kb)
		orgs[slug] = o
	}
	a, b := orgs["acme"], orgs["globex"]
	if got := queryTitleSet(t, h, a.editor, a.tenant, a.kb); !got["old"] {
		t.Fatalf("tenant acme must see its legacy upload; got %v", got)
	}
	if acl := documentACLs(h, b.tenant, b.kb)["old"]; len(acl) != 0 {
		t.Fatalf("a read in tenant acme must not stamp tenant globex, got %v", acl)
	}
	if got := queryTitleSet(t, h, b.editor, b.tenant, b.kb); !got["old"] {
		t.Fatalf("tenant globex must see its legacy upload on its own first read; got %v", got)
	}
}

func TestFetchDocumentStampsALegacyUploadAndDeniesAConnectorRow(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard(), WithSource("orders", newFakeSource(rowsWithoutACL())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	upKB := h.mustKB(editor, tenant, map[string]any{"name": "uploads", "classification": "public"})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+upKB+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "old", "title": "old", "body": "matrix retrieval written before the change"}},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("upload = %d %s", r.code, r.raw)
	}
	connKB := h.mustKB(editor, tenant, map[string]any{"name": "orders-kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, connKB, "orders")
	oldID := docIDsByTitle(t, h, editor, tenant, upKB)["old"]
	rowID := docIDsByTitle(t, h, editor, tenant, connKB)["row1"]
	clearACLs(t, h, tenant, upKB)
	clearACLs(t, h, tenant, connKB)

	mc := api.ModuleContext{Principal: h.scopedPrincipal(tenant), Tenant: tenant, Data: api.NewScopedData(h.st, tenant)}
	res, err := h.module().FetchDocument(context.Background(), mc, oldID)
	if err != nil {
		t.Fatalf("a legacy direct upload must be fetchable as the first read of the tenant, got %v", err)
	}
	if len(res.ACL) != 1 || res.ACL[0] != "anyone" {
		t.Errorf("fetched ACL = %v, want [anyone]", res.ACL)
	}
	_, err = h.module().FetchDocument(context.Background(), mc, rowID)
	if qe, ok := IsQueryError(err); !ok || qe.Kind != QueryErrNotFound {
		t.Errorf("a connector document without an ACL must be not found, got %v", err)
	}
}

func TestKBScanReportsDocumentsWithoutACL(t *testing.T) {
	src := newFakeSource([]contentsource.Document{
		{Source: "pgcontent", DocID: "a", Title: "a", Body: "plain row a"},
		{Source: "pgcontent", DocID: "b", Title: "b", Body: "plain row b"},
		{Source: "pgcontent", DocID: "c", Title: "c", Body: "plain row c", ACL: []string{"group:eng"}},
	})
	h := newDiscoveryHarness(t, WithSource("orders", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public"})
	ingestFromSource(t, h, editor, tenant, kbID, "orders")
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("KB scan = %d %s", r.code, r.raw)
	}
	if n, ok := r.body["documents_without_acl"].(float64); !ok || n != 2 {
		t.Fatalf("documents_without_acl = %v, want 2 (of 3 stored)", r.body["documents_without_acl"])
	}
}

// Only the default source mode of an inline upload is "direct". A caller that
// labels its inline documents export or live has not made a public upload.
func TestInlineUploadWithANonDirectModeIsDenied(t *testing.T) {
	h := newHarnessWith(t, noGroupsGuard())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "classification": "public"})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "x1", "title": "x1", "body": "matrix retrieval pushed by a sync agent", "source_mode": "export"}},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("inline ingest = %d %s", r.code, r.raw)
	}
	if queryTitleSet(t, h, editor, tenant, kbID)["x1"] {
		t.Error("an inline document labelled export with no ACL must be denied")
	}
	if acl := documentACLs(h, tenant, kbID)["x1"]; len(acl) != 0 {
		t.Errorf("its ACL must stay empty, got %v", acl)
	}
}
