// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

// the cadence-miss scan gains an exported, pump-drivable seam
// (RunCadenceScan): before it, detection ONLY ran when a human read
// schedules/graph/flows, so an unwatched estate never raised the
// "schedule went silent" Finding. This test pins the seam's contract: a bare
// ModuleContext (Tenant + Data, NO Principal — the composition-root pump has
// none) detects the miss exactly like the read-time piggyback.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// jsonInt reads a JSON-decoded number (float64 in map[string]any).
func jsonInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func TestS431_RunCadenceScanExportedSeam(t *testing.T) {
	clock := newManualClock()
	h, mod := newHarness(t, WithClock(clock))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	h.createSchedule(tok, tenant, "recurring", "agent", "cron-agent", "cron", "*/5 * * * *", 60)
	clock.advance(300 * time.Second) // > interval*defaultGrace for the 60s schedule

	// No read handler runs: the pump-shaped bare context drives the scan.
	mod.RunCadenceScan(context.Background(), api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(h.st, tenant)})

	if !h.waitForFinding(busCadenceMiss) {
		t.Fatal("RunCadenceScan with a bare pump context must raise the cadence-miss finding")
	}
	if got := h.findingsFor(busCadenceMiss, "cron-agent"); got == 0 {
		t.Fatal("the overdue schedule must have a cadence-miss finding")
	}
}

// The schedule revision ledger: create/patch snapshot the config in-tx;
// restore re-applies an earlier mutable shape with the patch verb's semantics;
// a foreign revision is a 404 (no cross-schedule existence leak).
func TestS431_ScheduleRevisions(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 3600)
	r := h.do("PATCH", "/v1/m/orchestration/schedules/"+id, tok, map[string]any{
		"desired_status": "paused", "expected_interval_seconds": 7200,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}

	r = h.do("GET", "/v1/m/orchestration/schedules/"+id+"/revisions", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 revisions (create,update), got %d (%s)", len(items), r.raw)
	}
	first := items[0].(map[string]any)
	if first["op"] != "create" || items[1].(map[string]any)["op"] != "update" {
		t.Fatalf("ops mismatch: %s", r.raw)
	}
	snap := first["snapshot"].(map[string]any)
	if snap["desired_status"] != "active" || jsonInt(snap["expected_interval_seconds"]) != 3600 {
		t.Fatalf("create snapshot mismatch: %v", snap)
	}

	// Restore the original shape (re-activates, interval back to 3600).
	r = h.do("POST", "/v1/m/orchestration/schedules/"+id+"/restore", tok, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("restore = %d %s", r.code, r.raw)
	}
	if r.body["desired_status"] != "active" || jsonInt(r.body["expected_interval_seconds"]) != 3600 {
		t.Fatalf("restore did not re-apply: %s", r.raw)
	}
	r = h.do("GET", "/v1/m/orchestration/schedules/"+id+"/revisions", tok, nil, tenantHdr(tenant))
	if items, _ = r.body["items"].([]any); len(items) != 3 || items[2].(map[string]any)["op"] != "restore" {
		t.Fatalf("want 3rd revision op=restore, got %s", r.raw)
	}

	// A revision belonging to ANOTHER schedule: 404.
	otherID := h.createSchedule(tok, tenant, "weekly", "agent", "other-agent", "cron", "0 0 * * 0", 0)
	if r := h.do("POST", "/v1/m/orchestration/schedules/"+otherID+"/restore", tok, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("foreign revision restore = %d, want 404 (%s)", r.code, r.raw)
	}
}
