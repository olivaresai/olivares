// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"net/url"
	"testing"
)

// The console's one search box (GET /v1/search) finds a session by its name and by the
// title cut from its first message, and opens it by its address. Sessions used to
// register no search kind, so every query answered nothing. The title is matched only
// for a caller who may read the conversation it was cut from, as a read of the run shows
// it only to them.
func TestSearchFindsASessionByItsNameAndItsTitle(t *testing.T) {
	f := messagesFixture(t, titleTranscript)
	run := "/v1/m/sessions/runs/" + f.runRef
	if r := f.h.doJSON("PUT", run+"/name", f.admin, map[string]any{"name": "dbg-console-1"}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("rename = %d %s", r.code, r.raw)
	}
	search := func(token, q string) []map[string]any {
		t.Helper()
		r := f.h.do("GET", "/v1/search?q="+url.QueryEscape(q), token, f.hdr)
		if r.code != http.StatusOK {
			t.Fatalf("search %q = %d %s", q, r.code, r.raw)
		}
		var hits []map[string]any
		list, _ := r.body["results"].([]any)
		for _, item := range list {
			if hit, _ := item.(map[string]any); hit["kind"] == "sessions.run" {
				hits = append(hits, hit)
			}
		}
		return hits
	}
	byName := search(f.admin, "DBG-console")
	if len(byName) != 1 || byName[0]["id"] != "run:"+f.runRef || byName[0]["name"] != "dbg-console-1" {
		t.Fatalf("search by name = %v, want the session as run:%s named dbg-console-1", byName, f.runRef)
	}
	if hits := search(f.admin, "authentication"); len(hits) != 0 {
		t.Fatalf("search by a word of an untitled session = %v, want none", hits)
	}
	if r := f.h.doJSON("POST", run+"/title", f.admin, map[string]any{}, f.hdr); r.code != http.StatusOK {
		t.Fatalf("title = %d %s", r.code, r.raw)
	}
	if hits := search(f.admin, "authentication"); len(hits) != 1 || hits[0]["id"] != "run:"+f.runRef {
		t.Fatalf("search by a word of its title = %v, want the session", hits)
	}
	viewer := f.h.viewerToken(f.admin, f.tenant, "v@search.io")
	if hits := search(viewer, "authentication"); len(hits) != 0 {
		t.Fatalf("a viewer who may not read the conversation found it by its title: %v", hits)
	}
	if hits := search(viewer, "dbg-console"); len(hits) != 1 {
		t.Fatalf("a viewer who may list sessions did not find one by its name: %v", hits)
	}
}
