// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"testing"
)

type scopedHistoryFixture struct{ got string }

func (s *scopedHistoryFixture) Timeline(context.Context, model.TenantID, string) ([]ReplayStep, error) {
	return nil, nil
}
func (s *scopedHistoryFixture) TimelineByLiveRef(_ context.Context, _ model.TenantID, ref string) ([]ReplayStep, error) {
	s.got = ref
	return []ReplayStep{{Key: "one", Input: "/fixture/scoped"}}, nil
}

func TestScopedHistoryHTTPAndPersistence(t *testing.T) {
	source := &scopedHistoryFixture{}
	h := newHarness(t, WithHistorySource(source))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scoped")
	live := model.NewID().String()
	for _, path := range []string{"replay", "compare"} {
		body := map[string]any{"live_ref": live, "baseline_variant": "before", "candidate_variant": "after"}
		if path == "replay" {
			body = map[string]any{"live_ref": live}
		}
		response := h.do("POST", "/v1/m/sandbox/"+path, admin, body, tenantHdr(tenant))
		if response.code != http.StatusCreated || response.body["live_ref"] != live || source.got != live {
			t.Fatalf("%s lost scoped target: %d %s", path, response.code, response.raw)
		}
		collection := "runs"
		if path == "compare" {
			collection = "comparisons"
		}
		read := h.do("GET", "/v1/m/sandbox/"+collection+"/"+response.body["id"].(string), admin, nil, tenantHdr(tenant))
		if read.code != http.StatusOK || read.body["live_ref"] != live {
			t.Fatalf("%s durable target missing: %d %s", path, read.code, read.raw)
		}
		if path == "compare" {
			for _, key := range []string{"baseline_run_ref", "candidate_run_ref"} {
				read := h.do("GET", "/v1/m/sandbox/runs/"+response.body[key].(string), admin, nil, tenantHdr(tenant))
				if read.code != http.StatusOK || read.body["live_ref"] != live {
					t.Fatal("comparison arm lost live_ref")
				}
			}
		}
		body["session_ref"] = "legacy"
		bad := h.do("POST", "/v1/m/sandbox/"+path, admin, body, tenantHdr(tenant))
		if bad.code != http.StatusBadRequest {
			t.Fatalf("ambiguous %s=%d", path, bad.code)
		}
	}
	legacy := h.do("POST", "/v1/m/sandbox/replay", admin, map[string]any{"session_ref": "old"}, tenantHdr(tenant))
	if legacy.code != http.StatusCreated || legacy.body["status"] != "degraded" || legacy.body["live_ref"] != nil {
		t.Fatal("legacy history acquired scoped attribution")
	}
}
