// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

// TestListRuns_ByClaudeSessionID covers the provenance lookup: "which runs
// DRIVE this observed session?", the question a unified session card asks to tell
// a session Olivares LAUNCHED from one it merely FOUND.
//
// The join key is the provider's session id — a run's claude_session_id IS the
// session_ref of the live/timeline tables (export.go:253 resolves a recording
// credential through that same identity).
func TestListRuns_ByClaudeSessionID(t *testing.T) {
	fr := &fakeRunner{}
	m := New(WithRunner(fr), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// launch creates a run whose bridged init frame carries sid (empty sid ⇒ the
	// process never announces one, which is the remote-control case: its I/O is
	// relayed to Anthropic's cloud, so no init frame ever reaches the bridge).
	launch := func(name, sid string) string {
		t.Helper()
		fr.initSID = sid
		r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
			"transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "name": name,
		}, tenantHdr(tenantA))
		if r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, r.code, r.raw)
		}
		ref, _ := r.body["run_ref"].(string)
		if ref == "" {
			t.Fatalf("create %s: no run_ref: %s", name, r.raw)
		}
		if sid != "" {
			// The id is captured off the bridge, asynchronously.
			waitFor(t, "claude session id captured for "+name, func() bool {
				g := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenantA))
				return g.body["claude_session_id"] == sid
			})
		}
		return ref
	}

	// names returns the `name` of every run the filtered list returned.
	names := func(query string, tenant string) []string {
		t.Helper()
		r := h.do("GET", "/v1/m/sessions/runs"+query, admin, map[string]string{"X-Olivares-Tenant": tenant})
		if r.code != http.StatusOK {
			t.Fatalf("list%s = %d %s", query, r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		out := make([]string, 0, len(items))
		for _, it := range items {
			row, _ := it.(map[string]any)
			n, _ := row["name"].(string)
			out = append(out, n)
		}
		return out
	}
	has := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		seen := map[string]bool{}
		for _, g := range got {
			seen[g] = true
		}
		for _, w := range want {
			if !seen[w] {
				return false
			}
		}
		return true
	}

	// TWO runs on ONE session is a real state, not a contrived one: measured on a live
	// engine on 2026-08-10, a resume and a second launch against the same Claude
	// session both carry the same claude_session_id. A lookup that answered with one
	// run would be picking a winner the plane never picked.
	launch("alpha-1", "sess-alpha")
	launch("alpha-2", "sess-alpha")
	launch("beta-1", "sess-beta")
	launch("unlinked", "") // no init frame ⇒ claude_session_id stays NULL

	if got := names("?claude_session_id=sess-alpha", tenantA.String()); !has(got, "alpha-1", "alpha-2") {
		t.Errorf("runs for sess-alpha = %v, want both alpha runs", got)
	}
	if got := names("?claude_session_id=sess-beta", tenantA.String()); !has(got, "beta-1") {
		t.Errorf("runs for sess-beta = %v, want [beta-1]", got)
	}

	// The direction of NO-FIRE: a filter that matched everything would pass every
	// assertion above. An unknown session must come back EMPTY — not "all runs".
	if got := names("?claude_session_id=sess-nobody", tenantA.String()); len(got) != 0 {
		t.Errorf("runs for an unknown session = %v, want none", got)
	}
	// And the NULL run is matched by no ref at all: the plane holds no evidence
	// linking it to any observed session, so it must not be attributed to one.
	if got := names("?claude_session_id=", tenantA.String()); !has(got, "alpha-1", "alpha-2", "beta-1", "unlinked") {
		t.Errorf("unfiltered list = %v, want all four runs", got)
	}

	// THE ASSERTION THAT MAKES IT A STORE FILTER. This endpoint pages by RECENCY and
	// ignores the cursor, so a filter applied to the page (the way `state` is) would
	// answer "…among the N most recent runs" while looking like it answered "…among
	// all runs". With limit=1 the most recent run is `unlinked`; a page filter would
	// therefore return NOTHING here and the console would render "discovered" for a
	// session Olivares launched itself. The third answer is "I did not look", never
	// "there is none".
	if got := names("?claude_session_id=sess-alpha&limit=1", tenantA.String()); len(got) == 0 {
		t.Errorf("filtered list with limit=1 = %v, want the linked run even though it is not on the most-recent page", got)
	}

	// Tenant isolation: the same session id under another tenant resolves to nothing.
	if got := names("?claude_session_id=sess-alpha", tenantB.String()); len(got) != 0 {
		t.Errorf("cross-tenant runs for sess-alpha = %v, want none", got)
	}
}
