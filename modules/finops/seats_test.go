// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
)

// seats_test.go proves the per-seat utilization surface end to end over the
// real API server (perms enforced): seat-snapshot ingest (upsert semantics),
// the active-vs-assigned join, and the honesty rules (no fabricated percentage
// without a denominator; billed/actor-less samples never count as active).

func TestSeatsIngestAndUtilization(t *testing.T) {
	h := newHarness(t, finops.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "editor@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "viewer@x.io", "viewer")
	hdr := tenantHdr(tenant)

	// Seat denominators for 2026-06-09: 10 assigned, 4 premium (Claude Code tier).
	if r := h.do("POST", "/v1/m/finops/seats", editor, map[string]any{
		"provider": "anthropic", "day": "2026-06-09",
		"assigned_seats": 10, "premium_seats": 4, "pending_invites": 1,
	}, hdr); r.code != http.StatusAccepted {
		t.Fatalf("seats ingest = %d %s", r.code, r.raw)
	}

	// Active side via the cost ingest: two distinct subscription actors on the
	// 9th (one with two models — distinct actors, not rows, drive utilization),
	// one actor-less estimated row (not countable), one BILLED actor (the billed
	// stream carries no per-actor subscription semantics — never counted), and
	// one actor on the 10th, a day with NO seat snapshot.
	post := func(body map[string]any) {
		t.Helper()
		if r := h.do("POST", "/v1/m/finops/cost", editor, body, hdr); r.code != http.StatusAccepted {
			t.Fatalf("cost ingest = %d %s", r.code, r.raw)
		}
	}
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"actor": "a@x.io", "cost_micro_usd": 100, "occurred_at": "2026-06-09T10:00:00Z"})
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-haiku-4-5",
		"actor": "a@x.io", "cost_micro_usd": 10, "occurred_at": "2026-06-09T11:00:00Z"})
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"actor": "b@x.io", "cost_micro_usd": 50, "occurred_at": "2026-06-09T12:00:00Z"})
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"cost_micro_usd": 30, "occurred_at": "2026-06-09T13:00:00Z"}) // no actor
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"actor": "c@x.io", "provenance": "billed", "cost_micro_usd": 70, "occurred_at": "2026-06-09T14:00:00Z"})
	post(map[string]any{"provider_ref": "anthropic", "model_ref": "claude-opus-4-8",
		"actor": "d@x.io", "cost_micro_usd": 20, "occurred_at": "2026-06-10T09:00:00Z"})

	r := h.do("GET", "/v1/m/finops/seats/utilization?provider=anthropic&from=2026-06-09&to=2026-06-10", viewer, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("utilization = %d %s", r.code, r.raw)
	}
	days, _ := r.body["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("utilization days = %v", r.body)
	}
	d9 := days[0].(map[string]any)
	if d9["day"] != "2026-06-09" || d9["active_actors"] != float64(2) {
		t.Errorf("day 9 = %v, want 2 active actors (a@x.io, b@x.io; actor-less and billed rows excluded)", d9)
	}
	if d9["assigned_seats"] != float64(10) || d9["utilization_pct"] != float64(20) {
		t.Errorf("day 9 utilization = %v, want 20%% of 10 seats", d9)
	}
	if d9["premium_utilization_pct"] != float64(50) {
		t.Errorf("day 9 premium utilization = %v, want 50%% of 4 premium seats", d9)
	}
	// The 10th has activity but no posted denominator: an honest no-percentage day.
	d10 := days[1].(map[string]any)
	if d10["active_actors"] != float64(1) || d10["has_seats"] != false {
		t.Errorf("day 10 = %v, want 1 active actor and has_seats=false", d10)
	}
	if _, fabricated := d10["utilization_pct"].(float64); fabricated && d10["utilization_pct"] != float64(0) {
		t.Errorf("day 10 must not fabricate a percentage without a denominator: %v", d10)
	}

	// Re-posting a day REPLACES the snapshot (state, never additive).
	if r := h.do("POST", "/v1/m/finops/seats", editor, map[string]any{
		"provider": "anthropic", "day": "2026-06-09", "assigned_seats": 4,
	}, hdr); r.code != http.StatusAccepted {
		t.Fatalf("seats re-ingest = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/finops/seats/utilization?provider=anthropic&from=2026-06-09&to=2026-06-09", viewer, nil, hdr)
	d9 = r.body["days"].([]any)[0].(map[string]any)
	if d9["assigned_seats"] != float64(4) || d9["utilization_pct"] != float64(50) {
		t.Errorf("re-posted snapshot must replace values: %v", d9)
	}

	// The privileged write is audited atomically (mirrors the cost ingest).
	if !h.hasAuditAction(tenant, "finops.seats.ingest") {
		t.Error("seat ingest must be audited to the real principal")
	}
}

func TestSeatsIngestDenyClosed(t *testing.T) {
	h := newHarness(t, finops.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "viewer@x.io", "viewer")
	hdr := tenantHdr(tenant)

	// A viewer cannot post seat snapshots (write-tier perm, deny-closed).
	if r := h.do("POST", "/v1/m/finops/seats", viewer, map[string]any{
		"provider": "anthropic", "day": "2026-06-09", "assigned_seats": 10,
	}, hdr); r.code != http.StatusForbidden {
		t.Fatalf("viewer seat ingest = %d, want 403", r.code)
	}

	// Malformed snapshots are rejected loudly.
	editor := h.roleToken(admin, tenant, "editor@x.io", "editor")
	for name, body := range map[string]map[string]any{
		"no provider": {"day": "2026-06-09", "assigned_seats": 1},
		"bad day":     {"provider": "anthropic", "day": "junio", "assigned_seats": 1},
		"negative":    {"provider": "anthropic", "day": "2026-06-09", "assigned_seats": -1},
	} {
		if r := h.do("POST", "/v1/m/finops/seats", editor, body, hdr); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, r.code)
		}
	}

	// Utilization requires explicit provider and day bounds.
	if r := h.do("GET", "/v1/m/finops/seats/utilization?from=2026-06-09&to=2026-06-09", viewer, nil, hdr); r.code != http.StatusBadRequest {
		t.Errorf("utilization without provider = %d, want 400", r.code)
	}
}
