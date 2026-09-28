// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRunStreamRetainsComparisonEvidence(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "stream-comparison")
	suite := comparisonFixture(t, h, admin, tenant, "stream")
	comparisonRun(t, h, admin, tenant, suite, "model-a")
	run := comparisonRun(t, h, admin, tenant, suite, "model-a")
	assertComparison(t, run.body, "comparable")
	get := h.do("GET", "/v1/m/evals/runs/"+run.body["id"].(string), admin, nil, tenantHdr(tenant))
	replay := h.do("GET", "/v1/m/evals/runs/"+run.body["id"].(string)+"/stream", admin, nil, tenantHdr(tenant))
	if replay.code != http.StatusOK {
		t.Fatal(replay.raw)
	}
	found := false
	for _, frame := range strings.Split(replay.raw, "\n\n") {
		if !strings.HasPrefix(frame, "event: summary\ndata: ") {
			continue
		}
		var summary map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "event: summary\ndata: ")), &summary); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(summary["comparison"], get.body["comparison"]) {
			t.Fatalf("SSE receipt differs from GET: %#v vs %#v", summary["comparison"], get.body["comparison"])
		}
		found = true
	}
	if !found || !strings.Contains(replay.raw, "event: done") {
		t.Fatalf("summary/done missing: %s", replay.raw)
	}
}
