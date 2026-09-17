// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestScopedMonitorIdentityPersistsAndUnknownFindingsCannotPass(t *testing.T) {
	a, b, external := model.NewID().String(), model.NewID().String(), model.NewID().String()
	var got SampleQuery
	source := fakeSessionSource{got: &got, samples: []SessionSample{
		{SessionRef: external, LiveRef: a, Attribution: "observed", ProfileRef: "profile-a", State: "ended", FindingsUnavailable: true},
		{SessionRef: external, LiveRef: b, Attribution: "observed", ProfileRef: "profile-b", State: "ended", FindingsUnavailable: true},
	}}
	h := newHarness(t, nil, WithSessionSource(&source))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scoped")
	response := h.do("POST", "/v1/m/evals/monitor", admin, map[string]any{"suite": "scoped-monitor"}, tenantHdr(tenant))
	if response.code != http.StatusOK || intOf(response.body["total"]) != 2 || intOf(response.body["passed"]) != 0 {
		t.Fatalf("monitor=%d %s", response.code, response.raw)
	}
	samples := response.body["samples"].([]any)
	if samples[0].(map[string]any)["live_ref"] == samples[1].(map[string]any)["live_ref"] {
		t.Fatal("response collapsed instance identity")
	}
	results := h.coreEvalResults(tenant, "scoped-monitor")
	if len(results) != 2 {
		t.Fatal("missing durable monitor results")
	}
	seen := map[string]bool{}
	for _, result := range results {
		if result.SubjectKind != "session_live" || result.SubjectID.String() == external || result.Metadata["live_ref"] != result.SubjectID.String() || result.Passed {
			t.Fatalf("monitor lost scope or invented pass: %+v", result)
		}
		seen[result.SubjectID.String()] = true
	}
	if !seen[a] || !seen[b] {
		t.Fatal("durable subjects collapsed")
	}
	source.samples = source.samples[:1]
	response = h.do("POST", "/v1/m/evals/monitor", admin, map[string]any{"suite": "scoped-one", "live_ref": a}, tenantHdr(tenant))
	if response.code != http.StatusOK || got.LiveRef != a {
		t.Fatal("exact selector did not reach source")
	}
	bad := h.do("POST", "/v1/m/evals/monitor", admin, map[string]any{"live_ref": a, "subject_ref": external}, tenantHdr(tenant))
	if bad.code != http.StatusBadRequest {
		t.Fatalf("ambiguous selector=%d", bad.code)
	}
}
