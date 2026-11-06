// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// limitedSource is a fake source whose operator set a deletion safeguard.
type limitedSource struct {
	*fakeSource
	limit int
}

func (s limitedSource) MaxDeletePercent() int { return s.limit }

// A source that stops listing 3 of its 5 documents (60 %) with the safeguard at
// 20 % deletes nothing, and the run says why.
func TestSyncDeleteSafeguardWithholdsLargeDeletion(t *testing.T) {
	docs := []contentsource.Document{
		{Source: "fs", DocID: "d1", Body: "one", ACL: []string{"anyone"}},
		{Source: "fs", DocID: "d2", Body: "two", ACL: []string{"anyone"}},
		{Source: "fs", DocID: "d3", Body: "three", ACL: []string{"anyone"}},
		{Source: "fs", DocID: "d4", Body: "four", ACL: []string{"anyone"}},
		{Source: "fs", DocID: "d5", Body: "five", ACL: []string{"anyone"}},
	}
	h := newHarnessWith(t, WithSource("fs", limitedSource{newFakeSource(docs), 20}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "fs"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("ingest: %d %s", r.code, r.raw)
	}

	h.addSource("fs", limitedSource{newFakeSource(docs[:2]), 20})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor, map[string]any{"source": "fs"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: %d %s", r.code, r.raw)
	}
	if d, _ := r.body["docs_deleted"].(float64); d != 0 {
		t.Fatalf("docs_deleted = %v, want 0: %s", r.body["docs_deleted"], r.raw)
	}
	reason, _ := r.body["deletes_withheld_reason"].(string)
	if !strings.Contains(reason, "3 of 5") || !strings.Contains(reason, "60%") || !strings.Contains(reason, "20%") {
		t.Fatalf("deletes_withheld_reason = %q, want the counts and both percentages", reason)
	}
	left := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(left); len(items) != 5 {
		t.Fatalf("after a withheld deletion: want 5 docs, got %d", len(items))
	}
}
