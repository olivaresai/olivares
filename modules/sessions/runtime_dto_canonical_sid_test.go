// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

// A session's canonical osn_ ID is what a peer send names and what the peers
// list holds, and the run read is where a person (and `olivares session show`) finds a
// session. The run carries it: the same ID its managed live row proves, one per run,
// accepted as-is by the sender's peers list.
func TestRunDTOCarriesItsCanonicalSessionID(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profA, profB := profiledHTTP(t, fr, &testClock{now: baseTime})
	runA, liveA := launchProfiledHTTP(t, h, admin, tenant, profA)
	runB, _ := launchProfiledHTTP(t, h, admin, tenant, profB)

	sidOf := func(run string) string {
		r := h.do("GET", "/v1/m/sessions/runs/"+run, admin, tenantHdr(tenant))
		sid, _ := r.body["canonical_sid"].(string)
		if r.code != http.StatusOK || !validCanonicalSID(sid) {
			t.Fatalf("run %s canonical_sid = %q (%d %s)", run, sid, r.code, r.raw)
		}
		return sid
	}
	sidA, sidB := sidOf(runA), sidOf(runB)
	if sidA == sidB {
		t.Fatalf("two runs share the canonical ID %s", sidA)
	}
	if r := h.do("GET", "/v1/m/sessions/live/by-id/"+liveA, admin, tenantHdr(tenant)); r.code != http.StatusOK || r.body["canonical_sid"] != sidA {
		t.Fatalf("live row of %s = %d %s, want canonical_sid %s", runA, r.code, r.raw, sidA)
	}
	if r := h.doJSON("PUT", "/v1/m/sessions/runs/"+runA+"/peers", admin, map[string]any{"peers": []string{sidB}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("peers with the shown ID = %d %s", r.code, r.raw)
	}
	for _, ref := range []string{runA, runB} {
		if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("stop = %d %s", r.code, r.raw)
		}
	}
}
